import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, identity, session } from "./auth";
import { objectID, revision } from "./users";

export const securityVersion = "security-v1";
const timestamp = session.shape.created_at;
const address = z.union([z.ipv4(), z.ipv6()]);
const count = z.union([z.literal("0"), revision]);
const metadata = z.string().regex(/^[A-Za-z0-9_.:\-]{1,128}$/);
export const requestSource = z.object({
  ip: address.nullable(), peer_ip: address.nullable(),
  kind: z.enum(["legacy", "peer", "trusted_forwarded", "unknown", "local", "session"]),
}).strict().refine(value => value.kind === "peer" ? value.ip !== null && value.ip === value.peer_ip
  : value.kind === "trusted_forwarded" ? value.ip !== null && value.peer_ip !== null
    : value.kind === "unknown" ? value.ip === null && value.peer_ip === null : true);
export const securityConfig = z.object({ revision, network_diagnostics_enabled: z.boolean() }).strict();
export const networkComparison = z.object({ reported_ip: address, trusted: z.literal(false), server_source: requestSource }).strict();
export const sourceRule = z.object({ ip: address, revision, blocked: z.boolean(), saved_at: timestamp, actor_id: objectID }).strict();
export const sourcePage = z.object({
  rules: z.array(sourceRule).max(100).refine(values => new Set(values.map(value => value.ip)).size === values.length),
  next_after: address.nullable(),
}).strict().refine(value => value.next_after === null || value.rules.at(-1)?.ip === value.next_after);
const loginOutcome = z.enum(["success", "failure", "limited", "blocked"]);
export const loginEvent = z.object({
  id: revision, occurred_at: timestamp, outcome: loginOutcome,
  user_id: objectID.nullable(), username: identity.shape.username.nullable(), source: requestSource,
}).strict().refine(value => (value.user_id === null) === (value.username === null));
export const loginPage = z.object({
  events: z.array(loginEvent).max(100).refine(values => new Set(values.map(value => value.id)).size === values.length),
  next_after: revision.nullable(), counts: z.object({ success: count, failure: count, limited: count, blocked: count }).strict(),
}).strict().refine(value => value.next_after === null || value.events.at(-1)?.id === value.next_after);
export const managementEvent = z.object({
  id: revision, operation_id: metadata, occurred_at: timestamp, actor_id: objectID.nullable(), task_id: metadata.nullable(),
  action: metadata, outcome: z.enum(["pending", "succeeded", "failed", "unknown", "denied", "conflict"]),
  target_type: metadata, target_id: metadata, target_revision: revision.nullable(), source_id: objectID.nullable(),
}).strict().refine(value => (value.actor_id === null) !== (value.task_id === null));
export const managementPage = z.object({
  events: z.array(managementEvent).max(100).refine(values => new Set(values.map(value => value.id)).size === values.length),
  next_after: revision.nullable(),
}).strict().refine(value => value.next_after === null || value.events.at(-1)?.id === value.next_after);

function nanoseconds(value: string) {
  const fraction = value[19] === "." ? value.slice(20, -1) : "";
  return BigInt(Date.parse(value.slice(0, 19) + "Z")) * 1_000_000n + BigInt(fraction.padEnd(9, "0"));
}
const range = z.object({ from: timestamp, until: timestamp, after: count, limit: z.number().int().min(1).max(100) }).strict();
const validRange = (value: z.infer<typeof range>) => {
  if (!timestamp.safeParse(value.from).success || !timestamp.safeParse(value.until).success) return false;
  const duration = nanoseconds(value.until) - nanoseconds(value.from);
  return duration > 0n && duration <= 31n * 24n * 60n * 60n * 1_000_000_000n;
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  SELF_SOURCE_PROTECTED: { status: 409, data: authErrorData }, NETWORK_DIAGNOSTICS_DISABLED: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const securityOutputs = {
  publicConfig: z.object({ network_diagnostics_enabled: z.boolean() }).strict(),
  config: securityConfig, source: requestSource, updateConfig: securityConfig, compareNetwork: networkComparison,
  sources: sourcePage, updateSource: sourceRule, loginAudit: loginPage, managementAudit: managementPage,
};
export const securityContract = {
  publicConfig: base.route({ method: "GET", path: "/api/v1/security/public" }).input(z.object({}).strict())
    .output(securityOutputs.publicConfig),
  source: base.route({ method: "GET", path: "/api/v1/security/source" }).input(z.object({}).strict()).output(requestSource),
  config: base.route({ method: "GET", path: "/api/v1/security/config" }).input(z.object({}).strict()).output(securityConfig),
  updateConfig: base.route({ method: "POST", path: "/api/v1/security/config/update" })
    .input(z.object({ revision, network_diagnostics_enabled: z.boolean() }).strict()).output(securityConfig),
  compareNetwork: base.route({ method: "POST", path: "/api/v1/security/network/compare" })
    .input(z.object({ reported_ip: address, consent: z.literal(true) }).strict()).output(networkComparison),
  sources: base.route({ method: "POST", path: "/api/v1/security/sources/list" })
    .input(z.object({ after: z.union([z.literal(""), address]), state: z.enum(["all", "blocked", "unblocked"]), limit: z.number().int().min(1).max(100) }).strict()).output(sourcePage),
  updateSource: base.route({ method: "POST", path: "/api/v1/security/sources/update" })
    .input(z.object({ ip: address, revision: count, blocked: z.boolean() }).strict()).output(sourceRule),
  loginAudit: base.route({ method: "POST", path: "/api/v1/security/login-audit" })
    .input(range.extend({ outcome: z.union([z.literal("all"), loginOutcome]) }).refine(validRange)).output(loginPage),
  managementAudit: base.route({ method: "POST", path: "/api/v1/security/management-audit" }).input(range.refine(validRange)).output(managementPage),
};

export type SecurityConfig = z.infer<typeof securityConfig>;
export type RequestSource = z.infer<typeof requestSource>;
export type LoginAuditPage = z.infer<typeof loginPage>;
export type ManagementAuditPage = z.infer<typeof managementPage>;
export type SourceRule = z.infer<typeof sourceRule>;
