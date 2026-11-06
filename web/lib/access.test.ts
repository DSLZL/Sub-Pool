import assert from "node:assert/strict";
import { test } from "node:test";
import type { Access } from "@sub-pool/contracts/users";
import { canAccess } from "./access";

test("navigation uses current explicit read grants and keeps management separate", () => {
  const access: Access = { user: { id: "A".repeat(22), username: "reader", role: "user", revision: "1" },
    all_data: false, features: ["usage", "pools"], account_ids: [], participant_ids: [] };
  assert.equal(canAccess("all", null), false);
  assert.equal(canAccess("usage", access), true);
  assert.equal(canAccess("pools", access), true);
  assert.equal(canAccess("admin", access), false);
  assert.equal(canAccess("accounts", access), false);
  assert.equal(canAccess("all", access), true);
  assert.equal(canAccess("unknown", access), false);
  assert.equal(canAccess("admin", { ...access, user: { ...access.user, role: "admin" }, all_data: true }), true);
});
