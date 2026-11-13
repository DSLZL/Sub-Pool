import { test } from "node:test";
import assert from "node:assert/strict";
import { participantExit, participantExitInput, participantExitResultInput } from "./participants";

const id = "a".repeat(21) + "A";
const receipt = { participant_id: id, previous_revision: "9007199254740993", revision: "9007199254740994", binding_id: "3", name: "Member", effective_at: "2026-11-11T01:02:03.000001Z", pool_contract_ids: ["9", "10"] };

test("exit receipts keep exact revisions and cannot invent settlement or change a binding", () => {
  assert.deepEqual(participantExit.parse(receipt), receipt);
  assert.ok(participantExit.safeParse({ ...receipt, pool_contract_ids: [] }).success);
  for (const change of [{ revision: "9007199254740993" }, { pool_contract_ids: ["9", "9"] }, { binding_id: "0" }, { settlement_complete: true }]) {
    assert.equal(participantExit.safeParse({ ...receipt, ...change }).success, false);
  }
  assert.ok(participantExitInput.safeParse({ id, revision: "1", confirmation: "Member" }).success);
  assert.equal(participantExitInput.safeParse({ id, revision: "1", confirmation: "Member", confirmed: true }).success, false);
  assert.equal(participantExitResultInput.safeParse({ id, revision: "1", confirmation: "Member" }).success, false);
});
