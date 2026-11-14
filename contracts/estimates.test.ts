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

test("only a complete explicitly simulated cost proof can expose conditional amounts, including known zero", () => {
  const exact = (numerator: string, denominator = "1") => ({ numerator, denominator });
  const proof = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id, source_start: "2026-11-07T11:00:00Z", closed_through: "2026-11-07T12:00:00Z", ledger_revision: "17", price_version: "2", standard_rate: "1", wallet_multiplier: "0.5", billing_type: "standard_wallet", price_effective_at: "2026-11-07T11:00:00Z", intervals: [
    { start_at: "2026-11-07T11:56:00Z", end_at: "2026-11-07T11:58:00Z", standard_cost: exact("3"), actual_cost: exact("3", "2"), entry_count: 1 },
    { start_at: "2026-11-07T11:58:00Z", end_at: "2026-11-07T12:00:00Z", standard_cost: exact("7"), actual_cost: exact("7", "2"), entry_count: 1 },
  ] };
  const scenario = { ...record, state: "scenario", complete: true, remaining: exact("72", "5"), limitations: ["SIMULATED_PROTOCOL_ONLY", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"], configuration: { ...configuration, assumptions: { ...configuration.assumptions, source_mode: "active" } }, windows: [{ ...record.windows[0], state: "scenario", observation_ids: ["1", "2", "3"], current_observation_id: "3", source_at: "2026-11-07T12:00:00Z", oldest_sample_at: "2026-11-07T11:56:00Z", current_used_percent: "40", period: { kind: "short", start_at: "2026-11-07T10:00:00Z", end_at: "2026-11-07T13:00:00Z", duration_seconds: "10800", basis: "native_reset_and_duration" }, capacity: exact("24"), remaining: exact("72", "5"), range_min: exact("30"), range_max: exact("35"), cost_proof: proof }] };
  const parsed = estimateRecord.parse(scenario);
  assert.deepEqual(parsed.remaining, exact("72", "5")); assert.equal(parsed.settlement_eligible, false);
  for (const changed of [
    { ...scenario, complete: false }, { ...scenario, model_calibrated: true }, { ...scenario, settlement_eligible: true },
    { ...scenario, configuration }, { ...scenario, limitations: record.limitations },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: undefined }] },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: { ...proof, environment: "native" } }] },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: { ...proof, epoch: "wrong" } }] },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: { ...proof, request_id: id } }] },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: { ...proof, intervals: [proof.intervals[0]] } }] },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: { ...proof, closed_through: "2026-11-07T11:59:59Z" } }] },
    { ...scenario, windows: [{ ...scenario.windows[0], cost_proof: { ...proof, price_effective_at: "2026-11-07T11:56:01Z" } }] },
    { ...scenario, windows: [{ ...scenario.windows[0], period: null }] },
    { ...scenario, windows: [{ ...scenario.windows[0], state: "evidence_incomplete" }] },
    { ...scenario, remaining: exact("-1") }, { ...scenario, remaining: exact("1", "0") }, { ...scenario, remaining: exact("0x10") },
  ]) assert.equal(estimateRecord.safeParse(changed).success, false);
  const current = { configuration: parsed.configuration, latest: parsed, current_state: "scenario", current_remaining: parsed.remaining, checked_at: "2026-11-07T12:00:01Z", needs_recalculation: false };
  assert.ok(estimatesOutputs.current.safeParse(current).success);
  for (const changed of [{ ...current, current_remaining: null }, { ...current, current_state: "source_stale" }, { ...current, current_remaining: exact("0") }, { ...current, latest: null }]) assert.equal(estimatesOutputs.current.safeParse(changed).success, false);
  const zero = estimateRecord.parse({ ...scenario, remaining: exact("0"), windows: [{ ...scenario.windows[0], current_used_percent: "100", remaining: exact("0") }] });
  assert.deepEqual(estimatesOutputs.current.parse({ ...current, latest: zero, current_remaining: zero.remaining }).current_remaining, exact("0"));
  assert.ok(estimatesOutputs.current.safeParse({ ...current, current_state: "source_stale", current_remaining: null }).success);
});
