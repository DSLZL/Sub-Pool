import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { createAccountsClient, createAuthClient, createConnectionClient, createEstimatesClient, createMonitoringClient, createObservationsClient, createUsageClient, createUsersClient } from "./client";

test("real Go PostgreSQL native current and scoped estimate history use local data through oRPC", { skip: !process.env.SUB_POOL_TEST_ESTIMATES_ORIGIN, timeout: 210000 }, async () => {
  const origin = process.env.SUB_POOL_TEST_ESTIMATES_ORIGIN, username = process.env.SUB_POOL_TEST_ESTIMATES_USERNAME, password = process.env.SUB_POOL_TEST_ESTIMATES_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable estimate fixture required"); assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => { const request = new Request(input, init); request.headers.set("Origin", origin ?? ""); if (cookie) request.headers.set("Cookie", cookie); const response = await fetch(request, { redirect: "error" }); const issued = response.headers.get("set-cookie"); if (issued) cookie = issued.split(";")[0] ?? ""; return response; };
    return { auth: createAuthClient(origin ?? "", fetcher), connection: createConnectionClient(origin ?? "", fetcher), accounts: createAccountsClient(origin ?? "", fetcher), estimates: createEstimatesClient(origin ?? "", fetcher), monitoring: createMonitoringClient(origin ?? "", fetcher), observations: createObservationsClient(origin ?? "", fetcher), usage: createUsageClient(origin ?? "", fetcher), users: createUsersClient(origin ?? "", fetcher) };
  }
  const admin = browser(), reader = browser(); await admin.auth.login({ username, password });
  let calls = 0, accountReads = 0, unexpected = 0;
  const secret = "isolated-estimate-management-key", reset = new Date(Date.now() + 3600000).toISOString();
  const upstream = createServer((request, response) => {
    calls++; const url = new URL(request.url ?? "", "http://fixture.test");
    if (request.method !== "GET" || request.headers["x-api-key"] !== secret || request.headers.cookie || !["/api/v1/admin/accounts/1", "/api/v1/admin/usage"].includes(url.pathname)) { unexpected++; response.writeHead(400).end(); return; }
    if (url.pathname === "/api/v1/admin/usage") { response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data: { items: [], total: 0, page: 1, page_size: 100 } })); return; }
    accountReads++; response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data: { id: 1, name: "Native estimate account", platform: "openai", type: "oauth", status: "active", schedulable: true, created_at: "2020-01-01T00:00:00Z", updated_at: "2020-01-01T00:00:00Z", error_message: "", credentials: { chatgpt_account_id: "private-estimate-resource", access_token: "never expose" }, extra: { codex_usage_updated_at: new Date(Math.floor(Date.now() / 1000) * 1000).toISOString(), codex_5h_used_percent: accountReads * 10, codex_5h_window_minutes: 180, codex_5h_reset_at: reset, codex_7d_used_percent: 42 } } }));
  });
  upstream.listen(0, "127.0.0.1"); await once(upstream, "listening"); const address = upstream.address(); assert.ok(address && typeof address === "object");
  try {
    const initial = await admin.connection.get({}), saved = await admin.connection.save({ revision: initial.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: secret, confirmation: "" }); assert.ok(saved.configuration.source_id);
    const binding = { source_id: saved.configuration.source_id, connection_revision: saved.configuration.revision };
    const adopted = await admin.accounts.adopt({ ...binding, upstream_id: "1", name: "Estimated locally", monitoring: true, source_mode: "passive" }); assert.ok(adopted.account); const a = adopted.account;
    assert.equal((await admin.estimates.current({ account_id: a.id })).current_state, "not_configured");
    const readerUser = await admin.users.create({ username: "estimates-history-reader", password: "ReaderPassword123", enabled: true, features: ["estimates"], account_ids: [a.id], participant_ids: [] });
    await reader.auth.login({ username: readerUser.username, password: "ReaderPassword123" });
    const before = calls;
    const input = { account_id: a.id, after: "0", limit: 100, window_kind: "all" as const, period_end_at: "" };
    assert.deepEqual(await reader.estimates.history(input), { account_id: a.id, records: [], next_after: null });
    assert.equal((await reader.estimates.current({ account_id: a.id })).latest, null);
    await admin.users.update({ id: readerUser.id, revision: readerUser.revision, username: readerUser.username, enabled: true, new_password: "", features: ["estimates"], account_ids: [], participant_ids: [] });
    await assert.rejects(reader.estimates.history(input), error => error instanceof ORPCError && error.status === 404);
    assert.equal(calls, before);
    assert.equal(unexpected, 0);
  } finally { upstream.closeAllConnections(); await new Promise<void>(resolve => upstream.close(() => resolve())); }
});
