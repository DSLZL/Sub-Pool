import assert from "node:assert/strict";
import { test } from "node:test";

import { createBackupsClient } from "./client";
import { backupArchive, backupFile, backupManifest, backupMaxFileBytes, backupTables, backupsOutputs, backupsVersion, restoreChecks, restorePlan } from "./backups";

const id = "AAAAAAAAAAAAAAAAAAAAAA";
const content = "SUB-POOL-PG-BACKUP/1\n" + "a".repeat(64) + "\n";
const manifest = () => ({
  format: "sub-pool-pg-v1", source_cutoff: "2026-11-08T01:00:00.123456Z", include_secrets: false, schema_fingerprint: "a".repeat(64), rows: "1",
  tables: backupTables.map(name => ({ name, rows: name === "auth_users" ? "1" : "0" })), excluded: ["auth_sessions", "backup_archives", "monitoring_worker", "restore_plans"],
});
const archive = () => ({ ...manifest(), id, kind: "export", created_at: "2026-11-08T01:00:01Z", actor_username: "administrator", file_sha256: "b".repeat(64), bytes: String(content.length) });
const plan = () => ({
  id, created_at: "2026-11-08T01:01:00Z", expires_at: "2026-11-08T01:11:00Z", state: "ready", reason: "READY", file_sha256: "b".repeat(64), target_basis: "c".repeat(64), before_archive_id: id, blockers: [],
  started_at: null as string | null, finished_at: null as string | null, reviewed_at: null as string | null, reviewed_by: null as string | null, source: manifest(),
});

test("backups require complete current table counts, protected material and explicit runtime exclusions", () => {
  assert.equal(backupsVersion, "backup-v2"); assert.equal(backupManifest.parse(manifest()).tables.length, 57);
  const beforeIssuance = ["weekly_wallet_issuance_phases", "weekly_wallet_issuances"];
  const beforeNativeFinancial = [...beforeIssuance, "native_wallet_financial_samples"];
  const beforeGroups = [...beforeNativeFinancial, "weekly_wallet_groups"];
  const beforeGrants = [...beforeGroups, "upstream_write_grants"];
  const beforeUsage = [...beforeGrants, "usage_day_jobs"];
  const beforeWallet = [...beforeUsage, "weekly_wallet_allocations"];
  const beforeWeekly = [...beforeWallet, "native_weekly_estimates"];
  const temporary = [...beforeWeekly, "temporary_events", "temporary_exits", "temporary_rounds"];
  for (const removed of [beforeIssuance, beforeNativeFinancial, beforeGroups, beforeGrants, beforeUsage, beforeWallet, beforeWeekly, ...[[], ["wallet_automation"], ["wallet_automation", "wallet_application_results"], ["wallet_automation", "wallet_application_results", "pricing_changes", "pricing_events"], ["wallet_automation", "wallet_application_results", "pricing_changes", "pricing_events", "dispatch_events", "dispatch_pauses"], ["wallet_automation", "wallet_application_results", "pricing_changes", "pricing_events", "dispatch_events", "dispatch_pauses", "announcement_reads"]].map(older => [...temporary, ...older])]) {
    const legacy = { ...manifest(), tables: manifest().tables.filter(table => !removed.includes(table.name)) };
    assert.ok(backupManifest.safeParse(legacy).success);
    assert.ok(backupFile.safeParse({ archive: { ...archive(), ...legacy }, filename: `sub-pool-${id}.spbak`, content }).success);
  }
  for (const removed of [["usage_day_jobs"], ["temporary_events"], ["temporary_exits"], ["temporary_rounds"], ["temporary_events", "temporary_exits"], ["announcement_reads"], ["pricing_events"], ["dispatch_events", "dispatch_pauses"]]) assert.equal(backupManifest.safeParse({ ...manifest(), tables: manifest().tables.filter(table => !removed.includes(table.name)) }).success, false);
  assert.equal(backupManifest.safeParse({ ...manifest(), tables: manifest().tables.filter(table => table.name !== "notifications") }).success, false);
  assert.ok(backupFile.safeParse({ archive: archive(), filename: `sub-pool-${id}.spbak`, content }).success);
  for (const value of [{ ...manifest(), tables: manifest().tables.slice(1) }, { ...manifest(), rows: "2" }, { ...manifest(), excluded: [] }, { ...manifest(), format: "sqlite" }]) assert.equal(backupManifest.safeParse(value).success, false);
  for (const value of [{ archive: archive(), filename: "name-only.spbak", content }, { archive: archive(), filename: `sub-pool-${id}.spbak`, content: "" }, { archive: { ...archive(), bytes: "1" }, filename: `sub-pool-${id}.spbak`, content }]) assert.equal(backupFile.safeParse(value).success, false);
  assert.equal(backupArchive.safeParse({ ...archive(), secret_key: "must never cross this boundary" }).success, false);
  assert.equal(backupsOutputs.list.safeParse({ version: backupsVersion, key_available: true, archives: [archive(), archive()], next_after: null }).success, false);
  const streamed = { ...archive(), format: "sub-pool-pg-v2", scope: "full", bytes: "500000000",
    excluded: ["auth_sessions", "backup_archives", "backup_blob_chunks", "backup_blobs", "monitoring_worker", "restore_plans"] };
  assert.ok(backupFile.safeParse({ archive: streamed, filename: `sub-pool-${id}.spbak` }).success);
  assert.ok(backupArchive.safeParse({ ...streamed, tables: streamed.tables.filter(table => !beforeIssuance.includes(table.name)) }).success);
  assert.equal(backupFile.safeParse({ archive: streamed, filename: `sub-pool-${id}.spbak`, content }).success, false);
  assert.equal(backupArchive.safeParse({ ...streamed, scope: "light" }).success, false);
});

