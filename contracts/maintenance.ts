import { oc } from "@orpc/contract";
import { z } from "zod";

import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { quotaWindow } from "./monitoring";
import { usageNanos } from "./usage";
import { estimateRecord } from "./estimates";

export const maintenanceVersion = "maintenance-v3";
export const maintenanceRulesVersion = "native-period-index-v1";
export const maintenanceHistoryRulesVersion = "retained-estimates-v2";
const timestamp = session.shape.created_at.regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/);
const count = z.union([z.literal("0"), revision]);
const fingerprint = z.string().regex(/^[a-f0-9]{64}$/);
const ruleName = z.string().regex(/^[a-z0-9][a-z0-9.-]{0,63}$/);
const kind = z.enum(["rebuild", "cycles", "upgrade"]);
const scopeFields = z.object({ account_id: objectID, from: timestamp, until: timestamp }).strict();
const validScope = (v: { from: string; until: string }) => {
  const from = usageNanos(v.from), until = usageNanos(v.until);
  return from !== null && until !== null && from < until && v.from.slice(0, 4) >= "1970" && v.until.slice(0, 4) < "9999";
};
const scope = scopeFields.refine(validScope);
export const maintenanceInput = scopeFields.extend({ kind, rules_version: ruleName }).refine(validScope);
const percent = quotaWindow.shape.used_percent.unwrap().refine(value => {
  const [coefficient, exponent = "0"] = value.split(/[eE]/);
  if (coefficient === undefined || value.length > 64 || !Number.isInteger(Number(exponent)) || Number(exponent) < -18 || Number(exponent) > 18) return false;
  try {
    const [whole, fraction = ""] = coefficient.split(".");
    if (whole === undefined) return false;
    const digits = BigInt(whole + fraction), scale = fraction.length - Number(exponent);
    return scale >= 0 ? digits <= 100n * 10n ** BigInt(scale) : digits * 10n ** BigInt(-scale) <= 100n;
  } catch { return false; }
});
const period = z.object({
  source_id: objectID, connection_revision: revision, account_revision: revision, source_mode: z.enum(["passive", "active"]),
  kind: z.enum(["short", "seven_day"]), start_at: timestamp, end_at: timestamp, duration_seconds: revision, basis: z.literal("native_reset_and_duration"),
  observation_ids: z.array(revision).min(1).max(2000).refine(ids => new Set(ids).size === ids.length),
  first_source_at: timestamp, last_source_at: timestamp, first_used_percent: percent, last_used_percent: percent,
}).strict().refine(v => {
  const start = usageNanos(v.start_at), end = usageNanos(v.end_at), first = usageNanos(v.first_source_at), last = usageNanos(v.last_source_at);
  return start !== null && end !== null && first !== null && last !== null && revision.safeParse(v.duration_seconds).success && end - start === BigInt(v.duration_seconds) * 1000000000n
    && start <= first && first <= last && last < end;
});
const estimateFields = estimateRecord.shape.windows.element.shape;
const financialState = z.enum(["scenario", "non_identifying_delta", "evidence_incomplete"]);
const financialWindow = z.object({
  kind: estimateFields.kind, observation_ids: estimateFields.observation_ids,
  state: financialState, capacity: estimateFields.capacity, remaining: estimateFields.remaining, range_min: estimateFields.range_min, range_max: estimateFields.range_max,
}).strict().refine(v => v.state === "scenario" ? v.observation_ids.length === 3 && v.capacity !== null && v.remaining !== null && v.range_min !== null && v.range_max !== null
  : v.capacity === null && v.remaining === null && v.range_min === null && v.range_max === null);
const financialResult = z.object({
  estimate_id: revision, generated_at: timestamp, original_rules: ruleName, original_state: estimateFields.state, original_remaining: estimateFields.remaining,
  state: financialState, remaining: estimateFields.remaining, windows: z.array(financialWindow).max(2),
}).strict().refine(v => {
  try {
    return new Set(v.windows.map(w => w.kind)).size === v.windows.length && (v.state === "scenario"
  ? v.remaining !== null && v.windows.length > 0 && v.windows.every(w => w.state === "scenario")
    && v.windows.some(w => w.remaining !== null && BigInt(w.remaining.numerator) * BigInt(v.remaining?.denominator ?? "1") === BigInt(v.remaining?.numerator ?? "0") * BigInt(w.remaining.denominator))
    && v.windows.every(w => w.remaining !== null && BigInt(w.remaining.numerator) * BigInt(v.remaining?.denominator ?? "1") >= BigInt(v.remaining?.numerator ?? "0") * BigInt(w.remaining.denominator))
  : v.remaining === null && (v.state === "evidence_incomplete" || v.windows.some(w => w.state === "non_identifying_delta")));
  } catch { return false; }
});
const financial = z.object({
  calculation_rules: z.literal("gate-003-rules-v1"), currency: z.literal("USD"), scan_complete: z.boolean(), records: z.number().int().min(0).max(2001), results: z.array(financialResult).max(2000),
}).strict().refine(v => v.scan_complete ? v.records <= 2000 && v.records === v.results.length && v.results.every(r => revision.safeParse(r.estimate_id).success)
  && v.results.every((r, i) => i === 0 || BigInt(r.estimate_id) > BigInt(v.results[i - 1]?.estimate_id ?? "0")) : v.records === 2001 && v.results.length === 0);
