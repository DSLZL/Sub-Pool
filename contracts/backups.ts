import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";

export const backupsVersion = "backup-v1";
export const backupFileFormat = "sub-pool-pg-v1";
export const backupMaxFileBytes = 12 * 1024 * 1024;
export const backupTables = [
  "account_events", "announcement_reads", "auth_api_keys", "auth_events", "auth_login_limits", "auth_users", "collection_jobs", "collection_runs",
  "connection_config", "connection_sources", "estimate_config", "estimate_results", "maintenance_plans", "managed_accounts", "management_events",
  "monitoring_config", "notification_config", "notifications", "observation_changes", "observation_management", "participant_bindings", "participant_events",
  "participants", "pool_contract_accounts", "pool_contract_participants", "pool_contracts", "pool_events", "pools", "quota_observations", "security_config",
  "source_blocks", "system_events", "system_settings", "usage_attributions", "usage_capture_facts", "usage_captures", "usage_events",
  "wallet_application_requests", "wallet_reads", "wallet_recommendations",
] as const;
// Keep protected files/receipts readable after an upgrade; Go alone decides restore compatibility.
const legacyTables = backupTables.filter(name => name !== "announcement_reads");
const timestamp = session.shape.created_at.regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/);
const fingerprint = z.string().regex(/^[a-f0-9]{64}$/);
const count = z.union([z.literal("0"), revision]);
const exclusions = z.tuple([z.literal("auth_sessions"), z.literal("backup_archives"), z.literal("monitoring_worker"), z.literal("restore_plans")]);
export const backupManifest = z.object({
  format: z.literal(backupFileFormat), source_cutoff: timestamp, include_secrets: z.boolean(), schema_fingerprint: fingerprint,
  rows: count.refine(v => BigInt(v) <= 250000n),
  tables: z.array(z.object({ name: z.enum(backupTables), rows: count.refine(v => BigInt(v) <= 100000n) }).strict()).min(legacyTables.length).max(backupTables.length),
  excluded: exclusions,
}).strict().refine(v => {
  const tables = v.tables.length === backupTables.length ? backupTables : legacyTables;
  return v.tables.every((t, i) => t.name === tables[i]) && v.tables.reduce((sum, t) => sum + BigInt(t.rows), 0n) === BigInt(v.rows);
});
export const backupArchive = backupManifest.safeExtend({
  id: objectID, kind: z.enum(["export", "pre_restore"]), created_at: timestamp, actor_username: z.string().min(1).max(256),
  file_sha256: fingerprint, bytes: revision.refine(v => BigInt(v) <= BigInt(backupMaxFileBytes)),
});
const content = z.string().min(1).max(backupMaxFileBytes).regex(/^SUB-POOL-PG-BACKUP\/1\n[A-Za-z0-9+/]+={0,2}\n$/)
  .refine(v => (v.length - "SUB-POOL-PG-BACKUP/1\n".length - 1) % 4 === 0);
export const backupFile = z.object({ archive: backupArchive, filename: z.string(), content }).strict()
  .refine(v => v.filename === `sub-pool-${v.archive.id}.spbak` && v.content.length === Number(v.archive.bytes));
const blocker = z.enum(["COLLECTION_ACTIVE", "MAIL_ACTIVE", "MAINTENANCE_ACTIVE", "REMOTE_WRITE_UNCONFIRMED", "BACKUP_COLLECTION_ACTIVE", "BACKUP_MAIL_ACTIVE", "BACKUP_MAINTENANCE_ACTIVE", "BACKUP_REMOTE_WRITE_UNCONFIRMED", "BACKUP_SOURCE_BLOCKED"]);
export const restorePlan = z.object({
  id: objectID, created_at: timestamp, expires_at: timestamp,
  state: z.enum(["ready", "blocked", "applying", "applied", "expired", "failed"]),
  reason: z.enum(["READY", "RESTORE_BLOCKED", "APPLYING", "APPLIED", "PLAN_EXPIRED", "BASIS_CHANGED", "BUSY_STATE_CHANGED", "WORKER_INTERRUPTED", "RESTORE_FAILED", "AUTHORIZATION_EXPIRED", "TARGET_REPLACED"]),
  file_sha256: fingerprint, target_basis: fingerprint, before_archive_id: objectID.nullable(), blockers: z.array(blocker).max(9),
  started_at: timestamp.nullable(), finished_at: timestamp.nullable(), reviewed_at: timestamp.nullable(), reviewed_by: z.string().min(1).max(256).nullable(), source: backupManifest,
}).strict().refine(v =>
  Date.parse(v.expires_at) > Date.parse(v.created_at)
  && (!["ready", "applying", "applied"].includes(v.state) || v.before_archive_id !== null)
  && (["applied", "expired", "failed"].includes(v.state) === (v.finished_at !== null))
  && (!["applying", "applied"].includes(v.state) || v.started_at !== null)
  && ((v.reviewed_at === null) === (v.reviewed_by === null))
  && (v.reviewed_at === null || v.state === "applied")
  && (v.state !== "blocked" || v.blockers.length > 0));
