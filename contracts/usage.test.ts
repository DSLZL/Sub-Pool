import assert from "node:assert/strict";
import { test } from "node:test";
import { once } from "node:events";
import { createServer } from "node:http";
import { ORPCError } from "@orpc/client";
import { usageCapture, usageFact, usageOutputs } from "./usage";
import { createAccountsClient, createAuthClient, createConnectionClient, createUsageClient, createUsersClient } from "./client";

const id = "a".repeat(21) + "A";
const timestamp = "2026-11-01T00:00:00Z";
const metrics = { input_tokens: "9007199254740993", output_tokens: "0", cache_creation_tokens: null, cache_read_tokens: null,
  cache_creation_5m_tokens: null, cache_creation_1h_tokens: null, image_input_tokens: null, image_output_tokens: null,
  total_cost: "0.000000000000000001", actual_cost: "0.2", account_stats_cost: null };
const fact = { id: "9007199254740993", user_id: "1", api_key_id: "2", group_id: null, subscription_id: null,
  request_id: null, model: "requested", upstream_model: "forwarded", upstream_response_model: null, billing_mode: "token",
  billing_type: 0, billing_candidate: "wallet", deduction_state: "unknown", rate_multiplier: null, account_rate_multiplier: null, created_at: timestamp, metrics };
const capture = { id: "1", account_id: id, source_id: id, connection_revision: "1", account_revision: "1", upstream_account_id: "1",
  start_at: timestamp, end_at: "2026-11-02T00:00:00Z", started_at: timestamp, captured_at: timestamp, source_current: true, connection_current: true,
  state: "exhausted", read: { id: "usage.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 }, pages_read: 1,
  upstream_day_total: "1", scanned_count: "1", observed_count: "1", timestamp_basis: "native_created_at", currency: "USD", complete: false, settlement_eligible: false,
  limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "PARTICIPANT_MAPPING_UNAVAILABLE", "REQUEST_START_TIME_UNAVAILABLE"] };

test("usage facts keep exact independent costs and fail closed on completeness and deduction claims", () => {
  assert.ok(usageFact.safeParse(fact).success);
  assert.ok(usageCapture.safeParse(capture).success);
  for (const forged of [{ ...fact, metrics: { ...metrics, input_tokens: 9007199254740992 } }, { ...fact, deduction_state: "confirmed" },
    { ...fact, api_key: "secret" }, { ...fact, billing_candidate: "subscription" }]) {
    assert.equal(usageFact.safeParse(forged).success, false);
  }
  for (const forged of [{ ...capture, complete: true }, { ...capture, settlement_eligible: true }, { ...capture, limitations: [] },
    { ...capture, observed_count: "2" }, { ...capture, observed_count: "invalid" }, { ...capture, source_current: false }]) {
    assert.equal(usageCapture.safeParse(forged).success, false);
  }
  assert.equal(usageOutputs.captures.safeParse({ captures: [{ ...capture, id: "10" }, { ...capture, id: "9" }], next_after: null }).success, false);
});

test("usage client validates nanosecond windows and never retries an interrupted capture", async () => {
  let calls = 0;
  const client = createUsageClient("https://example.test", async () => { calls++; throw new TypeError("response lost"); });
  const input = { id, revision: "1", source_id: id, connection_revision: "1", start_at: "2026-11-01T00:00:00.000000001Z", end_at: "2026-11-01T00:00:00.000000002Z" };
  await assert.rejects(client.capture(input));
  assert.equal(calls, 1);
  await assert.rejects(client.capture({ ...input, end_at: input.start_at }));
  await assert.rejects(client.capture({ ...input, end_at: "2026-11-01T00:00:00.000000000Z" }));
  assert.equal(calls, 1);
  const forged = createUsageClient("https://example.test", async () => Response.json({ ...capture, complete: true }));
  await assert.rejects(forged.capture({ ...input, start_at: timestamp, end_at: "2026-11-02T00:00:00Z" }));
});

