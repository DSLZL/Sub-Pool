import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";

export const connectionVersion = "connection-v1";
const timestamp = session.shape.created_at;
const baseURL = z.string().min(1).max(2048).url();
const credential = z.string().max(8192);
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
    "MODE_REQUIRES_JWT", "NO_VISIBLE_RESOURCE", "NO_VERIFIED_ADMIN_READ", "ACCOUNT_CHECK_REQUIRED", "WRITE_NOT_PROBED", "PRICING_CHECK_REQUIRED",
  ]),
  http_status: z.number().int().min(100).max(599).nullable(),
  latency_ms: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER).nullable(),
}).strict().refine(value => !["wallet.write", "scheduling.write", "pricing.write"].includes(value.id)
  || value.state === "unverified" && value.reason === "WRITE_NOT_PROBED");
export const diagnostic = z.object({
  checked_at: timestamp, revision, source_id: objectID.nullable(), saved: z.boolean(),
  version: z.string().min(1).max(128).nullable(), run_mode: z.enum(["standard", "simple"]).nullable(),
  checks: z.array(capabilityCheck).length(12).refine(checks => new Set(checks.map(check => check.id)).size === 12),
}).strict().refine(value => (value.version !== null) === (value.checks.find(check => check.id === "version.read")?.state === "available")
  && (value.run_mode !== null) === (value.checks.find(check => check.id === "run_mode")?.state === "available")
  && (!value.saved || value.source_id !== null));
export const configuration = z.object({
  revision, source_id: objectID.nullable(), base_url: z.union([z.literal(""), baseURL]),
  credential_set: z.boolean(), credential_kind: z.enum(["api_key", "bearer"]).nullable(),
  secret_storage_available: z.boolean(), saved_at: timestamp.nullable(), diagnostic: diagnostic.nullable(),
}).strict().refine(value => (value.source_id === null) === (value.base_url === "")
  && value.credential_set === (value.credential_kind !== null)
  && (!value.credential_set || value.source_id !== null)
  && (!value.diagnostic || value.diagnostic.saved && value.diagnostic.revision === value.revision && value.diagnostic.source_id === value.source_id));
export const settings = z.object({ revision, timezone: z.string().min(1).max(64), saved_at: timestamp.nullable() }).strict();
const positiveID = z.string().regex(/^[1-9]\d{0,18}$/).refine(value => BigInt(value) <= 9223372036854775807n);
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
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData },
  UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData },
  CONFLICT: { status: 409, data: authErrorData },
  CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData },
});
export const connectionContract = {
  get: base.route({ method: "GET", path: "/api/v1/connection" }).input(z.object({}).strict()).output(configuration),
  save: base.route({ method: "POST", path: "/api/v1/connection/save" })
    .input(z.object({ revision, base_url: baseURL, credential_action: z.enum(["keep", "replace", "clear"]),
      credential, confirmation: z.union([z.literal(""), z.literal("更换地址")]) }).strict()
      .refine(value => value.credential_action === "replace" ? value.credential.length > 0 : value.credential === ""))
    .output(z.object({ configuration, source_changed: z.boolean() }).strict()),
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
export type ConnectionDiagnostic = z.infer<typeof diagnostic>;
export type CapabilityCheck = z.infer<typeof capabilityCheck>;
export type DiscoveredAccount = z.infer<typeof discoveredAccount>;
export type Discovery = z.infer<typeof discovery>;
export type SystemSettings = z.infer<typeof settings>;
