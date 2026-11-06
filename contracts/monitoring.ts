import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";

export const monitoringVersion = "monitoring-v1";
const timestamp = session.shape.created_at;
const count = z.union([z.literal("0"), revision]);
const percent = z.string().max(64).regex(/^(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/)
  .refine(value => { const exponent = value.split(/[eE]/)[1]; return exponent === undefined || Number(exponent) >= -18 && Number(exponent) <= 18; });
const sourceMode = z.enum(["active", "passive"]);
const configFields = z.object({
  revision, enabled: z.boolean(), interval_seconds: z.number().int().min(60).max(86400),
  stale_after_seconds: z.number().int().min(60).max(604800), refresh_on_change: z.boolean(),
  near_reset_enabled: z.boolean(), near_reset_lead_seconds: z.number().int().min(60).max(86400),
  near_reset_interval_seconds: z.number().int().min(60).max(86400), stale_warnings_enabled: z.boolean(),
}).strict();
const validConfig = (v: z.infer<typeof configFields>) => v.stale_after_seconds >= v.interval_seconds && v.near_reset_interval_seconds <= v.interval_seconds;
export const monitoringConfig = configFields.extend({ saved_at: timestamp.nullable() }).refine(validConfig);
export const quotaWindow = z.object({
  kind: z.enum(["short", "seven_day"]), duration_seconds: revision.nullable(), used_percent: percent.nullable(),
  reset_at: timestamp.nullable(), reset_expired: z.boolean(),
}).strict().refine(v => !v.reset_expired || v.reset_at !== null);
export const quotaSnapshot = z.object({
  source_id: objectID, connection_revision: revision, account_revision: revision,
  source_mode: z.literal("passive"), source_at: timestamp.nullable(), received_at: timestamp,
  windows: z.array(quotaWindow).min(1).max(2).refine(values => new Set(values.map(v => v.kind)).size === values.length),
}).strict();
const reason = z.string().max(64).regex(/^[A-Z_]+$/);
const readCheck = z.object({
  id: z.literal("quota.read"), state: z.enum(["unverified", "available", "denied", "unsupported", "failed"]),
  reason, http_status: z.number().int().min(100).max(599).nullable(), latency_ms: z.number().int().nonnegative().nullable(),
}).strict();
export const collectionJob = z.object({
  id: revision, run_id: revision, account_id: objectID, source_id: objectID,
  connection_revision: revision, account_revision: revision, policy_revision: revision, source_mode: sourceMode,
  state: z.enum(["queued", "running", "succeeded", "failed", "skipped", "conflict", "interrupted"]), reason,
  queued_at: timestamp, started_at: timestamp.nullable(), finished_at: timestamp.nullable(),
  retry_after: timestamp.nullable(), read: readCheck.nullable(),
}).strict().refine(v => v.state === "queued" ? v.started_at === null && v.finished_at === null
  : v.state === "running" ? v.started_at !== null && v.finished_at === null : v.finished_at !== null);
export const collectionRun = z.object({
  id: revision, reason: z.enum(["manual", "scheduled"]), actor_id: objectID.nullable(), created_at: timestamp,
  state: z.enum(["queued", "running", "completed"]),
  counts: z.object({ queued: count, running: count, succeeded: count, failed: count, skipped: count, conflict: count, interrupted: count }).strict(),
}).strict().refine(v => (v.reason === "manual") === (v.actor_id !== null))
  .refine(v => v.state === (v.counts.running !== "0" ? "running" : v.counts.queued !== "0" ? "queued" : "completed"));
export const accountObservation = z.object({
  account_id: objectID, requested_mode: sourceMode, snapshot: quotaSnapshot.nullable(), latest_attempt: collectionJob.nullable(),
  freshness: z.enum(["missing", "unknown", "future", "fresh", "stale", "source_changed", "connection_changed", "connection_unavailable", "identity_unverified", "source_mode_changed"]),
  stale_warning: z.boolean(), next_scheduled_at: timestamp.nullable(),
}).strict().refine(v => (v.freshness === "missing") === (v.snapshot === null))
  .refine(v => v.latest_attempt === null || v.latest_attempt.account_id === v.account_id);
const paging = z.object({ after: count, limit: z.number().int().min(1).max(100) }).strict();
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const monitoringContract = {
  health: base.route({ method: "GET", path: "/health" }).input(z.object({}).strict())
    .output(z.object({ status: z.literal("ok"), scope: z.literal("process") }).strict()),
  config: base.route({ method: "GET", path: "/api/v1/monitoring/config" }).input(z.object({}).strict()).output(monitoringConfig),
  updateConfig: base.route({ method: "POST", path: "/api/v1/monitoring/config/update" }).input(configFields.refine(validConfig)).output(monitoringConfig),
  status: base.route({ method: "GET", path: "/api/v1/monitoring/status" }).input(z.object({}).strict()).output(z.object({
    enabled: z.boolean(), worker_seen_at: timestamp.nullable(), worker_recent: z.boolean(),
    active_available: z.literal(false), active_reason: z.literal("ACTIVE_QUERY_SIDE_EFFECT"),
  }).strict().refine(v => !v.worker_recent || v.worker_seen_at !== null)),
  collect: base.route({ method: "POST", path: "/api/v1/monitoring/collect" }).input(z.object({
    source_id: objectID, connection_revision: revision, all: z.boolean(),
    account_ids: z.array(objectID).max(500).refine(v => new Set(v).size === v.length),
  }).strict().refine(v => v.all ? v.account_ids.length === 0 : v.account_ids.length > 0)).output(collectionRun),
  runs: base.route({ method: "POST", path: "/api/v1/monitoring/runs/list" }).input(paging).output(z.object({
    runs: z.array(collectionRun).max(100), next_after: revision.nullable(),
  }).strict().refine(v => new Set(v.runs.map(r => r.id)).size === v.runs.length && (v.next_after === null || v.runs.at(-1)?.id === v.next_after))),
  run: base.route({ method: "POST", path: "/api/v1/monitoring/runs/get" }).input(paging.extend({ id: revision })).output(z.object({
    run: collectionRun, jobs: z.array(collectionJob).max(100), next_after: revision.nullable(),
  }).strict().refine(v => v.jobs.every(j => j.run_id === v.run.id) && new Set(v.jobs.map(j => j.id)).size === v.jobs.length && (v.next_after === null || v.jobs.at(-1)?.id === v.next_after))),
  account: base.route({ method: "POST", path: "/api/v1/monitoring/accounts/get" }).input(z.object({ id: objectID }).strict()).output(accountObservation),
};
export type MonitoringConfig = z.infer<typeof monitoringConfig>;
export type CollectionRun = z.infer<typeof collectionRun>;
export type CollectionJob = z.infer<typeof collectionJob>;
export type AccountObservation = z.infer<typeof accountObservation>;
