import assert from "node:assert/strict";
import { test } from "node:test";
import { createDispatchClient } from "./client";
import { dispatchAccountInput, dispatchAccounts, dispatchListInput, dispatchObservation } from "./dispatch";

const id = "A".repeat(21) + "Q";
const source_id = "B".repeat(21) + "A";
const capability = { can_pause: false, can_restore: false, automatic_restore_available: false, history_state: "unavailable", reason: "SCHEDULING_CONTROL_UNQUALIFIED", authority_revision: "1" };
const binding = { id, revision: "2", source_id, connection_revision: "3" };
const list = { ...capability, connection_revision: "3", accounts: [{ id, revision: "2", source_id, name: "Root", source_current: true }], next_after: null };
const observation = { ...capability, ...binding, checked_at: "2026-11-07T00:00:00.000000Z", configured_schedulable: false, native_status: "disabled", read: { id: "dispatch.account.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 } };

test("dispatch preserves explicit false, unavailable control and unknown failed reads", () => {
  assert.ok(dispatchAccounts.safeParse(list).success);
  assert.ok(dispatchObservation.safeParse(observation).success);
  assert.ok(dispatchObservation.safeParse({ ...observation, configured_schedulable: null, native_status: null, read: { ...observation.read, state: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" } }).success);
  for (const value of [{ ...list, can_pause: true }, { ...list, can_restore: true }, { ...list, automatic_restore_available: true }, { ...list, history_state: "empty" }, { ...list, accounts: [list.accounts[0], list.accounts[0]] }, { ...list, accounts: [], next_after: id }, { ...list, credentials: "secret" }]) assert.equal(dispatchAccounts.safeParse(value).success, false);
  for (const value of [{ ...observation, configured_schedulable: null }, { ...observation, native_status: null }, { ...observation, read: { ...observation.read, state: "failed", reason: "ACCOUNT_IDENTITY_CHANGED" } }, { ...observation, read: { ...observation.read, reason: "WRITE_NOT_PROBED" } }, { ...observation, raw_upstream: "secret" }]) assert.equal(dispatchObservation.safeParse(value).success, false);
});

test("dispatch inputs require exact current binding and bounded literal search without writes", () => {
  const input = { after: "", search: "%_\\", limit: 100 };
  assert.ok(dispatchListInput.safeParse(input).success);
  assert.ok(dispatchAccountInput.safeParse(binding).success);
  for (const value of [{ ...input, after: "foreign" }, { ...input, limit: 0 }, { ...input, limit: 101 }, { ...input, search: "中".repeat(86) }, { ...input, search: "bad\nname" }, { ...input, pause_until: "later" }]) assert.equal(dispatchListInput.safeParse(value).success, false);
  for (const value of [{ ...binding, revision: "02" }, { ...binding, source_id: "foreign" }, { ...binding, connection_revision: "0" }, { ...binding, schedulable: false }]) assert.equal(dispatchAccountInput.safeParse(value).success, false);
});

test("dispatch client validates both fixed read routes and rejects malformed success without retry", async () => {
  let calls = 0;
  const input = { after: "", search: "", limit: 100 };
  const client = createDispatchClient("http://127.0.0.1:3000", async (request, init) => {
    const req = new Request(request, init); calls++;
    assert.equal(req.method, "POST"); assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error"); assert.equal(init?.credentials, "same-origin");
    if (req.url.endsWith("/api/v1/dispatch/list")) { assert.deepEqual(await req.json(), input); return Response.json(list); }
    assert.equal(req.url, "http://127.0.0.1:3000/api/v1/dispatch/account"); assert.deepEqual(await req.json(), binding); return Response.json(observation);
  });
  assert.deepEqual(await client.list(input), list);
  assert.deepEqual(await client.account(binding), observation);
  assert.equal(calls, 2);
  const invalid = createDispatchClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ ...observation, can_restore: true }); });
  await assert.rejects(invalid.account(binding)); assert.equal(calls, 3);
  const denied = createDispatchClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "Denied", data: {} }, { status: 403 }); });
  await assert.rejects(denied.account(binding)); assert.equal(calls, 4);
});
