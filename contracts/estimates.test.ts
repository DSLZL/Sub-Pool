import assert from "node:assert/strict";
import { test } from "node:test";
import { createEstimatesClient } from "./client";
import { estimateAssumptionInput, estimateRecord, estimatesOutputs } from "./estimates";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A";
const assumptions = { plan: "empirical_min_v1" as const, cost_basis: "standard_usd" as const, keep_ratio: "0.8", premise: "Explicit independently declared scenario", windows: ["short" as const], source_id: id, connection_revision: "1", account_revision: "1" };
const configuration = { account_id: id, revision: "2", assumptions: { ...assumptions, source_mode: "passive" }, saved_at: "2026-11-07T12:00:00.0000001Z" };
const record = { id: "9007199254740993", account_id: id, generated_at: "2026-11-07T12:00:00.0000002Z", rules_version: "gate-003-rules-v1", configuration,
  trigger: "configuration", policy_revision: "1", management_revision: "1", observation_head: "0", capture_head: "0", state: "insufficient_samples", remaining: null, currency: "USD", complete: false, settlement_eligible: false, model_calibrated: false,
  limitations: ["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"], windows: [{ kind: "short", basis: "official", period: null, manual_start_at: null, manual_end_at: null, observation_ids: [], current_observation_id: null, source_at: null, oldest_sample_at: null, current_used_percent: null, state: "insufficient_samples", capacity: null, remaining: null, range_min: null, range_max: null }] };

test("estimates preserve precise identities, explicit assumptions and unavailable financial coverage", () => {
  assert.equal(estimateRecord.parse(record).id, "9007199254740993");
  for (const current_used_percent of ["1e1", "1E+2", "101"]) {
    const withQuota = { ...record, windows: [{ ...record.windows[0], current_observation_id: "1", source_at: record.generated_at, current_used_percent, state: "invalid_evidence" }] };
    assert.equal(estimateRecord.parse(withQuota).windows[0]?.current_used_percent, current_used_percent);
    for (const invalid of ["NaN", "-1", "2e19", "01"]) assert.equal(estimateRecord.safeParse({ ...withQuota, windows: [{ ...withQuota.windows[0], current_used_percent: invalid }] }).success, false);
  }
  assert.ok(estimateAssumptionInput.safeParse(assumptions).success);
  for (const keep_ratio of ["0", "1.01", "-1", "1/2", "8e-1", "01", "0.1234567890123456789"]) assert.equal(estimateAssumptionInput.safeParse({ ...assumptions, keep_ratio }).success, false);
  for (const keep_ratio of ["1", "1.0", "0.000000000000000001"]) assert.ok(estimateAssumptionInput.safeParse({ ...assumptions, keep_ratio }).success);
  for (const input of [{ ...assumptions, plan: "other" }, { ...assumptions, cost_basis: "actual_cost" }, { ...assumptions, windows: ["short", "short"] }, { ...assumptions, complete: true }, { ...assumptions, premise: "😀".repeat(129) }]) assert.equal(estimateAssumptionInput.safeParse(input).success, false);
  for (const altered of [{ ...record, account_id: other }, { ...record, complete: true }, { ...record, settlement_eligible: true }, { ...record, state: "scenario" }, { ...record, remaining: { numerator: "0", denominator: "1" } }, { ...record, generated_at: "2026-11-07T12:00:00Z" }, { ...record, configuration: { ...configuration, saved_at: null } }, { ...record, windows: [] }]) assert.equal(estimateRecord.safeParse(altered).success, false);
  const page = { account_id: id, records: [record, { ...record, id: "9007199254740994" }], next_after: "9007199254740994" };
  assert.ok(estimatesOutputs.history.safeParse(page).success);
  assert.equal(estimatesOutputs.history.safeParse({ ...page, records: [...page.records].reverse() }).success, false);
  assert.equal(estimatesOutputs.history.safeParse({ ...page, next_after: "1" }).success, false);
  assert.equal(estimatesOutputs.history.safeParse({ ...page, account_id: other }).success, false);
});

test("estimate client validates before dispatch and never replays a possibly persisted configuration", async () => {
  let calls = 0;
  const client = createEstimatesClient("https://example.test", async (input, init) => {
    calls++; const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/estimates/configure");
    assert.equal(request.method, "POST"); assert.equal(request.cache, "no-store"); assert.equal(request.credentials, "same-origin"); assert.equal(request.redirect, "error");
    throw new TypeError("connection lost after dispatch");
  });
  await assert.rejects(client.configure({ ...assumptions, account_id: id, revision: "1", keep_ratio: "0" })); assert.equal(calls, 0);
  await assert.rejects(client.configure({ ...assumptions, account_id: id, revision: "1" })); assert.equal(calls, 1);
  const forged = createEstimatesClient("https://example.test", async () => Response.json({ ...record, remaining: "0" }));
  await assert.rejects(forged.record({ id: record.id }));
  for (const value of [{ account_id: id, after: "0", limit: 1, window_kind: "all" as const, period_end_at: "2026-11-07T13:00:00Z" }, { account_id: id, after: "01", limit: 1, window_kind: "short" as const, period_end_at: "" }]) await assert.rejects(client.history(value));
  assert.equal(calls, 1);
});
