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
  assert.equal(maintenanceVersion, "maintenance-v1");
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