const projection = z.object({
  scan_complete: z.boolean(), observations: z.number().int().min(0).max(2001), excluded: z.number().int().min(0).max(2001), periods: z.array(period).max(4000),
  financial: financial.optional(),
}).strict().refine(v => v.excluded <= v.observations && (!v.scan_complete ? v.observations === 2001 && v.periods.length === 0 : v.observations <= 2000)
  && v.periods.reduce((sum, p) => sum + p.observation_ids.length, 0) <= 2 * (v.observations - v.excluded));
const issue = z.object({ code: z.enum(["OBSERVATION_LIMIT", "NO_LOCAL_OBSERVATIONS", "SOURCE_PROVENANCE_INVALID", "RECEIPT_MISMATCH", "PERIOD_EVIDENCE_MISSING", "SOURCE_TIME_CONFLICT", "NO_NATIVE_PERIODS", "RULE_UPGRADE_UNAVAILABLE", "RULES_UNVERIFIED", "NO_PRIOR_PROJECTION", "PERIOD_INDEX_DIFFERENCE", "REMOTE_OPERATION_UNRESOLVED", "ESTIMATE_HISTORY_LIMIT", "NO_ESTIMATE_HISTORY", "ESTIMATE_EVIDENCE_MISSING", "ESTIMATE_EVIDENCE_INVALID"]), blocking: z.boolean(), observation_id: revision.nullable(), window_kind: z.enum(["short", "seven_day"]).nullable(), estimate_id: revision.optional() }).strict()
  .refine(v => !["REMOTE_OPERATION_UNRESOLVED", "ESTIMATE_HISTORY_LIMIT", "NO_ESTIMATE_HISTORY", "ESTIMATE_EVIDENCE_MISSING", "ESTIMATE_EVIDENCE_INVALID"].includes(v.code) || v.blocking && v.observation_id === null && v.window_kind === null)
  .refine(v => ["ESTIMATE_EVIDENCE_MISSING", "ESTIMATE_EVIDENCE_INVALID"].includes(v.code) === (v.estimate_id !== undefined));
