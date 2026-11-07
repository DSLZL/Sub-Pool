import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { usageNanos } from "./usage";

export const notificationsVersion = "notifications-v1";
const timestamp = session.shape.created_at;
const count = z.union([z.literal("0"), revision]);
const emptyOrID = z.union([z.literal(""), objectID]);
const emptyOrTime = z.union([z.literal(""), timestamp]);
const address = z.string().max(254).regex(/^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$/).refine(v => !v.includes(".."));
const emptyOrAddress = z.union([z.literal(""), address]);
const smtp = z.object({ host: z.string().max(253), port: z.number().int().min(1).max(65535), tls: z.enum(["tls", "starttls", "loopback_plain"]), username: z.string().max(256).refine(v => !/[\r\n\0]/.test(v)) }).strict();
const preferences = z.object({ balance_low: z.boolean(), recommendation_changed: z.boolean(), estimate_changed: z.boolean(), collection_failed: z.boolean() }).strict();
const fields = { revision, enabled: z.boolean(), channel: z.enum(["smtp", "resend"]), from: emptyOrAddress, recipient: emptyOrAddress, smtp, preferences, dedupe_seconds: z.number().int().min(60).max(86400) };
const unavailable = <K extends string>(kind: K) => z.object({ kind: z.literal(kind), available: z.literal(false), effective: z.literal(false), reason: z.literal("FINANCIAL_BASIS_UNAVAILABLE") }).strict();
export const notificationConfiguration = z.object({
  ...fields, secret_set: z.boolean(), secret_storage_available: z.boolean(), saved_at: timestamp.nullable(),
  capabilities: z.tuple([unavailable("balance_low"), unavailable("recommendation_changed"), unavailable("estimate_changed"), z.object({ kind: z.literal("collection_failed"), available: z.literal(true), effective: z.boolean(), reason: z.literal("CONFIRMED_COLLECTION_FAILURES_ONLY") }).strict()]),
}).strict().refine(v => !v.capabilities[3].effective || v.enabled && v.preferences.collection_failed && !!v.from && !!v.recipient && (!v.secret_set || v.secret_storage_available));
export const notificationSaveInput = z.object({ ...fields, credential_action: z.enum(["keep", "replace", "clear"]), credential: z.string().max(8192).refine(v => !/[\r\n\0]/.test(v)) }).strict()
  .refine(v => v.credential_action === "replace" ? v.credential.length > 0 && (v.channel !== "resend" || /^[!-~]+$/.test(v.credential)) : v.credential === "");
