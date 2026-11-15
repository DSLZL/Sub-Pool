import assert from "node:assert/strict";
import { test } from "node:test";
import { createMaintenanceClient } from "@sub-pool/contracts/client";
import { maintenanceHistoryRulesVersion, maintenancePlan, maintenanceRulesVersion, type MaintenancePlan } from "@sub-pool/contracts/maintenance";
import { checkMaintenancePreflight, checkMaintenanceReceipt, maintenanceCanApply, maintenanceRange, maintenanceIssue, maintenanceResultText, maintenanceStatus, readMaintenancePlans } from "./maintenance-data";
import { proxyAPI } from "./api-proxy";

const account = "A".repeat(22), actor = "B".repeat(21) + "A";
const fixture = (id = "10") => maintenancePlan.parse({
  id, scope: { account_id: account, from: "2026-10-01T00:00:00Z", until: "2026-10-02T00:00:00Z" }, kind: "cycles", rules_version: maintenanceRulesVersion,
  basis: { fingerprint: "a".repeat(64), source_id: account, connection_revision: "2", account_revision: "3", policy_revision: "1", management_revision: "1", estimate_revision: "1", observation_head: "9007199254740993", capture_head: "0", estimate_head: "0", previous_applied_plan_id: null },
  preview: { scan_complete: true, observations: 1, excluded: 0, periods: [{ source_id: account, connection_revision: "2", account_revision: "3", source_mode: "passive", kind: "short", start_at: "2026-10-01T11:00:00Z", end_at: "2026-10-01T14:00:00Z", duration_seconds: "10800", basis: "native_reset_and_duration", observation_ids: ["9007199254740993"], first_source_at: "2026-10-01T12:00:00Z", last_source_at: "2026-10-01T12:00:00Z", first_used_percent: "0", last_used_percent: "0" }] },
  difference: { added: 1, removed: 0, changed: 0 }, issues: [{ code: "NO_PRIOR_PROJECTION", blocking: false, observation_id: null, window_kind: null }],
  limitations: ["RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "NO_FINANCIAL_RECALCULATION", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "RULE_UPGRADE_UNAVAILABLE"],
  created_by: actor, created_at: "2026-10-03T12:00:00Z", expires_at: "2026-10-03T12:10:00Z", state: "ready", reason: "READY", validity: "valid", requested_by: null, queued_at: null, started_at: null, finished_at: null, result_id: null,
});
function queued(): MaintenancePlan {
  const value = fixture();
  return maintenancePlan.parse({ ...value, state: "queued", reason: "QUEUED", validity: "pending", requested_by: actor, queued_at: "2026-10-03T12:01:00Z" });
}
function applied(): MaintenancePlan {
  return maintenancePlan.parse({ ...queued(), state: "applied", reason: "APPLIED", validity: "terminal", result_id: "10", started_at: "2026-10-03T12:01:01Z", finished_at: "2026-10-03T12:01:02Z" });
}

test("maintenance UTC range preserves exact saved bounds and a one-nanosecond half-open interval", () => {
  const now = Date.parse("2026-10-03T12:00:00.123Z"), from = "2026-10-01T00:00:00.123456789Z", until = "2026-10-01T00:00:00.123456790Z";
  assert.deepEqual(maintenanceRange(account, "cycles", from, until, now), { account_id: account, kind: "cycles", from, until, rules_version: maintenanceRulesVersion });
  for (const kind of ["rebuild", "upgrade"] as const) assert.equal(maintenanceRange(account, kind, from, until, now).rules_version, maintenanceHistoryRulesVersion);
  const value = fixture();
  checkMaintenancePreflight(maintenanceRange(account, value.kind, value.scope.from, value.scope.until, now), value, actor);
  assert.equal(maintenanceRange(account, "rebuild", "2024-02-29T00:00:00Z", "2024-03-01T00:00:00Z", now).from, "2024-02-29T00:00:00Z");
});

test("maintenance rejects a future bound even one nanosecond past the current instant", () => {
  const now = Date.parse("2026-10-03T12:00:00.123Z"), from = "2026-10-03T00:00:00Z";
  assert.equal(maintenanceRange(account, "cycles", from, "2026-10-03T12:00:00.123000000Z", now).until, "2026-10-03T12:00:00.123000000Z");
  assert.throws(() => maintenanceRange(account, "cycles", from, "2026-10-03T12:00:00.123000001Z", now), /不可在未来/);
});

test("maintenance rejects invalid, non-UTC, empty or reversed exact ranges and missing accounts", () => {
  const now = Date.parse("2026-10-03T12:00:00.123Z"), from = "2026-10-01T00:00:00Z", until = "2026-10-02T00:00:00Z";
  for (const invalid of ["", "2026-10-01", "2026-02-30T00:00:00Z", "2026-10-01T24:00:00Z", "2026-10-01T00:00:00+00:00", "2026-10-01T00:00:00.1234567890Z"]) {
    assert.throws(() => maintenanceRange(account, "rebuild", invalid, until, now));
    assert.throws(() => maintenanceRange(account, "rebuild", from, invalid, now));
  }
  assert.throws(() => maintenanceRange(account, "rebuild", until, from, now));
  assert.throws(() => maintenanceRange(account, "rebuild", from, from, now));
  assert.throws(() => maintenanceRange("", "rebuild", from, until, now));
});

test("maintenance reads all numeric plan pages across actual accounts before publishing newest-first rows", async () => {
  const other = "C".repeat(21) + "A", calls: string[] = [];
  const plans = await readMaintenancePlans({ plans: async input => {
    calls.push(input.account_id + ":" + input.after); assert.equal(input.limit, 1);
    if (input.account_id === other) return { account_id: other, plans: [{ ...fixture("11"), scope: { ...fixture().scope, account_id: other } }], next_after: null };
    return { account_id: account, plans: [fixture(input.after === "0" ? "9" : "10")], next_after: input.after === "0" ? "9" : null };
  } }, [{ id: account }, { id: other }]);
  assert.deepEqual(plans.map(plan => plan.id), ["11", "10", "9"]);
  assert.deepEqual(calls, [account + ":0", account + ":9", other + ":0"]);
  assert.equal(plans[1]?.preview.periods[0]?.first_used_percent, "0");
});

test("maintenance refuses partial, wrong-account, duplicate, backward and empty continuation pages", async () => {
  const first = { account_id: account, plans: [fixture("9")], next_after: "9" };
  for (const second of [
    { account_id: "C".repeat(21) + "A", plans: [fixture()], next_after: null },
    { account_id: account, plans: [fixture("9")], next_after: null },
    { account_id: account, plans: [fixture("8")], next_after: null },
    { account_id: account, plans: [], next_after: null },
    { account_id: account, plans: [fixture()], next_after: "9" },
    { account_id: account, plans: [fixture(), fixture("11")], next_after: null },
  ]) {
    await assert.rejects(readMaintenancePlans({ plans: async input => input.after === "0" ? first : second }, [{ id: account }]));
  }
  let calls = 0;
  await assert.rejects(readMaintenancePlans({ plans: async () => { if (++calls === 1) return first; throw new TypeError("continuation lost"); } }, [{ id: account }]));
  assert.equal(calls, 2);
  assert.deepEqual(await readMaintenancePlans({ plans: async () => ({ account_id: account, plans: [], next_after: null }) }, [{ id: account }]), []);
});

test("maintenance enforces total history count and memory bounds with valid full projection records", async () => {
  await assert.rejects(readMaintenancePlans({ plans: async input => {
    const id = String(BigInt(input.after) + 1n); return { account_id: account, plans: [fixture(id)], next_after: id };
  } }, [{ id: account }]), /2000/);
  const large = fixture(), period = large.preview.periods[0]; assert.ok(period);
  large.preview.observations = 2000; large.preview.periods = Array.from({ length: 4000 }, () => ({ ...period }));
  maintenancePlan.parse(large);
  let calls = 0;
  await assert.rejects(readMaintenancePlans({ plans: async input => {
    calls++; const id = String(BigInt(input.after) + 1n); return { account_id: account, plans: [{ ...large, id }], next_after: id };
  } }, [{ id: account }]), /32 MiB/);
  assert.ok(calls > 1 && calls < 30);
});

test("maintenance preflight receipt binds actor, kind, account, exact scope and rule without numeric rounding", () => {
  const value = fixture(), input = { ...value.scope, kind: value.kind, rules_version: value.rules_version };
  checkMaintenancePreflight({ ...input, from: "2026-10-01T00:00:00.000000000Z" }, value, actor);
  for (const changed of [
    { ...value, created_by: account }, { ...value, kind: "rebuild" as const }, { ...value, scope: { ...value.scope, account_id: actor } },
    { ...value, scope: { ...value.scope, until: "2026-10-02T00:00:00.000000001Z" } }, { ...value, rules_version: "unreviewed-rule" }, applied(),
  ]) assert.throws(() => checkMaintenancePreflight(input, changed, actor));
});

test("maintenance immutable receipt, actual lifecycle and expiry cannot manufacture an applied result", () => {
  const value = fixture(); checkMaintenanceReceipt(value, queued()); checkMaintenanceReceipt(value, applied());
  checkMaintenanceReceipt(value, { ...value, validity: "basis_changed" });
  assert.throws(() => checkMaintenanceReceipt(value, { ...queued(), basis: { ...value.basis, fingerprint: "b".repeat(64) } }));
  assert.throws(() => checkMaintenanceReceipt(value, { ...queued(), difference: { added: 2, removed: 0, changed: 0 } }));
  assert.equal(maintenanceStatus(queued()).label, "已排队"); assert.equal(queued().result_id, null);
  assert.equal(maintenanceStatus(applied()).label, "已应用");
  assert.equal(maintenanceCanApply(value, Date.parse(value.expires_at) - 1), true);
  assert.equal(maintenanceCanApply(value, Date.parse(value.expires_at)), false);
  assert.equal(maintenanceCanApply({ ...value, validity: "basis_changed" }, Date.parse(value.created_at)), false);
  assert.equal(maintenanceCanApply(queued(), Date.parse(value.created_at)), false);
  assert.equal(maintenanceStatus({ ...value, validity: "expired" }).label, "已失效");
  assert.match(maintenanceIssue({ code: "RULE_UPGRADE_UNAVAILABLE", blocking: true, observation_id: null, window_kind: null }).description, /尚未核准/);
  const blocked = maintenancePlan.parse({ ...value, state: "blocked", reason: "PREFLIGHT_BLOCKED", validity: "blocked",
    issues: [{ code: "REMOTE_OPERATION_UNRESOLVED", blocking: true, observation_id: null, window_kind: null }] });
  assert.equal(maintenanceCanApply(blocked, Date.parse(value.created_at)), false);
  assert.deepEqual(maintenanceIssue(blocked.issues[0]!), { title: "原生操作尚未核对", description: "计价、账号暂停或钱包操作正在执行或结果未知；请先核对原操作，再重新预检。" });
  assert.match(maintenanceIssue({ code: "ESTIMATE_EVIDENCE_MISSING", blocking: true, observation_id: null, window_kind: null, estimate_id: "9007199254740993" }).description, /未知.*估算 9007199254740993/);
});

test("maintenance applied messages distinguish retained conditional values, preserved past amounts and unapplied work", () => {
  const historical = maintenancePlan.parse({ ...applied(), kind: "rebuild", rules_version: maintenanceHistoryRulesVersion,
    preview: { ...applied().preview, financial: { calculation_rules: "gate-003-rules-v1", currency: "USD", scan_complete: true, records: 1,
      results: [{ estimate_id: "1", generated_at: "2026-10-01T12:00:00Z", original_rules: "gate-003-rules-v1", original_state: "non_identifying_delta", original_remaining: null,
        state: "non_identifying_delta", remaining: null, windows: [{ kind: "short", observation_ids: ["1", "2", "3"], state: "non_identifying_delta", capacity: null, remaining: null, range_min: null, range_max: null }] }] } },
    difference: { ...applied().difference, financial: { added: 1, removed: 0, changed: 0 } },
    limitations: ["RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "CONDITIONAL_SIMULATION_ONLY", "NOT_PAST_EXECUTED_AMOUNT", "MODEL_NOT_CALIBRATED", "ESTIMATE_REFERENCES_INCLUDED"],
  });
  assert.match(maintenanceResultText(historical), /条件复算版本 10.*原估算与过去已执行金额保留/);
  assert.doesNotMatch(maintenanceResultText(historical), /历史金额没有重算|0 USD/);
  assert.match(maintenanceResultText(applied()), /周期索引.*10.*历史金额没有重算/);
  assert.match(maintenanceResultText(queued()), /不代表已应用/);
});

const config = { backendOrigin: "http://127.0.0.1:18663", publicOrigin: "http://127.0.0.1:18665", allowLoopbackHTTP: true };
const cookie = "sub_pool_dev_session=" + "D".repeat(43);
const request = (action: string, method = "POST") => new Request(config.publicOrigin + "/api/v1/maintenance/" + action, { method,
  headers: { Origin: config.publicOrigin, Cookie: cookie, "Content-Type": "application/json" }, ...(method === "POST" ? { body: "{}" } : {}) });

test("maintenance proxy exposes exactly four fixed cookie-only client operations and validates replies", async () => {
  const seen: string[] = [];
  const client = createMaintenanceClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init); browser.headers.set("Cookie", cookie); browser.headers.set("Origin", config.publicOrigin);
    browser.headers.set("Authorization", "Bearer forged"); browser.headers.set("Forwarded", "for=203.0.113.1"); browser.headers.set("X-Forwarded-For", "203.0.113.1");
    return proxyAPI(browser, config, async (url, forwarded) => {
      const action = String(url).slice((config.backendOrigin + "/api/v1/maintenance/").length); seen.push(action);
      assert.equal(String(url), config.backendOrigin + "/api/v1/maintenance/" + action);
      const headers = new Headers(forwarded?.headers);
      assert.equal(headers.get("Cookie"), cookie); assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("Forwarded"), null); assert.equal(headers.get("X-Forwarded-For"), null);
      assert.equal(forwarded?.method, "POST"); assert.equal(forwarded?.cache, "no-store"); assert.equal(forwarded?.redirect, "error");
      return Response.json(action === "plans" ? { account_id: account, plans: [fixture()], next_after: null } : action === "apply" ? queued() : fixture());
    });
  });
  const value = fixture();
  await client.preflight({ ...value.scope, kind: value.kind, rules_version: value.rules_version });
  await client.plans({ account_id: account, after: "0", limit: 1 }); await client.plan({ id: "10" });
  assert.equal((await client.apply({ id: "10", basis: value.basis.fingerprint, confirmation: true })).state, "queued");
  assert.deepEqual(seen, ["preflight", "plans", "plan", "apply"]);
});

