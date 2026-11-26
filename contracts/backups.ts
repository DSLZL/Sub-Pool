import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";

export const backupsVersion = "backup-v2";
export const backupFileFormat = "sub-pool-pg-v1";
export const backupStreamFormat = "sub-pool-pg-v2";
export const backupMaxFileBytes = 12 * 1024 * 1024;
export const backupTables = [
  "account_events", "announcement_reads", "auth_api_keys", "auth_events", "auth_login_limits", "auth_users", "collection_jobs", "collection_runs",
  "connection_config", "connection_sources", "dispatch_events", "dispatch_pauses", "estimate_config", "estimate_results", "maintenance_plans", "managed_accounts", "management_events",
  "monitoring_config", "native_wallet_financial_samples", "native_weekly_estimates", "notification_config", "notifications", "observation_changes", "observation_management", "participant_bindings", "participant_events",
  "participants", "pool_contract_accounts", "pool_contract_participants", "pool_contracts", "pool_events", "pools", "pricing_changes", "pricing_events", "quota_observations", "security_config",
  "source_blocks", "system_events", "system_settings", "temporary_events", "temporary_exits", "temporary_rounds", "upstream_write_grants",
  "usage_attributions", "usage_capture_facts", "usage_captures", "usage_day_jobs", "usage_events",
	"wallet_application_requests", "wallet_application_results", "wallet_automation", "wallet_reads", "wallet_recommendations", "weekly_wallet_allocations", "weekly_wallet_groups",
  "weekly_wallet_issuance_phases", "weekly_wallet_issuances",
] as const;
const lightTableNames = new Set([
  "auth_users", "auth_api_keys", "connection_config", "connection_sources", "estimate_config", "managed_accounts",
  "monitoring_config", "notification_config", "observation_management", "participant_bindings", "participants",
  "pool_contract_accounts", "pool_contract_participants", "pool_contracts", "pools", "security_config", "source_blocks",
  "system_settings", "upstream_write_grants", "wallet_automation",
]);
const lightTables = backupTables.filter(name => lightTableNames.has(name));
const clearedTables = backupTables.filter(name => !lightTableNames.has(name));
const scope = z.enum(["full", "light"]);
export type BackupScope = z.infer<typeof scope>;
// Keep protected files/receipts readable after an upgrade; Go alone decides restore compatibility.
const beforeWeeklyIssuance = backupTables.filter(name => name !== "weekly_wallet_issuances" && name !== "weekly_wallet_issuance_phases");
const beforeNativeFinancial = beforeWeeklyIssuance.filter(name => name !== "native_wallet_financial_samples");
const beforeWeeklyWalletGroups = beforeNativeFinancial.filter(name => name !== "weekly_wallet_groups");
const beforeWriteGrants = beforeWeeklyWalletGroups.filter(name => name !== "upstream_write_grants");
const beforeUsageDays = beforeWriteGrants.filter(name => name !== "usage_day_jobs");
const beforeWeeklyWallet = beforeUsageDays.filter(name => name !== "weekly_wallet_allocations");
const beforeNativeWeekly = beforeWeeklyWallet.filter(name => name !== "native_weekly_estimates");
const beforeTemporary = beforeNativeWeekly.filter(name => name !== "temporary_events" && name !== "temporary_exits" && name !== "temporary_rounds");
const beforeWalletAutomation = beforeTemporary.filter(name => name !== "wallet_automation");
const beforeWalletResults = beforeWalletAutomation.filter(name => name !== "wallet_application_results");
const beforePricing = beforeWalletResults.filter(name => name !== "pricing_changes" && name !== "pricing_events");
const beforeDispatch = beforePricing.filter(name => name !== "dispatch_events" && name !== "dispatch_pauses");
const legacyTables = [beforeNativeFinancial, beforeWeeklyWalletGroups, beforeWriteGrants, beforeUsageDays, beforeWeeklyWallet, beforeNativeWeekly, beforeTemporary, beforeWalletAutomation, beforeWalletResults, beforePricing, beforeDispatch, beforeDispatch.filter(name => name !== "announcement_reads")];
const timestamp = session.shape.created_at.regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/);
const fingerprint = z.string().regex(/^[a-f0-9]{64}$/);
const count = z.union([z.literal("0"), revision]);
const exclusions = z.tuple([z.literal("auth_sessions"), z.literal("backup_archives"), z.literal("monitoring_worker"), z.literal("restore_plans")]);
const streamExclusions = z.tuple([z.literal("auth_sessions"), z.literal("backup_archives"), z.literal("backup_blob_chunks"), z.literal("backup_blobs"), z.literal("monitoring_worker"), z.literal("restore_plans")]);
const tableCount = z.object({ name: z.enum(backupTables), rows: count }).strict();
const matchingTables = (values: { name: string }[], names: readonly string[]) => values.length === names.length && values.every((table, i) => table.name === names[i]);
export const backupManifest = z.object({
  format: z.enum([backupFileFormat, backupStreamFormat]), scope: scope.optional(),
  source_cutoff: timestamp, include_secrets: z.boolean(), schema_fingerprint: fingerprint,
  rows: count, tables: z.array(tableCount).max(backupTables.length), excluded: z.union([exclusions, streamExclusions]),
}).strict().refine(v => {
  if (v.tables.reduce((sum, table) => sum + BigInt(table.rows), 0n) !== BigInt(v.rows)) return false;
  if (v.format === backupStreamFormat) {
    return v.excluded.length === 6 && v.scope !== undefined && (v.scope === "light" ? matchingTables(v.tables, lightTables)
      : matchingTables(v.tables, backupTables) || matchingTables(v.tables, beforeWeeklyIssuance));
  }
  return v.scope !== "light" && BigInt(v.rows) <= 250000n && v.tables.every(table => BigInt(table.rows) <= 100000n)
    && [backupTables, beforeWeeklyIssuance, ...legacyTables].some(tables => matchingTables(v.tables, tables));
});
export const backupArchive = backupManifest.safeExtend({
  id: objectID, kind: z.enum(["export", "pre_restore"]), created_at: timestamp, actor_username: z.string().min(1).max(256),
  file_sha256: fingerprint, bytes: revision,
}).refine(v => (v.format === backupStreamFormat || BigInt(v.bytes) <= BigInt(backupMaxFileBytes))
  && (v.kind !== "pre_restore" || v.scope !== "light" && v.include_secrets));
