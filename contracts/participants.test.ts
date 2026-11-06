import { test } from "node:test";
import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { ORPCError } from "@orpc/client";
import { attributedUsage, participant, usageAttribution } from "./participants";
import { revision } from "./users";
import { createAccountsClient, createAuthClient, createConnectionClient, createParticipantsClient, createUsageClient, createUsersClient } from "./client";

const id = "a".repeat(21) + "A";
const timestamp = "2026-11-01T00:00:00Z";
const metrics = { input_tokens: "9007199254740993", output_tokens: "0", cache_creation_tokens: null, cache_read_tokens: null,
  cache_creation_5m_tokens: null, cache_creation_1h_tokens: null, image_input_tokens: null, image_output_tokens: null,
  total_cost: "0.1", actual_cost: "0.2", account_stats_cost: null };
const attribution = { participant_id: id, binding_id: "1", participant_name: "Member", basis: "local_binding_at_log_time" };
const fact = { id: "9007199254740993", user_id: "21", api_key_id: "31", group_id: null, subscription_id: null, request_id: null,
  model: "requested", upstream_model: "forwarded", upstream_response_model: null, billing_mode: null, billing_type: 0, billing_candidate: "wallet",
  deduction_state: "unknown", rate_multiplier: null, account_rate_multiplier: null, created_at: timestamp, metrics };
const summary = { request_count: "1", metrics };

