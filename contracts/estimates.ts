import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { observationReason } from "./observations";
import { usageNanos } from "./usage";
import { quotaWindow } from "./monitoring";
import { sharePercent } from "./pools";

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
const state = z.enum(["not_configured", "invalid_evidence", "insufficient_samples", "condition_changed", "evidence_incomplete", "non_identifying_delta", "period_unknown", "source_stale", "manual_range_unusable", "evidence_limit", "scenario"]);
const amount = z.object({ numerator: z.string().regex(/^(0|[1-9][0-9]{0,255})$/), denominator: z.string().regex(/^[1-9][0-9]{0,255}$/) }).strict();
const positiveAmount = amount.refine(value => value.numerator !== "0");
const weeklyPoint = z.object({
  observation_id: revision, source_at: timestamp, used_percent: quotaWindow.shape.used_percent.unwrap(),
}).strict();
const weeklyPeriod = period.safeExtend({ kind: z.literal("seven_day"), duration_seconds: z.literal("604800") });
const weeklySample = z.object({
  start: weeklyPoint, end: weeklyPoint, standard_cost: positiveAmount, records: z.number().int().min(1).max(800000), read_at: timestamp,
}).strict().refine(value => (usageNanos(value.start.source_at) ?? 1n) < (usageNanos(value.end.source_at) ?? 0n)
  && (usageNanos(value.end.source_at) ?? 1n) <= (usageNanos(value.read_at) ?? 0n));
const weeklyCheckpoint = z.object({ observation: weeklyPoint, standard_cost: positiveAmount, capacity: positiveAmount }).strict();
const weeklyCumulative = z.object({
  read_at: timestamp, confidence: z.enum(["collecting", "unstable", "trusted"]), checkpoints: z.array(weeklyCheckpoint).min(1).max(3),
}).strict().refine(value => value.confidence === "collecting" || value.checkpoints.length === 3);
const weeklyShare = z.object({ share_percent: sharePercent, capacity: amount, remaining: amount }).strict();
const weeklyAllocation = z.object({
  pool_id: objectID, pool_name: z.string().min(1).max(256), contract_id: revision,
  members: z.array(weeklyShare.extend({ participant_id: objectID, name: z.string().min(1).max(256) })).max(500),
  reserve: weeklyShare.nullable(),
}).strict();
const nativeWeeklyRecord = z.object({
  id: revision, account_id: objectID, source_id: objectID, connection_revision: revision, account_revision: revision,
  generated_at: timestamp, observation: weeklyPoint, period: weeklyPeriod, samples: z.array(weeklySample).min(1).max(128).optional(),
  cumulative: weeklyCumulative.optional(),
  standard_cost: positiveAmount, percentage_point_increase: positiveAmount.optional(), records: z.number().int().min(1).max(800000),
  capacity: positiveAmount, remaining: amount,
}).strict().refine(value => {
  const start = usageNanos(value.period.start_at) ?? 1n;
  const observed = usageNanos(value.observation.source_at) ?? 0n;
  const generated = usageNanos(value.generated_at) ?? 0n;
  if (value.cumulative) {
    const { checkpoints, read_at } = value.cumulative;
    const last = checkpoints.at(-1);
    const read = usageNanos(read_at) ?? 0n;
    return value.samples === undefined && value.percentage_point_increase === undefined
      && last?.observation.observation_id === value.observation.observation_id
      && last.observation.source_at === value.observation.source_at && last.observation.used_percent === value.observation.used_percent
      && last.standard_cost.numerator === value.standard_cost.numerator && last.standard_cost.denominator === value.standard_cost.denominator
      && last.capacity.numerator === value.capacity.numerator && last.capacity.denominator === value.capacity.denominator
      && read >= observed && read <= generated && observed < (usageNanos(value.period.end_at) ?? 0n)
      && checkpoints.every((point, index) => {
        const prior = checkpoints[index - 1];
        const at = usageNanos(point.observation.source_at) ?? 0n;
        return start < at && (!prior || (usageNanos(prior.observation.source_at) ?? at) < at);
      });
  }
  const samples = value.samples;
  return value.percentage_point_increase !== undefined && samples !== undefined
    && samples.reduce((sum, sample) => sum + sample.records, 0) === value.records
    && samples.every((sample, index) => {
      const previous = samples[index - 1];
      return (!previous || (usageNanos(previous.end.source_at) ?? 1n) <= (usageNanos(sample.start.source_at) ?? 0n))
        && start <= (usageNanos(sample.start.source_at) ?? 0n)
        && (usageNanos(sample.end.source_at) ?? 1n) <= observed && (usageNanos(sample.read_at) ?? 1n) <= generated;
    });
});
const weeklyScanState = z.enum(["collecting", "usage_unavailable", "usage_denied", "usage_rate_limited", "usage_invalid", "pagination_changed", "usage_limit", "no_cost", "cost_unavailable"]);
const nativeWeeklyCurrent = z.object({
  state: z.union([weeklyScanState, z.enum(["ready", "condition_changed", "connection_unavailable", "monitoring_paused", "awaiting_observation", "quota_unavailable", "period_unknown", "source_stale", "invalid_evidence", "quota_decreased", "observation_limit", "awaiting_increase"])]),
  observation: weeklyPoint.nullable(), period: weeklyPeriod.nullable(), latest: nativeWeeklyRecord.nullable(), remaining: amount.nullable(),
  allocation: weeklyAllocation.optional(),
  progress: z.object({ state: weeklyScanState, day: timestamp, scanned: z.number().int().min(0).max(100000),
    total: z.number().int().min(0).max(100000), rechecking: z.boolean() }).strict().refine(value => value.scanned <= value.total).nullable(),
}).strict().refine(value => (value.observation === null) === (value.period === null)
  && (value.state === "ready" ? value.observation !== null && value.latest !== null && value.remaining !== null : value.latest === null && value.remaining === null)
  && (!value.allocation || value.latest?.cumulative?.confidence === "trusted" && value.latest.observation.used_percent === value.observation?.used_percent));
