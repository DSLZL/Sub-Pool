import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { backupTables, backupMaxFileBytes, type BackupArchive, type BackupFile, type RestorePlan, type RestoreChecks } from "@sub-pool/contracts/backups";
import { createBackupsClient } from "@sub-pool/contracts/client";
import { backupFailure, backupHash, backupRequestID, checkBackupFile, checkRestoreChecks, checkRestoreReceipt, readBackupUpload, readBackups, restoreCanApply } from "./backup-data";
import { restorePlanID } from "./backup-return";
import { proxyAPI } from "./api-proxy";

const id = (n: number) => { const b = Buffer.alloc(16); b.writeUInt32BE(n, 12); return b.toString("base64url"); };
const manifest = { format: "sub-pool-pg-v1", source_cutoff: "2026-11-08T01:00:00Z", include_secrets: true, schema_fingerprint: "a".repeat(64), rows: "0",
  tables: backupTables.map(name => ({ name, rows: "0" })), excluded: ["auth_sessions", "backup_archives", "monitoring_worker", "restore_plans"] } as const;
async function fileFixture(size = 96): Promise<BackupFile> {
  const content = "SUB-POOL-PG-BACKUP/1\n" + Buffer.alloc(size, 11).toString("base64") + "\n";
  const archive: BackupArchive = { ...manifest, tables: [...manifest.tables], excluded: [...manifest.excluded], id: id(1), kind: "export", created_at: "2026-11-08T02:00:00Z", actor_username: "administrator", file_sha256: await backupHash(content), bytes: String(content.length) };
  return { archive, filename: "sub-pool-" + archive.id + ".spbak", content };
}
function planFixture(sha = "b".repeat(64)): RestorePlan {
  return { ...{ id: id(2), created_at: "2026-11-08T02:00:00Z", expires_at: "2026-11-08T02:10:00Z", state: "ready", reason: "READY" } as const,
    file_sha256: sha, target_basis: "c".repeat(64), before_archive_id: id(3), blockers: [], started_at: null, finished_at: null, reviewed_at: null, reviewed_by: null,
    source: { ...manifest, tables: [...manifest.tables], excluded: [...manifest.excluded] } };
}
const applied = (): RestorePlan => ({ ...planFixture(), state: "applied", reason: "APPLIED", started_at: "2026-11-08T02:01:00Z", finished_at: "2026-11-08T02:01:01Z" });
const checksFixture = (): RestoreChecks => ({ plan_id: id(2), applied_at: applied().finished_at ?? "", source: planFixture().source, sessions: "prior_sessions_removed", active_api_keys: "0", monitoring_paused: true, mail_paused: true, connection_state: "verification_required", remote_effects: "not_replayed", reviewed_at: null, reviewed_by: null });

test("backup upload reads actual bounded UTF-8 content and verifies persisted file hash and immutable metadata", async () => {
  const file = await fileFixture();
  const upload = await readBackupUpload(new File([file.content], "owned.spbak")); assert.equal(upload.content, file.content); assert.equal(upload.sha256, file.archive.file_sha256);
  assert.equal((await checkBackupFile(file, file.archive)).content, file.content);
  await assert.rejects(checkBackupFile({ ...file, content: file.content.replace("CwsL", "Dw8P") }));
  await assert.rejects(checkBackupFile(file, { ...file.archive, include_secrets: false }));
  for (const text of ["owned.spbak", "{}", "SUB-POOL-PG-BACKUP/1\nbad\n"]) await assert.rejects(readBackupUpload(new File([text], "valid-name.spbak")));
  await assert.rejects(readBackupUpload(new File([new Uint8Array([255, 254])], "owned.spbak")));
  let read = false;
  await assert.rejects(readBackupUpload({ name: "huge.spbak", size: backupMaxFileBytes + 1, arrayBuffer: async () => { read = true; return new ArrayBuffer(0); } })); assert.equal(read, false);
  await assert.rejects(readBackupUpload({ name: "partial.spbak", size: 1, arrayBuffer: async () => new ArrayBuffer(0) }));
});