test("participant contracts preserve recorded attribution, redact private identity and safely reject invalid native IDs", () => {
  const p = { id, revision: "1", name: "Member", contact: null, notes: null, enabled: true, created_at: timestamp, saved_at: timestamp, source_current: true,
    binding: { id: "1", participant_id: id, source_id: id, upstream_user_id: "21", started_at: timestamp, ended_at: null, identity: null } };
  assert.ok(participant.safeParse(p).success);
  assert.equal(participant.safeParse({ ...p, notes: "private note" }).success, false);
  assert.equal(usageAttribution.safeParse({ ...attribution, basis: "mapping_not_recorded" }).success, false);
  const view = { start_at: timestamp, end_at: "2026-11-01T01:00:00Z", oldest_capture_at: timestamp, newest_capture_at: timestamp, summary, assigned: summary, unassigned: null, unassigned_visible: false,
    groups: [{ account_id: id, participant_id: id, basis: "local_binding_at_log_time", ...summary }], grain: "hour",
    buckets: [{ start_at: timestamp, end_at: "2026-11-01T01:00:00Z", ...summary }], records: [{ capture_id: "1", account_id: id, attribution, fact }], next_after: null,
    complete: false, settlement_eligible: false, currency: "USD", timestamp_basis: "native_created_at",
    limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"] };
  assert.ok(attributedUsage.safeParse(view).success);
  assert.equal(attributedUsage.safeParse({ ...view, complete: true }).success, false);
  assert.equal(attributedUsage.safeParse({ ...view, unassigned_visible: true }).success, false);
  assert.equal(attributedUsage.safeParse({ ...view, assigned: { ...summary, request_count: "0" } }).success, false);
  assert.equal(attributedUsage.safeParse({ ...view, records: [{ capture_id: "1", account_id: id, fact,
    attribution: { participant_id: null, binding_id: null, participant_name: null, basis: "outside_recorded_binding" } }] }).success, false);
  assert.equal(attributedUsage.safeParse({ ...view, summary: { ...summary, request_count: "bad" } }).success, false);
  for (const invalid of ["bad", "1.bad", "NaN", "01", "9223372036854775808", "1".repeat(64)]) {
    assert.doesNotThrow(() => revision.safeParse(invalid));
    assert.equal(revision.safeParse(invalid).success, false);
  }
});

test("participant mutations validate locally and never retry a lost reply", async () => {
  let calls = 0;
  const client = createParticipantsClient("http://127.0.0.1:3000", async () => { calls++; throw new TypeError("lost reply"); });
  const input = { source_id: id, connection_revision: "1", upstream_id: "21", name: "Member", contact: "", notes: "", enabled: true };
  await assert.rejects(client.create({ ...input, role: "admin" } as never));
  await assert.rejects(client.usage({ capture_ids: ["1"], participant_id: "", model: "", api_key_id: "", grain: "day", after: "1.bad", limit: 100 }));
  assert.equal(calls, 0);
  await assert.rejects(client.create(input));
  assert.equal(calls, 1);
});

test("real Go PostgreSQL participant ownership, scopes and immutable consumption through oRPC", { skip: !process.env.SUB_POOL_TEST_PARTICIPANTS_ORIGIN }, async () => {
  const origin = process.env.SUB_POOL_TEST_PARTICIPANTS_ORIGIN;
  const username = process.env.SUB_POOL_TEST_PARTICIPANTS_USERNAME;
  const password = process.env.SUB_POOL_TEST_PARTICIPANTS_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable participant fixture required");
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
      accounts: createAccountsClient(origin!, fetcher), usage: createUsageClient(origin!, fetcher), participants: createParticipantsClient(origin!, fetcher) };
  }
  const admin = browser(), reader = browser();
  await assert.rejects(admin.participants.list({ after: "", search: "", limit: 100 }), e => e instanceof ORPCError && e.defined && e.status === 401);
  await admin.auth.login({ username, password });
  let calls = 0, unexpected = 0, phase: "old" | "facts" = "old", deny = false;
  const secret = "adm_sc_native_participant_fixture";
  const oldStamp = new Date().toISOString();
  let pStamp = oldStamp, qStamp = oldStamp;
  const nativeRow = (rowID: string, user: number, stamp: string, account: number) => `{"id":${rowID},"account_id":${account},"user_id":${user},"api_key_id":31,"model":"requested","upstream_model":"forwarded","created_at":${JSON.stringify(stamp)},"input_tokens":9007199254740993,"output_tokens":0,"total_cost":0.1,"actual_cost":0.2,"billing_type":0,"api_key":{"key":"private-key"},"ip_address":"private-ip"}`;
  const nativeUser = (user: number) => ({ id: user, username: "same-name", email: `member${user}@example.test`, role: "user", status: "active", created_at: "2020-01-01T00:00:00Z", deleted_at: null,
    notes: "private-upstream-note", api_keys: [{ key: "private-api-key" }], balance: 999, allowed_groups: [1] });
  const upstream = createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://upstream.invalid");
    calls++;
    if (request.method !== "GET" || request.headers["x-api-key"] !== secret || request.headers.cookie) {
      unexpected++; response.writeHead(403).end("private unexpected"); return;
    }
    const send = (data: unknown) => response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data }));
    if (url.pathname === "/api/v1/admin/users") {
      if (url.searchParams.get("include_subscriptions") !== "false") unexpected++;
      send({ items: [nativeUser(21), nativeUser(22)], total: 2, page: 1, page_size: 100 }); return;
    }
    const userMatch = /^\/api\/v1\/admin\/users\/(\d+)$/.exec(url.pathname);
    if (userMatch) {
      if (deny) { response.writeHead(403).end("private " + secret); return; }
      send(nativeUser(Number(userMatch[1]))); return;
    }
    const accountMatch = /^\/api\/v1\/admin\/accounts\/([12])$/.exec(url.pathname);
    if (accountMatch) {
      const account = Number(accountMatch[1]);
      send({ id: account, name: `Native account ${account}`, platform: "openai", type: "oauth", status: "active", schedulable: true, error_message: "",
        credentials: { chatgpt_account_id: `private-resource-${account}`, access_token: "private-token" }, extra: {}, created_at: timestamp, updated_at: timestamp }); return;
    }
    if (url.pathname === "/api/v1/admin/usage") {
      const account = Number(url.searchParams.get("account_id"));
      const rows = phase === "old" ? [nativeRow("8", 21, oldStamp, account)] : account === 1
        ? [nativeRow("9", 21, pStamp, 1), nativeRow("10", 22, qStamp, 1), nativeRow("9007199254740993", 999, pStamp, 1)]
        : [nativeRow("11", 21, pStamp, 2)];
      response.writeHead(200, { "Content-Type": "application/json" }).end(`{"code":0,"data":{"items":[${rows.join(",")}],"total":${rows.length},"page":1,"page_size":100}}`); return;
    }
    unexpected++; response.writeHead(403).end("private unexpected route");
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  assert.ok(address && typeof address === "object");
  try {
    const config = await admin.connection.get({});
    const saved = await admin.connection.save({ revision: config.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: secret, confirmation: "" });
    const c = saved.configuration;
    assert.ok(c.source_id);
    const first = await admin.accounts.adopt({ source_id: c.source_id, connection_revision: c.revision, upstream_id: "1", name: "Visible", monitoring: false, source_mode: "passive" });
    const second = await admin.accounts.adopt({ source_id: c.source_id, connection_revision: c.revision, upstream_id: "2", name: "Other", monitoring: false, source_mode: "passive" });
    assert.ok(first.account && second.account);
    const a = first.account, b = second.account;
    const range = { start_at: new Date(Date.now() - 60000).toISOString(), end_at: new Date(Date.now() + 3600000).toISOString() };
    const captureInput = (account: typeof a) => ({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: c.revision, ...range });
    const old = await admin.usage.capture(captureInput(a));
    const candidates = await admin.participants.candidates({ source_id: c.source_id, revision: c.revision, page: 1, search: "" });
    assert.deepEqual(candidates.users.map(u => u.upstream_id), ["21", "22"]);
    assert.equal(JSON.stringify(candidates).includes("private-"), false);
    const local = { source_id: c.source_id, connection_revision: c.revision, upstream_id: "21", name: "Native P", contact: "private-contact", notes: "private-local-note", enabled: true };
    const madeP = await admin.participants.create(local);
    const madeQ = await admin.participants.create({ ...local, upstream_id: "22", name: "Native Q" });
    assert.ok(madeP.participant && madeQ.participant);
    const p = madeP.participant, q = madeQ.participant;
    pStamp = p.binding.started_at; qStamp = q.binding.started_at; phase = "facts";
    const ca = await admin.usage.capture(captureInput(a)), cb = await admin.usage.capture(captureInput(b));
    const query = { capture_ids: [ca.id, cb.id], participant_id: "", model: "", api_key_id: "", grain: "hour" as const, after: "0", limit: 100 };
    const global = await admin.participants.usage(query);
    assert.equal(global.summary.request_count, "4"); assert.equal(global.assigned.request_count, "3"); assert.equal(global.unassigned?.request_count, "1");
    assert.equal(global.summary.metrics.actual_cost, "0.8"); assert.equal(global.summary.metrics.account_stats_cost, null); assert.equal(global.complete, false);
    assert.equal(global.oldest_capture_at, ca.captured_at); assert.equal(global.newest_capture_at, cb.captured_at);
    const oldView = await admin.participants.usage({ ...query, capture_ids: [old.id] });
    assert.equal(oldView.assigned.request_count, "0"); assert.equal(oldView.unassigned?.request_count, "1");
    await assert.rejects(admin.participants.usage({ ...query, capture_ids: [old.id, ca.id] }), e => e instanceof ORPCError && e.defined && e.status === 400);
    const member = await admin.users.create({ username: "participant-reader", password: "ReaderPassword123", enabled: true, features: ["pools", "usage"], account_ids: [a.id], participant_ids: [p.id] });
    await reader.auth.login({ username: member.username, password: "ReaderPassword123" });
    const before = calls;
    const page = await reader.participants.list({ after: "", search: "", limit: 1 });
    assert.deepEqual(page.participants.map(x => x.id), [p.id]); assert.equal(page.next_after, null);
    assert.equal(page.participants[0]?.contact, null); assert.equal(page.participants[0]?.notes, null); assert.equal(page.participants[0]?.binding.identity, null);
    await assert.rejects(reader.participants.get({ id: q.id }), e => e instanceof ORPCError && e.defined && e.status === 404);
    await assert.rejects(reader.participants.create({ ...local, upstream_id: "23" }), e => e instanceof ORPCError && e.defined && e.status === 403);
    await assert.rejects(reader.participants.usage(query), e => e instanceof ORPCError && e.defined && e.status === 404);
    const selection = await reader.participants.captures({ account_id: a.id, after: "0", limit: 100 });
    assert.deepEqual(selection.captures.map(x => x.id), [ca.id]); assert.equal(JSON.stringify(selection).includes("observed_count"), false);
    const own = await reader.participants.usage({ ...query, capture_ids: [ca.id], limit: 1 });
    assert.equal(own.summary.request_count, "1"); assert.equal(own.summary.metrics.actual_cost, "0.2"); assert.equal(own.unassigned, null);
    assert.equal(own.records[0]?.fact.id, "9"); assert.equal(own.records[0]?.attribution.participant_name, "Native P"); assert.equal(JSON.stringify(own).includes("private-"), false);
    await assert.rejects(reader.usage.view({ id: ca.id, after: "0", limit: 100, model: "", user_id: "", api_key_id: "", grain: "day" }), e => e instanceof ORPCError && e.defined && e.status === 404);
    assert.equal(calls, before);
    await assert.rejects(admin.participants.update({ ...local, id: p.id, revision: p.revision, upstream_id: "23" }), e => e instanceof ORPCError && e.defined && e.status === 409);
    await assert.rejects(admin.participants.delete({ id: p.id, revision: p.revision, confirmation: p.name }), e => e instanceof ORPCError && e.defined && e.status === 409);
    const renamed = await admin.participants.update({ ...local, id: p.id, revision: p.revision, name: "Inactive renamed", enabled: false });
    assert.ok(renamed.participant); assert.equal(renamed.participant.enabled, false); assert.equal(renamed.read, null);
    const historical = await reader.participants.usage({ ...query, capture_ids: [ca.id] });
    assert.equal(historical.records[0]?.attribution.participant_name, "Native P");
    const change = { id: member.id, revision: member.revision, username: member.username, enabled: member.enabled, features: member.features,
      participant_ids: member.participant_ids, new_password: "", account_ids: [a.id, b.id] };
    let current = await admin.users.update(change);
    const cross = await reader.participants.usage(query);
    assert.equal(cross.summary.request_count, "2"); assert.equal(cross.summary.metrics.actual_cost, "0.4"); assert.equal(cross.unassigned, null);
    current = await admin.users.update({ ...change, revision: current.revision, account_ids: [] });
    await assert.rejects(reader.participants.usage(query), e => e instanceof ORPCError && e.defined && e.status === 404);
    const unused = await admin.participants.create({ ...local, upstream_id: "23", name: "Unused" });
    assert.ok(unused.participant);
    await admin.participants.delete({ id: unused.participant.id, revision: unused.participant.revision, confirmation: unused.participant.name });
    assert.ok((await admin.participants.bindings({ id: unused.participant.id, after: "0", limit: 100 })).bindings[0]?.ended_at);
    deny = true;
    const unavailable = await admin.participants.create({ ...local, upstream_id: "24" });
    assert.equal(unavailable.participant, null); assert.equal(unavailable.read?.reason, "FORBIDDEN");
    assert.equal(unexpected, 0);
  } finally {
    await reader.auth.logout({}); await admin.auth.logout({});
    upstream.closeAllConnections();
    await new Promise<void>((resolve, reject) => upstream.close(error => error && (error as NodeJS.ErrnoException).code !== "ERR_SERVER_NOT_RUNNING" ? reject(error) : resolve()));
  }
});
