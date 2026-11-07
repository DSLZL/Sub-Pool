import assert from "node:assert/strict";
import { test } from "node:test";
import type { DispatchAccounts, DispatchObservation } from "@sub-pool/contracts/dispatch";
import { dispatchControlText, dispatchObservationText, readDispatchAccounts, readDispatchObservation } from "./dispatch-data";

const id = Buffer.alloc(16, 1).toString("base64url"), secondID = Buffer.alloc(16, 2).toString("base64url"), sourceID = Buffer.alloc(16, 3).toString("base64url");
const capability = { can_pause: false, can_restore: false, automatic_restore_available: false, history_state: "unavailable", reason: "SCHEDULING_CONTROL_UNQUALIFIED", authority_revision: "1" } as const;
const item = { id, revision: "2", name: "Native root", source_id: sourceID, source_current: true };
const page: DispatchAccounts = { ...capability, connection_revision: "3", accounts: [item], next_after: null };
const input = { id, revision: "2", source_id: sourceID, connection_revision: "3" };
const observation: DispatchObservation = { ...capability, ...input, checked_at: "2026-11-08T00:00:00Z", configured_schedulable: false, native_status: "disabled", read: { id: "dispatch.account.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 } };

test("dispatch reads all selector pages under one connection and current authority", async () => {
  const afters: string[] = [];
  const value = await readDispatchAccounts(async after => { afters.push(after); return after === "" ? { ...page, next_after: id } : { ...page, accounts: [{ ...item, id: secondID }] }; }, "1");
  assert.deepEqual(afters, ["", id]); assert.deepEqual(value.accounts.map(account => account.id), [id, secondID]); assert.equal(value.last.next_after, null);
  const empty = await readDispatchAccounts(async () => ({ ...page, accounts: [] }), "1");
  assert.deepEqual(empty.accounts, []); assert.match(dispatchControlText(empty.last), /历史尚未开放/);
});

test("dispatch rejects mixed authority/source versions, repeats and partial selector reads", async () => {
  for (const bad of [{ ...page, authority_revision: "2" }, { ...page, connection_revision: "4", accounts: [{ ...item, id: secondID }] }, page, { ...page, accounts: [], next_after: secondID }, { ...page, accounts: [{ ...item, id: secondID }], next_after: id }]) {
    await assert.rejects(readDispatchAccounts(async after => after === "" ? { ...page, next_after: id } : bad, "1"));
  }
  await assert.rejects(readDispatchAccounts(async after => { if (after) throw new Error("later page failed"); return { ...page, next_after: id }; }, "1"));
  await assert.rejects(readDispatchAccounts(async () => page, "2"));
  let number = 0;
  await assert.rejects(readDispatchAccounts(async () => { const next = Buffer.alloc(16, ++number).toString("base64url"); return { ...page, accounts: [{ ...item, id: next }], next_after: next }; }, "1"));
  assert.equal(number, 20);
});

test("dispatch native details require all current binding fields and authority revision", async () => {
  assert.equal(await readDispatchObservation(async () => observation, input, "1"), observation);
  for (const change of [{ id: secondID }, { revision: "3" }, { source_id: secondID }, { connection_revision: "4" }, { authority_revision: "2" }]) await assert.rejects(readDispatchObservation(async () => ({ ...observation, ...change }), input, "1"));
});

test("dispatch display preserves false and unknown without inventing pause ownership or empty history", () => {
  assert.match(dispatchObservationText(observation), /已关闭/);
  assert.doesNotMatch(dispatchObservationText(observation), /暂停中|已恢复/);
  const unknown: DispatchObservation = { ...observation, configured_schedulable: null, native_status: null, read: { ...observation.read, state: "failed", reason: "INVALID_RESPONSE" } };
  assert.doesNotMatch(dispatchObservationText(unknown), /已关闭|已开启/);
  assert.match(dispatchObservationText({ ...unknown, read: { ...unknown.read, reason: "ACCOUNT_IDENTITY_CHANGED" } }), /身份已变化/);
  assert.match(dispatchObservationText({ ...unknown, read: { ...unknown.read, state: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" } }), /配置未知/);
  assert.match(dispatchControlText(null), /尚未读取/);
});
