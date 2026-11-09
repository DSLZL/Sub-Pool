import assert from "node:assert/strict";
import { test } from "node:test";
import { createDispatchClient } from "./client";
import { dispatchControl, dispatchHistory, dispatchPause } from "./dispatch";

const account = "A".repeat(21) + "Q";
const source = "B".repeat(21) + "A";
const at = "2026-11-09T00:00:00.000001Z";
const pause = { id: "9007199254740993", revision: "2", account_id: account, source_id: source,
  connection_revision: "3", account_revision: "1", state: "paused", reason: "VERIFIED_PAUSE",
  previous_schedulable: true, native_updated_at: at, restore_at: "2026-11-09T01:00:00Z", created_at: at, updated_at: at };
const input = { id: account, revision: "1", source_id: source, connection_revision: "3",
  request_id: account, restore_at: pause.restore_at, acknowledge_account_wide: true as const };

test("dispatch control keeps operational qualification separate from native write confirmation", () => {
  const enabled = { version: "dispatch-control-v1", source_id: source, connection_revision: "3",
    can_pause: true, can_restore: true, automatic_restore_available: true, reason: "CONTROLLED_ENVIRONMENT" };
  assert.ok(dispatchControl.safeParse(enabled).success);
  assert.ok(dispatchControl.safeParse({ ...enabled, can_pause: false, can_restore: false, reason: "ADMIN_REQUIRED" }).success);
  assert.ok(dispatchControl.safeParse({ ...enabled, source_id: null, can_pause: false, can_restore: false,
    automatic_restore_available: false, reason: "CONTROL_BINDING_REQUIRED" }).success);
  for (const invalid of [{ ...enabled, source_id: null }, { ...enabled, can_restore: false },
    { ...enabled, reason: "VERIFIED_WRITE" }, { ...enabled, automatic_restore_available: false }]) {
    assert.equal(dispatchControl.safeParse(invalid).success, false);
  }
});

test("dispatch receipts retain exact IDs, explicit false and unresolved outcomes without inventing restoration", () => {
  assert.ok(dispatchPause.safeParse(pause).success);
  assert.ok(dispatchPause.safeParse({ ...pause, previous_schedulable: false, reason: "PREVIOUSLY_DISABLED" }).success);
  assert.ok(dispatchPause.safeParse({ ...pause, state: "needs_review", reason: "WRITE_RESULT_UNKNOWN" }).success);
  assert.ok(dispatchPause.safeParse({ ...pause, state: "reviewed", reason: "MANUALLY_REVIEWED" }).success);
  for (const invalid of [{ ...pause, id: 9007199254740993 }, { ...pause, revision: "02" },
    { ...pause, state: "restored", reason: "WRITE_RESULT_UNKNOWN" }, { ...pause, state: "reviewed", reason: "VERIFIED_RESTORE" },
    { ...pause, previous_schedulable: null }, { ...pause, native_updated_at: null },
    { ...pause, updated_at: "2026-11-08T00:00:00Z" }, { ...pause, actor_id: account }, { ...pause, credentials: "PRIVATE" }]) {
    assert.equal(dispatchPause.safeParse(invalid).success, false);
  }
  const event = { id: "1", actor: "worker", action: "restore_result", occurred_at: at, pause };
  assert.ok(dispatchHistory.safeParse({ authority_revision: "1", events: [event], next_after: "1" }).success);
  assert.equal(dispatchHistory.safeParse({ authority_revision: "1", events: [event, event], next_after: null }).success, false);
  assert.equal(dispatchHistory.safeParse({ authority_revision: "1", events: [event], next_after: "2" }).success, false);
});

test("dispatch writes use fixed same-origin routes, validate acknowledgements and never retry a lost result", async () => {
  let calls = 0;
  const client = createDispatchClient("http://127.0.0.1:3000", async (request, init) => {
    const req = new Request(request, init); calls++;
    assert.equal(req.url, "http://127.0.0.1:3000/api/v1/dispatch/pause");
    assert.equal(req.method, "POST"); assert.equal(init?.redirect, "error");
    assert.equal(init?.cache, "no-store"); assert.equal(init?.credentials, "same-origin");
    assert.deepEqual(await req.json(), input);
    return Response.json(pause);
  });
  assert.deepEqual(await client.pause(input), pause); assert.equal(calls, 1);
  await assert.rejects(client.pause({ ...input, acknowledge_account_wide: false } as never)); assert.equal(calls, 1);
  const lost = createDispatchClient("http://127.0.0.1:3000", async () => { calls++; throw new TypeError("lost response"); });
  await assert.rejects(lost.restore({ id: pause.id, revision: pause.revision })); assert.equal(calls, 2);
  const malformed = createDispatchClient("http://127.0.0.1:3000", async () => {
    calls++; return Response.json({ ...pause, state: "restored", reason: "WRITE_RESULT_UNKNOWN" });
  });
  await assert.rejects(malformed.restore({ id: pause.id, revision: pause.revision })); assert.equal(calls, 3);
});