export const restoreChecks = z.object({
  plan_id: objectID, applied_at: timestamp, source: backupManifest, sessions: z.literal("prior_sessions_removed"), active_api_keys: count,
  monitoring_paused: z.boolean(), mail_paused: z.boolean(), connection_state: z.enum(["not_configured", "verification_required", "verified_after_restore"]),
  remote_effects: z.literal("not_replayed"), reviewed_at: timestamp.nullable(), reviewed_by: z.string().min(1).max(256).nullable(),
}).strict().refine(v => (v.reviewed_at === null) === (v.reviewed_by === null));
export const backupsOutputs = {
  list: z.object({ version: z.literal(backupsVersion), key_available: z.boolean(), archives: z.array(backupArchive).max(100), next_after: objectID.nullable() }).strict()
    .refine(v => new Set(v.archives.map(a => a.id)).size === v.archives.length && (v.next_after === null || v.archives.at(-1)?.id === v.next_after)),
  export: backupArchive, get: backupFile, preflight: restorePlan, plan: restorePlan, apply: restorePlan, checks: restoreChecks, review: restorePlan,
};
const base = oc.errors({
  BACKUP_INVALID: { status: 400, data: authErrorData }, BACKUP_SCHEMA_INCOMPATIBLE: { status: 409, data: authErrorData },
  BACKUP_LIMIT_EXCEEDED: { status: 413, data: authErrorData }, RESTORE_BUSY: { status: 409, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, PAYLOAD_TOO_LARGE: { status: 413, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData }, SERVICE_UNAVAILABLE: { status: 503, data: authErrorData },
});
const knownID = z.object({ id: objectID }).strict();
export const backupsContract = {
  export: base.route({ method: "POST", path: "/api/v1/backups/export" }).input(z.object({ request_id: objectID, include_secrets: z.boolean() }).strict()).output(backupArchive),
  get: base.route({ method: "POST", path: "/api/v1/backups/get" }).input(knownID).output(backupFile),
  list: base.route({ method: "POST", path: "/api/v1/backups/list" }).input(z.object({ after: z.union([z.literal(""), objectID]), limit: z.number().int().min(1).max(100) }).strict()).output(backupsOutputs.list),
  preflight: base.route({ method: "POST", path: "/api/v1/backups/preflight" }).input(z.object({ request_id: objectID, content }).strict()).output(restorePlan),
  plan: base.route({ method: "POST", path: "/api/v1/backups/plan" }).input(knownID).output(restorePlan),
  apply: base.route({ method: "POST", path: "/api/v1/backups/apply" }).input(z.object({ id: objectID, target_basis: fingerprint, acknowledged: z.literal(true) }).strict()).output(restorePlan),
  checks: base.route({ method: "POST", path: "/api/v1/backups/checks" }).input(knownID).output(restoreChecks),
  review: base.route({ method: "POST", path: "/api/v1/backups/review" }).input(z.object({ id: objectID, acknowledged: z.literal(true) }).strict()).output(restorePlan),
};
export type BackupArchive = z.infer<typeof backupArchive>;
export type BackupFile = z.infer<typeof backupFile>;
export type RestorePlan = z.infer<typeof restorePlan>;
export type RestoreChecks = z.infer<typeof restoreChecks>;
