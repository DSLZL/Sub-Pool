import { test } from "node:test";
import assert from "node:assert/strict";
import { participantDeletion, participantDeleteResultInput, usageHistoryRange } from "./participants";

const id = "a".repeat(21) + "A";
const receipt = { participant_id: id, previous_revision: "9007199254740993", revision: "9007199254740994", binding_id: "3", name: "Member", effective_at: "2026-11-15T01:02:03.000001Z" };

test("deletion results preserve an exact committed version and reject guessed success fields", () => {
  assert.deepEqual(participantDeletion.parse(receipt), receipt);
  for (const change of [{ revision: receipt.previous_revision }, { revision: "9007199254740995" }, { binding_id: "0" }, { settlement_complete: true }, { effective_at: "invalid" }]) {
    assert.equal(participantDeletion.safeParse({ ...receipt, ...change }).success, false);
  }
  assert.ok(participantDeleteResultInput.safeParse({ id, revision: "1" }).success);
  assert.equal(participantDeleteResultInput.safeParse({ id, revision: "1", confirmation: "Member" }).success, false);
});

test("explicit history ranges keep native nanosecond UTC and bounded half-open intervals", () => {
  assert.ok(usageHistoryRange.safeParse({ start_at: "2026-11-01T00:00:00.000000001Z", end_at: "2026-11-01T00:00:00.000000002Z" }).success);
  for (const value of [
    { start_at: "2026-11-02T00:00:00Z", end_at: "2026-11-01T00:00:00Z" },
    { start_at: "2025-01-01T00:00:00Z", end_at: "2026-11-01T00:00:00Z" },
    { start_at: "2026-11-01T00:00:00+08:00", end_at: "2026-11-02T00:00:00Z" },
  ]) assert.equal(usageHistoryRange.safeParse(value).success, false);
});
