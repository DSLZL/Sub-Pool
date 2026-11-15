import assert from "node:assert/strict";
import { test } from "node:test";
import { apiCatalog, type APIKey } from "@sub-pool/contracts/api-keys";
import type { Access } from "@sub-pool/contracts/users";
import { checkAPIKeyCatalog, checkAPIKeyReceipt, readAPIKeyPages } from "./api-keys-data";

const at = "2026-11-08T00:00:00.000001Z";
const key = (n: number): APIKey => {
  const id = String(n).padStart(21, "A") + "A";
  return { id, name: "client " + n, mode: "read", prefix: "spk_live_" + id, revision: "1",
    created_at: at, rotated_at: null, last_used_at: null, revoked_at: null };
};
const first = key(1), second = key(2);
const errors = Object.entries({ BAD_REQUEST: 400, UNAUTHORIZED: 401, FORBIDDEN: 403, SOURCE_BLOCKED: 403,
  NOT_FOUND: 404, METHOD_NOT_SUPPORTED: 405, CONFLICT: 409, CONNECTION_NOT_CONFIGURED: 409, CONNECTION_REVERIFICATION_REQUIRED: 409,
  TOO_MANY_REQUESTS: 429, INTERNAL_SERVER_ERROR: 500 }).map(([code, status]) => ({ code, status, meaning: "Safe explanation" }));
const catalog = apiCatalog.parse({ version: "api-keys-v1", credential_mode: "session",
  access: { user: { id: first.id, username: "reader", role: "user", revision: "1" }, all_data: false, features: ["usage", "pools"], account_ids: [second.id], participant_ids: [first.id] },
  operations: ["access", "catalog"].map(action => ({ name: "apiKeys." + action, path: "/api/v1/api-keys/" + action,
    method: "GET", feature: "", contract: "api-keys-v1", management: false, availability: "available" })), errors,
  limitations: ["RETAINED_LOCAL_FACTS_ONLY", "FULL_USAGE_COVERAGE_UNVERIFIED", "FINANCIAL_APPLICATION_UNAVAILABLE", "NO_COMBINED_OVERVIEW_ENDPOINT", "MUTATION_RESPONSE_LOSS_REQUIRES_READBACK"] });

test("Key reads consume every page using opaque server cursors and preserve unknown timestamps", async () => {
  const signal = new AbortController().signal, after: string[] = [];
  const high = { ...first, id: "z".repeat(21) + "A", prefix: "spk_live_" + "z".repeat(21) + "A" };
  const rows = await readAPIKeyPages({ list: async (input, options) => {
    assert.equal(options?.signal, signal); after.push(input.after);
    return input.after === "" ? { keys: [high], next_after: high.id } : { keys: [second], next_after: null };
  } }, signal);
  assert.deepEqual(after, ["", high.id]); assert.deepEqual(rows, [high, second]); assert.equal(rows[0]?.last_used_at, null);
});

test("Key reads reject duplicate, non-progressing, failed or over-limit pages without partial results", async () => {
  await assert.rejects(readAPIKeyPages({ list: async input => input.after === ""
    ? { keys: [first], next_after: first.id } : { keys: [first], next_after: null } }));
  await assert.rejects(readAPIKeyPages({ list: async () => ({ keys: [], next_after: first.id }) }));
  await assert.rejects(readAPIKeyPages({ list: async input => {
    if (input.after) throw new Error("later page unavailable");
    return { keys: [first], next_after: first.id };
  } }));
  let page = 0;
  await assert.rejects(readAPIKeyPages({ list: async () => {
    const rows = Array.from({ length: 100 }, () => key(++page));
    const last = rows.at(-1); assert.ok(last);
    return { keys: rows, next_after: last.id };
  } }));
  assert.ok(page >= 2000);
});

test("Key catalog must match the current full session authority, independent of grant array order", () => {
  assert.equal(checkAPIKeyCatalog(catalog, { ...catalog.access, features: ["pools", "usage"] }), catalog);
  for (const access of [{ ...catalog.access, user: { ...catalog.access.user, revision: "2" } },
    { ...catalog.access, user: { ...catalog.access.user, id: second.id } },
    { ...catalog.access, all_data: true }, { ...catalog.access, features: ["usage"] },
    { ...catalog.access, account_ids: [] }, { ...catalog.access, participant_ids: [] }] satisfies Access[]) {
    assert.throws(() => checkAPIKeyCatalog(catalog, access));
  }
  assert.throws(() => checkAPIKeyCatalog({ ...catalog, credential_mode: "read" }, catalog.access));
});

test("Key receipts bind create, rotation and revocation to the actual submitted metadata and revision", () => {
  const issued = { key: first, secret: first.prefix + "_" + "A".repeat(43) };
  assert.equal(checkAPIKeyReceipt({ kind: "create", name: first.name, mode: "read" }, issued), issued);
  const rotated = { key: { ...first, revision: "2", rotated_at: at }, secret: issued.secret };
  assert.equal(checkAPIKeyReceipt({ kind: "rotate", key: first }, rotated), rotated);
  const revoked = { ...first, revision: "2", revoked_at: at };
  assert.equal(checkAPIKeyReceipt({ kind: "revoke", key: first }, revoked), revoked);
  for (const value of [{ ...issued, key: second }, { ...issued, key: { ...first, mode: "management" as const } },
    { ...issued, key: { ...first, revision: "2" } }]) assert.throws(() => checkAPIKeyReceipt({ kind: "create", name: first.name, mode: "read" }, value));
  for (const value of [{ ...rotated, key: { ...rotated.key, id: second.id } }, { ...rotated, key: { ...rotated.key, revision: "3" } },
    { ...rotated, key: { ...rotated.key, created_at: "2026-11-08T00:00:00.000002Z" } }, { ...rotated, key: { ...rotated.key, rotated_at: null } }]) {
    assert.throws(() => checkAPIKeyReceipt({ kind: "rotate", key: first }, value));
  }
  assert.throws(() => checkAPIKeyReceipt({ kind: "revoke", key: first }, issued));
  assert.throws(() => checkAPIKeyReceipt({ kind: "revoke", key: first }, { ...revoked, revision: "1" }));
});
