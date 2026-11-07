import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { setTimeout } from "node:timers/promises";
import { ORPCError } from "@orpc/client";
import { createAccountsClient, createAuthClient, createConnectionClient, createEstimatesClient, createMonitoringClient, createObservationsClient, createUsageClient, createUsersClient } from "./client";

test("real Go PostgreSQL conditional estimates, producer updates, manual/current distinction and scoped immutable history through oRPC", { skip: !process.env.SUB_POOL_TEST_ESTIMATES_ORIGIN, timeout: 210000 }, async t => {
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
    const config = { ...binding, account_id: a.id, account_revision: a.revision, revision: "1", plan: "empirical_min_v1" as const, cost_basis: "standard_usd" as const, keep_ratio: "0.8", premise: "Explicit local scenario only; independent complete interval costs are absent", windows: ["short" as const, "seven_day" as const] };
    const first = await admin.estimates.configure(config); assert.equal(first.configuration.revision, "2"); assert.equal(first.remaining, null);
    for (let index = 0; index < 3; index++) {
      const run = await admin.monitoring.collect({ ...binding, all: false, account_ids: [a.id] });
      let retryAt: string | null = null, complete = false;
      for (const until = Date.now() + 15000; Date.now() < until;) { const page = await admin.monitoring.run({ id: run.id, after: "0", limit: 100 }); if (page.run.state === "completed") { assert.equal(page.jobs[0]?.state, "succeeded"); retryAt = page.jobs[0]?.retry_after ?? null; complete = true; break; } await setTimeout(100); }
      assert.ok(complete && retryAt); t.diagnostic("actual collector receipt " + (index + 1) + "/3 persisted");
      if (index < 2) { const delay = Date.parse(retryAt) - Date.now(); assert.ok(delay < 65000); await setTimeout(Math.max(0, delay) + 50); }
    }
    let current = await admin.estimates.current({ account_id: a.id }); assert.ok(current.latest); assert.equal(current.latest.trigger, "observation");
    assert.equal(current.latest.windows.find(w => w.kind === "short")?.state, "evidence_incomplete"); assert.equal(current.latest.windows.find(w => w.kind === "short")?.period?.duration_seconds, "10800");
    assert.equal(current.latest.windows.find(w => w.kind === "seven_day")?.state, "period_unknown"); assert.equal(current.current_remaining, null); assert.equal(current.latest.complete, false); assert.equal(current.latest.settlement_eligible, false);
    const facts = await admin.observations.list({ account_id: a.id, source_id: binding.source_id, source_mode: "passive", validity: "all", start_at: new Date(Date.now() - 86400000).toISOString(), end_at: new Date(Date.now() + 86400000).toISOString(), after: "0", limit: 100 }); const [left, middle, right] = facts.records; assert.ok(left && middle && right && facts.records.length === 3);
    const range = await admin.observations.start({ account_id: a.id, revision: "1", start_observation_id: left.id, end_observation_id: middle.id, window_kind: "short", reason: "Earlier selected sample range with later official use retained" }); assert.equal(range.recalculation_state, "recorded");
    current = await admin.estimates.current({ account_id: a.id }); assert.ok(current.latest); const manual = current.latest.windows.find(w => w.kind === "short"); assert.equal(manual?.basis, "manual"); assert.equal(manual?.manual_end_at, middle.snapshot.source_at); assert.equal(manual?.current_observation_id, right.id); assert.equal(manual?.state, "insufficient_samples");
    const capture = await admin.usage.capture({ id: a.id, revision: a.revision, ...binding, start_at: new Date(Date.now() - 3600000).toISOString(), end_at: new Date().toISOString() }); assert.equal(capture.state, "exhausted"); assert.equal(capture.complete, false);
    current = await admin.estimates.current({ account_id: a.id }); assert.ok(current.latest); assert.equal(current.latest.trigger, "usage"); assert.equal(current.latest.capture_head, capture.id);
    const before = calls; const user = await admin.users.create({ username: "estimate-reader", password: "ReaderPassword123", enabled: true, features: ["estimates"], account_ids: [a.id], participant_ids: [] }); await reader.auth.login({ username: user.username, password: "ReaderPassword123" });
    assert.equal((await reader.estimates.record({ id: first.id })).id, first.id); await assert.rejects(reader.estimates.configure(config), e => e instanceof ORPCError && e.status === 403); await assert.rejects(admin.estimates.configure(config), e => e instanceof ORPCError && e.status === 409);
    let latest = current.latest;
    for (let n = 0; n < 6; n++) latest = await admin.estimates.recompute({ account_id: a.id, revision: "2", latest_id: latest.id });
    await assert.rejects(admin.estimates.recompute({ account_id: a.id, revision: "2", latest_id: current.latest.id }), e => e instanceof ORPCError && e.status === 409);
    let after = "0", seen = 0; for (;;) { const page = await reader.estimates.history({ account_id: a.id, after, limit: 2, window_kind: "all", period_end_at: "" }); seen += page.records.length; if (page.next_after === null) break; after = page.next_after; } assert.ok(seen >= 10);
    assert.deepEqual(await admin.estimates.record({ id: first.id }), first);
    assert.equal(JSON.stringify(latest).includes("private-estimate-resource"), false); assert.equal(JSON.stringify(latest).includes(secret), false);
    await admin.users.update({ id: user.id, revision: user.revision, username: user.username, enabled: true, new_password: "", features: ["estimates"], account_ids: [], participant_ids: [] });
    for (const id of [first.id, "9223372036854775807"]) await assert.rejects(reader.estimates.record({ id }), e => e instanceof ORPCError && e.status === 404);
    await reader.auth.logout({}); await assert.rejects(reader.estimates.current({ account_id: a.id }), e => e instanceof ORPCError && e.status === 401);
    assert.equal(calls, before); assert.equal(calls, 5); assert.equal(unexpected, 0);
  } finally { upstream.closeAllConnections(); await new Promise<void>(resolve => upstream.close(() => resolve())); }
});
