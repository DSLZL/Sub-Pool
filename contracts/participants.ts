import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { usageBucket, usageCountValue as numberOf, usageFact, usageNanos, usageSummary } from "./usage";

export const participantsVersion = "participants-v1";
const timestamp = session.shape.created_at;
const text = (max: number) => z.string().max(max).refine(v => new TextEncoder().encode(v).length <= max && !/[\u0000-\u001f\u007f-\u009f]/.test(v));
const name = text(512).min(1).refine(v => v.trim() === v && [...v].length <= 128);
const count = z.string().max(32).regex(/^(?:0|[1-9]\d*)$/);
const reason = z.string().max(64).regex(/^[A-Z_]+$/);
export const nativeUser = z.object({
  upstream_id: revision, email: text(512), username: text(512), role: z.enum(["admin", "user"]),
  status: z.enum(["active", "disabled"]), created_at: timestamp,
}).strict();
export const participantBinding = z.object({
  id: revision, participant_id: objectID, source_id: objectID, upstream_user_id: revision,
  started_at: timestamp, ended_at: timestamp.nullable(), identity: nativeUser.nullable(),
}).strict().refine(v => v.identity === null || v.identity.upstream_id === v.upstream_user_id)
  .refine(v => v.ended_at === null || usageNanos(v.ended_at)! >= usageNanos(v.started_at)!);
export const participant = z.object({
  id: objectID, revision, name, contact: text(512).nullable(), notes: text(2048).nullable(), enabled: z.boolean(),
  created_at: timestamp, saved_at: timestamp, source_current: z.boolean(), binding: participantBinding,
}).strict().refine(v => v.id === v.binding.participant_id && v.binding.ended_at === null)
  .refine(v => (v.contact === null) === (v.notes === null) && (v.contact === null) === (v.binding.identity === null));
const read = z.object({
  id: z.literal("users.read"), state: z.enum(["unverified", "available", "denied", "unsupported", "failed"]), reason,
  http_status: z.number().int().min(100).max(599).nullable(), latency_ms: z.number().int().nonnegative().nullable(),
}).strict();
const mutation = z.object({ participant: participant.nullable(), read: read.nullable() }).strict()
  .refine(v => v.participant === null ? v.read !== null && v.read.state !== "available" : v.read === null || v.read.state === "available");
const basis = z.enum(["local_binding_at_log_time", "mapping_not_recorded", "missing_user_id", "outside_recorded_binding"]);
export const usageAttribution = z.object({
  participant_id: objectID.nullable(), binding_id: revision.nullable(), participant_name: name.nullable(), basis,
}).strict().refine(v => v.basis === "local_binding_at_log_time"
  ? v.participant_id !== null && v.binding_id !== null && v.participant_name !== null
  : v.participant_id === null && v.binding_id === null && v.participant_name === null);
const record = z.object({ capture_id: revision, account_id: objectID, attribution: usageAttribution, fact: usageFact }).strict();
const group = usageSummary.extend({ account_id: objectID, participant_id: objectID.nullable(), basis }).strict()
  .refine(v => (v.participant_id !== null) === (v.basis === "local_binding_at_log_time"));
const cursor = z.string().max(39).refine(v => v === "0" || v.split(".").length === 2 && v.split(".").every(x => revision.safeParse(x).success));
const period = (days: bigint) => z.object({ start_at: timestamp, end_at: timestamp }).strict().refine(v => {
  const from = usageNanos(v.start_at), to = usageNanos(v.end_at);
  return from !== null && to !== null && v.start_at.slice(0, 4) >= "1970" && v.end_at.slice(0, 4) < "9999" && to > from && to - from <= days * 86400n * 1000000000n;
});
const range = period(31n), historyRange = period(366n);
const selection = z.object({ id: revision, account_id: objectID, start_at: timestamp, end_at: timestamp, captured_at: timestamp }).strict()
  .refine(v => range.safeParse({ start_at: v.start_at, end_at: v.end_at }).success);
