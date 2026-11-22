import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { usageBucket, usageCountValue as numberOf, usageFact, usageNanos, usageSummary } from "./usage";

export const participantsVersion = "participants-v2";

export function usageCalendar(timezone: string) {
  const formatter = new Intl.DateTimeFormat("en-CA", { calendar: "iso8601", numberingSystem: "latn", timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit" });
  const day = (instant: string | number | Date) => {
    const parts = Object.fromEntries(formatter.formatToParts(new Date(instant)).map(part => [part.type, part.value]));
    return `${parts.year!.padStart(4, "0")}-${parts.month}-${parts.day}`;
  };
  const offsetFormatter = new Intl.DateTimeFormat("en-GB", { timeZone: timezone, numberingSystem: "latn", timeZoneName: "longOffset" });
  const offsetAt = (at: number) => {
    const label = offsetFormatter.formatToParts(new Date(at)).find(part => part.type === "timeZoneName")!.value;
    if (label === "GMT") return 0;
    const match = /^GMT([+-])(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(label);
    if (!match) throw new RangeError("Unsupported calendar offset");
    return (match[1] === "-" ? -1 : 1) * (Number(match[2]) * 3600 + Number(match[3]) * 60 + Number(match[4] ?? 0)) * 1000;
  };
  const cache = new Map<string, { start: number; end: number }[]>();
  const periods = (first: string, grain: "day" | "month") => {
    const key = `${grain}:${first}`, saved = cache.get(key);
    if (saved) return saved;
    const begin = Date.parse(`${first}T00:00:00Z`), next = new Date(begin);
    if (!Number.isFinite(begin)) throw new RangeError("Invalid calendar date");
    if (grain === "month") next.setUTCMonth(next.getUTCMonth() + 1);
    else next.setUTCDate(next.getUTCDate() + 1);
    const finish = next.getTime(), result: { start: number; end: number }[] = [];
    // shortcut: hourly IANA inspection needs Temporal transitions if two offset changes can occur within an hour.
    for (let at = begin - 172800000; at < finish + 172800000;) {
      const offset = offsetAt(at);
      let to = Math.min(at + 3600000, finish + 172800000);
      if (offsetAt(to - 1) !== offset) {
        let left = at + 1, right = to;
        while (left < right) {
          const middle = left + Math.floor((right - left) / 2);
          if (offsetAt(middle) === offset) left = middle + 1;
          else right = middle;
        }
        to = left;
      }
      const start = Math.max(at, begin - offset), end = Math.min(to, finish - offset), previous = result.at(-1);
      if (start < end) {
        if (previous?.end === start) previous.end = end;
        else result.push({ start, end });
      }
      at = to;
    }
    cache.set(key, result);
    return result;
  };
  const boundary = (date: string): string => {
    const found = periods(date, "day")[0];
    if (found) return new Date(found.start).toISOString();
    const next = new Date(`${date}T00:00:00Z`);
    next.setUTCDate(next.getUTCDate() + 1);
    return boundary(next.toISOString().slice(0, 10));
  };
  const bounds = (instant: string, grain: "day" | "month") => {
    const first = grain === "month" ? `${day(instant).slice(0, 7)}-01` : day(instant);
    const at = Date.parse(instant), found = periods(first, grain).find(period => period.start <= at && at < period.end);
    if (!found) throw new RangeError("Calendar period unavailable");
    return { start_at: new Date(found.start).toISOString(), end_at: new Date(found.end).toISOString() };
  };
  return { day, boundary, bounds };
}
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
export const participantExit = z.object({
  participant_id: objectID, previous_revision: revision, revision, binding_id: revision, name,
  effective_at: timestamp, pool_contract_ids: z.array(revision).max(500).refine(v => new Set(v).size === v.length),
}).strict().refine(v => BigInt(v.revision) === BigInt(v.previous_revision) + 1n);
const basis = z.enum(["local_binding_at_log_time", "mapping_not_recorded", "missing_user_id", "outside_recorded_binding"]);
export const usageAttribution = z.object({
  participant_id: objectID.nullable(), binding_id: revision.nullable(), participant_name: name.nullable(), basis,
}).strict().refine(v => v.basis === "local_binding_at_log_time"
  ? v.participant_id !== null && v.binding_id !== null && v.participant_name !== null
  : v.participant_id === null && v.binding_id === null && v.participant_name === null);
const record = z.object({ capture_id: revision, account_id: objectID, attribution: usageAttribution, fact: usageFact }).strict();
const group = usageSummary.extend({ account_id: objectID, participant_id: objectID.nullable(), basis }).strict()
  .refine(v => (v.participant_id !== null) === (v.basis === "local_binding_at_log_time"));
const keyGroup = usageSummary.extend({ account_id: objectID, participant_id: objectID.nullable(), basis, api_key_id: revision.nullable() }).strict()
  .refine(v => (v.participant_id !== null) === (v.basis === "local_binding_at_log_time"));
const cursor = z.string().max(39).refine(v => v === "0" || v.split(".").length === 2 && v.split(".").every(x => revision.safeParse(x).success));
const period = (days: bigint) => z.object({ start_at: timestamp, end_at: timestamp }).strict().refine(v => {
  const from = usageNanos(v.start_at), to = usageNanos(v.end_at);
  return from !== null && to !== null && v.start_at.slice(0, 4) >= "1970" && v.end_at.slice(0, 4) < "9999" && to > from && to - from <= days * 86400n * 1000000000n;
});
const range = period(31n);
export const usageHistoryRange = period(366n);
const selection = z.object({ id: revision, account_id: objectID, start_at: timestamp, end_at: timestamp, captured_at: timestamp }).strict()
  .refine(v => range.safeParse({ start_at: v.start_at, end_at: v.end_at }).success);
const limits = ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"];
const atMost = (left: string, right: string) => {
  const a = usageNanos(left), b = usageNanos(right);
  return a !== null && b !== null && a <= b;
};
const historyFields = {
  basis: z.literal("selected_captures"), timezone: text(64).min(1).refine(value => {
    try { usageCalendar(value); return value !== "Local"; } catch { return false; }
  }), settings_revision: revision, authority_revision: revision,
};
const historyMetadata = z.object({ ...historyFields, selections: z.array(selection).min(1).max(20) }).strict().refine(v => v.selections.every((item, index) => {
  const previous = v.selections[index - 1];
  return (!previous || numberOf(previous.id) < numberOf(item.id))
    && v.selections.slice(0, index).every(other => other.account_id !== item.account_id
      || atMost(other.end_at, item.start_at) || atMost(item.end_at, other.start_at));
}));const recordedMetadata = z.object({ ...historyFields, selections: z.array(selection).min(1).max(7340) }).strict().refine(v => {
  const byAccount = new Map<string, typeof v.selections>();
  for (const [index, item] of v.selections.entries()) {
    const previous = v.selections[index - 1];
    if (previous && (numberOf(previous.id) > numberOf(item.id) ||
      previous.id === item.id && (!atMost(previous.end_at, item.start_at) || previous.account_id !== item.account_id))) return false;
    const bucket = byAccount.get(item.account_id);
    if (bucket) bucket.push(item);
    else byAccount.set(item.account_id, [item]);
  }
  return [...byAccount.values()].every(items => {
    items.sort((a, b) => atMost(a.start_at, b.start_at) ? -1 : 1);
    return items.every((item, index) => index === 0 || atMost(items[index - 1]!.end_at, item.start_at));
  });
});

const legacyGrain = z.enum(["hour", "day"]), historicalGrain = z.enum(["day", "month"]);
const attributedUsageSchema = <G extends typeof legacyGrain | typeof historicalGrain>(grain: G, historical: boolean, recorded = false) => z.object({
  history: (recorded ? recordedMetadata : historyMetadata).optional(),
  start_at: timestamp, end_at: timestamp, oldest_capture_at: timestamp, newest_capture_at: timestamp, summary: usageSummary, assigned: usageSummary, unassigned: usageSummary.nullable(),
  query_start_at: timestamp.optional(), query_end_at: timestamp.optional(),
  unassigned_visible: z.boolean(), groups: z.array(group).max(20000), grain, buckets: z.array(usageBucket).max(745),
  key_groups: z.array(keyGroup).min(1).max(20000).optional(),
  records: z.array(record).max(100), next_after: cursor.nullable(), complete: z.literal(false), settlement_eligible: z.literal(false),
  currency: z.literal("USD"), timestamp_basis: z.literal("native_created_at"), limitations: z.array(reason).min(6).max(64),
}).strict().refine(v => (historical ? usageHistoryRange : range).safeParse({ start_at: v.start_at, end_at: v.end_at }).success)
  .refine(v => v.query_start_at === undefined && v.query_end_at === undefined
    || (historical ? usageHistoryRange : range).safeParse({ start_at: v.query_start_at, end_at: v.query_end_at }).success)
  .refine(v => usageNanos(v.oldest_capture_at)! <= usageNanos(v.newest_capture_at)!)
  .refine(v => limits.every(r => v.limitations.includes(r)) && new Set(v.limitations).size === v.limitations.length)
  .refine(v => v.unassigned_visible === (v.unassigned !== null))
  .refine(v => numberOf(v.assigned.request_count) + (v.unassigned === null ? 0n : numberOf(v.unassigned.request_count)) === numberOf(v.summary.request_count))
  .refine(v => v.unassigned_visible || v.records.every(r => r.attribution.participant_id !== null) && v.groups.every(g => g.participant_id !== null))
  .refine(v => numberOf(v.summary.request_count) >= BigInt(v.records.length) && (recorded || numberOf(v.summary.request_count) <= 20000n))
  .refine(v => v.groups.reduce((n, g) => n + numberOf(g.request_count), 0n) === numberOf(v.summary.request_count))
  .refine(v => {
    if (v.key_groups === undefined) return true;
    if (!historical || v.key_groups.reduce((n, g) => n + numberOf(g.request_count), 0n) !== numberOf(v.summary.request_count)) return false;
    const parentKey = (g: z.infer<typeof group>) => JSON.stringify([g.account_id, g.participant_id, g.basis]);
    const parents = new Map(v.groups.map(g => [parentKey(g), numberOf(g.request_count)])), counts = new Map<string, bigint>(), seen = new Set<string>();
    const valid = v.key_groups.every(g => {
      const key = JSON.stringify([parentKey(g), g.api_key_id]);
      if (seen.has(key) || !parents.has(parentKey(g)) || numberOf(g.request_count) === 0n || !v.unassigned_visible && g.participant_id === null) return false;
      seen.add(key);
      counts.set(parentKey(g), (counts.get(parentKey(g)) ?? 0n) + numberOf(g.request_count));
      return true;
    });
    return valid && [...parents].every(([key, count]) => counts.get(key) === count);
  })
  .refine(v => v.buckets.reduce((n, b) => n + numberOf(b.request_count), 0n) === numberOf(v.summary.request_count))
  .refine(v => v.records.every((r, i) => i === 0 || numberOf(r.capture_id) > numberOf(v.records[i - 1]!.capture_id)
    || r.capture_id === v.records[i - 1]!.capture_id && numberOf(r.fact.id) > numberOf(v.records[i - 1]!.fact.id)))
  .refine(v => v.next_after === null || v.records.at(-1) !== undefined && v.next_after === `${v.records.at(-1)!.capture_id}.${v.records.at(-1)!.fact.id}`)
  .refine(v => v.records.every(r => usageNanos(r.fact.created_at)! >= usageNanos(v.start_at)! && usageNanos(r.fact.created_at)! < usageNanos(v.end_at)!))
  .refine(v => v.records.every(r => v.buckets.some(bucket => atMost(bucket.start_at, r.fact.created_at)
    && usageNanos(r.fact.created_at)! < usageNanos(bucket.end_at)!)))
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
    let calendar: ReturnType<typeof usageCalendar>;
    try { calendar = usageCalendar(v.history.timezone); } catch { return false; }
    return selected.some(item => usageNanos(item.start_at) === from) && selected.some(item => usageNanos(item.end_at) === to)
      && selected.every(item => atMost(v.start_at, item.start_at) && atMost(item.end_at, v.end_at))
      && selected.some(item => usageNanos(item.captured_at) === usageNanos(v.oldest_capture_at))
      && selected.some(item => usageNanos(item.captured_at) === usageNanos(v.newest_capture_at))
      && selected.every(item => atMost(v.oldest_capture_at, item.captured_at) && atMost(item.captured_at, v.newest_capture_at))
      && v.records.every(record => selected.some(item => item.id === record.capture_id && item.account_id === record.account_id
        && atMost(item.start_at, record.fact.created_at) && atMost(record.fact.created_at, item.end_at) && usageNanos(record.fact.created_at) !== usageNanos(item.end_at)))
      && v.groups.every(group => selected.some(item => item.account_id === group.account_id))
      && v.buckets.every(bucket => {
        let bounds;
        try { bounds = calendar.bounds(bucket.start_at, v.grain as "day" | "month"); } catch { return false; }
        const starts = [bounds.start_at, v.start_at, v.query_start_at], ends = [bounds.end_at, v.end_at, v.query_end_at];
        return starts.every(value => value !== undefined && atMost(value, bucket.start_at))
          && starts.some(value => value !== undefined && usageNanos(value) === usageNanos(bucket.start_at))
          && ends.every(value => value !== undefined && atMost(bucket.end_at, value))
          && ends.some(value => value !== undefined && usageNanos(value) === usageNanos(bucket.end_at));
      });
  });
export const attributedUsage = attributedUsageSchema(legacyGrain, false);
export const participantDeletion = z.object({
  participant_id: objectID, previous_revision: revision, revision, binding_id: revision, name, effective_at: timestamp,
}).strict().refine(v => BigInt(v.revision) === BigInt(v.previous_revision) + 1n);
export const participantDeleteResultInput = z.object({ id: objectID, revision }).strict();
const fields = { source_id: objectID, connection_revision: revision, upstream_id: revision, name, contact: text(512), notes: text(2048), enabled: z.boolean() };
export const participantCreateInput = z.object(fields).strict();
export const participantUpdateInput = z.object({ id: objectID, revision, ...fields }).strict();
export const participantExitInput = z.object({ id: objectID, revision, confirmation: name }).strict();
export const participantExitResultInput = z.object({ id: objectID, revision }).strict();
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
  CONNECTION_REVERIFICATION_REQUIRED: { status: 409, data: authErrorData },
  PARTICIPANT_EXIT_BLOCKED: { status: 409, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const participantsOutputs = {
  delete: z.object({ deleted: z.literal(true) }).strict(),
  deleteResult: participantDeletion,
  exit: participantExit,
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
  recorded: z.object({
    view: attributedUsageSchema(historicalGrain, true, true).nullable(),
    basis: z.string().regex(/^[a-f0-9]{64}$/),
    missing_accounts: z.array(objectID).max(20),
  }).strict().refine(v => new Set(v.missing_accounts).size === v.missing_accounts.length &&
    v.missing_accounts.every(id => !v.view?.history?.selections.some(item => item.account_id === id))),
};
export const participantsContract = {
  candidates: base.route({ method: "POST", path: "/api/v1/participants/candidates" }).input(z.object({ source_id: objectID, revision, page: z.number().int().min(1).max(2147483647), search: text(256).refine(v => [...v].length <= 100) }).strict()).output(participantsOutputs.candidates),
  list: base.route({ method: "POST", path: "/api/v1/participants/list" }).input(z.object({ after: z.union([z.literal(""), objectID]), search: text(256), limit: paging.limit }).strict()).output(participantsOutputs.list),
  get: base.route({ method: "POST", path: "/api/v1/participants/get" }).input(z.object({ id: objectID }).strict()).output(participant),
  create: base.route({ method: "POST", path: "/api/v1/participants/create" }).input(participantCreateInput).output(mutation),
  update: base.route({ method: "POST", path: "/api/v1/participants/update" }).input(participantUpdateInput).output(mutation),
  exit: base.route({ method: "POST", path: "/api/v1/participants/exit" })
    .input(participantExitInput).output(participantExit),
  exitResult: base.route({ method: "POST", path: "/api/v1/participants/exit-result" }).input(participantExitResultInput).output(participantExit),
  delete: base.route({ method: "POST", path: "/api/v1/participants/delete" }).input(z.object({ id: objectID, revision, confirmation: name }).strict()).output(participantsOutputs.delete),
  deleteResult: base.route({ method: "POST", path: "/api/v1/participants/delete-result" }).input(participantDeleteResultInput).output(participantDeletion),
  bindings: base.route({ method: "POST", path: "/api/v1/participants/bindings" }).input(z.object({ id: objectID, ...paging }).strict()).output(participantsOutputs.bindings),
  captures: base.route({ method: "POST", path: "/api/v1/participants/usage/captures" }).input(z.object({ account_id: objectID, ...paging }).strict()).output(participantsOutputs.captures),
  usage: base.route({ method: "POST", path: "/api/v1/participants/usage/view" }).input(usageInput).output(attributedUsage),
  window: base.route({ method: "POST", path: "/api/v1/participants/usage/window" })
    .input(usageInput.extend(range.shape).refine(v => range.safeParse({ start_at: v.start_at, end_at: v.end_at }).success)).output(participantsOutputs.window),
  recorded: base.route({ method: "POST", path: "/api/v1/participants/usage/recorded" }).input(z.object({
    ...usageHistoryRange.shape, after: cursor, limit: z.number().int().min(0).max(100), account_ids: z.array(objectID).min(1).max(20),
    model: text(512), api_key_id: z.union([z.literal(""), revision]), participant_id: z.union([z.literal(""), objectID]),
    grain: historicalGrain, basis: z.union([z.literal(""), z.string().regex(/^[a-f0-9]{64}$/)]),
  }).strict().refine(v => usageHistoryRange.safeParse({ start_at: v.start_at, end_at: v.end_at }).success &&
    new Set(v.account_ids).size === v.account_ids.length && (v.after === "0" || v.limit > 0 && v.basis !== ""))).output(participantsOutputs.recorded),
  history: base.route({ method: "POST", path: "/api/v1/participants/usage/history" })
    .input(usageInput.extend({ ...usageHistoryRange.shape, grain: z.enum(["day", "month"]) }).refine(v => usageHistoryRange.safeParse({ start_at: v.start_at, end_at: v.end_at }).success)).output(participantsOutputs.history),
};
export type Participant = z.infer<typeof participant>;
export type ParticipantExit = z.infer<typeof participantExit>;
export type ParticipantDeletion = z.infer<typeof participantDeletion>;
export type ParticipantBinding = z.infer<typeof participantBinding>;
export type AttributedUsage = z.infer<typeof attributedUsage>;
export type UsageHistory = z.infer<typeof participantsOutputs.history>;