const difference = z.object({ added: z.number().int().nonnegative().max(4000), removed: z.number().int().nonnegative().max(4000), changed: z.number().int().nonnegative().max(4000) }).strict();
const legacyLimitations = ["RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "NO_FINANCIAL_RECALCULATION", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "RULE_UPGRADE_UNAVAILABLE"] as const;
const historyLimitations = ["RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "CONDITIONAL_SIMULATION_ONLY", "NOT_PAST_EXECUTED_AMOUNT", "MODEL_NOT_CALIBRATED", "ESTIMATE_REFERENCES_INCLUDED"] as const;
export const maintenancePlan = z.object({
  id: revision, scope, kind, rules_version: ruleName,
  basis: z.object({ fingerprint, source_id: objectID, connection_revision: revision, account_revision: revision, policy_revision: revision, management_revision: revision, estimate_revision: revision, observation_head: count, capture_head: count, estimate_head: count, previous_applied_plan_id: revision.nullable() }).strict(),
  preview: projection, difference: difference.extend({ financial: difference.optional() }),
  issues: z.array(issue).max(6008), limitations: z.array(z.enum([...legacyLimitations, ...historyLimitations])).min(6).max(8).refine(v => new Set(v).size === v.length),
  created_by: objectID, created_at: timestamp, expires_at: timestamp,
  state: z.enum(["ready", "blocked", "queued", "running", "applied", "expired", "failed"]), reason: z.enum(["READY", "PREFLIGHT_BLOCKED", "QUEUED", "RUNNING", "APPLIED", "BASIS_CHANGED", "PLAN_EXPIRED", "ACTOR_UNAVAILABLE", "WORKER_INTERRUPTED"]),
  validity: z.enum(["valid", "basis_changed", "expired", "blocked", "pending", "terminal"]),
  requested_by: objectID.nullable(), queued_at: timestamp.nullable(), started_at: timestamp.nullable(), finished_at: timestamp.nullable(), result_id: revision.nullable(),
}).strict().refine(v => {
  const created = usageNanos(v.created_at), expires = usageNanos(v.expires_at);
  const queued = v.queued_at === null ? null : usageNanos(v.queued_at), started = v.started_at === null ? null : usageNanos(v.started_at), finished = v.finished_at === null ? null : usageNanos(v.finished_at);
  if (created === null || expires === null || created >= expires || v.queued_at !== null && queued === null || v.started_at !== null && started === null || v.finished_at !== null && finished === null) return false;
  if ((v.requested_by === null) !== (v.queued_at === null)) return false;
  if (queued !== null && queued < created) return false;
  if (started !== null && (queued === null || started < queued)) return false;
  if (finished !== null && finished < (started ?? queued ?? created)) return false;
  if ((v.result_id !== null) !== (v.state === "applied") || v.result_id !== null && v.result_id !== v.id) return false;
  const historical = v.preview.financial;
  const knownRule = v.rules_version === maintenanceRulesVersion || v.rules_version === maintenanceHistoryRulesVersion;
  const requiredLimits = historical === undefined ? legacyLimitations : historyLimitations;
  if (v.limitations.length !== requiredLimits.length || !requiredLimits.every(l => v.limitations.includes(l))) return false;
  if (historical !== undefined && (v.rules_version !== maintenanceHistoryRulesVersion || v.difference.financial === undefined
    || historical.results.some(r => (usageNanos(r.generated_at) ?? 0n) < (usageNanos(v.scope.from) ?? 0n) || (usageNanos(r.generated_at) ?? 0n) >= (usageNanos(v.scope.until) ?? 0n)))) return false;
  if ((v.difference.financial !== undefined) !== (historical !== undefined)) return false;
  if (!knownRule && (v.preview.periods.length !== 0 || historical !== undefined)) return false;
  if (["ready", "queued", "running", "applied"].includes(v.state) && (!v.preview.scan_complete || v.preview.periods.length === 0 || v.issues.some(i => i.blocking) || !knownRule
    || (v.rules_version === maintenanceHistoryRulesVersion ? historical === undefined || !historical.scan_complete || historical.records === 0 || historical.results.some(r => r.state === "evidence_incomplete") : v.kind === "upgrade"))) return false;
  if (v.state === "ready" || v.state === "blocked") {
    return v.requested_by === null && v.started_at === null && v.finished_at === null && v.issues.some(i => i.blocking) === (v.state === "blocked")
      && (v.state === "blocked" ? v.validity === "blocked" && v.reason === "PREFLIGHT_BLOCKED" : ["valid", "basis_changed", "expired"].includes(v.validity) && v.reason === "READY");
  }
  if (v.state === "queued" || v.state === "running") return v.requested_by !== null && v.finished_at === null && (v.started_at !== null) === (v.state === "running") && v.validity === "pending" && v.reason === (v.state === "queued" ? "QUEUED" : "RUNNING");
  if (v.finished_at === null || v.validity !== "terminal") return false;
  if (v.state === "applied") return v.started_at !== null && v.reason === "APPLIED";
  return v.state === "expired" ? ["BASIS_CHANGED", "PLAN_EXPIRED"].includes(v.reason) : v.requested_by !== null && ["ACTOR_UNAVAILABLE", "WORKER_INTERRUPTED"].includes(v.reason);
});
export const maintenanceOutputs = {
  plan: maintenancePlan,
  plans: z.object({ account_id: objectID, plans: z.array(maintenancePlan).max(20), next_after: revision.nullable() }).strict().refine(v => v.plans.every((p, i) => {
    const previous = v.plans[i - 1];
    return p.scope.account_id === v.account_id && revision.safeParse(p.id).success && (previous === undefined || BigInt(p.id) > BigInt(previous.id));
  }) && (v.next_after === null || v.plans.at(-1)?.id === v.next_after)),
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const maintenanceContract = {
  preflight: base.route({ method: "POST", path: "/api/v1/maintenance/preflight" }).input(maintenanceInput).output(maintenancePlan),
  plan: base.route({ method: "POST", path: "/api/v1/maintenance/plan" }).input(z.object({ id: revision }).strict()).output(maintenancePlan),
  plans: base.route({ method: "POST", path: "/api/v1/maintenance/plans" }).input(z.object({ account_id: objectID, after: count, limit: z.number().int().min(1).max(20) }).strict()).output(maintenanceOutputs.plans),
  apply: base.route({ method: "POST", path: "/api/v1/maintenance/apply" }).input(z.object({ id: revision, basis: fingerprint, confirmation: z.literal(true) }).strict()).output(maintenancePlan),
};
export type MaintenancePlan = z.infer<typeof maintenancePlan>;
export type MaintenanceInput = z.infer<typeof maintenanceInput>;
