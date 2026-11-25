import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";

import { ORPCError } from "@orpc/client";
import { createAuthClient, createBackupsClient, createConnectionClient } from "./client";
import { backupTables } from "./backups";

test("real Go PostgreSQL protected backup files, immutable preflight, lost replies and fresh post-restore verification through oRPC", { skip: !process.env.SUB_POOL_TEST_BACKUPS_ORIGIN, timeout: 45000 }, async () => {
  const origin = process.env.SUB_POOL_TEST_BACKUPS_ORIGIN, username = process.env.SUB_POOL_TEST_BACKUPS_USERNAME, password = process.env.SUB_POOL_TEST_BACKUPS_PASSWORD;
  assert.ok(origin && username && password, "explicit owned backup fixture required"); assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  let cookie = "", loseExport = false, losePreflight = false, loseApply = false;
  const commands = { export: 0, preflight: 0, apply: 0 };
  const fetcher: typeof fetch = async (input, init) => {
    const request = new Request(input, init); request.headers.set("Origin", origin); if (cookie) request.headers.set("Cookie", cookie);
    const response = await fetch(request, { redirect: "error" }); const issued = response.headers.get("set-cookie"); if (issued) cookie = issued.split(";")[0] ?? "";
    const path = new URL(request.url).pathname;
    let lost = false;
    if (path.endsWith("/backups/export")) { commands.export++; if (response.ok && loseExport) { loseExport = false; lost = true; } }
    if (path.endsWith("/backups/preflight")) { commands.preflight++; if (response.ok && losePreflight) { losePreflight = false; lost = true; } }
    if (path.endsWith("/backups/apply")) { commands.apply++; if (response.ok && loseApply) { loseApply = false; lost = true; } }
    if (lost) { await response.text(); throw new TypeError("owned fixture dropped committed command response"); }
    return response;
  };
  const auth = createAuthClient(origin, fetcher), backups = createBackupsClient(origin, fetcher), connection = createConnectionClient(origin, fetcher);
  await auth.login({ username, password });
  let calls = 0, unexpected = 0;
  const secret = "owned-backup-upstream-management-key";
  const upstream = createServer((request, response) => {
    calls++; response.setHeader("Content-Type", "application/json");
    const path = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
    if (request.method !== "GET" || request.headers.cookie || path !== "/health" && request.headers["x-api-key"] !== secret) { unexpected++; response.writeHead(400).end(); return; }
    if (path === "/health") { response.end(JSON.stringify({ status: "ok" })); return; }
    if (path === "/api/v1/admin/system/version") { response.end(JSON.stringify({ code: 0, data: { version: "owned-backup-native" } })); return; }
    if (["/api/v1/admin/accounts", "/api/v1/admin/groups", "/api/v1/admin/usage", "/api/v1/admin/users"].includes(path)) { response.end(JSON.stringify({ code: 0, data: { items: [], total: 0, page: 1, page_size: 1 } })); return; }
    unexpected++; response.writeHead(404).end();
  });
  upstream.listen(0, "127.0.0.1"); await once(upstream, "listening"); const address = upstream.address(); assert.ok(address && typeof address === "object");
  try {
    const initial = await connection.get({});
    const saved = await connection.save({ revision: initial.revision, base_url: `http://127.0.0.1:${address.port}`, credential_action: "replace", credential: secret, confirmation: initial.source_id ? "更换地址" : "" });
    const baseline = await connection.test({ revision: saved.configuration.revision, base_url: saved.configuration.base_url, credential: "" });
    assert.ok(baseline.checks.some(c => c.id === "management_auth" && c.state === "available"));
    const grantView = await connection.writeGrants({});
    assert.ok(grantView.source_id);
    const granted = await connection.updateWriteGrant({ source_id: grantView.source_id, connection_revision: grantView.connection_revision,
      capability: "pricing", expected_grant_id: "0", enabled: true, confirmation_sha256: grantView.confirmation_sha256, current_password: password });
    const active = granted.grants.find(status => status.capability === "pricing")?.grant;
    assert.ok(active && active.revoked_at === null);
    const originalSettings = await connection.settings({});
    const operation = randomBytes(16).toString("base64url"); loseExport = true;
    await assert.rejects(backups.export({ request_id: operation, include_secrets: true }));
    const file = await backups.get({ id: operation });
    const { content } = file;
    assert.ok(content, "legacy JSON download must contain the protected file");
    assert.equal(file.archive.id, operation); assert.deepEqual(file.archive.tables.map(table => table.name), backupTables); assert.equal(file.archive.include_secrets, true);
    assert.ok(content.length > 65536, "owned fixture must cross the old JSON body bound");
    assert.equal(Buffer.byteLength(content), Number(file.archive.bytes));
    assert.equal(createHash("sha256").update(content).digest("hex"), file.archive.file_sha256);
    assert.ok(!content.includes(password) && !content.includes(secret), "private value appeared in protected material");
    const omitted = await backups.export({ request_id: randomBytes(16).toString("base64url"), include_secrets: false }); assert.equal(omitted.include_secrets, false);
    assert.ok((await backups.list({ after: "", limit: 100 })).archives.some(a => a.id === operation));
    const stale = await backups.preflight({ request_id: randomBytes(16).toString("base64url"), content }); assert.equal(stale.state, "ready");
    await connection.updateSettings({ revision: originalSettings.revision, timezone: originalSettings.timezone === "UTC" ? "Asia/Taipei" : "UTC" });
    const expired = await backups.apply({ id: stale.id, target_basis: stale.target_basis, acknowledged: true }); assert.equal(expired.state, "expired"); assert.equal(expired.reason, "BASIS_CHANGED");
    const preflightID = randomBytes(16).toString("base64url"); losePreflight = true;
    await assert.rejects(backups.preflight({ request_id: preflightID, content }));
    const plan = await backups.plan({ id: preflightID }); assert.equal(plan.state, "ready"); assert.ok(plan.before_archive_id);
    const before = await backups.get({ id: plan.before_archive_id }); assert.equal(before.archive.kind, "pre_restore"); assert.equal(before.archive.include_secrets, true);
    const callsBeforeRestore = calls; loseApply = true;
    await assert.rejects(backups.apply({ id: plan.id, target_basis: plan.target_basis, acknowledged: true }));
    await assert.rejects(auth.current({}), e => e instanceof ORPCError && e.status === 401);
    await auth.login({ username, password });
    const receipt = await backups.plan({ id: plan.id }); assert.equal(receipt.state, "applied"); assert.ok(receipt.finished_at);
    assert.equal((await connection.settings({})).timezone, originalSettings.timezone);
    let checks = await backups.checks({ id: plan.id }); assert.deepEqual(checks.source.tables, file.archive.tables); assert.equal(checks.connection_state, "verification_required"); assert.equal(checks.active_api_keys, "0"); assert.equal(checks.monitoring_paused, true); assert.equal(checks.mail_paused, true);
    assert.equal(calls, callsBeforeRestore, "replacement replayed an upstream operation");
    await backups.review({ id: plan.id, acknowledged: true }); checks = await backups.checks({ id: plan.id }); assert.equal(checks.connection_state, "verification_required"); assert.ok(checks.reviewed_at);
    const restored = await connection.get({}); assert.equal(restored.diagnostic, null); assert.ok(restored.source_id);
    const restoredGrants = await connection.writeGrants({});
    assert.equal(restoredGrants.source_id, grantView.source_id);
    assert.equal(restoredGrants.connection_revision, restored.revision);
    const revoked = restoredGrants.grants.find(status => status.capability === "pricing")?.grant;
    assert.ok(revoked?.revoked_at && revoked.revoked_by);
    assert.equal(revoked.id, active.id);
    assert.equal(revoked.granted_at, active.granted_at);
    assert.equal(revoked.granted_by, active.granted_by);
    assert.equal(revoked.confirmation_sha256, active.confirmation_sha256);
    await assert.rejects(connection.discover({ revision: restored.revision, source_id: restored.source_id, page: 1, search: "" }), e => e instanceof ORPCError && e.defined && e.code === "CONNECTION_REVERIFICATION_REQUIRED" && e.status === 409);
    const verified = await connection.test({ revision: restored.revision, base_url: restored.base_url, credential: "" }); assert.ok(verified.checks.some(c => c.id === "management_auth" && c.state === "available"));
    checks = await backups.checks({ id: plan.id }); assert.equal(checks.connection_state, "verified_after_restore"); assert.equal(checks.monitoring_paused, true); assert.equal(checks.mail_paused, true);
    assert.equal((await backups.get({ id: plan.before_archive_id })).archive.file_sha256, before.archive.file_sha256);
    assert.deepEqual(commands, { export: 2, preflight: 2, apply: 2 }); assert.equal(unexpected, 0);
  } finally { upstream.closeAllConnections(); await new Promise<void>(resolve => upstream.close(() => resolve())); }
});
