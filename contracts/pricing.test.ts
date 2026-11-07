import assert from "node:assert/strict";
import { test } from "node:test";
import { createPricingClient } from "./client";
import { pricingDefault, pricingDefaultInput, pricingGroup, pricingGroupInput, pricingGroups, pricingGroupsInput } from "./pricing";

const source_id = "A".repeat(21) + "Q";
const read = { id: "pricing.groups.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 };
const context = { revision: "2", source_id, checked_at: "2026-11-07T00:00:00.000000Z", run_mode: null, mode_read: { ...read, id: "run_mode", state: "unverified", reason: "MODE_REQUIRES_JWT", http_status: null, latency_ms: null }, can_apply: false, write_reason: "PRICING_WRITE_UNQUALIFIED" };
const groups = { ...context, page: 1, total: "1", next_page: null, groups: [{ id: "9223372036854775807", name: "Native group", status: "active", account_count: "1" }], read };
const unavailableConfig = { rate_multiplier: null, force_openai_fast: null, free_openai_fast: null, long_context_pricing_enabled: null, model_rules: null, unreported: ["rate_multiplier", "force_openai_fast", "free_openai_fast", "long_context_pricing_enabled", "model_pricing"], projection: "base_fields_only", unit: "native_config_value" };
const detail = { ...context, page: 1, total: null, next_page: null, group_id: "9223372036854775807", group: groups.groups[0], configuration: unavailableConfig, read: { ...read, id: "pricing.group.read" }, accounts: [], accounts_read: { ...read, id: "pricing.accounts.read", state: "denied", reason: "FORBIDDEN", http_status: 403 } };

test("pricing schemas preserve partial reads and reject forged capability, pagination, precision and field coverage", () => {
  assert.ok(pricingGroups.safeParse(groups).success);
  assert.ok(pricingGroup.safeParse(detail).success);
  for (const value of [{ ...groups, can_apply: true }, { ...groups, run_mode: "standard" }, { ...groups, total: "2" }, { ...groups, groups: [groups.groups[0], groups.groups[0]], total: "2" }, { ...groups, raw_upstream: "secret" }, { ...groups, groups: [{ ...groups.groups[0], id: "9223372036854775808" }] }]) assert.equal(pricingGroups.safeParse(value).success, false);
  for (const value of [{ ...detail, group_id: "1" }, { ...detail, total: "0" }, { ...detail, configuration: { ...unavailableConfig, unreported: [] } }, { ...detail, configuration: { ...unavailableConfig, rate_multiplier: 1.25 } }]) assert.equal(pricingGroup.safeParse(value).success, false);
  const missing = { ...context, model: "missing", found: false, values: null, unreported: [], projection: "base_fields_only", unprojected: ["reasoning_effort_multipliers"], unit: "USD_per_token", read: { ...read, id: "pricing.default.read" } };
  assert.ok(pricingDefault.safeParse(missing).success);
  assert.equal(pricingDefault.safeParse({ ...missing, found: null }).success, false);
  assert.equal(pricingDefault.safeParse({ ...missing, found: true, values: { input_price: 0 } }).success, false);
});

test("pricing inputs bind source, exact target and bounded text without accepting write intents", () => {
  const input = { revision: "2", source_id, page: 1, search: "native" };
  assert.ok(pricingGroupsInput.safeParse(input).success);
  assert.ok(pricingGroupInput.safeParse({ revision: "2", source_id, group_id: "9223372036854775807", page: 1 }).success);
  assert.ok(pricingDefaultInput.safeParse({ revision: "2", source_id, model: "model & name" }).success);
  for (const value of [{ ...input, search: "中".repeat(34) }, { ...input, search: "bad\nname" }, { ...input, page: 0 }, { ...input, source_id: "foreign" }, { ...input, confirmation: "apply" }]) assert.equal(pricingGroupsInput.safeParse(value).success, false);
});

test("pricing client uses fixed validated reads and does not retry malformed success or a native denial", async () => {
  let calls = 0;
  const client = createPricingClient("http://127.0.0.1:3000", async (input, init) => {
    const req = new Request(input, init); calls++;
    assert.equal(req.url, "http://127.0.0.1:3000/api/v1/pricing/groups"); assert.equal(req.method, "POST");
    assert.deepEqual(await req.json(), { revision: "2", source_id, page: 1, search: "" });
    assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error");
    return Response.json(groups);
  });
  assert.deepEqual(await client.groups({ revision: "2", source_id, page: 1, search: "" }), groups); assert.equal(calls, 1);
  const bad = createPricingClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ ...groups, can_apply: true }); });
  await assert.rejects(bad.groups({ revision: "2", source_id, page: 1, search: "" })); assert.equal(calls, 2);
  const denied = createPricingClient("http://127.0.0.1:3000", async () => { calls++; return Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "Denied", data: {} }, { status: 403 }); });
  await assert.rejects(denied.group({ revision: "2", source_id, group_id: "1", page: 1 })); assert.equal(calls, 3);
});
