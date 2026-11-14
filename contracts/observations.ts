import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { quotaSnapshot } from "./monitoring";
import { sharePercent, shareUnits } from "./pools";
import { usageNanos, usageSummary } from "./usage";
import { usageCalendar } from "./participants";

export const observationsVersion = "observations-v1";
const timestamp = session.shape.created_at;
const utcInput = timestamp.regex(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/);
const count = z.union([z.literal("0"), revision]);
const reason = z.string().min(1).max(512).refine(value => new TextEncoder().encode(value).length <= 512 && value.trim() === value && !/\p{Cc}/u.test(value));
const kind = z.enum(["short", "seven_day"]);
const paging = z.object({ after: count, limit: z.number().int().min(1).max(100) }).strict();
const range = z.object({ start_at: utcInput, end_at: utcInput }).strict().refine(value => {
  const start = usageNanos(value.start_at), end = usageNanos(value.end_at);
  return start !== null && end !== null && start < end && value.start_at.slice(0, 4) >= "1970" && value.end_at.slice(0, 4) < "9999";
});
export const observation = z.object({
  id: revision, job_id: revision, account_id: objectID, snapshot: quotaSnapshot, management_revision: revision,
  excluded: z.boolean(), manual_start: z.boolean(), review_reason: reason.nullable(), reviewed_at: timestamp.nullable(),
}).strict().refine(value => (value.review_reason === null) === (value.reviewed_at === null) && (!value.excluded || value.review_reason !== null));
const event = z.object({
  id: revision, account_id: objectID, revision, action: z.enum(["exclude", "restore", "start", "revoke"]),
  observation_id: revision.nullable(), start_observation_id: revision.nullable(), end_observation_id: revision.nullable(),
  window_kind: kind.nullable(), reason, actor_id: objectID.nullable(), occurred_at: timestamp,
}).strict().refine(value => BigInt(value.revision) > 1n).refine(value => value.action === "exclude" || value.action === "restore"
  ? value.observation_id !== null && value.start_observation_id === null && value.end_observation_id === null && value.window_kind === null
  : value.action === "start" ? value.observation_id === null && value.start_observation_id !== null && value.end_observation_id !== null && value.start_observation_id !== value.end_observation_id && value.window_kind !== null
    : value.observation_id === null && value.start_observation_id === null && value.end_observation_id === null && value.window_kind === null);
const period = z.object({ kind, start_at: timestamp, end_at: timestamp, duration_seconds: revision, basis: z.literal("native_reset_and_duration") }).strict().refine(value => {
  const start = usageNanos(value.start_at), end = usageNanos(value.end_at), seconds = BigInt(value.duration_seconds);
  return start !== null && end !== null && seconds <= 315360000n && end - start === seconds * 1000000000n;
});
const startRange = z.object({
  event, start: observation, end: observation, period: period.nullable(), usable: z.boolean(),
  reason: z.enum(["VERIFIED_LOCAL_RANGE", "PERIOD_EVIDENCE_MISSING", "ENDPOINT_EXCLUDED", "SOURCE_CHANGED", "CONNECTION_UNAVAILABLE", "CONNECTION_CHANGED", "IDENTITY_UNVERIFIED", "SOURCE_MODE_CHANGED", "SOURCE_TIME_UNVERIFIED", "WINDOW_EXPIRED", "SOURCE_STALE"]),
}).strict().refine(value => value.event.action === "start" && value.event.start_observation_id === value.start.id && value.event.end_observation_id === value.end.id
  && value.start.manual_start && !value.end.manual_start
  && value.start.account_id === value.end.account_id && value.event.account_id === value.start.account_id
  && value.usable === (value.reason === "VERIFIED_LOCAL_RANGE") && (!value.usable || !value.start.excluded && !value.end.excluded && value.period !== null))
  .refine(value => {
    const selected = value.period;
    if (selected === null) return !value.usable;
    if (selected.kind !== value.event.window_kind) return false;
    const from = usageNanos(selected.start_at), to = usageNanos(selected.end_at);
    if (from === null || to === null) return false;
    const firstAt = usageNanos(value.start.snapshot.source_at ?? ""), lastAt = usageNanos(value.end.snapshot.source_at ?? "");
    if (firstAt === null || lastAt === null || firstAt >= lastAt || value.start.snapshot.source_id !== value.end.snapshot.source_id) return false;
    return [value.start, value.end].every(item => {
      const sourceAt = usageNanos(item.snapshot.source_at ?? ""), receivedAt = usageNanos(item.snapshot.received_at);
      const window = item.snapshot.windows.find(window => window.kind === selected.kind);
      return sourceAt !== null && receivedAt !== null && sourceAt <= receivedAt && sourceAt >= from && sourceAt < to
        && window?.used_percent != null && window.duration_seconds === selected.duration_seconds && usageNanos(window.reset_at ?? "") === to;
    });
  });
