import assert from "node:assert/strict";
import { test } from "node:test";
import { observationsOutputs, type DailyObservationHistory } from "@sub-pool/contracts/observations";
import { participantsOutputs, type UsageHistory } from "@sub-pool/contracts/participants";
import { readCurrentKeyUsage, readDailyObservationHistory, readUsageAccountHistory, usageKeyCosts } from "./usage-account-data";

const id = "a".repeat(21) + "A";
const input = { account_id: id, settings_revision: "3", start_at: "2026-11-07T00:00:00.000Z", end_at: "2026-11-08T00:00:00.000Z" };
const expected = { authority: "7", timezone: "UTC", scope: "account" as const };
const history = (): DailyObservationHistory => {
  const value = observationsOutputs.dailyHistory.parse({
  rules_version: "recorded-day-last-v1", account_id: id, timezone: "UTC", settings_revision: "3", authority_revision: "7",
  query_start_at: "2026-11-07T00:00:00Z", query_end_at: "2026-11-08T00:00:00Z", time_basis: "received_at", complete: false, settlement_eligible: false, current: null,
  records: [{ day: "2026-11-07", receipt_count: "2", observation: { id: "10", job_id: "1", account_id: id, management_revision: "1", manual_start: false, excluded: false, review_reason: null, reviewed_at: null,
    snapshot: { source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive", source_at: null, received_at: "2026-11-07T12:00:00.123456Z", windows: [{ kind: "short", duration_seconds: null, reset_at: null, reset_expired: false, used_percent: "0" }] } },
  usage: { observation_id: "10", account_id: id, scope: "account", basis: "native_window_interval", currency: "USD", timestamp_basis: "native_created_at", complete: false, settlement_eligible: false,
    windows: [{ kind: "short", start_at: null, end_at: null, reason: "NATIVE_WINDOW_UNVERIFIED", capture: null }] } }],
  });
  const [row] = value.records; assert.ok(row);
  value.current = { observation: row.observation, usage: { ...row.usage } };
  return value;
};

test("daily reads accept equivalent UTC precision while retaining recorded zero, missing costs and receipt count", async () => {
  const value = await readDailyObservationHistory(input, expected, async query => { assert.deepEqual(query, input); return history(); });
  assert.equal(value.records[0]?.observation.snapshot.windows[0]?.used_percent, "0");
  assert.equal(value.records[0]?.usage.windows[0]?.capture, null);
  assert.equal(value.records[0]?.receipt_count, "2");
  assert.equal(value.complete, false);
});

test("daily reads discard a different account, authority, settings, timezone, scope or nanosecond interval", async () => {
  const changes: ((value: DailyObservationHistory) => void)[] = [
    value => { value.account_id = "b".repeat(21) + "A"; }, value => { value.authority_revision = "8"; },
    value => { value.settings_revision = "4"; }, value => { value.timezone = "Asia/Taipei"; },
    value => { value.query_start_at = "2026-11-07T00:00:00.000000001Z"; }, value => { value.query_end_at = "2026-11-08T00:00:00.000000001Z"; },
    value => { for (const row of value.records) row.usage.scope = "authorized_participants"; },
    value => { assert.ok(value.current); value.current.usage.scope = "authorized_participants"; },
  ];
  for (const change of changes) await assert.rejects(readDailyObservationHistory(input, expected, async () => { const value = history(); change(value); return value; }), /每日快照/);
  await assert.rejects(readDailyObservationHistory(input, expected, async () => { throw new Error("late read failed"); }), /late read failed/);
});


function first<T>(values: T[]): T { const value = values[0]; assert.ok(value !== undefined); return value; }
function metadata(value: UsageHistory) { assert.ok(value.history); return value.history; }
function currentPair(value: DailyObservationHistory) { assert.ok(value.current); return value.current; }
const historyExpected = { ...expected, account: id, settingsRevision: input.settings_revision };
const metrics = { input_tokens: "9007199254740993", output_tokens: "0", cache_creation_tokens: null, cache_read_tokens: null,
  cache_creation_5m_tokens: null, cache_creation_1h_tokens: null, image_input_tokens: null, image_output_tokens: null,
  total_cost: "0.000000000000000001", actual_cost: "0", account_stats_cost: null };
const summary = { request_count: "1", metrics }, captureID = "9007199254740993", sourceAt = "2026-11-07T12:00:00Z";
const selected = { id: captureID, account_id: id, start_at: input.start_at, end_at: input.end_at, captured_at: "2026-11-07T13:00:00Z" };
const selections = [selected];
const historyInput = { capture_ids: [captureID], participant_id: "", model: "", api_key_id: "", grain: "month" as const, after: "0", limit: 1, start_at: input.start_at, end_at: input.end_at };
function usageHistory(grain: "day" | "month" = "month", endAt = input.end_at): UsageHistory {
  return participantsOutputs.history.parse({ start_at: input.start_at, end_at: input.end_at, query_start_at: input.start_at, query_end_at: endAt,
    oldest_capture_at: selected.captured_at, newest_capture_at: selected.captured_at,
    history: { basis: "selected_captures", timezone: "UTC", settings_revision: "3", authority_revision: "7", selections },
    summary, assigned: summary, unassigned: { request_count: "0", metrics: Object.fromEntries(Object.keys(metrics).map(key => [key, null])) }, unassigned_visible: true,
    groups: [{ account_id: id, participant_id: id, basis: "local_binding_at_log_time", ...summary }],
    key_groups: [{ account_id: id, participant_id: id, basis: "local_binding_at_log_time", api_key_id: "9", ...summary }],
    grain, buckets: [{ start_at: input.start_at, end_at: endAt, ...summary }],
    records: [{ capture_id: captureID, account_id: id, attribution: { participant_id: id, binding_id: "1", participant_name: "Recorded member", basis: "local_binding_at_log_time" },
      fact: { id: "9007199254740993", user_id: "21", api_key_id: "9", group_id: null, subscription_id: null, request_id: null,
        model: "recorded", upstream_model: null, upstream_response_model: null, billing_mode: null, billing_type: 0, billing_candidate: "wallet", deduction_state: "unknown",
        rate_multiplier: null, account_rate_multiplier: null, created_at: "2026-11-07T10:00:00Z", metrics } }], next_after: null,
    complete: false, settlement_eligible: false, currency: "USD", timestamp_basis: "native_created_at",
    limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"] });
}
function currentDaily(): DailyObservationHistory {
  const value = history(), current = currentPair(value);
  value.records = [];
  current.observation.snapshot.source_at = sourceAt;
  current.observation.snapshot.received_at = "2026-11-07T12:01:00Z";
  current.observation.snapshot.windows = [{ kind: "seven_day", duration_seconds: "86400", reset_at: input.end_at, reset_expired: false, used_percent: "0" }];
  current.usage.windows = [{ kind: "seven_day", start_at: input.start_at, end_at: sourceAt, reason: "RECORDED_WINDOW_USAGE",
    capture: { id: captureID, captured_at: selected.captured_at, state: "partial", summary,
      limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_SELECTED_CAPTURE", "QUOTA_CAUSALITY_UNVERIFIED"] } }];
  return observationsOutputs.dailyHistory.parse(value);
}

test("month reads retain server amounts, exact selections and clipped query bounds without paging detail", async () => {
  let calls = 0;
  const value = await readUsageAccountHistory(historyInput, historyExpected, selections, async query => { calls++; assert.deepEqual(query, historyInput); return usageHistory(); });
  assert.equal(calls, 1);
  assert.equal(value.buckets[0]?.metrics.total_cost, metrics.total_cost);
  assert.equal(value.grain, "month");
  assert.equal(value.history?.selections[0]?.id, captureID);
});


test("account summaries reject changed authority, account, capture, scope, calendar or nanosecond range", async () => {
  const changes: ((value: UsageHistory) => void)[] = [
    value => { metadata(value).authority_revision = "8"; }, value => { metadata(value).settings_revision = "4"; },
    value => { metadata(value).timezone = "Asia/Taipei"; }, value => { value.grain = "day"; },
    value => { value.query_start_at = "2026-11-07T00:00:00.000000001Z"; }, value => { value.query_end_at = "2026-11-08T00:00:00.000000001Z"; },
    value => { first(metadata(value).selections).id = "9"; }, value => { first(metadata(value).selections).captured_at = sourceAt; },
    value => { first(metadata(value).selections).start_at = "2026-11-06T00:00:00Z"; }, value => { first(metadata(value).selections).end_at = "2026-11-09T00:00:00Z"; },
    value => { first(metadata(value).selections).account_id = "b".repeat(21) + "A"; }, value => { value.unassigned_visible = false; },
    value => { first(value.key_groups ?? []).account_id = "b".repeat(21) + "A"; }, value => { first(value.records).account_id = "b".repeat(21) + "A"; },
  ];
  for (const change of changes) await assert.rejects(readUsageAccountHistory(historyInput, historyExpected, selections, async () => { const value = usageHistory(); change(value); return value; }), /账号历史/);
});

test("current Key summaries use exactly the retained native interval and scoped capture summary", async () => {
  const value = await readCurrentKeyUsage(currentDaily(), historyExpected, async query => {
    assert.deepEqual(query, { ...historyInput, grain: "day", end_at: sourceAt });
    return usageHistory("day", sourceAt);
  }, new Date("2026-11-07T14:00:00Z"));
  assert.ok(value);
  assert.equal(value.summary.metrics.total_cost, metrics.total_cost);
  assert.equal(usageKeyCosts(value)(first(value.records)), metrics.total_cost);
  await assert.rejects(readCurrentKeyUsage(currentDaily(), historyExpected, async () => {
    const value = usageHistory("day", sourceAt); value.summary.metrics.total_cost = "0"; return value;
  }, new Date("2026-11-07T14:00:00Z")), /捕获不一致/);
  const wrong = currentDaily(); currentPair(wrong).usage.scope = "authorized_participants";
  await assert.rejects(readCurrentKeyUsage(wrong, historyExpected, async () => usageHistory(), new Date(sourceAt)), /授权或账号/);
});

test("expired, missing or incoherent current native windows make no history request or older fallback", async () => {
  let calls = 0;
  const read = async () => { calls++; return usageHistory(); };
  for (const date of ["2026-11-08T00:00:00Z", "2026-11-06T23:59:59Z"]) assert.equal(await readCurrentKeyUsage(currentDaily(), historyExpected, read, new Date(date)), null);
  const changes: ((value: DailyObservationHistory) => void)[] = [
    value => { value.current = null; }, value => { first(currentPair(value).observation.snapshot.windows).duration_seconds = null; },
    value => { first(currentPair(value).observation.snapshot.windows).reset_expired = true; },
    value => { first(currentPair(value).usage.windows).capture = null; }, value => { currentPair(value).observation.snapshot.source_at = null; },
    value => { first(currentPair(value).usage.windows).end_at = "2026-11-07T12:00:00.000000001Z"; },
  ];
  for (const change of changes) { const value = currentDaily(); change(value); assert.equal(await readCurrentKeyUsage(value, historyExpected, read, new Date("2026-11-07T14:00:00Z")), null); }
  assert.equal(calls, 0);
});

test("Key display keeps zero and unknown separate and never crosses account, member, basis or Key", () => {
  const value = usageHistory(), row = first(value.records), other = "b".repeat(21) + "A";
  first(value.key_groups ?? []).metrics.total_cost = "0";
  const cost = usageKeyCosts(value);
  assert.equal(cost(row), "0");
  for (const changed of [{ ...row, account_id: other }, { ...row, attribution: { ...row.attribution, participant_id: other } },
    { ...row, attribution: { ...row.attribution, participant_id: null, basis: "outside_recorded_binding" as const } },
    { ...row, fact: { ...row.fact, api_key_id: "10" } }, { ...row, fact: { ...row.fact, api_key_id: null } }]) assert.equal(cost(changed), null);
  assert.equal(usageKeyCosts(null)(row), null);
  delete value.key_groups;
  assert.equal(usageKeyCosts(value)(row), null);
});
