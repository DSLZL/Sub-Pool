import assert from "node:assert/strict";
import { test } from "node:test";
import { createEstimatesClient } from "@sub-pool/contracts/client";
import { estimateRecord, type EstimateCurrent } from "@sub-pool/contracts/estimates";
import { estimateAmountText, estimateHistoryFilter, readEstimatePage, readEstimateRecord, readEstimateSummaries } from "./estimates-data";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A";
function record(result = "10", revision = "2") {
  return estimateRecord.parse({ id: result, account_id: id, generated_at: "2026-11-07T12:00:00.0000002Z", rules_version: "gate-003-rules-v1",
    configuration: { account_id: id, revision, saved_at: "2026-11-07T12:00:00.0000001Z", assumptions: { plan: "empirical_min_v1", cost_basis: "standard_usd", keep_ratio: "0.812345678901234567", premise: "Declared premise is retained exactly", windows: ["short"], source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive" } },
    trigger: "configuration", policy_revision: "1", management_revision: "1", observation_head: "3", capture_head: "0", state: "evidence_incomplete", remaining: null, currency: "USD", complete: false, settlement_eligible: false, model_calibrated: false,
    limitations: ["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"],
    windows: [{ kind: "short", basis: "official", period: { kind: "short", start_at: "2026-11-07T10:00:00Z", end_at: "2026-11-07T13:00:00Z", duration_seconds: "10800", basis: "native_reset_and_duration" }, manual_start_at: null, manual_end_at: null, observation_ids: ["1", "2", "3"], current_observation_id: "3", source_at: "2026-11-07T12:00:00Z", oldest_sample_at: "2026-11-07T11:58:00Z", current_used_percent: "0", state: "evidence_incomplete", capacity: null, remaining: null, range_min: null, range_max: null }] });
}
function current(latest = record()): EstimateCurrent { return { configuration: latest.configuration, latest, current_state: latest.state, current_remaining: null, needs_recalculation: false, checked_at: "2026-11-07T12:00:01Z" }; }
const account = { id, name: "Own recorded estimate account" };

const filter = { window_kind: "all" as const, period_end_at: "" };

test("estimate history validates exact identity, retained amount and cursor without rereading current state", async () => {
  const first = "9007199254740993", second = "9007199254740994";
  const calls: string[] = [];
  const client = createEstimatesClient("https://example.test", async (input, init) => {
    const request = new Request(input, init);
    calls.push(new URL(request.url).pathname);
    const args = await request.json();
    assert.equal(args.account_id, id);
    const next = args.after === "0";
    return Response.json({ account_id: id, records: [record(next ? first : second)], next_after: next ? first : null });
  });
  const read = (after: string) => readEstimatePage(id, after, () => client.history({ account_id: id, after, limit: 1, ...filter }), filter);
  const page = await read("0");
  assert.equal(page.next_after, first);
  const later = await read(first);
  assert.equal(later.records[0]?.id, second);
  assert.equal(estimateAmountText(later.records[0]?.remaining ?? null), "未知");
  assert.deepEqual(calls, ["/api/v1/estimates/history", "/api/v1/estimates/history"]);
});

test("estimate history rejects cross-account records, nonadvancing cursors and nonmatching periods", async () => {
  const rows = [record("10")];
  const read = (page: { account_id: string; records: typeof rows; next_after: string | null }) =>
    readEstimatePage(id, "9", async () => page, filter);
  for (const page of [
    { account_id: other, records: rows, next_after: null },
    { account_id: id, records: [record("9")], next_after: null },
    { account_id: id, records: [rows[0], rows[0]].flatMap(row => row ? [row] : []), next_after: null },
    { account_id: id, records: rows, next_after: "9" },
  ]) await assert.rejects(read(page));
  const otherPeriod = estimateHistoryFilter("short", "2026-11-07T13:00:00.000000001Z");
  await assert.rejects(readEstimatePage(id, "0", async () => ({ account_id: id, records: rows, next_after: null }), otherPeriod));
  await assert.rejects(readEstimateRecord(other, "10", async () => record()));
});

test("native estimate summaries bind the response to the requested account", async () => {
  const signal = new AbortController().signal;
  const good = await readEstimateSummaries([account], { current: async () => current() }, signal);
  assert.equal(good[0]?.account.id, id);
  await assert.rejects(readEstimateSummaries([{ ...account, id: other }], { current: async () => current() }, signal));
});