export const legacyBackupContent = z.string().min(1).max(backupMaxFileBytes).regex(/^SUB-POOL-PG-BACKUP\/1\n[A-Za-z0-9+/]+={0,2}\n$/)
  .refine(v => (v.length - "SUB-POOL-PG-BACKUP/1\n".length - 1) % 4 === 0);
export const backupFile = z.object({ archive: backupArchive, filename: z.string(), content: legacyBackupContent.optional() }).strict()
  .refine(v => v.filename === `sub-pool-${v.archive.id}.spbak` && (v.archive.format === backupStreamFormat
    ? v.content === undefined : v.content?.length === Number(v.archive.bytes)));
const blocker = z.enum(["COLLECTION_ACTIVE", "MAIL_ACTIVE", "MAINTENANCE_ACTIVE", "REMOTE_WRITE_UNCONFIRMED", "BACKUP_COLLECTION_ACTIVE", "BACKUP_MAIL_ACTIVE", "BACKUP_MAINTENANCE_ACTIVE", "BACKUP_REMOTE_WRITE_UNCONFIRMED", "BACKUP_SOURCE_BLOCKED", "WEEKLY_WALLET_HISTORY_CHANGED"]);
export const restorePlan = z.object({
  id: objectID, created_at: timestamp, expires_at: timestamp,
  state: z.enum(["ready", "blocked", "applying", "applied", "expired", "failed"]),
  reason: z.enum(["READY", "RESTORE_BLOCKED", "APPLYING", "APPLIED", "PLAN_EXPIRED", "BASIS_CHANGED", "BUSY_STATE_CHANGED", "WORKER_INTERRUPTED", "RESTORE_FAILED", "AUTHORIZATION_EXPIRED", "TARGET_REPLACED"]),
  file_sha256: fingerprint, target_basis: fingerprint, before_archive_id: objectID.nullable(), blockers: z.array(blocker).max(blocker.options.length),
  source_bytes: revision.optional(), cleared_tables: z.array(tableCount).max(backupTables.length).optional(), before_downloaded: z.boolean().optional(),
  started_at: timestamp.nullable(), finished_at: timestamp.nullable(), reviewed_at: timestamp.nullable(), reviewed_by: z.string().min(1).max(256).nullable(), source: backupManifest,
}).strict().refine(v =>
  Date.parse(v.expires_at) > Date.parse(v.created_at)
  && (!["ready", "applying", "applied"].includes(v.state) || v.before_archive_id !== null)
  && (["applied", "expired", "failed"].includes(v.state) === (v.finished_at !== null))
  && (!["applying", "applied"].includes(v.state) || v.started_at !== null)
  && ((v.reviewed_at === null) === (v.reviewed_by === null))
  && (v.reviewed_at === null || v.state === "applied")
  && (v.state !== "blocked" || v.blockers.length > 0)
  && (v.source.format !== backupStreamFormat || v.source_bytes !== undefined)
  && (v.source.scope === "light" ? matchingTables(v.cleared_tables ?? [], clearedTables) : !v.cleared_tables?.length)
  && (!v.before_downloaded || v.state === "ready" && !!v.source_bytes && !!v.before_archive_id));
