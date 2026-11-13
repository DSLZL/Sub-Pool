import assert from "node:assert/strict";
import { test } from "node:test";
import { pricingOperation } from "@sub-pool/contracts/pricing";
import { dispatchHistory } from "@sub-pool/contracts/dispatch";
import { overviewReview } from "./overview-data";

const source = "A".repeat(21) + "Q";
const timestamp = "2026-11-11T00:00:00Z";
const pricing = (id: string, state = "needs_review", reason = "WRITE_RESULT_UNKNOWN") => pricingOperation.parse({
  id, revision: "2", actor_id: source, request_id: source, source_id: source, connection_revision: "2", group_id: "1", group_name: "Owned group",
  changes: [{ kind: "long_context", enabled: false, value: null }], state, reason, before_fingerprint: "a".repeat(64),
  after_fingerprint: null, created_at: timestamp, updated_at: timestamp,
});
const event = (id: string, pauseID: string, state = "needs_review", reason = "WRITE_RESULT_UNKNOWN") => {
  const result = dispatchHistory.parse({
  authority_revision: "1", next_after: null, events: [{ id, actor: "administrator", action: "pause_result", occurred_at: timestamp,
    pause: { id: pauseID, revision: id, account_id: source, source_id: source, connection_revision: "2", account_revision: "1",
      state, reason, previous_schedulable: true, native_updated_at: timestamp, restore_at: "2026-11-12T00:00:00Z", created_at: timestamp, updated_at: timestamp } }],
  }).events[0];
  assert.ok(result);
  return result;
};

test("overview counts unresolved operations once and uses the latest event per pause", () => {
  const summary = overviewReview(true, [pricing("1"), pricing("2", "rejected", "UPSTREAM_REJECTED"), pricing("3", "reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN")],
    [event("1", "1"), event("2", "1"), event("3", "2"), event("4", "2", "reviewed", "MANUALLY_REVIEWED"), event("5", "3", "paused", "VERIFIED_PAUSE")]);
  assert.equal(summary.count, 2);
  assert.match(summary.text, /计价 1 · 调度 1/);
  assert.match(summary.text, /非远端实时核验/);
});

test("overview requires complete reads, preserves real zero and discards partial counts", () => {
  assert.equal(overviewReview(true, [], []).count, 0);
  for (const [pricingRecords, dispatchEvents] of [[null, []], [[], null], [[pricing("1")], null], [null, [event("1", "1")]]] as const) {
    const summary = overviewReview(true, pricingRecords, dispatchEvents);
    assert.equal(summary.count, null); assert.match(summary.text, /不能推断/);
  }
  assert.equal(overviewReview(true, [pricing("1", "reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN")], [event("1", "1"), event("2", "1", "reviewed", "MANUALLY_REVIEWED")]).count, 0);
});

test("ordinary overview counts only authorized dispatch and never includes administrator pricing", () => {
  const summary = overviewReview(false, [pricing("1")], [event("1", "1")]);
  assert.equal(summary.count, 1);
  assert.match(summary.text, /仅当前授权内/); assert.match(summary.text, /非全局数量/); assert.doesNotMatch(summary.text, /计价/);
  assert.equal(overviewReview(false, null, []).count, 0);
  assert.equal(overviewReview(false, null, null).count, null);
});