const limits = ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"];
const atMost = (left: string, right: string) => {
  const a = usageNanos(left), b = usageNanos(right);
  return a !== null && b !== null && a <= b;
};
const historyMetadata = z.object({
  basis: z.literal("selected_captures"), timezone: z.literal("UTC"), authority_revision: revision,
  selections: z.array(selection).min(1).max(20),
}).strict().refine(v => v.selections.every((item, index) => {
  const previous = v.selections[index - 1];
  return (!previous || numberOf(previous.id) < numberOf(item.id))
    && v.selections.slice(0, index).every(other => other.account_id !== item.account_id
      || atMost(other.end_at, item.start_at) || atMost(item.end_at, other.start_at));
}));
const legacyGrain = z.enum(["hour", "day"]), historicalGrain = z.enum(["day", "month"]);
const attributedUsageSchema = <G extends typeof legacyGrain | typeof historicalGrain>(grain: G, historical: boolean) => z.object({
  history: historyMetadata.optional(),
  start_at: timestamp, end_at: timestamp, oldest_capture_at: timestamp, newest_capture_at: timestamp, summary: usageSummary, assigned: usageSummary, unassigned: usageSummary.nullable(),
  query_start_at: timestamp.optional(), query_end_at: timestamp.optional(),
  unassigned_visible: z.boolean(), groups: z.array(group).max(20000), grain, buckets: z.array(usageBucket).max(745),
  records: z.array(record).max(100), next_after: cursor.nullable(), complete: z.literal(false), settlement_eligible: z.literal(false),
  currency: z.literal("USD"), timestamp_basis: z.literal("native_created_at"), limitations: z.array(reason).min(6).max(64),
}).strict().refine(v => (historical ? historyRange : range).safeParse({ start_at: v.start_at, end_at: v.end_at }).success)
  .refine(v => v.query_start_at === undefined && v.query_end_at === undefined
    || (historical ? historyRange : range).safeParse({ start_at: v.query_start_at, end_at: v.query_end_at }).success)
  .refine(v => usageNanos(v.oldest_capture_at)! <= usageNanos(v.newest_capture_at)!)
  .refine(v => limits.every(r => v.limitations.includes(r)) && new Set(v.limitations).size === v.limitations.length)
  .refine(v => v.unassigned_visible === (v.unassigned !== null))
  .refine(v => numberOf(v.assigned.request_count) + (v.unassigned === null ? 0n : numberOf(v.unassigned.request_count)) === numberOf(v.summary.request_count))
  .refine(v => v.unassigned_visible || v.records.every(r => r.attribution.participant_id !== null) && v.groups.every(g => g.participant_id !== null))
  .refine(v => numberOf(v.summary.request_count) >= BigInt(v.records.length) && numberOf(v.summary.request_count) <= 20000n)
  .refine(v => v.groups.reduce((n, g) => n + numberOf(g.request_count), 0n) === numberOf(v.summary.request_count))
  .refine(v => v.buckets.reduce((n, b) => n + numberOf(b.request_count), 0n) === numberOf(v.summary.request_count))
  .refine(v => v.records.every((r, i) => i === 0 || numberOf(r.capture_id) > numberOf(v.records[i - 1]!.capture_id)
    || r.capture_id === v.records[i - 1]!.capture_id && numberOf(r.fact.id) > numberOf(v.records[i - 1]!.fact.id)))
  .refine(v => v.next_after === null || v.records.at(-1) !== undefined && v.next_after === `${v.records.at(-1)!.capture_id}.${v.records.at(-1)!.fact.id}`)
  .refine(v => v.records.every(r => usageNanos(r.fact.created_at)! >= usageNanos(v.start_at)! && usageNanos(r.fact.created_at)! < usageNanos(v.end_at)!))
  .refine(v => {
    if (v.query_start_at === undefined) return true;
    const from = usageNanos(v.query_start_at), to = usageNanos(v.query_end_at ?? "");
    return from !== null && to !== null && v.records.every(r => {
      const at = usageNanos(r.fact.created_at);
      return at !== null && at >= from && at < to;
    }) && v.buckets.every(b => {
      const start = usageNanos(b.start_at), end = usageNanos(b.end_at);
      return start !== null && end !== null && start >= from && end <= to;
    });
  })
  .refine(v => v.buckets.every((b, i) => numberOf(b.request_count) > 0n && usageNanos(b.start_at)! >= usageNanos(v.start_at)!
    && usageNanos(b.end_at)! <= usageNanos(v.end_at)! && usageNanos(b.start_at)! < usageNanos(b.end_at)!
    && (i === 0 || usageNanos(v.buckets[i - 1]!.end_at)! <= usageNanos(b.start_at)!)))
  .refine(v => {
    if (!historical) return v.history === undefined;
    if (!v.history || v.query_start_at === undefined || v.query_end_at === undefined || !("grain" in v)) return false;
    const selected = v.history.selections, from = usageNanos(v.start_at), to = usageNanos(v.end_at);
    return selected.some(item => usageNanos(item.start_at) === from) && selected.some(item => usageNanos(item.end_at) === to)
      && selected.every(item => atMost(v.start_at, item.start_at) && atMost(item.end_at, v.end_at))
      && selected.some(item => usageNanos(item.captured_at) === usageNanos(v.oldest_capture_at))
      && selected.some(item => usageNanos(item.captured_at) === usageNanos(v.newest_capture_at))
      && selected.every(item => atMost(v.oldest_capture_at, item.captured_at) && atMost(item.captured_at, v.newest_capture_at))
      && v.records.every(record => selected.some(item => item.id === record.capture_id && item.account_id === record.account_id
        && atMost(item.start_at, record.fact.created_at) && atMost(record.fact.created_at, item.end_at) && usageNanos(record.fact.created_at) !== usageNanos(item.end_at)))
      && v.groups.every(group => selected.some(item => item.account_id === group.account_id))
      && v.buckets.every(bucket => {
        const calendarStart = v.grain === "month" ? `${bucket.start_at.slice(0, 7)}-01T00:00:00Z` : `${bucket.start_at.slice(0, 10)}T00:00:00Z`;
        const next = new Date(calendarStart);
        if (v.grain === "month") next.setUTCMonth(next.getUTCMonth() + 1);
        else next.setUTCDate(next.getUTCDate() + 1);
        const starts = [calendarStart, v.start_at, v.query_start_at], ends = [next.toISOString(), v.end_at, v.query_end_at];
        return starts.every(value => value !== undefined && atMost(value, bucket.start_at))
          && starts.some(value => value !== undefined && usageNanos(value) === usageNanos(bucket.start_at))
          && ends.every(value => value !== undefined && atMost(bucket.end_at, value))
          && ends.some(value => value !== undefined && usageNanos(value) === usageNanos(bucket.end_at));
      });
  });
