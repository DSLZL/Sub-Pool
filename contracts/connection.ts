import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";

export const connectionVersion = "connection-v2";
const timestamp = session.shape.created_at;
const baseURL = z.string().min(1).max(2048).url();
const credential = z.string().max(8192);
const positiveID = z.string().regex(/^[1-9]\d{0,18}$/).refine(value => BigInt(value) <= 9223372036854775807n);
const capabilityID = z.enum([
  "health", "version.read", "accounts.read", "usage.read", "wallet.read", "pricing.read", "run_mode",
  "management_auth", "quota.read", "wallet.write", "scheduling.write", "pricing.write",
]);
export const capabilityCheck = z.object({
  id: capabilityID,
  state: z.enum(["available", "denied", "unsupported", "unverified", "failed"]),
  reason: z.enum([
    "NOT_CONFIGURED", "VERIFIED_READ", "INVALID_RESPONSE", "NETWORK_ERROR", "TIMEOUT", "REDIRECT_REFUSED",
    "UPSTREAM_ERROR", "UNAUTHORIZED", "FORBIDDEN", "COMPLIANCE_REQUIRED", "ROUTE_UNAVAILABLE", "RATE_LIMITED",
    "MODE_REQUIRES_JWT", "VERSION_UNVERIFIED", "NO_VISIBLE_RESOURCE", "NO_VERIFIED_ADMIN_READ", "ACCOUNT_CHECK_REQUIRED", "WRITE_NOT_PROBED", "PRICING_CHECK_REQUIRED",
    "PRICING_FIELDS_UNREPORTED", "VERIFIED_PASSIVE_QUOTA", "QUOTA_IDENTITY_UNVERIFIED", "QUOTA_WINDOW_UNVERIFIED", "QUOTA_SNAPSHOT_STALE",
    "ATOMIC_BALANCE_REQUIRED", "STANDARD_MODE_REQUIRED", "CONTROL_BINDING_REQUIRED", "WRITE_EVIDENCE_REQUIRED", "VERIFIED_WRITE",
  ]),
  http_status: z.number().int().min(100).max(599).nullable(),
  latency_ms: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER).nullable(),
  evidence: z.object({ upstream_id: positiveID, verified_at: timestamp, account_revision: revision.optional() }).strict().optional(),
}).strict().refine(({ id, state, reason, evidence, http_status, latency_ms }) => {
  if (reason === "VERSION_UNVERIFIED") return id === "version.read" && state === "available" && evidence === undefined;
  const controlledWrite = id === "wallet.write" || id === "scheduling.write" || id === "pricing.write";
  if (reason === "VERIFIED_WRITE") return controlledWrite && state === "available" && !!evidence
    && evidence.account_revision === undefined && http_status === null && latency_ms === null;
  if (reason === "VERIFIED_PASSIVE_QUOTA") return id === "quota.read" && state === "available" && !!evidence?.account_revision;
  if (evidence) return id === "pricing.read" && state === "available" && reason === "VERIFIED_READ" && evidence.account_revision === undefined;
  if (reason === "ATOMIC_BALANCE_REQUIRED") return id === "wallet.write" && state === "unverified";
  if (controlledWrite) return state === "unverified" && [
    "WRITE_NOT_PROBED", "MODE_REQUIRES_JWT", "STANDARD_MODE_REQUIRED", "CONTROL_BINDING_REQUIRED", "WRITE_EVIDENCE_REQUIRED",
  ].includes(reason);
  return true;
});
export const diagnostic = z.object({
  checked_at: timestamp, revision, source_id: objectID.nullable(), saved: z.boolean(),
  version: z.string().min(1).max(128).nullable(), version_since: timestamp.optional(), run_mode: z.enum(["standard", "simple"]).nullable(),
  checks: z.array(capabilityCheck).length(12).refine(checks => new Set(checks.map(check => check.id)).size === 12),
}).strict().refine(value => (value.version !== null) === (value.checks.find(check => check.id === "version.read")?.state === "available")
  && (value.run_mode !== null) === (value.checks.find(check => check.id === "run_mode")?.state === "available")
  && (!value.saved || value.source_id !== null)
  && (!value.version_since || value.version !== null)
  && value.checks.every(check => {
    if (check.reason === "VERIFIED_WRITE") return value.saved && value.version_since !== undefined && value.run_mode === "standard";
    if (check.id === "quota.read" && check.state === "available") return value.saved && check.reason === "VERIFIED_PASSIVE_QUOTA";
    if (check.id === "pricing.read" && check.state === "available") return check.evidence !== undefined;
    return true;
  }));
export const configuration = z.object({
  revision, source_id: objectID.nullable(), base_url: z.union([z.literal(""), baseURL]),
  credential_set: z.boolean(), credential_kind: z.enum(["api_key", "bearer"]).nullable(),
  secret_storage_available: z.boolean(), saved_at: timestamp.nullable(), diagnostic: diagnostic.nullable(),
}).strict().refine(value => (value.source_id === null) === (value.base_url === "")
  && value.credential_set === (value.credential_kind !== null)
  && (!value.credential_set || value.source_id !== null)
  && (!value.diagnostic || value.diagnostic.saved && value.diagnostic.revision === value.revision && value.diagnostic.source_id === value.source_id));
export const settings = z.object({ revision, timezone: z.string().min(1).max(64), saved_at: timestamp.nullable() }).strict();
const count = z.string().regex(/^(0|[1-9]\d{0,18})$/).pipe(z.string().refine(value => BigInt(value) <= 9223372036854775807n));
export const connectionImpact = z.object({
  revision, source_id: objectID.nullable(), checked_at: timestamp,
  managed_accounts: count, participant_bindings: count, dispatch_open: count, pricing_open: count,
  wallet_rejected: count, wallet_unconfirmed: count,
}).strict().refine(value => value.source_id !== null || [value.managed_accounts, value.participant_bindings,
  value.dispatch_open, value.pricing_open, value.wallet_rejected, value.wallet_unconfirmed].every(count => count === "0"));
