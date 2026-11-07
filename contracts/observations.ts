import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { quotaSnapshot } from "./monitoring";
import { sharePercent, shareUnits } from "./pools";
import { usageNanos } from "./usage";

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
export const observationsOutputs = {
  list: z.object({ records: z.array(observation).max(100), next_after: revision.nullable(), query_start_at: timestamp, query_end_at: timestamp, time_basis: z.literal("received_at") }).strict()
    .refine(value => range.safeParse({ start_at: value.query_start_at, end_at: value.query_end_at }).success && numbered(value.records, value.next_after))
    .refine(value => {
      const start = usageNanos(value.query_start_at), end = usageNanos(value.query_end_at);
      return start !== null && end !== null && value.records.every(item => { const at = usageNanos(item.snapshot.received_at); return at !== null && at >= start && at < end; });
    }),
  get: z.object({ observation, basis }).strict(),
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
export const observationsContract = {
  list: base.route({ method: "POST", path: "/api/v1/observations/list" }).input(paging.extend({
    account_id: z.union([z.literal(""), objectID]), source_id: z.union([z.literal(""), objectID]), source_mode: z.enum(["all", "active", "passive"]), validity: z.enum(["all", "included", "excluded", "manual_start"]), ...range.shape,
  }).refine(value => range.safeParse({ start_at: value.start_at, end_at: value.end_at }).success)).output(observationsOutputs.list),
  get: base.route({ method: "POST", path: "/api/v1/observations/get" }).input(z.object({ id: revision }).strict()).output(observationsOutputs.get),
  events: base.route({ method: "POST", path: "/api/v1/observations/events" }).input(paging.extend({ account_id: objectID })).output(observationsOutputs.events),
  state: base.route({ method: "POST", path: "/api/v1/observations/state" }).input(z.object({ account_id: objectID }).strict()).output(observationManagement),
  review: base.route({ method: "POST", path: "/api/v1/observations/review" }).input(managementInput.extend({ observation_id: revision, excluded: z.boolean() })).output(observation),
  start: base.route({ method: "POST", path: "/api/v1/observations/start" }).input(managementInput.extend({ start_observation_id: revision, end_observation_id: revision, window_kind: kind }).refine(value => value.start_observation_id !== value.end_observation_id)).output(observationManagement),
  revoke: base.route({ method: "POST", path: "/api/v1/observations/revoke" }).input(managementInput).output(observationManagement),
};
export type Observation = z.infer<typeof observation>;
export type ObservationDetail = z.infer<typeof observationsOutputs.get>;
export type ObservationManagement = z.infer<typeof observationManagement>;
export type ObservationEvent = z.infer<typeof event>;
export { reason as observationReason };
