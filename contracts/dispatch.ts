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
export const dispatchContract = {
  list: base.route({ method: "POST", path: "/api/v1/dispatch/list" }).input(dispatchListInput).output(dispatchAccounts),
  account: base.route({ method: "POST", path: "/api/v1/dispatch/account" }).input(dispatchAccountInput).output(dispatchObservation),
};
export type DispatchAccounts = z.infer<typeof dispatchAccounts>;
export type DispatchObservation = z.infer<typeof dispatchObservation>;