test("maintenance proxy rejects undeclared/cross-origin/query/malformed/secret responses and unrelated errors", async () => {
  let calls = 0; const fetcher = async () => { calls++; return Response.json(fixture()); };
  assert.equal((await proxyAPI(request("cancel"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("plan", "GET"), config, fetcher)).status, 405);
  assert.equal((await proxyAPI(request("plan?x=1"), config, fetcher)).status, 400);
  const cross = request("apply"); cross.headers.set("Origin", "https://other.example");
  assert.equal((await proxyAPI(cross, config, fetcher)).status, 403); assert.equal(calls, 0);
  for (const value of [{ ...fixture(), secret: "private-output" }, { ...fixture(), state: "applied", result_id: "10" }]) {
    const response = await proxyAPI(request("plan"), config, async () => Response.json(value));
    assert.equal(response.status, 502); assert.ok(!(await response.text()).includes("private-output"));
  }
  const response = await proxyAPI(request("plan"), config, async () => Response.json({ defined: true, code: "SELF_SOURCE_PROTECTED", status: 409, data: { retry_at: null } }, { status: 409 }));
  assert.equal(response.status, 502);
  const denied = await proxyAPI(request("plan"), config, async () => Response.json({ defined: true, code: "SOURCE_BLOCKED", status: 403, data: { retry_at: null } }, { status: 403 }));
  assert.equal(denied.status, 403);
});

test("maintenance proxy accepts a bounded real full projection beyond the ordinary proxy response cap", async () => {
  const value = fixture(), period = value.preview.periods[0]; assert.ok(period);
  value.preview.observations = 1000; value.preview.periods = Array.from({ length: 1000 }, () => ({ ...period }));
  maintenancePlan.parse(value); assert.ok(JSON.stringify(value).length > 262144);
  const response = await proxyAPI(request("plan"), config, async () => Response.json(value));
  assert.equal(response.status, 200); assert.equal(response.headers.get("Cache-Control"), "no-store");
});

test("maintenance lost confirmation transport is not retried by the proxy or current oRPC client", async () => {
  let calls = 0;
  const client = createMaintenanceClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init); browser.headers.set("Cookie", cookie); browser.headers.set("Origin", config.publicOrigin);
    return proxyAPI(browser, config, async () => { calls++; throw new TypeError("committed response lost"); });
  });
  await assert.rejects(client.apply({ id: "10", basis: fixture().basis.fingerprint, confirmation: true }));
  assert.equal(calls, 1);
});
