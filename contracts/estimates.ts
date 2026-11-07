import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { observationReason } from "./observations";
import { usageNanos } from "./usage";
import { quotaWindow } from "./monitoring";

export const estimatesVersion = "estimates-v1";
const timestamp = session.shape.created_at;
const count = z.union([z.literal("0"), revision]);
const kind = z.enum(["short", "seven_day"]);
const keep = z.string().regex(/^(0|[1-9][0-9]{0,37})(\.[0-9]{1,18})?$/).refine(value => {
  const [whole, fraction = ""] = value.split(".");
  return whole === "0" ? /[1-9]/.test(fraction) : whole === "1" && !/[1-9]/.test(fraction);
});
export const estimateAssumptionInput = z.object({
  plan: z.literal("empirical_min_v1"), cost_basis: z.literal("standard_usd"), keep_ratio: keep,
  premise: observationReason, windows: z.array(kind).min(1).max(2).refine(value => new Set(value).size === value.length),
  source_id: objectID, connection_revision: revision, account_revision: revision,
}).strict();
const assumptions = estimateAssumptionInput.extend({ source_mode: z.enum(["passive", "active"]) });
const configuration = z.object({ account_id: objectID, revision, assumptions: assumptions.nullable(), saved_at: timestamp.nullable() }).strict()
  .refine(value => (value.assumptions === null) === (value.saved_at === null));
const period = z.object({ kind, start_at: timestamp, end_at: timestamp, duration_seconds: revision, basis: z.literal("native_reset_and_duration") }).strict().refine(value => {
  const from = usageNanos(value.start_at), to = usageNanos(value.end_at), seconds = BigInt(value.duration_seconds);
  return from !== null && to !== null && seconds <= 315360000n && to - from === seconds * 1000000000n;
});
const state = z.enum(["not_configured", "invalid_evidence", "insufficient_samples", "condition_changed", "evidence_incomplete", "non_identifying_delta", "period_unknown", "source_stale", "manual_range_unusable", "evidence_limit"]);
const window = z.object({
  kind, basis: z.enum(["official", "manual"]), period: period.nullable(), manual_start_at: timestamp.nullable(), manual_end_at: timestamp.nullable(),
  observation_ids: z.array(revision).max(3).refine(value => new Set(value).size === value.length), current_observation_id: revision.nullable(), source_at: timestamp.nullable(), oldest_sample_at: timestamp.nullable(),
  current_used_percent: quotaWindow.shape.used_percent,
  state, capacity: z.null(), remaining: z.null(), range_min: z.null(), range_max: z.null(),
}).strict().refine(value => value.period === null || value.period.kind === value.kind)
  .refine(value => (value.observation_ids.length === 0) === (value.oldest_sample_at === null))
  .refine(value => value.basis === "official" ? value.manual_start_at === null && value.manual_end_at === null : value.manual_start_at !== null && value.manual_end_at !== null && (usageNanos(value.manual_start_at) ?? 0n) < (usageNanos(value.manual_end_at) ?? 0n))
  .refine(value => value.current_observation_id !== null || value.source_at === null && value.current_used_percent === null);
export const estimateRecord = z.object({
  id: revision, account_id: objectID, generated_at: timestamp, rules_version: z.literal("gate-003-rules-v1"), configuration,
  trigger: z.enum(["configuration", "manual_recompute", "observation", "usage", "review", "range"]),
  policy_revision: revision, management_revision: revision, observation_head: count, capture_head: count,
  state, windows: z.array(window).min(1).max(2), remaining: z.null(), currency: z.literal("USD"),
  complete: z.literal(false), settlement_eligible: z.literal(false), model_calibrated: z.literal(false),
  limitations: z.array(z.enum(["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"])).length(4).refine(values => new Set(values).size === 4),
}).strict().refine(value => value.account_id === value.configuration.account_id && value.configuration.assumptions !== null
  && value.windows.length === value.configuration.assumptions.windows.length
  && value.windows.every((window, index) => window.kind === value.configuration.assumptions?.windows[index])
  && (usageNanos(value.configuration.saved_at ?? "") ?? 1n) <= (usageNanos(value.generated_at) ?? 0n));
const current = z.object({
  configuration, latest: estimateRecord.nullable(), current_state: z.union([state, z.literal("recalculation_required")]), current_remaining: z.null(),
  needs_recalculation: z.boolean(), checked_at: timestamp,
}).strict().refine(value => value.latest === null || value.latest.account_id === value.configuration.account_id)
  .refine(value => value.needs_recalculation === (value.current_state === "recalculation_required"));
export const estimatesOutputs = {
  current, record: estimateRecord, configure: estimateRecord, recompute: estimateRecord,
  history: z.object({ account_id: objectID, records: z.array(estimateRecord).max(100), next_after: revision.nullable() }).strict().refine(value => value.records.every((record, index) => {
    const previous = value.records[index - 1];
    return record.account_id === value.account_id && (previous === undefined || BigInt(record.id) > BigInt(previous.id));
  }) && (value.next_after === null || value.records.at(-1)?.id === value.next_after)),
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData },
  SOURCE_BLOCKED: { status: 403, data: authErrorData }, NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const estimatesContract = {
  current: base.route({ method: "POST", path: "/api/v1/estimates/current" }).input(z.object({ account_id: objectID }).strict()).output(current),
  record: base.route({ method: "POST", path: "/api/v1/estimates/record" }).input(z.object({ id: revision }).strict()).output(estimateRecord),
  configure: base.route({ method: "POST", path: "/api/v1/estimates/configure" }).input(estimateAssumptionInput.extend({ account_id: objectID, revision })).output(estimateRecord),
  recompute: base.route({ method: "POST", path: "/api/v1/estimates/recompute" }).input(z.object({ account_id: objectID, revision, latest_id: count }).strict()).output(estimateRecord),
  history: base.route({ method: "POST", path: "/api/v1/estimates/history" }).input(z.object({ account_id: objectID, after: count, limit: z.number().int().min(1).max(100), window_kind: z.enum(["all", "short", "seven_day"]), period_end_at: z.union([z.literal(""), timestamp.regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/)]) }).strict().refine(value => value.period_end_at === "" || value.window_kind !== "all" && value.period_end_at.slice(0, 4) >= "1970" && value.period_end_at.slice(0, 4) < "9999")).output(estimatesOutputs.history),
};
export type EstimateRecord = z.infer<typeof estimateRecord>;
export type EstimateCurrent = z.infer<typeof current>;
export type EstimateAssumptionInput = z.infer<typeof estimateAssumptionInput>;
