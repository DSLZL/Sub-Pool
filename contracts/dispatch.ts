import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { capabilityCheck } from "./connection";
import { objectID, revision } from "./users";

export const dispatchVersion = "dispatch-v1";
const text = (limit: number) => z.string().min(1).max(limit).refine(value => new TextEncoder().encode(value).length <= limit && !/[\p{Cc}]/u.test(value));
const capability = {
  can_pause: z.literal(false), can_restore: z.literal(false), automatic_restore_available: z.literal(false),
  history_state: z.literal("unavailable"), reason: z.literal("SCHEDULING_CONTROL_UNQUALIFIED"),
};
const binding = { id: objectID, revision, source_id: objectID, connection_revision: revision };
export const dispatchListInput = z.object({ after: z.union([z.literal(""), objectID]), search: z.union([z.literal(""), text(256)]), limit: z.number().int().min(1).max(100) }).strict();
export const dispatchAccountInput = z.object(binding).strict();
export const dispatchAccounts = z.object({
  ...capability, connection_revision: revision, authority_revision: revision, next_after: objectID.nullable(),
  accounts: z.array(z.object({ id: objectID, revision, name: text(512), source_id: objectID, source_current: z.boolean() }).strict()).max(100),
}).strict().refine(value => new Set(value.accounts.map(account => account.id)).size === value.accounts.length
  && (value.next_after === null || value.next_after === value.accounts.at(-1)?.id));
const check = z.object({
  ...capabilityCheck.shape, id: z.literal("dispatch.account.read"),
  reason: z.union([capabilityCheck.shape.reason, z.literal("ACCOUNT_IDENTITY_CHANGED"), z.literal("RESOURCE_IDENTITY_UNVERIFIED")]),
}).strict();
export const dispatchObservation = z.object({
  ...capability, ...binding, authority_revision: revision, checked_at: session.shape.created_at,
  configured_schedulable: z.boolean().nullable(), native_status: text(64).nullable(), read: check,
}).strict().refine(value => value.read.state === "available"
  ? value.read.reason === "VERIFIED_READ" && value.configured_schedulable !== null && value.native_status !== null
  : value.configured_schedulable === null && value.native_status === null);
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData }, SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
});
export const dispatchControlVersion = "dispatch-control-v1";
const timestamp = session.shape.created_at;
export const dispatchControl = z.object({
  version: z.literal(dispatchControlVersion), source_id: objectID.nullable(), connection_revision: revision,
  can_pause: z.boolean(), can_restore: z.boolean(), automatic_restore_available: z.boolean(),
  reason: z.enum(["CONTROL_BINDING_REQUIRED", "CONTROLLED_ENVIRONMENT", "ADMIN_REQUIRED"]),
}).strict().refine(value => value.can_pause === value.can_restore
  && (value.reason === "CONTROLLED_ENVIRONMENT"
    ? value.can_pause && value.automatic_restore_available && value.source_id !== null
    : !value.can_pause && (value.reason === "ADMIN_REQUIRED" ? value.automatic_restore_available : !value.automatic_restore_available)));