test("real Go PostgreSQL usage capture, exact facts, scoped denial and immutable read through oRPC", {
  skip: !process.env.SUB_POOL_TEST_USAGE_ORIGIN, timeout: 30000,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_USAGE_ORIGIN;
  const username = process.env.SUB_POOL_TEST_USAGE_USERNAME;
  const password = process.env.SUB_POOL_TEST_USAGE_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable usage fixture required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      request.headers.set("Origin", origin!);
      if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" });
      const issued = response.headers.get("set-cookie");
      if (issued) cookie = issued.split(";")[0] ?? "";
      return response;
    };
    return { auth: createAuthClient(origin!, fetcher), users: createUsersClient(origin!, fetcher), connection: createConnectionClient(origin!, fetcher),
      accounts: createAccountsClient(origin!, fetcher), usage: createUsageClient(origin!, fetcher) };
  }
  const admin = browser(), reader = browser();
  const binding = { id, revision: "1", source_id: id, connection_revision: "1", start_at: timestamp, end_at: "2026-11-02T00:00:00Z" };
  await assert.rejects(admin.usage.capture(binding), e => e instanceof ORPCError && e.defined && e.status === 401);
  await admin.auth.login({ username, password });
  let calls = 0, unexpected = 0, deny = false;
  const secret = "adm_sc_native_usage_fixture";
  const upstream = createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://upstream.invalid");
    if (request.method !== "GET" || request.headers["x-api-key"] !== secret || request.headers.cookie || !["/api/v1/admin/accounts/1", "/api/v1/admin/usage"].includes(url.pathname)) {
      unexpected++; response.writeHead(403).end("private unexpected"); return;
    }
    if (url.pathname === "/api/v1/admin/accounts/1") {
      response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data: {
        id: 1, name: "Native usage", platform: "openai", type: "oauth", status: "active", schedulable: true, error_message: "",
        credentials: { chatgpt_account_id: "private-resource", access_token: "private-token" }, extra: {}, created_at: timestamp, updated_at: timestamp,
      } })); return;
    }
    calls++;
    if (deny) { response.writeHead(403).end("private upstream " + secret); return; }
    if (url.searchParams.get("timezone") !== "UTC" || url.searchParams.get("exact_total") !== "true" || url.searchParams.get("account_id") !== "1") unexpected++;
    const rows = ["9", "10", "9007199254740993"].map((rowID, i) => `{"id":${rowID},"account_id":1,"user_id":21,"api_key_id":31,"model":"requested","upstream_model":"forwarded","created_at":"${timestamp}","input_tokens":9007199254740993,"output_tokens":0,"total_cost":0.1,"actual_cost":0.2,"billing_type":${i === 2 ? 1 : 0},"api_key":{"key":"private-key"},"ip_address":"private-ip"}`);
    response.writeHead(200, { "Content-Type": "application/json" }).end(`{"code":0,"data":{"items":[${rows.join(",")}],"total":3,"page":1,"page_size":100}}`);
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  assert.ok(address && typeof address === "object");
  try {
    const before = await admin.connection.get({});
    const saved = await admin.connection.save({ revision: before.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: secret, confirmation: "" });
    assert.ok(saved.configuration.source_id);
    const adopted = await admin.accounts.adopt({ source_id: saved.configuration.source_id, connection_revision: saved.configuration.revision,
      upstream_id: "1", name: "Usage fixture", monitoring: false, source_mode: "passive" });
    assert.ok(adopted.account);
    const account = adopted.account;
    const input = { ...binding, id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: saved.configuration.revision };
    const captured = await admin.usage.capture(input);
    assert.equal(captured.state, "exhausted"); assert.equal(captured.complete, false); assert.equal(captured.observed_count, "3");
    const query = { id: captured.id, after: "0", limit: 1, model: "", user_id: "", api_key_id: "", grain: "day" as const };
    const first = await admin.usage.view(query);
    assert.equal(first.records[0]?.id, "9"); assert.equal(first.next_after, "9");
    assert.equal(first.summary.metrics.input_tokens, "27021597764222979"); assert.equal(first.summary.metrics.total_cost, "0.3");
    assert.equal(first.summary.metrics.actual_cost, "0.6"); assert.equal(first.summary.metrics.account_stats_cost, null);
    assert.equal(first.summary.metrics.output_tokens, "0"); assert.equal(first.summary.metrics.cache_creation_tokens, null);
    const rest = await admin.usage.view({ ...query, after: "9", limit: 100, user_id: "21", api_key_id: "31", model: "requested" });
    assert.deepEqual(rest.records.map(f => f.id), ["10", "9007199254740993"]);
    assert.equal(rest.records[1]?.billing_candidate, "subscription"); assert.equal(rest.records[1]?.deduction_state, "unknown");
    assert.equal(JSON.stringify(rest).includes("private-"), false);
    const user = await admin.users.create({ username: "usage-reader", password: "ReaderPassword123", enabled: true,
      features: ["usage"], account_ids: [account.id], participant_ids: [id] });
    await reader.auth.login({ username: user.username, password: "ReaderPassword123" });
    await assert.rejects(reader.usage.capture(input), e => e instanceof ORPCError && e.defined && e.status === 403);
    await assert.rejects(reader.usage.view(query), e => e instanceof ORPCError && e.defined && e.status === 404);
    await assert.rejects(reader.usage.captures({ account_id: account.id, after: "0", limit: 100 }), e => e instanceof ORPCError && e.defined && e.status === 404);
    assert.equal(calls, 1);
    deny = true;
    const failed = await admin.usage.capture(input);
    assert.equal(failed.state, "failed"); assert.equal(failed.read.reason, "FORBIDDEN"); assert.equal(failed.observed_count, "0");
    const historical = await admin.usage.view(query);
    assert.equal(historical.summary.request_count, "3"); assert.equal(calls, 2); assert.equal(unexpected, 0);
    const empty = await admin.usage.view({ ...query, user_id: "22" });
    assert.equal(empty.summary.request_count, "0"); assert.equal(empty.summary.metrics.actual_cost, null); assert.deepEqual(empty.buckets, []);
    const page = await admin.usage.captures({ account_id: account.id, after: "0", limit: 1 });
    assert.equal(page.captures[0]?.id, captured.id); assert.equal(page.next_after, captured.id);
    await reader.auth.logout({}); await admin.auth.logout({});
  } finally {
    upstream.closeAllConnections();
    await new Promise<void>((resolve, reject) => upstream.close(error => error && (error as NodeJS.ErrnoException).code !== "ERR_SERVER_NOT_RUNNING" ? reject(error) : resolve()));
  }
});
