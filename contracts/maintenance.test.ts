import assert from "node:assert/strict";
import { test } from "node:test";

import { createMaintenanceClient } from "./client";
import { maintenanceContract, maintenanceInput, maintenanceOutputs, maintenancePlan, maintenanceRulesVersion, maintenanceVersion } from "./maintenance";

const id = "AAAAAAAAAAAAAAAAAAAAAA";
const fixture = () => ({
  id: "10", scope: { account_id: id, from: "2026-10-01T12:00:00Z", until: "2026-10-01T12:01:00Z" }, kind: "rebuild", rules_version: maintenanceRulesVersion,
  basis: { fingerprint: "a".repeat(64), source_id: id, connection_revision: "2", account_revision: "3", policy_revision: "1", management_revision: "1", estimate_revision: "1", observation_head: "9007199254740993", capture_head: "0", estimate_head: "0", previous_applied_plan_id: null },
  preview: { scan_complete: true, observations: 1, excluded: 0, periods: [{ source_id: id, connection_revision: "2", account_revision: "3", source_mode: "passive", kind: "short", start_at: "2026-10-01T11:00:00Z", end_at: "2026-10-01T14:00:00Z", duration_seconds: "10800", basis: "native_reset_and_duration", observation_ids: ["9007199254740993"], first_source_at: "2026-10-01T12:00:00Z", last_source_at: "2026-10-01T12:00:00Z", first_used_percent: "0", last_used_percent: "0" }] },
  difference: { added: 1, removed: 0, changed: 0 }, issues: [{ code: "NO_PRIOR_PROJECTION", blocking: false, observation_id: null, window_kind: null }],
  limitations: ["RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "NO_FINANCIAL_RECALCULATION", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "RULE_UPGRADE_UNAVAILABLE"],
  created_by: id, created_at: "2026-10-01T12:02:00Z", expires_at: "2026-10-01T12:12:00Z", state: "ready", reason: "READY", validity: "valid", requested_by: null as string | null,
  queued_at: null as string | null, started_at: null as string | null, finished_at: null as string | null, result_id: null as string | null,
});

function firstPeriod(periods: ReturnType<typeof fixture>["preview"]["periods"]) {
  const period = periods[0];
  assert.ok(period);
  return period;
}

test("maintenance preserves actual native periods, exact samples and explicit financial limits", () => {
  const value = maintenancePlan.parse(fixture());
  assert.equal(maintenanceVersion, "maintenance-v3");
  assert.equal(firstPeriod(value.preview.periods).duration_seconds, "10800");
  assert.equal(firstPeriod(value.preview.periods).first_used_percent, "0");
  assert.deepEqual(firstPeriod(value.preview.periods).observation_ids, ["9007199254740993"]);
  assert.equal(value.result_id, null);
  assert.ok(value.limitations.includes("NO_FINANCIAL_RECALCULATION"));
  assert.ok(maintenanceInput.safeParse({ ...value.scope, kind: "cycles", rules_version: maintenanceRulesVersion }).success);
  assert.ok(maintenanceInput.safeParse({ ...value.scope, kind: "upgrade", rules_version: "unreviewed-v2" }).success);
  for (const extra of [{ from: value.scope.until }, { from: "2026-02-30T00:00:00Z" }, { until: "2026-10-01T12:01:00+00:00" }, { wallet_target: "1" }]) {
    assert.equal(maintenanceInput.safeParse({ ...value.scope, kind: "rebuild", rules_version: maintenanceRulesVersion, ...extra }).success, false);
  }
});

test("maintenance rejects malformed bounds, exact out-of-range percentages and fabricated successful states", () => {
  for (const change of [
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).duration_seconds = "18000"; },
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).duration_seconds = "not-a-number"; },
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).last_source_at = firstPeriod(v.preview.periods).end_at; },
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).last_used_percent = "100.000000000000000001"; },
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).last_used_percent = "1e999999999"; },
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).last_used_percent = "1/2"; },
    (v: ReturnType<typeof fixture>) => { firstPeriod(v.preview.periods).observation_ids.push("9007199254740993"); },
    (v: ReturnType<typeof fixture>) => { v.preview.scan_complete = false; },
    (v: ReturnType<typeof fixture>) => { v.rules_version = "unreviewed-v2"; },
    (v: ReturnType<typeof fixture>) => { v.kind = "upgrade"; },
    (v: ReturnType<typeof fixture>) => { v.state = "applied"; v.result_id = v.id; },
  ]) {
    const value = fixture(); change(value);
    assert.doesNotThrow(() => maintenancePlan.safeParse(value));
    assert.equal(maintenancePlan.safeParse(value).success, false);
  }
  const blocked = fixture();
  blocked.kind = "upgrade"; blocked.rules_version = "unreviewed-v2"; blocked.state = "blocked"; blocked.reason = "PREFLIGHT_BLOCKED"; blocked.validity = "blocked";
  blocked.preview.periods = [];
  blocked.issues.push({ code: "RULE_UPGRADE_UNAVAILABLE", blocking: true, observation_id: null, window_kind: null });
  assert.ok(maintenancePlan.safeParse(blocked).success);
});