export const observationManagement = z.object({
  account_id: objectID, revision, manual_start: startRange.nullable(), recalculation_state: z.enum(["not_implemented", "not_configured", "required", "recorded"]),
}).strict().refine(value => value.manual_start === null || value.manual_start.event.account_id === value.account_id
  && BigInt(value.manual_start.event.revision) <= BigInt(value.revision)
  && value.manual_start.start.management_revision === value.revision && value.manual_start.end.management_revision === value.revision);
const basis = z.object({
  contract_id: revision.nullable(), members: z.array(z.object({ participant_id: objectID, name: z.string().min(1).max(256).refine(value => [...value].length <= 128 && value.trim() === value && !/\p{Cc}/u.test(value)), share_percent: sharePercent }).strict()).max(500),
  unallocated_percent: sharePercent.nullable(), time_basis: z.enum(["unavailable", "recorded_local_contract_at_source_time"]),
  usage_state: z.literal("unavailable"), estimate_state: z.literal("unavailable"),
}).strict().refine(value => (value.contract_id === null) === (value.time_basis === "unavailable")
  && (value.contract_id !== null || value.members.length === 0 && value.unallocated_percent === null)
  && new Set(value.members.map(member => member.participant_id)).size === value.members.length)
  .refine(value => {
    const allocated = value.members.reduce((sum, member) => sum + shareUnits(member.share_percent), 0);
    return allocated <= 100000000 && (value.unallocated_percent === null || allocated + shareUnits(value.unallocated_percent) === 100000000);
  });
const numbered = (items: { id: string }[], next: string | null) => items.every((item, index) => {
  const previous = items[index - 1];
  return previous === undefined || BigInt(item.id) > BigInt(previous.id);
}) && (next === null || items.at(-1)?.id === next);
const usageWindow = z.object({
  kind, start_at: timestamp.nullable(), end_at: timestamp.nullable(),
  reason: z.enum(["NATIVE_WINDOW_UNVERIFIED", "NO_RECORDED_WINDOW_CAPTURE", "RECORDED_WINDOW_USAGE"]),
  capture: z.object({ id: revision, captured_at: timestamp, state: z.enum(["exhausted", "partial", "failed"]), summary: usageSummary,
    limitations: z.array(z.string().max(64).regex(/^[A-Z_]+$/)).min(6).max(16) }).strict().nullable(),
}).strict().refine(value => value.reason === "NATIVE_WINDOW_UNVERIFIED" ? value.start_at === null && value.end_at === null && value.capture === null
  : value.start_at !== null && value.end_at !== null && range.safeParse({ start_at: value.start_at, end_at: value.end_at }).success
    && (value.capture !== null) === (value.reason === "RECORDED_WINDOW_USAGE"));
const usageEvidence = z.object({ observation_id: revision, account_id: objectID, scope: z.enum(["account", "authorized_participants"]), basis: z.literal("native_window_interval"),
  currency: z.literal("USD"), timestamp_basis: z.literal("native_created_at"), complete: z.literal(false), settlement_eligible: z.literal(false), windows: z.array(usageWindow).max(2) }).strict()
  .refine(value => new Set(value.windows.map(window => window.kind)).size === value.windows.length)
  .refine(value => value.windows.every(window => {
    const capture = window.capture;
    return !capture || ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_SELECTED_CAPTURE", "QUOTA_CAUSALITY_UNVERIFIED", ...(value.scope === "authorized_participants" ? ["ONLY_AUTHORIZED_PARTICIPANTS"] : [])].every(reason => capture.limitations.includes(reason));
  }));
