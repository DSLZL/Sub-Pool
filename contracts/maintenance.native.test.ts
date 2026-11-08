import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { setTimeout } from "node:timers/promises";

import { ORPCError } from "@orpc/client";

import { createAccountsClient, createAuthClient, createConnectionClient, createMaintenanceClient, createMonitoringClient, createObservationsClient, createUsersClient } from "./client";
import { maintenanceRulesVersion } from "./maintenance";

test("real Go PostgreSQL offline maintenance plans, explicit queued results, lost-response reconciliation and current admin scope through oRPC", { skip: !process.env.SUB_POOL_TEST_MAINTENANCE_ORIGIN, timeout: 45000 }, async t => {
  const origin = process.env.SUB_POOL_TEST_MAINTENANCE_ORIGIN, username = process.env.SUB_POOL_TEST_MAINTENANCE_USERNAME, password = process.env.SUB_POOL_TEST_MAINTENANCE_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable maintenance fixture required"); assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  const target = origin;
  let loseApply = false, applyRequests = 0;
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init); request.headers.set("Origin", target); if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" }); const issued = response.headers.get("set-cookie"); if (issued) cookie = issued.split(";")[0] ?? "";
      if (request.url.endsWith("/maintenance/apply")) { applyRequests++; if (loseApply && response.ok) { loseApply = false; await response.text(); throw new TypeError("owned fixture dropped the committed queue response"); } }
      return response;
    };
    return { auth: createAuthClient(target, fetcher), connection: createConnectionClient(target, fetcher), accounts: createAccountsClient(target, fetcher), maintenance: createMaintenanceClient(target, fetcher), monitoring: createMonitoringClient(target, fetcher), observations: createObservationsClient(target, fetcher), users: createUsersClient(target, fetcher) };
  }
  const admin = browser(); await admin.auth.login({ username, password });
  let calls = 0, unexpected = 0;
  const secret = "owned-maintenance-management-fixture", reset = new Date(Date.now() + 3600000).toISOString();
  const upstream = createServer((request, response) => {
    calls++;
    if (request.method !== "GET" || request.url !== "/api/v1/admin/accounts/1" || request.headers["x-api-key"] !== secret || request.headers.cookie) { unexpected++; response.writeHead(400).end(); return; }
    response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data: { id: 1, name: "Owned maintenance account", platform: "openai", type: "oauth", status: "active", schedulable: true, created_at: "2020-01-01T00:00:00Z", updated_at: "2020-01-01T00:00:00Z", error_message: "", credentials: { chatgpt_account_id: "owned-maintenance-resource", access_token: "never persist" }, extra: { codex_usage_updated_at: new Date(Date.now() - 1000).toISOString(), codex_5h_used_percent: 0, codex_5h_window_minutes: 180, codex_5h_reset_at: reset } } }));
  });
  upstream.listen(0, "127.0.0.1"); await once(upstream, "listening"); const address = upstream.address(); assert.ok(address && typeof address === "object");
  try {
    const initial = await admin.connection.get({}), saved = await admin.connection.save({ revision: initial.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: secret, confirmation: "" }); assert.ok(saved.configuration.source_id);
    const binding = { source_id: saved.configuration.source_id, connection_revision: saved.configuration.revision };
    const adopted = await admin.accounts.adopt({ ...binding, upstream_id: "1", name: "Native maintenance", monitoring: true, source_mode: "passive" }); assert.ok(adopted.account); const account = adopted.account;
    const run = await admin.monitoring.collect({ ...binding, all: false, account_ids: [account.id] });
    let collected = false;
    for (const until = Date.now() + 10000; Date.now() < until;) { const status = await admin.monitoring.run({ id: run.id, after: "0", limit: 100 }); if (status.run.state === "completed") { assert.equal(status.jobs[0]?.state, "succeeded"); collected = true; break; } await setTimeout(50); }
    assert.ok(collected, "actual background collection must persist an owned raw fact");
    const query = { account_id: account.id, source_id: binding.source_id, source_mode: "passive" as const, validity: "all" as const, start_at: "1970-01-01T00:00:00Z", end_at: "9998-12-31T00:00:00Z", after: "0", limit: 100 };
    const facts = await admin.observations.list(query); assert.equal(facts.records.length, 1); const fact = facts.records[0]; assert.ok(fact); await setTimeout(5);
    const input = { account_id: account.id, from: fact.snapshot.received_at, until: new Date().toISOString(), kind: "rebuild" as const, rules_version: maintenanceRulesVersion };
    const before = calls;
    const plan = await admin.maintenance.preflight(input), stale = await admin.maintenance.preflight(input);
    assert.equal(plan.state, "ready"); assert.equal(plan.validity, "valid"); assert.equal(plan.result_id, null); assert.equal(plan.preview.periods[0]?.duration_seconds, "10800"); assert.equal(plan.preview.periods[0]?.first_used_percent, "0");
    const queued = await admin.maintenance.apply({ id: plan.id, basis: plan.basis.fingerprint, confirmation: true }); assert.equal(queued.state, "queued"); assert.equal(queued.result_id, null);
    async function completed(id: string) { for (const until = Date.now() + 10000; Date.now() < until;) { const current = await admin.maintenance.plan({ id }); if (current.state === "applied") return current; assert.ok(["queued", "running"].includes(current.state)); await setTimeout(50); } throw new Error("owned maintenance background result not observed"); }
    const applied = await completed(plan.id); assert.equal(applied.result_id, plan.id); assert.ok(applied.finished_at);
    assert.equal((await admin.maintenance.plan({ id: stale.id })).validity, "basis_changed");
    await assert.rejects(admin.maintenance.apply({ id: stale.id, basis: stale.basis.fingerprint, confirmation: true }), error => error instanceof ORPCError && error.status === 409);
    const next = await admin.maintenance.preflight(input); assert.equal(next.basis.previous_applied_plan_id, plan.id); assert.deepEqual(next.difference, { added: 0, removed: 0, changed: 0 });
    const beforeLoss = applyRequests; loseApply = true;
    await assert.rejects(admin.maintenance.apply({ id: next.id, basis: next.basis.fingerprint, confirmation: true })); assert.equal(applyRequests, beforeLoss + 1);
    const reconciled = await completed(next.id); assert.equal(reconciled.result_id, next.id); assert.equal(applyRequests, beforeLoss + 1);
    const duplicate = await admin.maintenance.apply({ id: next.id, basis: next.basis.fingerprint, confirmation: true }); assert.equal(duplicate.result_id, next.id);
    const blocked = await admin.maintenance.preflight({ ...input, kind: "upgrade", rules_version: "unreviewed-v2" }); assert.equal(blocked.state, "blocked"); assert.equal(blocked.preview.periods.length, 0); assert.ok(blocked.issues.some(issue => issue.blocking && issue.code === "RULE_UPGRADE_UNAVAILABLE"));
    const user = await admin.users.create({ username: "maintenance.reader", password: "OwnedReaderPassword123", enabled: true, features: ["observations", "security"], account_ids: [account.id], participant_ids: [] });
    const reader = browser(); await reader.auth.login({ username: user.username, password: "OwnedReaderPassword123" });
    await assert.rejects(reader.maintenance.plan({ id: plan.id }), error => error instanceof ORPCError && error.status === 403);
    const history = await admin.maintenance.plans({ account_id: account.id, after: "0", limit: 20 }); assert.equal(history.plans.filter(p => p.state === "applied").length, 2);
    assert.deepEqual(await admin.observations.list(query), facts); assert.equal(calls, before); assert.equal(unexpected, 0);
    t.diagnostic("owned account " + account.id + "; applied plans " + plan.id + "," + next.id + "; lost response reconciled without replay; upstream calls unchanged");
  } finally { upstream.close(); await once(upstream, "close"); }
});