test("unresolved native operations remain global blockers with no invented observation or applied result", () => {
  const value = { ...fixture(), state: "blocked", reason: "PREFLIGHT_BLOCKED", validity: "blocked",
    issues: [{ code: "REMOTE_OPERATION_UNRESOLVED", blocking: true, observation_id: null, window_kind: null }] };
  assert.ok(maintenancePlan.safeParse(value).success);
  assert.equal(maintenancePlan.safeParse({ ...value, state: "ready", reason: "READY", validity: "valid" }).success, false);
  for (const change of [{ blocking: false }, { observation_id: "1" }, { window_kind: "short" }]) {
    assert.equal(maintenancePlan.safeParse({ ...value, issues: [{ ...value.issues[0], ...change }] }).success, false);
  }
});

test("maintenance queue and terminal results stay distinct and numeric pagination remains exact", () => {
  const queued = fixture(); queued.state = "queued"; queued.reason = "QUEUED"; queued.validity = "pending"; queued.requested_by = id; queued.queued_at = "2026-10-01T12:03:00Z";
  assert.equal(maintenancePlan.parse(queued).result_id, null);
  const applied = { ...queued, state: "applied", reason: "APPLIED", validity: "terminal", started_at: "2026-10-01T12:03:01Z", finished_at: "2026-10-01T12:03:02Z", result_id: queued.id };
  assert.equal(maintenancePlan.parse(applied).result_id, "10");
  const earlier = { ...fixture(), id: "9" };
  assert.ok(maintenanceOutputs.plans.safeParse({ account_id: id, plans: [earlier, fixture()], next_after: "10" }).success);
  for (const plans of [[fixture(), earlier], [earlier, earlier], [{ ...earlier, id: "malformed" }, fixture()]]) {
    assert.doesNotThrow(() => maintenanceOutputs.plans.safeParse({ account_id: id, plans, next_after: null }));
    assert.equal(maintenanceOutputs.plans.safeParse({ account_id: id, plans, next_after: null }).success, false);
  }
});

test("maintenance client uses fixed routes, validates replies and never retries unknown writes", async () => {
  let calls = 0;
  const fetcher = (async (request: Request, init?: RequestInit) => {
    calls++;
    assert.equal(request.url, "https://pool.example/api/v1/maintenance/apply");
    assert.equal(init?.redirect, "error");
    assert.equal(init?.cache, "no-store");
    throw new TypeError("response lost");
  }) as typeof fetch;
  const client = createMaintenanceClient("https://pool.example", fetcher);
  await assert.rejects(client.apply({ id: "10", basis: "a".repeat(64), confirmation: true }));
  assert.equal(calls, 1);
  await assert.rejects(client.apply({ id: "10", basis: "a".repeat(64), confirmation: false } as never));
  assert.equal(calls, 1);
  const malformed = createMaintenanceClient("https://pool.example", (async () => Response.json({ ...fixture(), state: "applied", result_id: "10" })) as typeof fetch);
  await assert.rejects(malformed.plan({ id: "10" }));
  assert.deepEqual(Object.keys(maintenanceContract).sort(), ["apply", "plan", "plans", "preflight"]);
});

