import assert from "node:assert/strict";
import { test } from "node:test";
import { observationsOutputs, type DailyObservationHistory } from "@sub-pool/contracts/observations";
import { readDailyObservationHistory } from "./usage-account-data";

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
