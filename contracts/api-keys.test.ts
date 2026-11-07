import assert from "node:assert/strict";
import { test } from "node:test";
import { apiCatalog, apiKey, apiKeyIssuance, apiKeyPage } from "./api-keys";
import { createAPIKeysClient } from "./client";

const id = "A".repeat(22), secondID = "B".repeat(21) + "A";
const at = "2026-11-08T00:00:00.000001Z";
const key = { id, name: "report client", mode: "read", prefix: "spk_live_" + id, revision: "1",
  created_at: at, rotated_at: null, last_used_at: null, revoked_at: null };
const issuance = { key, secret: key.prefix + "_" + "A".repeat(43) };
const access = { user: { id, username: "reader", role: "user", revision: "1" },
  all_data: false, features: [], account_ids: [], participant_ids: [] };
const operations = [
  { name: "apiKeys.access", path: "/api/v1/api-keys/access", method: "GET", feature: "", contract: "api-keys-v1", management: false, availability: "available" },
  { name: "apiKeys.catalog", path: "/api/v1/api-keys/catalog", method: "GET", feature: "", contract: "api-keys-v1", management: false, availability: "available" },
];
const errors = Object.entries({ BAD_REQUEST: 400, UNAUTHORIZED: 401, FORBIDDEN: 403, SOURCE_BLOCKED: 403,
  NOT_FOUND: 404, METHOD_NOT_SUPPORTED: 405, CONFLICT: 409, CONNECTION_NOT_CONFIGURED: 409,
  TOO_MANY_REQUESTS: 429, INTERNAL_SERVER_ERROR: 500 }).map(([code, status]) => ({ code, status, meaning: "Public explanation" }));
const catalog = { version: "api-keys-v1", access, credential_mode: "read", operations, errors,
  limitations: ["RETAINED_LOCAL_FACTS_ONLY", "FULL_USAGE_COVERAGE_UNVERIFIED", "FINANCIAL_APPLICATION_UNAVAILABLE", "NO_COMBINED_OVERVIEW_ENDPOINT", "MUTATION_RESPONSE_LOSS_REQUIRES_READBACK"] };

test("API Key metadata preserves unknown last-use and binds one-time secrets to the key", () => {
  assert.ok(apiKey.safeParse(key).success);
  assert.ok(apiKeyIssuance.safeParse(issuance).success);
  assert.ok(apiKeyIssuance.safeParse({ ...issuance, key: { ...key, name: "中文 😀 client" } }).success);
  for (const value of [{ ...key, secret: issuance.secret }, { ...key, digest: "private" }, { ...key, prefix: "spk_live_wrong" },
    { ...key, mode: "readonly" }, { ...key, last_used_at: "" }, { ...key, revision: "01" }]) assert.equal(apiKey.safeParse(value).success, false);
  for (const value of [{ ...issuance, secret: "browser-session" },
    { ...issuance, secret: "spk_live_" + secondID + "_" + "A".repeat(43) },
    { ...issuance, secret: issuance.secret.slice(0, -1) + "B" },
    { ...issuance, key: { ...key, revoked_at: at } }]) assert.equal(apiKeyIssuance.safeParse(value).success, false);
  assert.ok(apiKeyPage.safeParse({ keys: [key], next_after: null }).success);
  for (const value of [{ keys: [key, key], next_after: null }, { keys: [], next_after: id },
    { keys: [key], next_after: secondID }]) assert.equal(apiKeyPage.safeParse(value).success, false);
});

test("API catalog is current-authority scoped and cannot advertise financial dispatch", () => {
  assert.ok(apiCatalog.safeParse(catalog).success);
  const management = { name: "walletApplications.request", path: "/api/v1/wallet-applications/request", method: "POST",
    feature: "balances", contract: "wallet-applications-v1", management: true, availability: "not_dispatched_only" };
  assert.ok(apiCatalog.safeParse({ ...catalog, credential_mode: "management", access: { ...access, all_data: true, user: { ...access.user, role: "admin" } },
    operations: [...operations, management] }).success);
  for (const value of [{ ...catalog, credential_mode: "management" }, { ...catalog, operations: [...operations, management] },
    { ...catalog, operations: [...operations, { ...operations[0], name: "accounts.get", path: "/api/v1/accounts/get", method: "POST", feature: "accounts", contract: "accounts-v1" }] },
    { ...catalog, operations: [operations[0], operations[0]] }, { ...catalog, limitations: catalog.limitations.slice(1) },
    { ...catalog, errors: [] }, { ...catalog, errors: errors.map(row => ({ ...row, status: 200 })) },
    { ...catalog, operations: operations.map(row => ({ ...row, management: true, availability: "available" })) }]) assert.equal(apiCatalog.safeParse(value).success, false);
});

test("API Key client validates strict lifecycle inputs and never retries a lost issuance", async () => {
  let calls = 0;
  const input = { name: "report client", mode: "read" as const };
  const client = createAPIKeysClient("http://127.0.0.1:3000", async (request, init) => {
    const req = new Request(request, init); calls++;
    assert.equal(req.url, "http://127.0.0.1:3000/api/v1/api-keys/create");
    assert.equal(req.method, "POST"); assert.deepEqual(await req.json(), input);
    assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error");
    return Response.json(issuance);
  });
  assert.deepEqual(await client.create(input), issuance);
  for (const value of [{ ...input, name: "" }, { ...input, name: " padded " }, { ...input, name: "bad\nname" },
    { ...input, name: "\ud800" }, { ...input, name: "x".repeat(81) }, { ...input, mode: "readonly" },
    { ...input, user_id: secondID }]) await assert.rejects(client.create(value as never));
  assert.equal(calls, 1);
  const lost = createAPIKeysClient("http://127.0.0.1:3000", async () => { calls++; throw new TypeError("connection lost"); });
  await assert.rejects(lost.create(input)); assert.equal(calls, 2);
  const malformed = createAPIKeysClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ ...issuance, secret: "wrong" }); });
  await assert.rejects(malformed.rotate({ id, revision: "1" })); assert.equal(calls, 3);
});