const retainedFixture = () => {
  const value = fixture(), first = firstPeriod(value.preview.periods);
  return {
    ...value, kind: "upgrade", rules_version: "retained-estimates-v2",
    preview: { ...value.preview, observations: 3, periods: [{ ...first, source_mode: "active", observation_ids: ["11", "12", "13"], last_source_at: "2026-10-01T12:00:20Z", first_used_percent: "10", last_used_percent: "40" }],
      financial: { calculation_rules: "gate-003-rules-v1", currency: "USD", scan_complete: true, records: 1, results: [{
        estimate_id: "9007199254740993", generated_at: "2026-10-01T12:00:30Z", original_rules: "gate-003-rules-v1", original_state: "scenario", original_remaining: { numerator: "1", denominator: "1" },
        state: "scenario", remaining: { numerator: "72", denominator: "5" }, windows: [{ kind: "short", observation_ids: ["11", "12", "13"], state: "scenario",
          capacity: { numerator: "24", denominator: "1" }, remaining: { numerator: "72", denominator: "5" }, range_min: { numerator: "30", denominator: "1" }, range_max: { numerator: "35", denominator: "1" } }],
      }] } },
    difference: { ...value.difference, financial: { added: 1, removed: 0, changed: 0 } },
    limitations: ["RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "CONDITIONAL_SIMULATION_ONLY", "NOT_PAST_EXECUTED_AMOUNT", "MODEL_NOT_CALIBRATED", "ESTIMATE_REFERENCES_INCLUDED"],
  };
};

function firstRetainedResult(value: ReturnType<typeof retainedFixture>) { const result = value.preview.financial.results[0]; assert.ok(result); return result; }

test("retained rule preserves old/new exact conditional values and legacy versions without creating a wallet target", () => {
  const plan = maintenancePlan.parse(retainedFixture()), result = plan.preview.financial?.results[0];
  assert.equal(result?.estimate_id, "9007199254740993");
  assert.deepEqual(result?.original_remaining, { numerator: "1", denominator: "1" });
  assert.deepEqual(result?.remaining, { numerator: "72", denominator: "5" });
  assert.ok(plan.limitations.includes("NOT_PAST_EXECUTED_AMOUNT"));
  assert.equal(maintenancePlan.parse(fixture()).preview.financial, undefined);
  const queued = { ...plan, state: "queued", reason: "QUEUED", validity: "pending", requested_by: id, queued_at: "2026-10-01T12:03:00Z" };
  assert.equal(maintenancePlan.parse(queued).result_id, null);
  assert.equal(maintenancePlan.parse({ ...queued, state: "applied", reason: "APPLIED", validity: "terminal", result_id: plan.id, started_at: "2026-10-01T12:03:01Z", finished_at: "2026-10-01T12:03:02Z" }).result_id, plan.id);
});

test("retained replies reject incomplete scans, incorrect amounts, unsupported rules and scoped attribution without throwing", () => {
  for (const mutate of [
    (v: ReturnType<typeof retainedFixture>) => { v.preview.financial.records = 2; },
    (v: ReturnType<typeof retainedFixture>) => { v.preview.financial.scan_complete = false; },
    (v: ReturnType<typeof retainedFixture>) => { v.preview.financial.calculation_rules = "unreviewed-v2"; },
    (v: ReturnType<typeof retainedFixture>) => { v.rules_version = maintenanceRulesVersion; },
    (v: ReturnType<typeof retainedFixture>) => { v.limitations = fixture().limitations; },
    (v: ReturnType<typeof retainedFixture>) => { firstRetainedResult(v).generated_at = v.scope.until; },
    (v: ReturnType<typeof retainedFixture>) => { firstRetainedResult(v).remaining.numerator = "999"; },
    (v: ReturnType<typeof retainedFixture>) => { firstRetainedResult(v).remaining.denominator = "0"; },
    (v: ReturnType<typeof retainedFixture>) => { firstRetainedResult(v).remaining.numerator = "not-a-number"; },
    (v: ReturnType<typeof retainedFixture>) => { firstRetainedResult(v).estimate_id = "malformed"; },
    (v: ReturnType<typeof retainedFixture>) => { v.preview.financial.results.push(structuredClone(firstRetainedResult(v))); v.preview.financial.records++; },
  ]) {
    const value = retainedFixture(); mutate(value);
    assert.doesNotThrow(() => maintenancePlan.safeParse(value));
    assert.equal(maintenancePlan.safeParse(value).success, false);
  }
  const value = retainedFixture();
  assert.equal(maintenancePlan.safeParse({ ...value, preview: { ...value.preview, financial: undefined } }).success, false);
  assert.equal(maintenancePlan.safeParse({ ...value, wallet_target: "14.4" }).success, false);
  const blocked = { ...value, state: "blocked", reason: "PREFLIGHT_BLOCKED", validity: "blocked",
    issues: [{ code: "ESTIMATE_EVIDENCE_MISSING", blocking: true, observation_id: null, window_kind: null, estimate_id: "9007199254740993" }] };
  assert.ok(maintenancePlan.safeParse(blocked).success);
  assert.equal(maintenancePlan.safeParse({ ...blocked, issues: [{ ...blocked.issues[0], estimate_id: undefined }] }).success, false);
});