test("restore receipts distinguish before-image, completion and manual review from connection verification", () => {
  assert.ok(restorePlan.safeParse(plan()).success);
  for (const value of [{ ...plan(), before_archive_id: null }, { ...plan(), state: "applied" }, { ...plan(), state: "blocked" }, { ...plan(), reviewed_at: "2026-11-08T01:02:00Z", reviewed_by: "administrator" }]) assert.equal(restorePlan.safeParse(value).success, false);
  const applied = { ...plan(), state: "applied", reason: "APPLIED", started_at: "2026-11-08T01:02:00Z", finished_at: "2026-11-08T01:02:01Z" };
  assert.ok(restorePlan.safeParse(applied).success);
  const checks = { plan_id: id, applied_at: applied.finished_at, source: manifest(), sessions: "prior_sessions_removed", active_api_keys: "0", monitoring_paused: true, mail_paused: true, connection_state: "verification_required", remote_effects: "not_replayed", reviewed_at: "2026-11-08T01:03:00Z", reviewed_by: "administrator" };
  assert.equal(restoreChecks.parse(checks).connection_state, "verification_required");
  assert.equal(restoreChecks.safeParse({ ...checks, connection_state: "assumed_verified" }).success, false);
  assert.equal(restoreChecks.safeParse({ ...checks, remote_effects: "replayed" }).success, false);
});

test("backup commands reject filename-only uploads, missing acknowledgement and oversized material before dispatch", async () => {
  let calls = 0;
  const client = createBackupsClient("http://127.0.0.1:4200", async () => { calls++; throw new Error("must not dispatch invalid input"); });
  await assert.rejects(client.preflight({ request_id: id, filename: "example.spbak" } as never));
  await assert.rejects(client.apply({ id, target_basis: "a".repeat(64), acknowledged: false } as never));
  await assert.rejects(client.preflight({ request_id: id, content: "x".repeat(backupMaxFileBytes + 1) }));
  assert.equal(calls, 0);
});

test("large protected files validate without recursive regular-expression limits", () => {
  const large = "SUB-POOL-PG-BACKUP/1\n" + "A".repeat(8 * 1024 * 1024) + "\n";
  assert.ok(backupFile.safeParse({ archive: { ...archive(), bytes: String(large.length) }, filename: `sub-pool-${id}.spbak`, content: large }).success);
});

test("lost backup replies keep caller-known operation IDs and do not retry writes", async () => {
  let exports = 0, reads = 0;
  const client = createBackupsClient("http://127.0.0.1:4200", async (input, init) => {
    const request = new Request(input, init);
    if (new URL(request.url).pathname.endsWith("/export")) { exports++; assert.deepEqual(await request.json(), { request_id: id, include_secrets: false }); throw new TypeError("owned lost export response"); }
    reads++; assert.deepEqual(await request.json(), { id });
    return Response.json({ archive: archive(), filename: `sub-pool-${id}.spbak`, content });
  });
  await assert.rejects(client.export({ request_id: id, include_secrets: false }));
  const file = await client.get({ id }); assert.equal(file.archive.id, id); assert.equal(file.content, content);
  assert.equal(exports, 1); assert.equal(reads, 1);
});