const state = z.enum(["queued", "dispatching", "accepted", "failed", "unknown", "skipped"]);
const reason = z.enum(["QUEUED", "DISPATCHING", "TRANSPORT_ACCEPTED", "REMOTE_REJECTED", "TRANSPORT_UNAVAILABLE", "MESSAGE_INVALID", "TRANSPORT_OUTCOME_UNKNOWN", "REMOTE_PROTOCOL_UNKNOWN", "CHANNEL_INCOMPLETE", "CHANNEL_DISABLED", "EVENT_DISABLED", "DUPLICATE_WINDOW", "CONFIGURATION_CHANGED", "SUBJECT_CHANGED", "SECRET_STORAGE_UNAVAILABLE", "ACTOR_UNAVAILABLE", "WORKER_INTERRUPTED", "ACCEPTED", "FAILED", "UNKNOWN", "SKIPPED"]);
const eventReason = z.enum(["EXPLICIT_ADMINISTRATOR_TEST", "COLLECTION_READ_FAILED", "CONNECTION_UNAVAILABLE", "UPSTREAM_UNAUTHORIZED", "UPSTREAM_FORBIDDEN", "UPSTREAM_RATE_LIMITED", "UPSTREAM_UNAVAILABLE", "UPSTREAM_TIMEOUT", "INVALID_RESPONSE"]);
const details = z.object({ config_revision: revision, channel: z.enum(["smtp", "resend"]), from: emptyOrAddress, recipient: emptyOrAddress, subject: z.string().max(128), body: z.string().max(2048), event_reason: eventReason, event_key: z.string().regex(/^(test:[A-Za-z0-9_-]{22}|collection:[1-9]\d{0,18})$/), actor_id: objectID.nullable(), provider_id: z.string().regex(/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/).nullable() }).strict();
export const notification = z.object({
  id: revision, kind: z.enum(["test", "collection_failed"]), subject_type: z.enum(["system", "account"]), subject_id: z.union([objectID, z.literal("test")]), participant_id: objectID.nullable(),
  created_at: timestamp, started_at: timestamp.nullable(), finished_at: timestamp.nullable(), state, reason,
  visibility: z.enum(["administrator", "subject_summary"]), details: details.nullable(),
}).strict().refine(v => {
  if ((v.visibility === "administrator") !== (v.details !== null)) return false;
  if (v.kind === "test" ? v.subject_type !== "system" || v.subject_id !== "test" : v.subject_type !== "account" || !objectID.safeParse(v.subject_id).success) return false;
  if (v.visibility === "subject_summary" && (v.kind !== "collection_failed" || v.reason !== v.state.toUpperCase())) return false;
  if (v.visibility === "administrator") {
    const allowed: Record<z.infer<typeof state>, string[]> = { queued: ["QUEUED"], dispatching: ["DISPATCHING"], accepted: ["TRANSPORT_ACCEPTED"], failed: ["REMOTE_REJECTED", "TRANSPORT_UNAVAILABLE", "MESSAGE_INVALID"], unknown: ["TRANSPORT_OUTCOME_UNKNOWN", "REMOTE_PROTOCOL_UNKNOWN", "WORKER_INTERRUPTED"], skipped: ["CHANNEL_INCOMPLETE", "CHANNEL_DISABLED", "EVENT_DISABLED", "DUPLICATE_WINDOW", "CONFIGURATION_CHANGED", "SUBJECT_CHANGED", "SECRET_STORAGE_UNAVAILABLE", "ACTOR_UNAVAILABLE"] };
    if (!allowed[v.state].includes(v.reason)) return false;
    if (v.state === "accepted" && v.details?.channel === "resend" && v.details.provider_id === null) return false;
  }
  if (v.details && ((v.kind === "test") !== (v.details.actor_id !== null) || (v.kind === "test") !== v.details.event_key.startsWith("test:"))) return false;
  const at = usageNanos(v.created_at), start = v.started_at === null ? null : usageNanos(v.started_at), end = v.finished_at === null ? null : usageNanos(v.finished_at);
  if (at === null || start !== null && start < at || end !== null && (end < at || start !== null && end < start)) return false;
  return v.state === "queued" ? start === null && end === null : v.state === "dispatching" ? start !== null && end === null : end !== null && (v.state !== "accepted" || start !== null);
});
export const notificationQuery = z.object({ start_at: emptyOrTime, end_at: emptyOrTime, kind: z.enum(["", "test", "collection_failed"]), participant_id: emptyOrID, subject_type: z.enum(["", "system", "account", "participant"]), subject_id: z.union([emptyOrID, z.literal("test")]), state: z.union([z.literal(""), state]), after: count, limit: z.number().int().min(1).max(100) }).strict().refine(v => {
  if (v.subject_id && (!v.subject_type || v.subject_id === "test" && v.subject_type !== "system")) return false;
  if (!v.start_at || !v.end_at) return v.start_at === v.end_at;
  const start = usageNanos(v.start_at), end = usageNanos(v.end_at);
  return start !== null && end !== null && end > start && end - start <= 31n * 86400n * 1000000000n;
});
const page = z.object({ notifications: z.array(notification).max(100), next_after: revision.nullable() }).strict().refine(v => v.notifications.every((row, index) => { const previous = v.notifications[index - 1]; return previous === undefined || BigInt(row.id) > BigInt(previous.id) && row.visibility === previous.visibility; }) && (v.next_after === null || v.notifications.at(-1)?.id === v.next_after));
const base = oc.errors({ BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData }, NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData } });
export const notificationOutputs = { configuration: notificationConfiguration, test: notification, get: notification, list: page };
export const notificationsContract = {
  configuration: base.route({ method: "POST", path: "/api/v1/notifications/config" }).input(z.object({}).strict()).output(notificationConfiguration),
  save: base.route({ method: "POST", path: "/api/v1/notifications/config/update" }).input(notificationSaveInput).output(notificationConfiguration),
  test: base.route({ method: "POST", path: "/api/v1/notifications/test" }).input(z.object({ request_id: objectID, revision }).strict()).output(notification),
  get: base.route({ method: "POST", path: "/api/v1/notifications/get" }).input(z.object({ id: revision }).strict()).output(notification),
  list: base.route({ method: "POST", path: "/api/v1/notifications/list" }).input(notificationQuery).output(page),
};
export type Notification = z.infer<typeof notification>;
export type NotificationConfiguration = z.infer<typeof notificationConfiguration>;
