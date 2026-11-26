import assert from "node:assert/strict";
import { test } from "node:test";
import type { DispatchControl, DispatchEvent, DispatchHistory } from "@sub-pool/contracts/dispatch";
import { dispatchDeadlineInput, dispatchReasonText, dispatchStatusText, dispatchTimeInput, readDispatchHistory } from "./dispatch-control-data";
import { DispatchReadError } from "./dispatch-data";

const at = "2026-11-09T00:00:00.000001Z";
const event: DispatchEvent = { id: "1", actor: "worker", action: "restore_result", occurred_at: at,
  pause: { id: "1", revision: "2", account_id: "A".repeat(21) + "Q", source_id: "B".repeat(21) + "A",
    connection_revision: "3", account_revision: "1", state: "needs_review", reason: "WRITE_RESULT_UNKNOWN",
    previous_schedulable: false, native_updated_at: at, restore_at: at, created_at: at, updated_at: at } };
const page = (events: DispatchEvent[], next_after: string | null = null): DispatchHistory => ({ authority_revision: "1", events, next_after });

test("dispatch handoff uses latest scoped receipts without claiming current native state or reviewed restoration", () => {
  const control: DispatchControl = { version: "dispatch-control-v1", source_id: event.pause.source_id,
    connection_revision: "3", can_pause: true, can_restore: true, automatic_restore_available: true, reason: "CONTROLLED_ENVIRONMENT" };
  assert.match(dispatchStatusText(control, [event]), /1 条待核对/);
  const reviewed: DispatchEvent = { ...event, id: "2", pause: { ...event.pause, revision: "3", state: "reviewed", reason: "MANUALLY_REVIEWED" } };
  assert.doesNotMatch(dispatchStatusText(control, [event, reviewed]), /1 条待核对/);
  assert.match(dispatchStatusText(control, [event, reviewed], event.pause.account_id), /跟踪已结束，恢复未知/);
  assert.doesNotMatch(dispatchStatusText(control, [event], "other-account"), /1 条待核对/);
});

test("dispatch history consumes scoped pages and discards a failed or changed authority traversal", async () => {
  const calls: string[] = [];
  const result = await readDispatchHistory(async after => {
    calls.push(after); return after === "" ? page([event], "1") : page([{ ...event, id: "9007199254740993" }]);
  }, "1");
  assert.deepEqual(calls, ["", "1"]); assert.deepEqual(result.map(row => row.id), ["1", "9007199254740993"]);
  await assert.rejects(readDispatchHistory(async after => {
    if (after) throw new Error("later page failed"); return page([event], "1");
  }, "1"));
  await assert.rejects(readDispatchHistory(async () => ({ ...page([event]), authority_revision: "2" }), "1"), error => error instanceof DispatchReadError && error.refreshIdentity);
  for (const invalid of [page([event, event]), page([{ ...event, id: "2" }, event]), page([], "1"), page([event], "2")]) {
    await assert.rejects(readDispatchHistory(async () => invalid, "1"), error => error instanceof DispatchReadError && !error.refreshIdentity);
  }
});

test("dispatch deadline uses explicit browser-local wall time and rejects rollover, expiry and unbounded dates", () => {
  const now = Date.parse("2026-11-09T00:00:00Z"), future = new Date(now + 3600_000);
  assert.equal(dispatchDeadlineInput(dispatchTimeInput(future), now), future.toISOString());
  for (const invalid of ["", "2026-11-31T12:00", "2026-13-01T12:00", "2026-11-09T24:00", dispatchTimeInput(new Date(now)), dispatchTimeInput(new Date(now + 31 * 86400_000))]) {
    assert.equal(dispatchDeadlineInput(invalid, now), null);
  }
  assert.ok(dispatchReasonText(event.pause).includes("结果待核对"));
  assert.ok(dispatchReasonText({ ...event.pause, state: "reviewed", reason: "MANUALLY_REVIEWED" }).includes("跟踪已结束，恢复未知"));
});
