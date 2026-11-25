import assert from "node:assert/strict";
import { test } from "node:test";
import type { LoginAuditPage, ManagementAuditPage, SourceRule } from "@sub-pool/contracts/security";
import { checkConfigReceipt, checkSourceReceipt, readSecurityLogins, readSecurityManagement, readSecuritySources, securityRange } from "./security-data";

const actor = "A".repeat(22), at = "2026-11-08T01:02:03.000001Z";
const range = { from: "2026-11-08T00:00:00Z", until: "2026-11-09T00:00:00Z" };
const source = { ip: "127.0.0.1", peer_ip: "127.0.0.1", kind: "peer" as const };
const event = (id: string): LoginAuditPage["events"][number] => ({ id, occurred_at: at, outcome: "success", user_id: actor, username: "reader", source });
const counts = { success: "2", failure: "0", limited: "0", blocked: "0" };
const rule = (ip: string): SourceRule => ({ ip, revision: "1", blocked: true, saved_at: at, actor_id: actor, reason: null, expires_at: null });

test("security reads retain UTC boundaries, exact bigint cursors/counts and mixed-family source history", async () => {
  assert.deepEqual(securityRange(Date.parse(range.from)), { from: "2026-11-08T00:00:00.000Z", until: "2026-11-08T00:00:00.001Z" });
  const ids = ["9007199254740993", "9007199254740994"], cursors: string[] = [];
  const result = await readSecurityLogins({ loginAudit: async input => {
    cursors.push(input.after); assert.equal(input.from, range.from); assert.equal(input.until, range.until); assert.equal(input.outcome, "all");
    return input.after === "0" ? { events: [event(ids[0])], counts, next_after: ids[0] } : { events: [event(ids[1])], counts, next_after: null };
  } }, range);
  assert.deepEqual(cursors, ["0", ids[0]]); assert.deepEqual(result.events.map(row => row.id), ids); assert.deepEqual(result.counts, counts);
  const sources = await readSecuritySources({ sources: async input => {
    assert.equal(input.state, "all");
    return input.after === "" ? { rules: [{ ...rule("192.0.2.1"), blocked: false }], next_after: "192.0.2.1" }
      : { rules: [rule("::c000:201")], next_after: null };
  } });
  assert.deepEqual(sources.map(row => [row.ip, row.blocked]), [["192.0.2.1", false], ["::c000:201", true]]);
});

test("login pages reject partial, repeated, empty, drifted, wrong-range and incomplete-count results", async () => {
  const first: LoginAuditPage = { events: [event("1")], counts, next_after: "1" };
  for (const next of [
    async (): Promise<LoginAuditPage> => { throw new Error("later page unavailable"); },
    async () => ({ ...first, next_after: null }),
    async () => ({ ...first, events: [], next_after: null }),
    async () => ({ events: [event("2")], counts: { ...counts, success: "3" }, next_after: null }),
    async () => ({ events: [{ ...event("2"), occurred_at: range.until }], counts, next_after: null }),
  ]) {
    await assert.rejects(readSecurityLogins({ loginAudit: input => input.after === "0" ? Promise.resolve(first) : next() }, range));
  }
  await assert.rejects(readSecurityLogins({ loginAudit: async () => ({ ...first, next_after: null }) }, range), /统计/);
  const empty = await readSecurityLogins({ loginAudit: async () => ({ events: [], counts: { ...counts, success: "0" }, next_after: null }) }, range);
  assert.equal(empty.counts.success, "0"); assert.deepEqual(empty.events, []);
});

test("source pages reject malformed continuation and enforce a bounded complete read", async () => {
  await assert.rejects(readSecuritySources({ sources: async () => ({ rules: [rule("192.0.2.1")], next_after: "192.0.2.2" }) }));
  await assert.rejects(readSecuritySources({ sources: async input => input.after === "" ? { rules: [rule("192.0.2.1")], next_after: "192.0.2.1" } : { rules: [rule("192.0.2.1")], next_after: null } }));
  let calls = 0;
  await assert.rejects(readSecuritySources({ sources: async () => {
    const page = calls++, rules = Array.from({ length: 100 }, (_, index) => rule(`198.18.${Math.floor((page * 100 + index) / 250)}.${(page * 100 + index) % 250 + 1}`));
    return { rules, next_after: rules.at(-1)?.ip ?? null };
  } }), /2000|游标/);
  assert.equal(calls, 20);
});

test("management reads preserve actor/task correlation and every outcome within the requested range", async () => {
  const outcomes = ["pending", "succeeded", "failed", "unknown", "denied", "conflict"] as const;
  const rows: ManagementAuditPage["events"] = outcomes.map((outcome, index) => ({ id: String(index + 1), occurred_at: at, operation_id: `operation:${index}`, actor_id: index % 2 ? null : actor,
    task_id: index % 2 ? `task:${index}` : null, action: "security.source.block", outcome, target_type: "source_ip", target_id: "192.0.2.1", target_revision: null, source_id: null }));
  const result = await readSecurityManagement({ managementAudit: async input => input.after === "0" ? { events: rows.slice(0, 3), next_after: "3" } : { events: rows.slice(3), next_after: null } }, range);
  assert.deepEqual(result, rows);
  await assert.rejects(readSecurityManagement({ managementAudit: async () => ({ events: [{ ...rows[0], occurred_at: "2026-11-07T23:59:59.999999999Z" }], next_after: null }) }, range));
});

test("security write receipts bind actor, exact address, intention and the next exact revision", () => {
  const change = { kind: "source" as const, ip: "192.0.2.1", revision: "9007199254740993", blocked: false };
  const receipt = { ...rule(change.ip), revision: "9007199254740994", blocked: false };
  checkSourceReceipt(change, receipt, actor);
  for (const bad of [{ ...receipt, ip: "192.0.2.2" }, { ...receipt, blocked: true }, { ...receipt, revision: change.revision }, { ...receipt, actor_id: "B".repeat(22) }]) assert.throws(() => checkSourceReceipt(change, bad, actor));
  const timed = { ...change, blocked: true, reason: "限时校验", expires_at: "2026-11-24T03:16:00.123450Z" };
  const timedReceipt = { ...receipt, blocked: true, reason: timed.reason, expires_at: "2026-11-24T03:16:00.12345Z" };
  checkSourceReceipt(timed, timedReceipt, actor);
  assert.throws(() => checkSourceReceipt(timed, { ...timedReceipt, expires_at: "2026-11-24T03:16:00.123451Z" }, actor));
  assert.throws(() => checkSourceReceipt(timed, { ...timedReceipt, reason: "其他规则" }, actor));
  const config = { kind: "config" as const, revision: "1", network_diagnostics_enabled: true };
  checkConfigReceipt(config, { revision: "2", network_diagnostics_enabled: true });
  assert.throws(() => checkConfigReceipt(config, { revision: "1", network_diagnostics_enabled: true }));
  assert.throws(() => checkConfigReceipt(config, { revision: "2", network_diagnostics_enabled: false }));
});