test("backup catalog reads every page or rejects the entire incomplete, changed or oversized catalog", async () => {
  const base = (await fileFixture()).archive;
  const page = (archives: BackupArchive[], next_after: string | null = null, key_available = true) => ({ version: "backup-v1" as const, archives, next_after, key_available });
  const a = { ...base, id: id(10), created_at: "2026-11-08T02:00:00.000000002Z" }, b = { ...base, id: id(11), created_at: "2026-11-08T02:00:00.000000001Z" };
  const seen: string[] = []; const result = await readBackups({ list: async ({ after }) => { seen.push(after); return after ? page([b]) : page([a], a.id); } });
  assert.deepEqual(seen, ["", a.id]); assert.deepEqual(result.archives.map(a => a.id), [a.id, b.id]);
  const bad = [
    async ({ after }: { after: string }) => after ? page([]) : page([a], a.id),
    async ({ after }: { after: string }) => after ? page([b], null, false) : page([a], a.id),
    async ({ after }: { after: string }) => after ? page([a]) : page([a], a.id),
    async ({ after }: { after: string }) => after ? page([a]) : page([b], b.id),
    async () => page([a], b.id),
    async ({ after }: { after: string }) => { if (after) throw new TypeError("continuation lost"); return page([a], a.id); },
  ];
  for (const list of bad) await assert.rejects(readBackups({ list }));
  await assert.rejects(readBackups({ list: async ({ after }) => {
    const start = after ? Number(Buffer.from(after, "base64url").readUInt32BE(12)) + 1 : 1;
    const rows = Array.from({ length: 100 }, (_, i) => ({ ...base, id: id(start + i) }));
    return page(rows, rows.at(-1)?.id ?? null);
  } }));
});

test("restore confirmation requires a matching actual before-image download, exact immutable basis and live expiry", () => {
  const before = planFixture(), after = applied();
  checkRestoreReceipt(before, after); checkRestoreChecks(after, checksFixture());
  for (const change of [{ id: id(9) }, { target_basis: "d".repeat(64) }, { before_archive_id: id(8) }, { file_sha256: "e".repeat(64) }, { source: { ...before.source, include_secrets: false } }]) assert.throws(() => checkRestoreReceipt(before, { ...after, ...change }));
  assert.throws(() => checkRestoreChecks(after, { ...checksFixture(), plan_id: id(9) }));
  assert.throws(() => checkRestoreChecks(before, checksFixture()));
  const now = Date.parse(before.expires_at) - 1;
  assert.equal(restoreCanApply(before, null, now), false); assert.equal(restoreCanApply(before, id(8), now), false);
  assert.equal(restoreCanApply(before, before.before_archive_id, now), true); assert.equal(restoreCanApply(before, before.before_archive_id, now + 1), false);
  assert.equal(restoreCanApply(after, before.before_archive_id, now), false);
  assert.equal(restoreCanApply({ ...before, blockers: ["BACKUP_SOURCE_BLOCKED"] }, before.before_archive_id, now), false);
  assert.equal(checksFixture().connection_state, "verification_required");
});

test("fresh login accepts only one canonical public restore ID and request IDs contain no credentials", () => {
  const valid = id(2); assert.equal(restorePlanID("?restore=" + valid), valid);
  for (const search of ["", "?restore=", "?restore=https://other.example", "?restore=../../", "?restore=" + valid + "&restore=" + valid, "?restore=" + valid + "%0A", "?restore=" + "A".repeat(21) + "B"]) assert.equal(restorePlanID(search), null);
  const requests = new Set(Array.from({ length: 100 }, backupRequestID)); assert.equal(requests.size, 100);
  for (const value of requests) assert.equal(restorePlanID("?restore=" + value), value);
});

const config = { backendOrigin: "http://127.0.0.1:18683", publicOrigin: "http://127.0.0.1:18685", allowLoopbackHTTP: true };
const cookie = "sub_pool_dev_session=" + "D".repeat(43);
const request = (action: string, body: unknown = {}, method = "POST") => new Request(config.publicOrigin + "/api/v1/backups/" + action, { method,
  headers: { Origin: config.publicOrigin, Cookie: cookie, "Content-Type": "application/json" }, ...(method === "POST" ? { body: JSON.stringify(body) } : {}) });

test("backup proxy validates all eight fixed cookie-only operations including actual file content beyond 64 KiB", async () => {
  const file = await fileFixture(100000), plan = planFixture(file.archive.file_sha256), seen: string[] = [];
  const client = createBackupsClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init); browser.headers.set("Origin", config.publicOrigin); browser.headers.set("Cookie", cookie + "; other=private");
    for (const name of ["Authorization", "X-Api-Key", "Forwarded", "X-Forwarded-For"]) browser.headers.set(name, "forged");
    return proxyAPI(browser, config, async (url, forwarded) => {
      const action = String(url).split("/").at(-1) ?? ""; seen.push(action); assert.equal(String(url), config.backendOrigin + "/api/v1/backups/" + action);
      const headers = new Headers(forwarded?.headers); assert.equal(headers.get("Cookie"), cookie);
      for (const name of ["Authorization", "X-Api-Key", "Forwarded", "X-Forwarded-For"]) assert.equal(headers.get(name), null);
      assert.equal(forwarded?.method, "POST"); assert.equal(forwarded?.redirect, "error"); assert.equal(forwarded?.cache, "no-store");
      if (action === "preflight") assert.equal(JSON.parse(new TextDecoder().decode(forwarded?.body as Uint8Array)).content, file.content);
      return Response.json(action === "get" ? file : action === "export" ? file.archive : action === "list" ? { version: "backup-v1", key_available: true, archives: [file.archive], next_after: null } : action === "checks" ? checksFixture() : action === "apply" || action === "review" ? { ...applied(), file_sha256: plan.file_sha256 } : plan);
    });
  });
  await client.export({ request_id: id(1), include_secrets: true }); const actual = await client.get({ id: id(1) }); await checkBackupFile(actual);
  await client.list({ after: "", limit: 100 }); await client.preflight({ request_id: id(2), content: file.content }); await client.plan({ id: id(2) });
  await client.apply({ id: id(2), target_basis: plan.target_basis, acknowledged: true }); await client.checks({ id: id(2) }); await client.review({ id: id(2), acknowledged: true });
  assert.deepEqual(seen, ["export", "get", "list", "preflight", "plan", "apply", "checks", "review"]);
});