const weeklyHistoryPageSize = 10;
const weeklyHistory = z.object({ account_id: objectID, records: z.array(nativeWeeklyRecord).max(weeklyHistoryPageSize), next_before: revision.nullable() }).strict()
  .refine(value => value.records.every((record, index) => {
    const previous = value.records[index - 1];
    return record.account_id === value.account_id && (!previous || BigInt(record.id) < BigInt(previous.id));
  }) && (value.next_before === null || value.records.length === weeklyHistoryPageSize && value.next_before === value.records.at(-1)?.id));
const money = z.string().regex(/^(0|[1-9][0-9]{0,15})(\.[0-9]{1,8})?$/);
const costProof = z.object({
  protocol: z.literal("sub-pool-simulator-v1"), environment: z.literal("simulated"), epoch: objectID,
  source_start: timestamp, closed_through: timestamp, ledger_revision: revision, price_version: revision,
  standard_rate: money.refine(value => /[1-9]/.test(value)), wallet_multiplier: money, billing_type: z.enum(["standard_wallet", "subscription"]), price_effective_at: timestamp,
  intervals: z.array(z.object({ start_at: timestamp, end_at: timestamp, standard_cost: amount, actual_cost: amount, entry_count: z.number().int().min(1).max(10000) }).strict()).length(2),
}).strict().refine(value => {
  const [first, last] = value.intervals;
  return first !== undefined && last !== undefined && value.intervals.every(interval => (usageNanos(interval.start_at) ?? 1n) < (usageNanos(interval.end_at) ?? 0n))
    && usageNanos(first.end_at) === usageNanos(last.start_at)
    && (usageNanos(value.source_start) ?? 1n) <= (usageNanos(first.start_at) ?? 0n)
    && (usageNanos(value.price_effective_at) ?? 1n) <= (usageNanos(first.start_at) ?? 0n)
    && (usageNanos(value.closed_through) ?? 0n) >= (usageNanos(last.end_at) ?? 1n);
});
const window = z.object({
  kind, basis: z.enum(["official", "manual"]), period: period.nullable(), manual_start_at: timestamp.nullable(), manual_end_at: timestamp.nullable(),
  observation_ids: z.array(revision).max(3).refine(value => new Set(value).size === value.length), current_observation_id: revision.nullable(), source_at: timestamp.nullable(), oldest_sample_at: timestamp.nullable(),
  current_used_percent: quotaWindow.shape.used_percent,
  state, capacity: amount.nullable(), remaining: amount.nullable(), range_min: amount.nullable(), range_max: amount.nullable(), cost_proof: costProof.optional(),
}).strict().refine(value => value.period === null || value.period.kind === value.kind)
  .refine(value => (value.observation_ids.length === 0) === (value.oldest_sample_at === null))
  .refine(value => value.basis === "official" ? value.manual_start_at === null && value.manual_end_at === null : value.manual_start_at !== null && value.manual_end_at !== null && (usageNanos(value.manual_start_at) ?? 0n) < (usageNanos(value.manual_end_at) ?? 0n))
  .refine(value => value.current_observation_id !== null || value.source_at === null && value.current_used_percent === null)
  .refine(value => value.state === "scenario" ? value.cost_proof !== undefined && value.capacity !== null && value.remaining !== null && value.range_min !== null && value.range_max !== null
    : value.capacity === null && value.remaining === null && value.range_min === null && value.range_max === null)
  .refine(value => value.cost_proof === undefined || value.period !== null && value.observation_ids.length === 3 && value.oldest_sample_at !== null && value.source_at !== null
    && usageNanos(value.oldest_sample_at) === usageNanos(value.cost_proof.intervals[0]?.start_at ?? "")
    && (usageNanos(value.cost_proof.intervals[1]?.end_at ?? "") ?? 1n) <= (usageNanos(value.source_at) ?? 0n)
    && (usageNanos(value.period.start_at) ?? 1n) <= (usageNanos(value.oldest_sample_at) ?? 0n)
    && (usageNanos(value.source_at) ?? 1n) < (usageNanos(value.period.end_at) ?? 0n));
