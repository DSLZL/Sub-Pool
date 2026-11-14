import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { CapabilityCheck, Discovery } from "@sub-pool/contracts/connection";
import { checkText, connectionFailure, displayTime, readConnectionImpact, readDiscoveredPages, readManagedPages } from "./connection-data";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A", stamp = "2026-11-06T01:02:03Z";
const read: CapabilityCheck = { id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 };
const account: ManagedAccount = {
  id, revision: "1", source_id: id, upstream_id: "1", name: "Local", monitoring: true, source_mode: "passive", source_current: true,
  identity_state: "verified", connection_available: true, observed_connection_revision: "1", collection_state: "awaiting_collector", read,
  snapshot: { account: { upstream_id: "1", name: "Remote", platform: "openai", type: "oauth", status: "active", schedulable: true,
    eligibility: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" },
  runtime: { last_used_at: null, rate_limited_at: null, rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, error_present: null } },
  last_attempt_at: stamp, observed_at: stamp, created_at: stamp, saved_at: stamp,
};

test("source-change impact accepts only the exact saved source and configuration revision", async () => {
  const config = { revision: "3", source_id: id, base_url: "https://source.test", credential_set: false,
    credential_kind: null, secret_storage_available: true, saved_at: stamp, diagnostic: null };
  const impact = { revision: "3", source_id: id, checked_at: stamp, managed_accounts: "9007199254740993",
    participant_bindings: "0", dispatch_open: "1", pricing_open: "2", wallet_rejected: "0", wallet_unconfirmed: "3" };
  assert.deepEqual(await readConnectionImpact(async () => impact, config), impact);
  for (const change of [{ revision: "2" }, { source_id: other }, { source_id: null }]) {
    await assert.rejects(readConnectionImpact(async () => ({ ...impact, ...change }), config));
  }
  await assert.rejects(readConnectionImpact(async () => { throw new Error("lost response"); }, config));
  let finish: (value: typeof impact) => void = () => { throw new Error("read has not started"); };
  const held = readConnectionImpact(() => new Promise(resolve => { finish = resolve; }), config);
  finish({ ...impact, revision: "4" });
  await assert.rejects(held);
});

test("managed account reads require every page and one connection revision", async () => {
  const cursors: string[] = [];
  const result = await readManagedPages(async after => {
    cursors.push(after);
    return { accounts: [{ ...account, id: after ? other : id }], next_after: after ? null : id, connection_revision: "1" };
  }, "1");
  assert.deepEqual(cursors, ["", id]); assert.equal(result.length, 2);
  await assert.rejects(readManagedPages(async () => ({ accounts: [account], next_after: id, connection_revision: "1" }), "1"));
  await assert.rejects(readManagedPages(async () => ({ accounts: [], next_after: null, connection_revision: "2" }), "1"));
  await assert.rejects(readManagedPages(async after => { if (after) throw new Error("second page lost"); return { accounts: [account], next_after: id, connection_revision: "1" }; }, "1"));
});

test("discovery never exposes a denied second page, changing coverage or an incomplete list", async () => {
  const base: Discovery = { source_id: id, revision: "1", page: 1, total: "2", next_page: 2, accounts: [account.snapshot.account], read };
  const complete = await readDiscoveredPages(async page => page === 1 ? base : { ...base, page, next_page: null, accounts: [{ ...account.snapshot.account, upstream_id: "2" }] }, id, "1");
  assert.equal(complete.accounts.length, 2);
  const partial = await readDiscoveredPages(async page => page === 1 ? base : { ...base, page, total: null, next_page: null, accounts: [], read: { ...read, state: "denied", reason: "FORBIDDEN", http_status: 403 } }, id, "1");
  assert.deepEqual(partial.accounts, []); assert.equal(partial.read.reason, "FORBIDDEN");
  for (const change of [{ source_id: other }, { revision: "2" }, { total: "3" }, { next_page: null }, { page: 2 }]) {
    await assert.rejects(readDiscoveredPages(async () => ({ ...base, ...change }), id, "1"));
  }
  const empty = await readDiscoveredPages(async () => ({ ...base, total: "0", next_page: null, accounts: [] }), id, "1");
  assert.deepEqual(empty.accounts, []); assert.equal(empty.read.state, "available");
});

test("connection feedback trusts defined codes and preserves zero, unknown time and display timezone", () => {
  const denied = connectionFailure(new ORPCError("SOURCE_BLOCKED", { defined: true, status: 403, message: "PRIVATE_BODY", data: { retry_at: null } }));
  assert.equal(denied.status, 403); assert.ok(!denied.text.includes("PRIVATE_BODY"));
  assert.equal(connectionFailure(new ORPCError("CONFLICT", { defined: true, status: 500, data: { retry_at: null } })).status, 0);
  assert.equal(connectionFailure(new TypeError("PRIVATE_SECRET")).status, 0);
  assert.match(checkText(read), /0 ms/);
  assert.equal(displayTime(null, "UTC"), "暂无记录");
  assert.match(displayTime(stamp, null), /UTC/);
  assert.match(displayTime(stamp, "Asia/Tokyo"), /Asia\/Tokyo/);
  assert.match(displayTime(stamp, "invalid/display-zone"), /2026-11-06T01:02:03Z.*UTC/);
});