export const restoreChecks = z.object({
  plan_id: objectID, applied_at: timestamp, source: backupManifest, sessions: z.literal("prior_sessions_removed"), active_api_keys: count,
  monitoring_paused: z.boolean(), mail_paused: z.boolean(), connection_state: z.enum(["not_configured", "verification_required", "verified_after_restore"]),
  remote_effects: z.literal("not_replayed"), reviewed_at: timestamp.nullable(), reviewed_by: z.string().min(1).max(256).nullable(),
}).strict().refine(v => (v.reviewed_at === null) === (v.reviewed_by === null));
export const backupsOutputs = {
  list: z.object({ version: z.enum([backupsVersion, "backup-v1"]), key_available: z.boolean(), archives: z.array(backupArchive).max(100), next_after: objectID.nullable() }).strict()
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
  export: base.route({ method: "POST", path: "/api/v1/backups/export" }).input(z.object({ request_id: objectID, include_secrets: z.boolean(), scope: scope.optional() }).strict()).output(backupArchive),
  get: base.route({ method: "POST", path: "/api/v1/backups/get" }).input(knownID).output(backupFile),
  list: base.route({ method: "POST", path: "/api/v1/backups/list" }).input(z.object({ after: z.union([z.literal(""), objectID]), limit: z.number().int().min(1).max(100) }).strict()).output(backupsOutputs.list),
  preflight: base.route({ method: "POST", path: "/api/v1/backups/preflight" }).input(z.union([
    z.object({ request_id: objectID, content: legacyBackupContent }).strict(),
    z.object({ request_id: objectID, archive_id: objectID }).strict(),
  ])).output(restorePlan),
  plan: base.route({ method: "POST", path: "/api/v1/backups/plan" }).input(knownID).output(restorePlan),
  apply: base.route({ method: "POST", path: "/api/v1/backups/apply" }).input(z.object({ id: objectID, target_basis: fingerprint, acknowledged: z.literal(true) }).strict()).output(restorePlan),
  checks: base.route({ method: "POST", path: "/api/v1/backups/checks" }).input(knownID).output(restoreChecks),
  review: base.route({ method: "POST", path: "/api/v1/backups/review" }).input(z.object({ id: objectID, acknowledged: z.literal(true) }).strict()).output(restorePlan),
};
export type BackupArchive = z.infer<typeof backupArchive>;
export type BackupFile = z.infer<typeof backupFile>;
export type RestorePlan = z.infer<typeof restorePlan>;
export type RestoreChecks = z.infer<typeof restoreChecks>;
