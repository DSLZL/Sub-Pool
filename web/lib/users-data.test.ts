import assert from "node:assert/strict";
import { test } from "node:test";
import type { ManagedUser } from "@sub-pool/contracts/users";
import { checkUserSave } from "./users-data";

const id = (n: number) => Buffer.alloc(16, n).toString("base64url");
const user = (): ManagedUser => ({
  id: id(1), revision: "9", username: "reader", role: "user", enabled: true,
  created_at: "2026-11-11T00:00:00Z", last_login_at: null,
  features: ["accounts", "pools"], account_ids: [id(2), id(3)], participant_ids: [id(4)],
  feature_count: 2, account_count: 2, participant_count: 1,
});

test("user create acknowledgement must confirm initial revision and every submitted public field", () => {
  const input = user(), created = { ...input, revision: "1" };
  assert.doesNotThrow(() => checkUserSave(created, input));
  for (const wrong of [{ revision: "2" }, { username: "other-reader" }, { role: "admin" as const }, { enabled: false },
    { features: ["accounts", "balances"] as ManagedUser["features"] },
    { account_ids: [id(2), id(5)] }, { participant_ids: [id(2)] }]) {
    assert.throws(() => checkUserSave({ ...created, ...wrong }, input));
  }
});

test("user update acknowledgement binds identity, exact next revision and independent grant sets without mutating input", () => {
  const input = user(), previous = { id: input.id, revision: "8" }, frozen = JSON.stringify(input);
  assert.doesNotThrow(() => checkUserSave({ ...input, features: [...input.features].reverse(), account_ids: [...input.account_ids].reverse() }, input, previous));
  assert.equal(JSON.stringify(input), frozen);
  for (const wrong of [{ id: id(6) }, { revision: "8" }, { revision: "10" },
    { account_ids: [id(4)] }, { participant_ids: [id(2), id(3)] }, { username: "other-reader" }]) {
    assert.throws(() => checkUserSave({ ...input, ...wrong }, input, previous));
  }
  const large = { id: input.id, revision: "9007199254740993" };
  assert.doesNotThrow(() => checkUserSave({ ...input, revision: "9007199254740994" }, input, large));
  assert.throws(() => checkUserSave({ ...input, revision: "9007199254740992" }, input, large));
});