export const estimateRecord = z.object({
  id: revision, account_id: objectID, generated_at: timestamp, rules_version: z.literal("gate-003-rules-v1"), configuration,
  trigger: z.enum(["configuration", "manual_recompute", "observation", "usage", "review", "range"]),
  policy_revision: revision, management_revision: revision, observation_head: count, capture_head: count,
  state, windows: z.array(window).min(1).max(2), remaining: amount.nullable(), currency: z.literal("USD"),
  complete: z.boolean(), settlement_eligible: z.literal(false), model_calibrated: z.literal(false),
  limitations: z.array(z.enum(["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET", "SIMULATED_PROTOCOL_ONLY"])).min(3).max(5).refine(values => new Set(values).size === values.length),
}).strict().refine(value => value.account_id === value.configuration.account_id && value.configuration.assumptions !== null
  && value.windows.length === value.configuration.assumptions.windows.length
  && value.windows.every((window, index) => window.kind === value.configuration.assumptions?.windows[index])
  && (usageNanos(value.configuration.saved_at ?? "") ?? 1n) <= (usageNanos(value.generated_at) ?? 0n))
  .refine(value => {
    const simulated = value.windows.some(window => window.cost_proof !== undefined);
    const required = ["MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET", ...(value.complete ? [] : ["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED"]), ...(simulated ? ["SIMULATED_PROTOCOL_ONLY"] : [])];
    return value.complete === value.windows.every(window => window.cost_proof !== undefined)
      && (!simulated || value.configuration.assumptions?.source_mode === "active")
      && new Set(value.windows.flatMap(window => window.cost_proof ? [window.cost_proof.epoch] : [])).size <= 1
      && value.limitations.length === required.length && required.every(reason => value.limitations.includes(reason as typeof value.limitations[number]))
      && value.windows.every(window => window.cost_proof === undefined || (usageNanos(window.cost_proof.closed_through) ?? 1n) <= (usageNanos(value.generated_at) ?? 0n))
      && (value.state === "scenario" ? value.complete && value.remaining !== null && value.windows.every(window => window.state === "scenario") : value.remaining === null);
  });
const current = z.object({
  configuration, latest: estimateRecord.nullable(), current_state: z.union([state, z.literal("recalculation_required")]), current_remaining: amount.nullable(),
  needs_recalculation: z.boolean(), checked_at: timestamp,
  native_weekly: nativeWeeklyCurrent.optional(), simulation_configurable: z.literal(true).optional(),
}).strict().refine(value => value.latest === null || value.latest.account_id === value.configuration.account_id)
  .refine(value => !value.native_weekly || value.simulation_configurable === undefined &&
    (!value.native_weekly.latest || value.native_weekly.latest.account_id === value.configuration.account_id))
  .refine(value => value.needs_recalculation === (value.current_state === "recalculation_required"))
  .refine(value => value.current_state === "scenario" ? value.latest?.state === "scenario" && value.latest.complete && value.latest.configuration.revision === value.configuration.revision && value.current_remaining !== null
    && JSON.stringify(value.current_remaining) === JSON.stringify(value.latest.remaining) : value.current_remaining === null);
export const estimatesOutputs = {
  current, record: estimateRecord, configure: estimateRecord, recompute: estimateRecord, weeklyHistory,
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
  weeklyHistory: base.route({ method: "POST", path: "/api/v1/estimates/weekly-history" }).input(z.object({ account_id: objectID, before: count }).strict()).output(weeklyHistory),
  configure: base.route({ method: "POST", path: "/api/v1/estimates/configure" }).input(estimateAssumptionInput.extend({ account_id: objectID, revision })).output(estimateRecord),
  recompute: base.route({ method: "POST", path: "/api/v1/estimates/recompute" }).input(z.object({ account_id: objectID, revision, latest_id: count }).strict()).output(estimateRecord),
  history: base.route({ method: "POST", path: "/api/v1/estimates/history" }).input(z.object({ account_id: objectID, after: count, limit: z.number().int().min(1).max(100), window_kind: z.enum(["all", "short", "seven_day"]), period_end_at: z.union([z.literal(""), timestamp.regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/)]) }).strict().refine(value => value.period_end_at === "" || value.window_kind !== "all" && value.period_end_at.slice(0, 4) >= "1970" && value.period_end_at.slice(0, 4) < "9999")).output(estimatesOutputs.history),
};
export type EstimateRecord = z.infer<typeof estimateRecord>;
export type EstimateCurrent = z.infer<typeof current>;
export type EstimateAssumptionInput = z.infer<typeof estimateAssumptionInput>;
export type NativeWeeklyRecord = z.infer<typeof nativeWeeklyRecord>;
export type NativeWeeklyCurrent = z.infer<typeof nativeWeeklyCurrent>;
