import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { access, objectID, readFeature, revision } from "./users";
import { participantsVersion } from "./participants";
import { walletsVersion } from "./wallets";
import { walletApplicationsVersion } from "./wallet-applications";

export const apiKeysVersion = "api-keys-v1";
export const apiKeyMode = z.enum(["read", "management"]);
const timestamp = session.shape.created_at;
const name = z.string().min(1).max(256).refine(value => value === value.trim()
  && [...value].length <= 80 && new TextEncoder().encode(value).length <= 256
  && !/[\p{Cc}\uD800-\uDFFF]/u.test(value));
export const apiKey = z.object({
  id: objectID, name, mode: apiKeyMode, prefix: z.string(), revision,
  created_at: timestamp, rotated_at: timestamp.nullable(),
  last_used_at: timestamp.nullable(), revoked_at: timestamp.nullable(),
}).strict().refine(value => value.prefix === `spk_live_${value.id}`);
const secret = z.string().regex(/^spk_live_[A-Za-z0-9_-]{21}[AQgw]_[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/);
export const apiKeyIssuance = z.object({ key: apiKey, secret }).strict()
  .refine(value => value.secret.startsWith(`${value.key.prefix}_`) && value.key.revoked_at === null);
export const apiKeyPage = z.object({
  keys: z.array(apiKey).max(100), next_after: objectID.nullable(),
}).strict().refine(value => new Set(value.keys.map(row => row.id)).size === value.keys.length
  && (value.next_after === null || value.keys.at(-1)?.id === value.next_after));
export const apiOperation = z.object({
  name: z.string().regex(/^[A-Za-z]+\.[A-Za-z]+$/),
  path: z.string().max(128).regex(/^\/api\/v1\/[a-z0-9/-]+$/),
  method: z.enum(["GET", "POST"]), feature: z.union([z.literal(""), readFeature]),
  contract: z.enum(["api-keys-v1", "accounts-v1", "monitoring-v1", participantsVersion, "pools-v1",
    "observations-v1", "estimates-v1", walletsVersion, walletApplicationsVersion, "notifications-v1"]),
  management: z.boolean(), availability: z.enum(["available", "not_dispatched_only", "protocol_simulation_only"]),
}).strict().refine(value => value.management === (value.availability !== "available"));
const limitations = ["RETAINED_LOCAL_FACTS_ONLY", "FULL_USAGE_COVERAGE_UNVERIFIED", "FINANCIAL_APPLICATION_UNAVAILABLE",
  "NO_COMBINED_OVERVIEW_ENDPOINT", "MUTATION_RESPONSE_LOSS_REQUIRES_READBACK"] as const;
const errorStatus = { BAD_REQUEST: 400, UNAUTHORIZED: 401, FORBIDDEN: 403, SOURCE_BLOCKED: 403,
  NOT_FOUND: 404, METHOD_NOT_SUPPORTED: 405, CONFLICT: 409, CONNECTION_NOT_CONFIGURED: 409,
  TOO_MANY_REQUESTS: 429, INTERNAL_SERVER_ERROR: 500 } as const;
export const apiError = z.object({ code: z.enum(Object.keys(errorStatus) as (keyof typeof errorStatus)[]),
  status: z.number().int(), meaning: z.string().min(1).max(256),
}).strict().refine(value => value.status === errorStatus[value.code]);
export const apiCatalog = z.object({
  version: z.literal(apiKeysVersion), access, credential_mode: z.enum(["session", "read", "management"]),
  operations: z.array(apiOperation).min(2).max(100),
  limitations: z.array(z.enum(limitations)).length(limitations.length)
    .refine(values => new Set(values).size === limitations.length),
  errors: z.array(apiError).length(Object.keys(errorStatus).length)
    .refine(values => new Set(values.map(row => row.code)).size === Object.keys(errorStatus).length),
}).strict().refine(value => (value.credential_mode !== "management" || value.access.all_data)
  && new Set(value.operations.map(row => row.name)).size === value.operations.length
  && new Set(value.operations.map(row => `${row.method}:${row.path}`)).size === value.operations.length
  && value.operations.every(row => (row.feature === "" || value.access.all_data || value.access.features.includes(row.feature))
    && (!row.management || value.access.all_data && value.credential_mode !== "read")));
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
const versioned = z.object({ id: objectID, revision }).strict();
export const apiKeysOutputs = { access, catalog: apiCatalog, list: apiKeyPage, create: apiKeyIssuance,
  rotate: apiKeyIssuance, revoke: apiKey };
export const apiKeysContract = {
  access: base.route({ method: "GET", path: "/api/v1/api-keys/access" }).input(z.object({}).strict()).output(access),
  catalog: base.route({ method: "GET", path: "/api/v1/api-keys/catalog" }).input(z.object({}).strict()).output(apiCatalog),
  list: base.route({ method: "POST", path: "/api/v1/api-keys/list" })
    .input(z.object({ after: z.union([z.literal(""), objectID]), limit: z.number().int().min(1).max(100) }).strict()).output(apiKeyPage),
  create: base.route({ method: "POST", path: "/api/v1/api-keys/create" })
    .input(z.object({ name, mode: apiKeyMode }).strict()).output(apiKeyIssuance),
  rotate: base.route({ method: "POST", path: "/api/v1/api-keys/rotate" }).input(versioned).output(apiKeyIssuance),
  revoke: base.route({ method: "POST", path: "/api/v1/api-keys/revoke" }).input(versioned).output(apiKey),
};
export type APIKey = z.infer<typeof apiKey>;
export type APIKeyIssuance = z.infer<typeof apiKeyIssuance>;
export type APICatalog = z.infer<typeof apiCatalog>;
