import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";

export const usageVersion = "usage-v1";
const timestamp = session.shape.created_at;
const count = z.string().max(32).regex(/^(?:0|[1-9]\d*)$/);
const countValue = (v: string) => /^(?:0|[1-9]\d{0,31})$/.test(v) ? BigInt(v) : -1n;
const decimal = z.string().max(96).regex(/^(?:0|[1-9]\d*)(?:\.\d{1,36})?$/);
const text = z.string().max(512).refine(v => new TextEncoder().encode(v).length <= 512 && !/[\u0000-\u001f\u007f-\u009f]/.test(v));
const reason = z.string().max(64).regex(/^[A-Z_]+$/);
const metrics = z.object({
  input_tokens: count.nullable(), output_tokens: count.nullable(), cache_creation_tokens: count.nullable(), cache_read_tokens: count.nullable(),
  cache_creation_5m_tokens: count.nullable(), cache_creation_1h_tokens: count.nullable(), image_input_tokens: count.nullable(), image_output_tokens: count.nullable(),
  total_cost: decimal.nullable(), actual_cost: decimal.nullable(), account_stats_cost: decimal.nullable(),
}).strict();
export const usageFact = z.object({
  id: revision, user_id: revision.nullable(), api_key_id: revision.nullable(), group_id: revision.nullable(), subscription_id: revision.nullable(),
  request_id: text.min(1).nullable(), model: text.min(1), upstream_model: text.min(1).nullable(), upstream_response_model: text.min(1).nullable(),
  billing_mode: text.min(1).nullable(), billing_type: z.number().int().min(-128).max(127).nullable(),
  billing_candidate: z.enum(["wallet", "subscription", "unknown"]), deduction_state: z.literal("unknown"),
  rate_multiplier: decimal.nullable(), account_rate_multiplier: decimal.nullable(), created_at: timestamp, metrics,
}).strict().refine(v => v.billing_candidate === (v.billing_type === 0 ? "wallet" : v.billing_type === 1 ? "subscription" : "unknown"));
const read = z.object({
  id: z.literal("usage.read"), state: z.enum(["unverified", "available", "denied", "unsupported", "failed"]), reason,
  http_status: z.number().int().min(100).max(599).nullable(), latency_ms: z.number().int().nonnegative().nullable(),
}).strict();
export const usageCapture = z.object({
  id: revision, account_id: objectID, source_id: objectID, connection_revision: revision, account_revision: revision, upstream_account_id: revision,
  start_at: timestamp, end_at: timestamp, started_at: timestamp, captured_at: timestamp, source_current: z.boolean(), connection_current: z.boolean(),
  collection_day: z.iso.date().optional(),
  state: z.enum(["exhausted", "partial", "failed"]), read, pages_read: z.number().int().nonnegative(), upstream_day_total: count.nullable(),
  scanned_count: count, observed_count: count, timestamp_basis: z.literal("native_created_at"), currency: z.literal("USD"),
  complete: z.literal(false), settlement_eligible: z.literal(false), limitations: z.array(reason).min(5).max(7),
}).strict().refine(v => !v.connection_current || v.source_current)
  .refine(v => v.collection_day === undefined ?
    countValue(v.observed_count) <= countValue(v.scanned_count) && countValue(v.scanned_count) <= 1000n && v.pages_read <= 10 :
    v.state === "exhausted" && v.start_at === v.collection_day + "T00:00:00Z" &&
    utcNanos(v.end_at)! - utcNanos(v.start_at)! === 86400000000000n && v.observed_count === v.scanned_count)
  .refine(v => ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "PARTICIPANT_MAPPING_UNAVAILABLE", "REQUEST_START_TIME_UNAVAILABLE"].every(r => v.limitations.includes(r)))
  .refine(v => new Set(v.limitations).size === v.limitations.length)
  .refine(v => v.state === "failed" ? v.pages_read === 0 && v.scanned_count === "0" && v.upstream_day_total === null
    : v.pages_read > 0 && (v.state !== "exhausted" || v.read.state === "available" && v.upstream_day_total === v.scanned_count));