export const attributedUsage = attributedUsageSchema(legacyGrain, false);
const fields = { source_id: objectID, connection_revision: revision, upstream_id: revision, name, contact: text(512), notes: text(2048), enabled: z.boolean() };
export const participantCreateInput = z.object(fields).strict();
export const participantUpdateInput = z.object({ id: objectID, revision, ...fields }).strict();
const paging = { after: z.union([z.literal("0"), revision]), limit: z.number().int().min(1).max(100) };
const usageInput = z.object({
  capture_ids: z.array(revision).min(1).max(20).refine(v => new Set(v).size === v.length),
  participant_id: z.union([z.literal(""), objectID]), model: text(512), api_key_id: z.union([z.literal(""), revision]),
  grain: z.enum(["hour", "day"]), after: cursor, limit: paging.limit,
}).strict();
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData }, NOT_FOUND: { status: 404, data: authErrorData },
  CONFLICT: { status: 409, data: authErrorData }, CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const participantsOutputs = {
  candidates: z.object({ source_id: objectID, connection_revision: revision, page: z.number().int().min(1).max(2147483647), next_page: z.number().int().min(2).max(2147483647).nullable(), total: count.nullable(), users: z.array(nativeUser).max(100), read }).strict()
    .refine(v => v.read.state === "available" ? v.total !== null : v.users.length === 0 && v.total === null && v.next_page === null),
  list: z.object({ participants: z.array(participant).max(100), next_after: objectID.nullable() }).strict()
    .refine(v => v.next_after === null || v.participants.at(-1)?.id === v.next_after),
  participant, mutation,
  bindings: z.object({ bindings: z.array(participantBinding).max(100), next_after: revision.nullable() }).strict()
    .refine(v => v.bindings.every((b, i) => i === 0 || numberOf(b.id) > numberOf(v.bindings[i - 1]!.id)))
    .refine(v => v.next_after === null || v.bindings.at(-1)?.id === v.next_after),
  captures: z.object({ captures: z.array(selection).max(100), next_after: revision.nullable() }).strict()
    .refine(v => v.captures.every((c, i) => i === 0 || numberOf(c.id) > numberOf(v.captures[i - 1]!.id)))
    .refine(v => v.next_after === null || v.captures.at(-1)?.id === v.next_after),
  usage: attributedUsage,
  window: attributedUsage.refine(v => v.query_start_at !== undefined && v.query_end_at !== undefined),
  history: attributedUsageSchema(historicalGrain, true),
};
export const participantsContract = {
  candidates: base.route({ method: "POST", path: "/api/v1/participants/candidates" }).input(z.object({ source_id: objectID, revision, page: z.number().int().min(1).max(2147483647), search: text(256).refine(v => [...v].length <= 100) }).strict()).output(participantsOutputs.candidates),
  list: base.route({ method: "POST", path: "/api/v1/participants/list" }).input(z.object({ after: z.union([z.literal(""), objectID]), search: text(256), limit: paging.limit }).strict()).output(participantsOutputs.list),
  get: base.route({ method: "POST", path: "/api/v1/participants/get" }).input(z.object({ id: objectID }).strict()).output(participant),
  create: base.route({ method: "POST", path: "/api/v1/participants/create" }).input(participantCreateInput).output(mutation),
  update: base.route({ method: "POST", path: "/api/v1/participants/update" }).input(participantUpdateInput).output(mutation),
  delete: base.route({ method: "POST", path: "/api/v1/participants/delete" }).input(z.object({ id: objectID, revision, confirmation: name }).strict()).output(z.object({ deleted: z.literal(true) }).strict()),
  bindings: base.route({ method: "POST", path: "/api/v1/participants/bindings" }).input(z.object({ id: objectID, ...paging }).strict()).output(participantsOutputs.bindings),
  captures: base.route({ method: "POST", path: "/api/v1/participants/usage/captures" }).input(z.object({ account_id: objectID, ...paging }).strict()).output(participantsOutputs.captures),
  usage: base.route({ method: "POST", path: "/api/v1/participants/usage/view" }).input(usageInput).output(attributedUsage),
  window: base.route({ method: "POST", path: "/api/v1/participants/usage/window" })
    .input(usageInput.extend(range.shape).refine(v => range.safeParse({ start_at: v.start_at, end_at: v.end_at }).success)).output(participantsOutputs.window),
  history: base.route({ method: "POST", path: "/api/v1/participants/usage/history" })
    .input(usageInput.extend({ ...historyRange.shape, grain: z.enum(["day", "month"]) }).refine(v => historyRange.safeParse({ start_at: v.start_at, end_at: v.end_at }).success)).output(participantsOutputs.history),
};
export type Participant = z.infer<typeof participant>;
export type ParticipantBinding = z.infer<typeof participantBinding>;
export type AttributedUsage = z.infer<typeof attributedUsage>;
export type UsageHistory = z.infer<typeof participantsOutputs.history>;