export const discoveredAccount = z.object({
  upstream_id: positiveID, name: z.string().min(1).max(512), platform: z.string().min(1).max(64),
  type: z.string().min(1).max(64), status: z.string().min(1).max(64), schedulable: z.boolean(),
  eligibility: z.enum(["unsupported", "unverified"]),
  reason: z.enum(["NOT_OPENAI_OAUTH", "RESOURCE_IDENTITY_UNVERIFIED"]),
}).strict();
export const discovery = z.object({
  revision, source_id: objectID, page: z.number().int().min(1).max(2147483647),
  total: z.string().regex(/^(0|[1-9]\d*)$/).refine(value => BigInt(value) <= 214748364700n).nullable(),
  next_page: z.number().int().min(2).max(2147483647).nullable(),
  accounts: z.array(discoveredAccount).max(100).refine(accounts => new Set(accounts.map(account => account.upstream_id)).size === accounts.length),
  read: capabilityCheck,
}).strict().refine(value => value.read.id === "accounts.read"
  && (value.read.state === "available"
    ? value.total !== null && (value.next_page === null || value.next_page === value.page + 1)
    : value.total === null && value.next_page === null && value.accounts.length === 0));
const writeCapability = z.enum(["pricing", "scheduling", "wallet"]);
const confirmationSHA256 = z.string().regex(/^[0-9a-f]{64}$/);
const writeGrant = z.object({
  id: revision, granted_by: objectID, granted_at: timestamp, confirmation_sha256: confirmationSHA256,
  revoked_by: objectID.nullable(), revoked_at: timestamp.nullable(),
}).strict().refine(value => (value.revoked_by === null) === (value.revoked_at === null));
const writeGrants = z.object({
  source_id: objectID.nullable(), connection_revision: revision, upstream_writes_disabled: z.boolean(),
  confirmation_text: z.string().min(1).max(1024), confirmation_sha256: confirmationSHA256,
  grants: z.array(z.object({ capability: writeCapability, grant: writeGrant.nullable() }).strict()).length(3),
}).strict().refine(value => new Set(value.grants.map(status => status.capability)).size === 3 &&
  (value.source_id !== null || value.grants.every(status => status.grant === null)));
export const writeGrantUpdate = z.object({
  source_id: objectID, connection_revision: revision, capability: writeCapability,
  expected_grant_id: z.union([z.literal("0"), revision]), enabled: z.boolean(), confirmation_sha256: confirmationSHA256,
  current_password: z.string().min(1).max(128).refine(value => new TextEncoder().encode(value).length <= 128),
}).strict();
export const connectionOutputs = {
  get: configuration, impact: connectionImpact, save: z.object({ configuration, source_changed: z.boolean() }).strict(),
  test: diagnostic, discover: discovery, settings, updateSettings: settings, writeGrants,
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData },
  UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData },
  SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData },
  CONFLICT: { status: 409, data: authErrorData },
  CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  CONNECTION_REVERIFICATION_REQUIRED: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
});
export const connectionContract = {
  get: base.route({ method: "GET", path: "/api/v1/connection" }).input(z.object({}).strict()).output(configuration),
  impact: base.route({ method: "GET", path: "/api/v1/connection/impact" }).input(z.object({}).strict()).output(connectionImpact),
  writeGrants: base.route({ method: "GET", path: "/api/v1/connection/write-grants" }).input(z.object({}).strict()).output(writeGrants),
  updateWriteGrant: base.errors({ CURRENT_PASSWORD_INVALID: { status: 403, data: authErrorData } })
    .route({ method: "POST", path: "/api/v1/connection/write-grants/update" }).input(writeGrantUpdate).output(writeGrants),
  save: base.route({ method: "POST", path: "/api/v1/connection/save" })
    .input(z.object({ revision, base_url: baseURL, credential_action: z.enum(["keep", "replace", "clear"]),
      credential, confirmation: z.union([z.literal(""), z.literal("更换地址")]) }).strict()
      .refine(value => value.credential_action === "replace" ? value.credential.length > 0 : value.credential === ""))
    .output(connectionOutputs.save),
  test: base.route({ method: "POST", path: "/api/v1/connection/test" })
    .input(z.object({ revision, base_url: baseURL, credential }).strict()).output(diagnostic),
  discover: base.route({ method: "POST", path: "/api/v1/connection/discover" })
    .input(z.object({ revision, source_id: objectID, page: z.number().int().min(1).max(2147483647), search: z.string().max(256) }).strict())
    .output(discovery),
  settings: base.route({ method: "GET", path: "/api/v1/settings" }).input(z.object({}).strict()).output(settings),
  updateSettings: base.route({ method: "POST", path: "/api/v1/settings/update" })
    .input(z.object({ revision, timezone: z.string().min(1).max(64) }).strict()).output(settings),
};

export type ConnectionConfiguration = z.infer<typeof configuration>;
export type ConnectionImpact = z.infer<typeof connectionImpact>;
export type ConnectionWriteGrants = z.infer<typeof writeGrants>;
export type ConnectionWriteCapability = z.infer<typeof writeCapability>;
export type ConnectionDiagnostic = z.infer<typeof diagnostic>;
export type CapabilityCheck = z.infer<typeof capabilityCheck>;
export type DiscoveredAccount = z.infer<typeof discoveredAccount>;
export type Discovery = z.infer<typeof discovery>;
export type SystemSettings = z.infer<typeof settings>;