const summary = z.object({ request_count: count, metrics }).strict();
const bucket = summary.extend({ start_at: timestamp, end_at: timestamp });
const paging = z.object({ after: z.union([z.literal("0"), revision]), limit: z.number().int().min(1).max(100) }).strict();
function utcNanos(value: string) {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value)) return null;
  const seconds = Date.parse(value.slice(0, 19) + "Z");
  if (!Number.isFinite(seconds)) return null;
  return BigInt(seconds) * 1000000n + BigInt((value[19] === "." ? value.slice(20, -1) : "").padEnd(9, "0"));
}
const timeRange = z.object({ start_at: timestamp, end_at: timestamp }).strict().refine(v => {
  const from = utcNanos(v.start_at), to = utcNanos(v.end_at);
  return from !== null && to !== null && v.start_at.slice(0, 4) >= "1970" && v.end_at.slice(0, 4) < "9999" && to > from && to - from <= 31n * 86400n * 1000000000n;
});
const usageDay = z.object({
  day: z.iso.date(), state: z.enum(["queued", "collecting", "partial", "waiting", "complete", "blocked"]),
  reason: z.string().max(64).regex(/^[A-Z_]+$/), records: count, total: count.nullable(),
  next_attempt_at: timestamp.nullable(), completed_at: timestamp.nullable(),
}).strict().refine(v => (v.total === null || countValue(v.records) <= countValue(v.total)) &&
  (v.state !== "complete" || v.records === v.total && v.completed_at !== null && v.next_attempt_at === null));
export const usageOutputs = {
  days: z.object({ days: z.array(usageDay).max(32) }).strict()
    .refine(v => v.days.every((item, i) => i === 0 || item.day > v.days[i - 1]!.day)),
  capture: usageCapture,
  captures: z.object({ captures: z.array(usageCapture).max(100), next_after: revision.nullable() }).strict()
    .refine(v => v.captures.every((c, i) => i === 0 || countValue(c.id) > countValue(v.captures[i - 1]!.id)))
    .refine(v => v.next_after === null || v.captures.at(-1)?.id === v.next_after),
  view: z.object({ capture: usageCapture, summary, grain: z.enum(["hour", "day"]), buckets: z.array(bucket).max(745), records: z.array(usageFact).max(100), next_after: revision.nullable() }).strict()
    .refine(v => v.records.every((f, i) => i === 0 || countValue(f.id) > countValue(v.records[i - 1]!.id)))
    .refine(v => v.next_after === null || v.records.at(-1)?.id === v.next_after)
    .refine(v => countValue(v.summary.request_count) <= countValue(v.capture.observed_count) && countValue(v.summary.request_count) >= BigInt(v.records.length))
    .refine(v => v.buckets.reduce((n, b) => n + countValue(b.request_count), 0n) === countValue(v.summary.request_count))
    .refine(v => v.records.every(f => utcNanos(f.created_at)! >= utcNanos(v.capture.start_at)! && utcNanos(f.created_at)! < utcNanos(v.capture.end_at)!))
    .refine(v => v.buckets.every((b, i) => countValue(b.request_count) > 0n && utcNanos(b.start_at)! >= utcNanos(v.capture.start_at)!
      && utcNanos(b.end_at)! <= utcNanos(v.capture.end_at)! && utcNanos(b.start_at)! < utcNanos(b.end_at)!
      && (i === 0 || utcNanos(v.buckets[i - 1]!.end_at)! <= utcNanos(b.start_at)!))),
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  CONNECTION_REVERIFICATION_REQUIRED: { status: 409, data: authErrorData }, SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
const captureInput = z.object({
  id: objectID, revision, source_id: objectID, connection_revision: revision,
  start_at: timestamp, end_at: timestamp,
}).strict().refine(v => timeRange.safeParse({ start_at: v.start_at, end_at: v.end_at }).success);
export const usageContract = {
  capture: base.route({ method: "POST", path: "/api/v1/usage/capture" }).input(captureInput).output(usageOutputs.capture),
  queueDays: base.route({ method: "POST", path: "/api/v1/usage/days/queue" }).input(captureInput).output(usageOutputs.days),
  dayStatus: base.route({ method: "POST", path: "/api/v1/usage/days/status" }).input(captureInput).output(usageOutputs.days),
  captures: base.route({ method: "POST", path: "/api/v1/usage/captures/list" }).input(paging.extend({ account_id: objectID })).output(usageOutputs.captures),
  view: base.route({ method: "POST", path: "/api/v1/usage/captures/get" }).input(paging.extend({
    id: revision, model: text, user_id: z.union([z.literal(""), revision]), api_key_id: z.union([z.literal(""), revision]), grain: z.enum(["hour", "day"]),
  })).output(usageOutputs.view),
};
export type UsageDay = z.infer<typeof usageDay>;
export type UsageCapture = z.infer<typeof usageCapture>;
export type UsageFact = z.infer<typeof usageFact>;
export type UsageView = z.infer<typeof usageOutputs.view>;
export { metrics as usageMetrics, summary as usageSummary, bucket as usageBucket, countValue as usageCountValue, utcNanos as usageNanos };