const pauseReasons = {
  pausing: ["PAUSE_PENDING"],
  paused: ["VERIFIED_PAUSE", "PREVIOUSLY_DISABLED", "DEADLINE_CHANGED", "READ_RETRY_PENDING"],
  restoring: ["RESTORE_PENDING"],
  restored: ["VERIFIED_RESTORE", "PREVIOUS_STATE_RETAINED"],
  failed: ["DEADLINE_PASSED"],
  needs_review: ["CONTROL_BINDING_REQUIRED", "LOCAL_ACCOUNT_CHANGED", "CONNECTION_UNAVAILABLE", "NATIVE_AUTHORITY_UNVERIFIED",
    "NATIVE_READ_UNAVAILABLE", "NATIVE_ACCOUNT_UNVERIFIED", "NATIVE_ACCOUNT_CHANGED", "WRITE_RESULT_UNKNOWN", "PROCESS_INTERRUPTED_RESULT_UNKNOWN"],
  reviewed: ["MANUALLY_REVIEWED"],
};
export const dispatchPause = z.object({
  id: revision, revision, account_id: objectID, source_id: objectID, connection_revision: revision, account_revision: revision,
  state: z.enum(["pausing", "paused", "restoring", "restored", "failed", "needs_review", "reviewed"]), reason: z.string(),
  previous_schedulable: z.boolean(), native_updated_at: timestamp, restore_at: timestamp, created_at: timestamp, updated_at: timestamp,
}).strict().refine(value => pauseReasons[value.state].includes(value.reason) && Date.parse(value.updated_at) >= Date.parse(value.created_at));
export const dispatchHistory = z.object({
	  authority_revision: revision,
  events: z.array(z.object({ id: revision, actor: z.enum(["administrator", "worker"]),
    action: z.enum(["pause", "pause_result", "reschedule", "restore", "restore_result", "review", "interrupted", "retry"]),
    occurred_at: timestamp, pause: dispatchPause }).strict()).max(100), next_after: revision.nullable(),
}).strict().refine(value => new Set(value.events.map(row => row.id)).size === value.events.length
  && (value.next_after === null || value.events.at(-1)?.id === value.next_after));
const changeInput = z.object({ id: revision, revision }).strict();
export const dispatchControlOutputs = {
  "/api/v1/dispatch/control": dispatchControl, "/api/v1/dispatch/pause": dispatchPause,
  "/api/v1/dispatch/get": dispatchPause, "/api/v1/dispatch/history": dispatchHistory,
  "/api/v1/dispatch/reschedule": dispatchPause, "/api/v1/dispatch/restore": dispatchPause, "/api/v1/dispatch/review": dispatchPause,
};
export const dispatchContract = {
  control: base.route({ method: "POST", path: "/api/v1/dispatch/control" }).input(z.object({}).strict()).output(dispatchControl),
  pause: base.route({ method: "POST", path: "/api/v1/dispatch/pause" })
    .input(z.object({ ...binding, request_id: objectID, restore_at: timestamp, acknowledge_account_wide: z.literal(true) }).strict()).output(dispatchPause),
  get: base.route({ method: "POST", path: "/api/v1/dispatch/get" }).input(z.object({ id: revision }).strict()).output(dispatchPause),
  history: base.route({ method: "POST", path: "/api/v1/dispatch/history" })
    .input(z.object({ account_id: z.union([z.literal(""), objectID]), after: z.union([z.literal(""), revision]), limit: z.number().int().min(1).max(100) }).strict()).output(dispatchHistory),
  reschedule: base.route({ method: "POST", path: "/api/v1/dispatch/reschedule" })
    .input(changeInput.extend({ restore_at: timestamp }).strict()).output(dispatchPause),
  restore: base.route({ method: "POST", path: "/api/v1/dispatch/restore" }).input(changeInput).output(dispatchPause),
  review: base.route({ method: "POST", path: "/api/v1/dispatch/review" })
    .input(changeInput.extend({ acknowledge_result_unknown: z.literal(true) }).strict()).output(dispatchPause),
  list: base.route({ method: "POST", path: "/api/v1/dispatch/list" }).input(dispatchListInput).output(dispatchAccounts),
  account: base.route({ method: "POST", path: "/api/v1/dispatch/account" }).input(dispatchAccountInput).output(dispatchObservation),
};
export type DispatchAccounts = z.infer<typeof dispatchAccounts>;
export type DispatchObservation = z.infer<typeof dispatchObservation>;
export type DispatchControl = z.infer<typeof dispatchControl>;
export type DispatchPause = z.infer<typeof dispatchPause>;
export type DispatchHistory = z.infer<typeof dispatchHistory>;
export type DispatchEvent = DispatchHistory["events"][number];
