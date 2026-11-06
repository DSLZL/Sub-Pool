import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { accountModels, managedAccount, managedView } from "./accounts";
import { createAccountsClient, createAuthClient, createConnectionClient, createUsersClient } from "./client";

const id = "a".repeat(21) + "A";
const stamp = "2026-11-06T00:00:00Z";
const read = { id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 };
const initial = {
  id, revision: "1", source_id: id, upstream_id: "1", name: "Local", monitoring: true, source_mode: "active", source_current: true,
  connection_available: true, observed_connection_revision: "1",
  identity_state: "unverified", collection_state: "unverified", read,
  snapshot: { account: { upstream_id: "1", name: "Remote", platform: "openai", type: "oauth", status: "active", schedulable: true,
    eligibility: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" },
  runtime: { last_used_at: null, rate_limited_at: null, rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, error_present: null } },
  last_attempt_at: stamp, observed_at: stamp, created_at: stamp, saved_at: stamp,
};

test("managed schemas retain unknown facts and reject identity or access overclaims", () => {
  assert.ok(managedAccount.safeParse(initial).success);
  assert.equal(managedView.safeParse({ account: initial, connection_revision: "2" }).success, false);
  assert.ok(managedView.safeParse({ account: { ...initial, collection_state: "connection_changed" }, connection_revision: "2" }).success);
  assert.ok(managedView.safeParse({ account: { ...initial, connection_available: false, collection_state: "connection_unavailable" }, connection_revision: "2" }).success);
  for (const value of [{ ...initial, resource_fingerprint: "private" }, { ...initial, collection_state: "awaiting_collector" },
    { ...initial, upstream_id: "2" }, { ...initial, identity_state: "verified" }]) {
    assert.equal(managedAccount.safeParse(value).success, false);
  }
  const models = { account_id: id, source_id: id, connection_revision: "1", checked_at: stamp, models: [], mapping: [], passthrough: null,
    mapping_mode: "empty", access_proven: false, account_read: read, catalog_read: read };
  assert.ok(accountModels.safeParse(models).success);
  assert.equal(accountModels.safeParse({ ...models, access_proven: true }).success, false);
});

test("accounts client validates both directions and never replays uncertain local writes", async () => {
  let calls = 0;
  const client = createAccountsClient("https://example.test", async () => { calls++; throw new TypeError("Connection lost"); });
  await assert.rejects(client.update({ id, revision: "1", name: "Local", monitoring: false, source_mode: "active" }));
  assert.equal(calls, 1);
  await assert.rejects(client.update({ id, revision: "1", name: "", monitoring: false, source_mode: "active" }));
  assert.equal(calls, 1);
  const extra = createAccountsClient("https://example.test", async () => Response.json({ account: { ...initial, credentials: "never" }, connection_revision: "1" }));
  await assert.rejects(extra.get({ id }));
});

test("real Go PostgreSQL managed account scope, monitoring and advisory models through oRPC", {
  skip: !process.env.SUB_POOL_TEST_ACCOUNTS_ORIGIN,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_ACCOUNTS_ORIGIN;
  const username = process.env.SUB_POOL_TEST_ACCOUNTS_USERNAME;
  const password = process.env.SUB_POOL_TEST_ACCOUNTS_PASSWORD;
  assert.ok(origin && username && password, "explicit isolated accounts fixture required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  const transport = () => {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      request.headers.set("Origin", origin);
      if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" });
      const issued = response.headers.get("set-cookie");
      if (issued) cookie = issued.split(";")[0] ?? "";
      return response;
    };
    return fetcher;
  };
  const fetcher = transport();
  const auth = createAuthClient(origin, fetcher);
  const accounts = createAccountsClient(origin, fetcher);
  const connections = createConnectionClient(origin, fetcher);
  const users = createUsersClient(origin, fetcher);
  await assert.rejects(accounts.list({ after: "", search: "", limit: 100 }), error => error instanceof ORPCError && error.status === 401);
  await auth.login({ username, password });
  let calls = 0;
  let denied = false;
  let changed = false;
  const upstream = createServer((request, response) => {
    calls++;
    assert.equal(request.method, "GET");
    assert.equal(request.headers.cookie, undefined);
    assert.equal(request.headers["x-api-key"], "adm_sc_accounts_test_only");
    const path = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
    response.writeHead(denied ? 403 : 200, { "content-type": "application/json" });
    if (denied) return response.end(JSON.stringify({ message: "private-failure" }));
    if (path.endsWith("/models")) return response.end(JSON.stringify({ code: 0, data: [{ id: "candidate", display_name: "Advisory candidate", secret: "private-model" }] }));
    const remoteID = path.split("/").at(-1);
    const full = { id: 0, name: "Remote", platform: "openai", type: "oauth", status: "active", schedulable: true,
      credentials: { access_token: "private-token", chatgpt_account_id: changed ? "private-changed-resource" : "private-resource", model_mapping: { candidate: "target" } },
      extra: { openai_passthrough: true }, created_at: stamp, updated_at: stamp, error_message: "", last_used_at: null };
    if (remoteID === "2") return response.end(JSON.stringify({ code: 0, data: { id: 2, name: "Restricted", platform: "openai", type: "oauth", status: "active", schedulable: false, credentials: { model_mapping: {} } } }));
    response.end(JSON.stringify({ code: 0, data: full }).replace('"id":0', `"id":${remoteID}`));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  assert.ok(address && typeof address === "object");
  const upstreamOrigin = `http://127.0.0.1:${address.port}`;
  try {
    const current = await connections.get({});
    const saved = await connections.save({ revision: current.revision, base_url: upstreamOrigin, credential_action: "replace", credential: "adm_sc_accounts_test_only", confirmation: "" });
    let source = saved.configuration;
    assert.ok(source.source_id);
    const adopted = await accounts.adopt({ source_id: source.source_id, connection_revision: source.revision, upstream_id: "9223372036854775807", name: "Local name", monitoring: true, source_mode: "active" });
    let account = adopted.account;
    assert.ok(account);
    assert.equal(account.upstream_id, "9223372036854775807");
    assert.equal(account.collection_state, "awaiting_collector");
    assert.ok(!JSON.stringify(account).includes("private-"));
    const hidden = await accounts.adopt({ source_id: source.source_id, connection_revision: source.revision, upstream_id: "2", name: "Hidden", monitoring: true, source_mode: "passive" });
    assert.ok(hidden.account);
    assert.equal(hidden.account.identity_state, "unverified");
    const member = await users.create({ username: "account.reader", password: "ReaderPassword123", enabled: true, features: ["accounts"], account_ids: [account.id], participant_ids: [] });
    const memberFetcher = transport();
    const memberAuth = createAuthClient(origin, memberFetcher);
    const memberAccounts = createAccountsClient(origin, memberFetcher);
    await memberAuth.login({ username: member.username, password: "ReaderPassword123" });
    const page = await memberAccounts.list({ after: "", search: "", limit: 1 });
    assert.deepEqual(page.accounts.map(item => item.id), [account.id]);
    assert.equal(page.next_after, null);
    const before = calls;
    await assert.rejects(memberAccounts.get({ id: hidden.account.id }), error => error instanceof ORPCError && error.status === 404);
    await assert.rejects(memberAccounts.models({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: source.revision }), error => error instanceof ORPCError && error.status === 403);
    assert.equal(calls, before);
    const paused = await accounts.update({ id: account.id, revision: account.revision, name: "Renamed", monitoring: false, source_mode: "passive" });
    assert.equal(paused.account.collection_state, "paused");
    assert.equal(paused.account.observed_at, account.observed_at);
    assert.equal(calls, before);
    await assert.rejects(accounts.update({ id: account.id, revision: account.revision, name: "Stale", monitoring: true, source_mode: "active" }), error => error instanceof ORPCError && error.status === 409);
    account = (await accounts.update({ id: account.id, revision: paused.account.revision, name: "Renamed", monitoring: true, source_mode: "active" })).account;
    const models = await accounts.models({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: source.revision });
    assert.equal(models.mapping_mode, "passthrough");
    assert.equal(models.access_proven, false);
    assert.equal(models.models[0]?.id, "candidate");
    assert.ok(!JSON.stringify(models).includes("private-"));
    const observedRevision = account.observed_connection_revision;
    source = (await connections.save({ revision: source.revision, base_url: upstreamOrigin, credential_action: "clear", credential: "", confirmation: "" })).configuration;
    const cleared = await memberAccounts.get({ id: account.id });
    assert.equal(cleared.account.collection_state, "connection_unavailable");
    assert.equal(cleared.account.connection_available, false);
    assert.equal(cleared.account.observed_connection_revision, observedRevision);
    const beforeClearRead = calls;
    await assert.rejects(accounts.refresh({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: source.revision }), error => error instanceof ORPCError && error.status === 409);
    assert.equal(calls, beforeClearRead);
    source = (await connections.save({ revision: source.revision, base_url: upstreamOrigin, credential_action: "replace", credential: "adm_sc_accounts_test_only", confirmation: "" })).configuration;
    assert.equal((await accounts.get({ id: account.id })).account.collection_state, "connection_changed");
    denied = true;
    const failed = await accounts.refresh({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: source.revision });
    assert.equal(failed.account.read.reason, "FORBIDDEN");
    assert.equal(failed.account.collection_state, "read_unavailable");
    assert.equal(failed.account.observed_at, account.observed_at);
    assert.equal(failed.account.observed_connection_revision, observedRevision);
    denied = false;
    account = (await accounts.refresh({ id: account.id, revision: failed.account.revision, source_id: account.source_id, connection_revision: source.revision })).account;
    assert.equal(account.collection_state, "awaiting_collector");
    assert.equal(account.observed_connection_revision, source.revision);
    changed = true;
    const conflict = await accounts.refresh({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: source.revision });
    assert.equal(conflict.account.identity_state, "conflict");
    await connections.save({ revision: source.revision, base_url: upstreamOrigin + "/changed", credential_action: "replace", credential: "adm_sc_accounts_test_only", confirmation: "更换地址" });
    const beforeChangeRead = calls;
    await assert.rejects(accounts.refresh({ id: account.id, revision: conflict.account.revision, source_id: account.source_id, connection_revision: source.revision }), error => error instanceof ORPCError && error.status === 409);
    assert.equal(calls, beforeChangeRead);
    assert.equal((await memberAccounts.get({ id: account.id })).account.collection_state, "source_changed");
    await memberAuth.logout({});
    await auth.logout({});
    await assert.rejects(accounts.get({ id: account.id }), error => error instanceof ORPCError && error.status === 401);
  } finally {
    await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});