test("backup proxy rejects undeclared, malformed, cross-origin, oversized or unrelated error material before exposing it", async () => {
  let calls = 0; const fetcher = async () => { calls++; return Response.json({}); };
  assert.equal((await proxyAPI(request("cancel"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("get", {}, "GET"), config, fetcher)).status, 405);
  assert.equal((await proxyAPI(request("get?x=1"), config, fetcher)).status, 400);
  const cross = request("apply"); cross.headers.set("Origin", "https://other.example"); assert.equal((await proxyAPI(cross, config, fetcher)).status, 403);
  assert.equal((await proxyAPI(request("apply", { padding: "x".repeat(65537) }), config, fetcher)).status, 400);
  assert.equal((await proxyAPI(request("preflight", { content: "x".repeat(16 * 1024 * 1024) }), config, fetcher)).status, 400);
  assert.equal(calls, 0);
  const file = await fileFixture();
  for (const body of [{ ...file, raw_password: "must-not-return" }, { ...file, filename: "../private" }]) {
    const response = await proxyAPI(request("get"), config, async () => Response.json(body)); assert.equal(response.status, 502); assert.ok(!(await response.text()).includes("must-not-return"));
  }
  assert.equal((await proxyAPI(request("get"), config, async () => new Response("x".repeat(16 * 1024 * 1024 + 1), { headers: { "Content-Type": "application/json" } }))).status, 502);
  assert.equal((await proxyAPI(request("plan"), config, async () => Response.json({ defined: true, code: "SELF_SOURCE_PROTECTED", status: 409, data: { retry_at: null } }, { status: 409 }))).status, 502);
});

test("backup native error codes remain declared, safe and domain-specific through the actual oRPC proxy", async () => {
  for (const [code, status] of [["BACKUP_INVALID", 400], ["BACKUP_SCHEMA_INCOMPATIBLE", 409], ["BACKUP_LIMIT_EXCEEDED", 413], ["RESTORE_BUSY", 409], ["SECRET_STORAGE_UNAVAILABLE", 503], ["SOURCE_BLOCKED", 403]] as const) {
    const client = createBackupsClient(config.publicOrigin, async (input, init) => {
      const browser = new Request(input, init); browser.headers.set("Origin", config.publicOrigin); browser.headers.set("Cookie", cookie);
      return proxyAPI(browser, config, async () => Response.json({ defined: true, code, status, message: "private database text", data: { retry_at: null } }, { status }));
    });
    await assert.rejects(client.plan({ id: id(2) }), error => { assert.ok(error instanceof ORPCError); assert.equal(error.defined, true, code + ": " + error.code + " / " + error.status); assert.equal(backupFailure(error).status, status); assert.ok(!error.message.includes("private")); return true; });
    if (code.startsWith("BACKUP_") || code === "RESTORE_BUSY") {
      const unrelated = new Request(config.publicOrigin + "/api/v1/maintenance/plan", { method: "POST", headers: { Origin: config.publicOrigin, "Content-Type": "application/json" }, body: "{}" });
      assert.equal((await proxyAPI(unrelated, config, async () => Response.json({ defined: true, code, status, data: { retry_at: null } }, { status }))).status, 502);
    }
  }
});

test("lost backup apply replies remain unknown and neither proxy nor client retries the command", async () => {
  let calls = 0;
  const client = createBackupsClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init); browser.headers.set("Origin", config.publicOrigin); browser.headers.set("Cookie", cookie);
    return proxyAPI(browser, config, async () => { calls++; throw new TypeError("committed response lost"); });
  });
  await assert.rejects(client.apply({ id: id(2), target_basis: planFixture().target_basis, acknowledged: true }), error => { assert.equal(backupFailure(error).status, 0); return true; });
  assert.equal(calls, 1);
});
