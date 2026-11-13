import assert from "node:assert/strict";
import { test } from "node:test";
import { pricingDefault, pricingGroup, pricingGroups, pricingOperation } from "@sub-pool/contracts/pricing";
import { checkPricingReceipt, pricingActionAllowed, pricingDefaultText, pricingOperationText, pricingReadText, pricingRequestID, pricingRuleText, readPricingDefault, readPricingGroup, readPricingGroups, readPricingHistory } from "./pricing-data";

const source = "A".repeat(21) + "Q", revision = "2";
const check = { state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 };
const context = { source_id: source, revision, checked_at: "2026-11-07T00:00:00.000000Z", run_mode: null, mode_read: { id: "run_mode", state: "unverified", reason: "MODE_REQUIRES_JWT", http_status: null, latency_ms: null }, can_apply: false, write_reason: "PRICING_WRITE_UNQUALIFIED" };
const group = { id: "9", name: "Native group", status: "active", account_count: "101" };
const configuration = { rate_multiplier: "1.25", force_openai_fast: true, free_openai_fast: false, long_context_pricing_enabled: true, model_rules: [], unreported: [], projection: "base_fields_only", unit: "native_config_value" };
const targets = (page: number) => pricingGroups.parse({ ...context, page, total: "101", next_page: page === 1 ? 2 : null, groups: Array.from({ length: page === 1 ? 100 : 1 }, (_, index) => ({ ...group, id: String((page - 1) * 100 + index + 1) })), read: { ...check, id: "pricing.groups.read" } });
const detail = (page: number) => pricingGroup.parse({ ...context, page, group_id: "9", group, configuration, read: { ...check, id: "pricing.group.read" }, total: "101", next_page: page === 1 ? 2 : null,
  accounts: Array.from({ length: page === 1 ? 100 : 1 }, (_, index) => ({ upstream_id: String((page - 1) * 100 + index + 1), name: "Native account", platform: "openai", type: "oauth", status: "active", schedulable: true, eligibility: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" })), accounts_read: { ...check, id: "pricing.accounts.read" } });

test("pricing target reads require complete stable pages and discard a valid first page on later denial", async () => {
  const all = await readPricingGroups(async page => targets(page), source, revision);
  assert.equal(all.groups.length, 101); assert.equal(all.groups[100]?.id, "101");
  const denied = await readPricingGroups(async page => page === 1 ? targets(page) : { ...targets(page), groups: [], total: null, next_page: null, read: { ...targets(page).read, state: "denied", reason: "FORBIDDEN", http_status: 403 } }, source, revision);
  assert.deepEqual(denied.groups, []); assert.equal(denied.last.read.state, "denied");
  assert.match(pricingReadText(denied.last.read), /读取权限/);
});

test("pricing target changes, repeated identities, inconsistent totals and continuations reject the whole read", async () => {
  for (const change of [
    (page: ReturnType<typeof targets>) => ({ ...page, revision: "3" }),
    (page: ReturnType<typeof targets>) => ({ ...page, source_id: "B".repeat(21) + "Q" }),
    (page: ReturnType<typeof targets>) => ({ ...page, groups: [{ ...group, id: "1" }] }),
    (page: ReturnType<typeof targets>) => ({ ...page, total: "102" }),
  ]) await assert.rejects(readPricingGroups(async page => page === 1 ? targets(page) : change(targets(page)), source, revision));
  await assert.rejects(readPricingGroups(async () => ({ ...targets(1), next_page: 3 }), source, revision));
});

test("selected pricing reads preserve independent config when a later account page is denied", async () => {
  const all = await readPricingGroup(async page => detail(page), source, revision, "9");
  assert.equal(all.accounts.length, 101); assert.equal(all.snapshot.configuration?.rate_multiplier, "1.25");
  const denied = await readPricingGroup(async page => page === 1 ? detail(page) : { ...detail(page), accounts: [], total: null, next_page: null, accounts_read: { ...detail(page).accounts_read, state: "denied", reason: "FORBIDDEN", http_status: 403 } }, source, revision, "9");
  assert.deepEqual(denied.accounts, []); assert.equal(denied.snapshot.configuration?.free_openai_fast, false); assert.equal(denied.snapshot.accounts_read.state, "denied");
  for (const change of [
    (page: ReturnType<typeof detail>) => ({ ...page, group_id: "8" }),
    (page: ReturnType<typeof detail>) => ({ ...page, configuration: { ...configuration, rate_multiplier: "2", model_rules: [], unreported: [], projection: "base_fields_only", unit: "native_config_value" } }),
    (page: ReturnType<typeof detail>) => ({ ...page, accounts: [{ ...page.accounts[0], upstream_id: "1" }] }),
  ]) await assert.rejects(readPricingGroup(async page => page === 1 ? detail(page) : pricingGroup.parse(change(detail(page))), source, revision, "9"));
});

test("pricing history requires stable source, monotonic numeric pages and complete continuation reads", async () => {
  const operation = (id: string) => pricingOperation.parse({ id, revision: "2", actor_id: source, request_id: source, source_id: source, connection_revision: revision, group_id: "9", group_name: "Native group", changes: [{ kind: "long_context", enabled: false, value: null }], state: "needs_review", reason: "WRITE_RESULT_UNKNOWN", before_fingerprint: "a".repeat(64), after_fingerprint: null, created_at: "2026-11-10T00:00:00Z", updated_at: "2026-11-10T00:00:01Z" });
  const page = (ids: string[], next_after: string | null = null) => ({ operations: ids.map(operation), next_after });
  const result = await readPricingHistory(async after => after === "0" ? page(["9"], "9") : page(["10"]), source);
  assert.deepEqual(result.map(item => item.id), ["9", "10"]);
  for (const read of [
    async () => page(["9"], "8"),
    async (after: string) => after === "0" ? page(["9"], "9") : page(["9"]),
    async (after: string) => after === "0" ? page(["9"], "9") : { operations: [{ ...operation("10"), source_id: "B".repeat(21) + "Q" }], next_after: null },
    async (after: string) => { if (after !== "0") throw new TypeError("later page lost"); return page(["9"], "9"); },
    async () => page([], "9"),
  ]) await assert.rejects(readPricingHistory(read, source));
  const global = await readPricingHistory(async after => after === "0" ? page(["9"], "9") : { operations: [{ ...operation("10"), source_id: "B".repeat(21) + "Q" }], next_after: null }, "");
  assert.deepEqual(global.map(item => item.id), ["9", "10"]);
  await assert.rejects(readPricingHistory(async after => { if (after !== "0") throw new TypeError("later global page lost"); return page(["9"], "9"); }, ""));
  await assert.rejects(readPricingHistory(async () => page(["9"], "9"), ""));
  const first = result[0]; assert.ok(first);
  checkPricingReceipt(first, { source_id: source, connection_revision: revision, group_id: "9", request_id: source });
  assert.throws(() => checkPricingReceipt(first, { source_id: source, connection_revision: revision, group_id: "8", request_id: source }));
  assert.match(pricingOperationText(first), /不能.*重试/);
  const ids = Array.from({ length: 100 }, pricingRequestID); assert.equal(new Set(ids).size, 100); for (const id of ids) assert.match(id, /^[A-Za-z0-9_-]{21}[AQgw]$/);
});

test("native default reads bind source, revision and selected model without filling unknown or rounding values", async () => {
  const model = "actual-native-model";
  const missing = pricingDefault.parse({ ...context, model, found: false, values: null, unreported: [], projection: "base_fields_only", unprojected: ["reasoning_effort_multipliers"], unit: "USD_per_token", read: { ...check, id: "pricing.default.read" } });
  assert.equal(await readPricingDefault(async () => missing, source, revision, model), missing);
  for (const change of [{ source_id: "B".repeat(21) + "Q" }, { revision: "3" }, { model: "another-model" }]) await assert.rejects(readPricingDefault(async () => ({ ...missing, ...change }), source, revision, model));
  assert.match(pricingDefaultText(missing, model, null), /保持未知，不补 0/);
  const values = { input_price: "0.000000000000000123", output_price: "0", cache_write_price: null, cache_write_1h_price: null, cache_read_price: null, image_input_price: null, image_output_price: null };
  const known = pricingDefault.parse({ ...missing, found: true, values, native_details: { reasoning_effort_multipliers: '{"high":1.125}' }, unprojected: [] });
  const text = pricingDefaultText(known, model, null);
  assert.match(text, /USD\/token/); assert.ok(text.includes(values.input_price)); assert.ok(text.includes("output_price=0")); assert.match(text, /非分组最终价/);
  assert.match(pricingDefaultText(known, "different-model", null), /正在查询/);
  assert.equal(pricingDefaultText(known, model, "读取失败"), "读取失败");
  const rule = { models: [model], billing_mode: "token" as const, values: { ...values, per_request_price: null, fast_multiplier: "2", flex_multiplier: null }, unreported: [], unprojected: [], native_details: { intervals: '[{"min_tokens":9007199254740993,"input_price":0.000000000000000123}]' } };
  const rendered = pricingRuleText(rule);
  assert.ok(rendered.includes("9007199254740993")); assert.ok(rendered.includes(values.input_price)); assert.match(rendered, /并非最终扣费/);
});

test("retained unknown pricing can be locally reviewed without a native connection, while remote actions require current source and revision", () => {
  const unknown = pricingOperation.parse({ id: "1", revision: "2", actor_id: source, request_id: source, source_id: source, connection_revision: revision, group_id: "9", group_name: "Native group",
    changes: [{ kind: "long_context", enabled: false, value: null }], state: "needs_review", reason: "WRITE_RESULT_UNKNOWN", before_fingerprint: "a".repeat(64), after_fingerprint: null,
    created_at: "2026-11-10T00:00:00Z", updated_at: "2026-11-10T00:00:01Z" });
  const current = { sourceID: source, revision }, replacement = { sourceID: "B".repeat(21) + "Q", revision };
  const rejected = pricingOperation.parse({ ...unknown, state: "rejected", reason: "UPSTREAM_REJECTED" });
  const applied = pricingOperation.parse({ ...unknown, state: "applied", reason: "APPLIED", after_fingerprint: "b".repeat(64) });
  for (const target of [null, current, replacement, { ...current, revision: "3" }]) assert.equal(pricingActionAllowed("review", unknown, target), true);
  assert.equal(pricingActionAllowed("retry", rejected, current), true);
  assert.equal(pricingActionAllowed("revert", applied, current), true);
  for (const target of [null, replacement, { ...current, revision: "3" }]) {
    assert.equal(pricingActionAllowed("retry", rejected, target), false);
    assert.equal(pricingActionAllowed("revert", applied, target), false);
  }
  for (const [state, reason] of [["applying", "APPLY_PENDING"], ["reverting", "REVERT_PENDING"], ["reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN"]] as const) {
    const operation = pricingOperation.parse({ ...unknown, state, reason, after_fingerprint: state === "reverting" ? "b".repeat(64) : null });
    for (const kind of ["review", "retry", "revert"] as const) assert.equal(pricingActionAllowed(kind, operation, current), false);
  }
  for (const operation of [rejected, applied]) assert.equal(pricingActionAllowed("review", operation, current), false);
  assert.equal(pricingActionAllowed("retry", unknown, current), false);
  assert.equal(pricingActionAllowed("revert", unknown, current), false);
});
