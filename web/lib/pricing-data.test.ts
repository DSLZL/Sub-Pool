import assert from "node:assert/strict";
import { test } from "node:test";
import { pricingGroup, pricingGroups } from "@sub-pool/contracts/pricing";
import { pricingReadText, readPricingGroup, readPricingGroups } from "./pricing-data";

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
