import assert from "node:assert/strict";
import { test } from "node:test";
import { createPricingClient } from "./client";
import { pricingApplyInput, pricingChange, pricingControl, pricingHistory, pricingHistoryInput, pricingOperation } from "./pricing";

const id = "A".repeat(21) + "Q", fingerprint = "a".repeat(64);
const change = { kind: "fast", force_fast: true, free_fast: false, value: null } as const;
const input = { source_id: id, revision: "2", group_id: "1", request_id: id, expected_fingerprint: fingerprint, acknowledge_shared_impact: true, changes: [change] } as const;
const operation = {
  id: "1", revision: "2", actor_id: id, request_id: id, source_id: id, connection_revision: "2", group_id: "1", group_name: "Owned group", changes: [change],
  state: "applied", reason: "APPLIED", before_fingerprint: fingerprint, after_fingerprint: "b".repeat(64), created_at: "2026-11-10T00:00:00Z", updated_at: "2026-11-10T00:00:01Z",
};

test("pricing control binds explicit ownership, native conditions and complete operation states", () => {
  const configuration = { rate_multiplier: "1", force_openai_fast: false, free_openai_fast: false, long_context_pricing_enabled: true, model_rules: [], unreported: [], projection: "base_fields_only", unit: "native_config_value" };
  const control = { version: "pricing-control-v1", source_id: id, connection_revision: "2", group_id: "1", can_apply: true, reason: "CONTROLLED_ENVIRONMENT", fingerprint, configuration, owned_change_id: null, checked_at: operation.created_at };
  assert.ok(pricingControl.safeParse(control).success);
  for (const bad of [{ ...control, reason: "STANDARD_ADMIN_REQUIRED" }, { ...control, fingerprint: null }, { ...control, owned_change_id: "1" }, { ...control, secret: "native" }]) assert.equal(pricingControl.safeParse(bad).success, false);
  assert.ok(pricingOperation.safeParse(operation).success);
  for (const bad of [{ ...operation, after_fingerprint: null }, { ...operation, state: "needs_review" }, { ...operation, state: "reviewed", reason: "REVERTED" }, { ...operation, state: "rejected", reason: "UPSTREAM_REJECTED" }, { ...operation, updated_at: "2026-11-09T00:00:00Z" }]) assert.equal(pricingOperation.safeParse(bad).success, false);
  for (const [state, reason] of [["needs_review", "WRITE_RESULT_UNKNOWN"], ["reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN"], ["rejected", "UPSTREAM_REJECTED"]]) assert.ok(pricingOperation.safeParse({ ...operation, state, reason, after_fingerprint: null }).success);
  assert.ok(pricingHistory.safeParse({ operations: [{ ...operation, id: "9" }, { ...operation, id: "10" }], next_after: "10" }).success);
  for (const ids of [["10", "9"], ["9", "9"]]) assert.equal(pricingHistory.safeParse({ operations: ids.map(id => ({ ...operation, id })), next_after: ids.at(-1) }).success, false);
});

test("pricing changes reject rounded native numbers, duplicate edits, implicit acknowledgement and unrelated fields", () => {
  assert.ok(pricingApplyInput.safeParse(input).success);
  const scalar = { kind: "model_price", rule_index: 0, price_field: "input_price", value: "0.00000001" };
  assert.ok(pricingChange.safeParse(scalar).success);
  assert.ok(pricingChange.safeParse({ ...scalar, value: null }).success);
  for (const value of ["0.100000000000000001", "1e-8", "-1", "1.0", "NaN"]) assert.equal(pricingChange.safeParse({ ...scalar, value }).success, false);
  for (const bad of [{ ...input, acknowledge_shared_impact: false }, { ...input, changes: [change, change] }, { ...input, changes: [{ ...change, enabled: false }] }, { ...input, expected_fingerprint: "stale" }, { ...input, changes: [{ ...scalar, rule_index: 100 }] }]) assert.equal(pricingApplyInput.safeParse(bad).success, false);
});

test("pricing write client validates before dispatch and never replays an uncertain network result", async () => {
  let calls = 0;
  const client = createPricingClient("http://127.0.0.1:3000", async (source, init) => {
    const request = new Request(source, init); calls++;
    assert.equal(request.url, "http://127.0.0.1:3000/api/v1/pricing/apply");
    assert.deepEqual(await request.json(), input);
    throw new TypeError("owned response lost after write");
  });
  await assert.rejects(client.apply({ ...input, acknowledge_shared_impact: false } as never)); assert.equal(calls, 0);
  await assert.rejects(client.apply({ ...input, changes: [...input.changes] })); assert.equal(calls, 1);
});

test("pricing history explicitly selects global or source-local receipts with the same strict read contract", async () => {
  const input = { source_id: "", group_id: "", after: "0", limit: 100 };
  for (const source_id of ["", id]) assert.ok(pricingHistoryInput.safeParse({ ...input, source_id }).success);
  for (const bad of [{ group_id: "", after: "0", limit: 100 }, { ...input, source_id: null }, { ...input, source_id: "invalid" }, { ...input, after: "01" }, { ...input, limit: 101 }]) assert.equal(pricingHistoryInput.safeParse(bad).success, false);
  let calls = 0;
  const client = createPricingClient("http://127.0.0.1:3000", async (source, init) => {
    const request = new Request(source, init); calls++;
    assert.equal(request.url, "http://127.0.0.1:3000/api/v1/pricing/history");
    assert.deepEqual(await request.json(), input);
    return Response.json({ operations: [operation, { ...operation, id: "2", source_id: "B".repeat(21) + "Q" }], next_after: null });
  });
  const result = await client.history(input);
  assert.equal(result.operations.length, 2); assert.equal(calls, 1);
});