export function observationUsageMatches(item: Observation, evidence: z.infer<typeof usageEvidence>) {
  return evidence.observation_id === item.id && evidence.account_id === item.account_id
    && evidence.windows.length === item.snapshot.windows.length && evidence.windows.every(entry => {
      const window = item.snapshot.windows.find(window => window.kind === entry.kind);
      if (!window) return false;
      if (entry.start_at === null || entry.end_at === null) return true;
      const from = usageNanos(entry.start_at), end = usageNanos(entry.end_at), reset = usageNanos(window.reset_at ?? ""), received = usageNanos(item.snapshot.received_at);
      return from !== null && end !== null && reset !== null && received !== null && window.duration_seconds !== null
        && end === usageNanos(item.snapshot.source_at ?? "") && end <= received && end < reset
        && from + BigInt(window.duration_seconds) * 1000000000n === reset;
    });
}
export const dailyObservationInput = z.object({ account_id: objectID, settings_revision: revision, ...range.shape }).strict().refine(value => {
  if (!range.safeParse({ start_at: value.start_at, end_at: value.end_at }).success) return false;
  const start = usageNanos(value.start_at), end = usageNanos(value.end_at);
  return start !== null && end !== null && end - start <= 31n * 86400n * 1000000000n;
});
const dailyHistory = z.object({
  rules_version: z.literal("recorded-day-last-v1"), account_id: objectID,
  timezone: z.string().min(1).max(64).refine(value => { try { usageCalendar(value); return value !== "Local"; } catch { return false; } }),
  settings_revision: revision, authority_revision: revision, query_start_at: timestamp, query_end_at: timestamp, time_basis: z.literal("received_at"),
  complete: z.literal(false), settlement_eligible: z.literal(false),
  current: z.object({ observation, usage: usageEvidence }).strict().nullable(),
  records: z.array(z.object({ day: z.string().regex(/^\d{4}-\d{2}-\d{2}$/), receipt_count: revision, observation, usage: usageEvidence }).strict()).max(64),
}).strict().refine(value => {
  if (!dailyObservationInput.safeParse({ account_id: value.account_id, settings_revision: value.settings_revision, start_at: value.query_start_at, end_at: value.query_end_at }).success) return false;
  const start = usageNanos(value.query_start_at), end = usageNanos(value.query_end_at);
  if (start === null || end === null) return false;
  const current = value.current;
  if (current && (current.observation.account_id !== value.account_id || current.usage.account_id !== value.account_id || current.usage.observation_id !== current.observation.id)) return false;
  let calendar;
  try { calendar = usageCalendar(value.timezone); } catch { return false; }
  return value.records.every((row, index) => {
    const at = usageNanos(row.observation.snapshot.received_at), previous = value.records[index - 1];
    return at !== null && at >= start && at < end && row.day === calendar.day(row.observation.snapshot.received_at)
      && (!previous || previous.day < row.day) && row.observation.account_id === value.account_id
      && row.usage.account_id === value.account_id && row.usage.observation_id === row.observation.id;
  });
});
const observationPage = z.object({ records: z.array(observation).max(100), next_after: revision.nullable(), query_start_at: timestamp, query_end_at: timestamp, time_basis: z.literal("received_at") }).strict();
function observationPageMatches(value: z.infer<typeof observationPage>) {
  const start = usageNanos(value.query_start_at), end = usageNanos(value.query_end_at);
  return range.safeParse({ start_at: value.query_start_at, end_at: value.query_end_at }).success && numbered(value.records, value.next_after)
    && start !== null && end !== null && value.records.every(item => { const at = usageNanos(item.snapshot.received_at); return at !== null && at >= start && at < end; });
}
export const observationsOutputs = {
  list: observationPage.refine(observationPageMatches),
  listUsage: observationPage.extend({ usage_evidence: z.array(usageEvidence).max(100) }).refine(observationPageMatches)
    .refine(value => value.records.length === value.usage_evidence.length && value.records.every((item, index) => {
      const evidence = value.usage_evidence[index];
      return evidence !== undefined && observationUsageMatches(item, evidence);
    })),
  get: z.object({ observation, basis }).strict(),
  estimateLink: z.object({ observation_id: revision, account_id: objectID, estimate: z.object({ id: revision, generated_at: timestamp }).strict().nullable() }).strict(),
  recommendationLink: z.object({ observation_id: revision, account_id: objectID, recommendation: z.object({ id: revision, participant_id: objectID, estimate_id: revision, generated_at: timestamp }).strict().nullable() }).strict(),
  usageEvidence, dailyHistory,
  events: z.object({ account_id: objectID, events: z.array(event).max(100), next_after: revision.nullable() }).strict()
    .refine(value => numbered(value.events, value.next_after) && value.events.every(event => event.account_id === value.account_id)),
  state: observationManagement, review: observation, start: observationManagement, revoke: observationManagement,
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  OBSERVATION_RANGE_UNVERIFIED: { status: 409, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
const managementInput = z.object({ account_id: objectID, revision, reason }).strict();
const listInput = paging.extend({
  account_id: z.union([z.literal(""), objectID]), source_id: z.union([z.literal(""), objectID]), source_mode: z.enum(["all", "active", "passive"]), validity: z.enum(["all", "included", "excluded", "manual_start"]), ...range.shape,
}).refine(value => range.safeParse({ start_at: value.start_at, end_at: value.end_at }).success);
export const observationsContract = {
  list: base.route({ method: "POST", path: "/api/v1/observations/list" }).input(listInput).output(observationsOutputs.list),
  listUsage: base.route({ method: "POST", path: "/api/v1/observations/list-usage" }).input(listInput).output(observationsOutputs.listUsage),
  get: base.route({ method: "POST", path: "/api/v1/observations/get" }).input(z.object({ id: revision }).strict()).output(observationsOutputs.get),
  estimateLink: base.route({ method: "POST", path: "/api/v1/observations/estimate-link" }).input(z.object({ id: revision }).strict()).output(observationsOutputs.estimateLink),
  recommendationLink: base.route({ method: "POST", path: "/api/v1/observations/recommendation-link" }).input(z.object({ id: revision }).strict()).output(observationsOutputs.recommendationLink),
  usageEvidence: base.route({ method: "POST", path: "/api/v1/observations/usage-evidence" }).input(z.object({ id: revision }).strict()).output(observationsOutputs.usageEvidence),
  dailyHistory: base.route({ method: "POST", path: "/api/v1/observations/daily-history" }).input(dailyObservationInput).output(dailyHistory),
  events: base.route({ method: "POST", path: "/api/v1/observations/events" }).input(paging.extend({ account_id: objectID })).output(observationsOutputs.events),
  state: base.route({ method: "POST", path: "/api/v1/observations/state" }).input(z.object({ account_id: objectID }).strict()).output(observationManagement),
  review: base.route({ method: "POST", path: "/api/v1/observations/review" }).input(managementInput.extend({ observation_id: revision, excluded: z.boolean() })).output(observation),
  start: base.route({ method: "POST", path: "/api/v1/observations/start" }).input(managementInput.extend({ start_observation_id: revision, end_observation_id: revision, window_kind: kind }).refine(value => value.start_observation_id !== value.end_observation_id)).output(observationManagement),
  revoke: base.route({ method: "POST", path: "/api/v1/observations/revoke" }).input(managementInput).output(observationManagement),
};
export type Observation = z.infer<typeof observation>;
export type ObservationDetail = z.infer<typeof observationsOutputs.get>;
export type ObservationEstimateLink = z.infer<typeof observationsOutputs.estimateLink>;
export type ObservationRecommendationLink = z.infer<typeof observationsOutputs.recommendationLink>;
export type ObservationUsageEvidence = z.infer<typeof observationsOutputs.usageEvidence>;
export type DailyObservationHistory = z.infer<typeof dailyHistory>;
export type DailyObservationInput = z.infer<typeof dailyObservationInput>;
export type ObservationManagement = z.infer<typeof observationManagement>;
export type ObservationEvent = z.infer<typeof event>;
export { reason as observationReason };
