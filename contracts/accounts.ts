import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { capabilityCheck, discoveredAccount } from "./connection";
import { objectID, revision } from "./users";

export const accountsVersion = "accounts-v1";
const timestamp = session.shape.created_at;
const name = z.string().min(1).max(128).refine(value => value.trim() === value && !/\p{Cc}/u.test(value));
const sourceMode = z.enum(["active", "passive"]);
const identityState = z.enum(["unverified", "verified", "shadow", "unsupported", "conflict"]);
export const runtime = z.object({
  last_used_at: timestamp.nullable(), rate_limited_at: timestamp.nullable(), rate_limit_reset_at: timestamp.nullable(),
  overload_until: timestamp.nullable(), temp_unschedulable_until: timestamp.nullable(), error_present: z.boolean().nullable(),
}).strict();
export const managedAccount = z.object({
  id: objectID, revision, source_id: objectID, upstream_id: revision, name,
  monitoring: z.boolean(), source_mode: sourceMode, source_current: z.boolean(), identity_state: identityState,
  connection_available: z.boolean(), observed_connection_revision: revision,
  collection_state: z.enum(["paused", "source_changed", "connection_unavailable", "connection_changed", "read_unavailable", "unverified", "shadow", "unsupported", "conflict", "awaiting_collector"]),
  snapshot: z.object({ account: discoveredAccount, runtime }).strict(), read: capabilityCheck,
  last_attempt_at: timestamp, observed_at: timestamp, created_at: timestamp, saved_at: timestamp,
}).strict().refine(value => value.read.id === "accounts.read" && value.snapshot.account.upstream_id === value.upstream_id
  && (value.collection_state === (!value.monitoring ? "paused" : !value.source_current ? "source_changed"
    : !value.connection_available ? "connection_unavailable" : value.read.state !== "available" ? "read_unavailable"
      : value.identity_state === "verified" ? "awaiting_collector" : value.identity_state)
    || value.monitoring && value.source_current && value.connection_available && value.read.state === "available" && value.collection_state === "connection_changed"));
function currentObservation(value: z.infer<typeof managedAccount>, currentRevision: string) {
  return !value.monitoring || !value.source_current || !value.connection_available || value.read.state !== "available"
    || (value.collection_state === "connection_changed") === (value.observed_connection_revision !== currentRevision);
}
export const managedView = z.object({ account: managedAccount, connection_revision: revision }).strict()
  .refine(value => currentObservation(value.account, value.connection_revision));
export const managedPage = z.object({
  accounts: z.array(managedAccount).max(100).refine(values => new Set(values.map(value => value.id)).size === values.length),
  next_after: objectID.nullable(), connection_revision: revision,
}).strict().refine(value => (value.next_after === null || value.accounts.at(-1)?.id === value.next_after)
  && value.accounts.every(account => currentObservation(account, value.connection_revision)));
export const adoption = z.object({
  account: managedAccount.nullable(), read: capabilityCheck, connection_revision: revision,
}).strict().refine(value => value.read.id === "accounts.read" && (value.account !== null) === (value.read.state === "available")
  && (value.account === null || currentObservation(value.account, value.connection_revision)));
export const accountModels = z.object({
  account_id: objectID, source_id: objectID, connection_revision: revision, checked_at: timestamp,
  models: z.array(z.object({ id: z.string().min(1).max(256), name: z.string().min(1).max(512) }).strict()).max(1000)
    .refine(models => new Set(models.map(model => model.id)).size === models.length),
  mapping: z.array(z.object({ from: z.string().min(1).max(256), to: z.string().min(1).max(256) }).strict()).max(1000)
    .refine(mapping => new Set(mapping.map(value => value.from)).size === mapping.length).nullable(),
  passthrough: z.boolean().nullable(), mapping_mode: z.enum(["unknown", "empty", "configured", "passthrough"]), access_proven: z.literal(false),
  account_read: capabilityCheck, catalog_read: capabilityCheck,
}).strict().refine(value => value.account_read.id === "accounts.read" && value.catalog_read.id === "accounts.read"
  && (value.catalog_read.state === "available" || value.models.length === 0)
  && (value.account_read.state === "available" || value.mapping === null && value.passthrough === null)
  && value.mapping_mode === (value.passthrough === true ? "passthrough"
    : value.mapping !== null && value.mapping.length === 0 ? "empty" : value.mapping?.length ? "configured" : "unknown"));

const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, NOT_FOUND: { status: 404, data: authErrorData },
  SOURCE_BLOCKED: { status: 403, data: authErrorData },
  CONFLICT: { status: 409, data: authErrorData }, CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
});
const readInput = z.object({ id: objectID, revision, source_id: objectID, connection_revision: revision }).strict();
export const accountsContract = {
  list: base.route({ method: "POST", path: "/api/v1/accounts/list" })
    .input(z.object({ after: z.union([objectID, z.literal("")]), search: z.string().max(256), limit: z.number().int().min(1).max(100) }).strict()).output(managedPage),
  get: base.route({ method: "POST", path: "/api/v1/accounts/get" }).input(z.object({ id: objectID }).strict()).output(managedView),
  adopt: base.route({ method: "POST", path: "/api/v1/accounts/adopt" })
    .input(z.object({ source_id: objectID, connection_revision: revision, upstream_id: revision, name, monitoring: z.boolean(), source_mode: sourceMode }).strict()).output(adoption),
  update: base.route({ method: "POST", path: "/api/v1/accounts/update" })
    .input(z.object({ id: objectID, revision, name, monitoring: z.boolean(), source_mode: sourceMode }).strict()).output(managedView),
  refresh: base.route({ method: "POST", path: "/api/v1/accounts/refresh" }).input(readInput).output(managedView),
  models: base.route({ method: "POST", path: "/api/v1/accounts/models" }).input(readInput).output(accountModels),
};

export type ManagedAccount = z.infer<typeof managedAccount>;
export type ManagedAccountView = z.infer<typeof managedView>;
export type ManagedAccountPage = z.infer<typeof managedPage>;
export type AccountModelConfiguration = z.infer<typeof accountModels>;
