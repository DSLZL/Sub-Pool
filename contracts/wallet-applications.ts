import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { participant } from "./participants";
import { walletExpected } from "./wallets";
import { usageNanos } from "./usage";

export const walletApplicationsVersion = "wallet-applications-v1";
const timestamp = session.shape.created_at;
const count = z.union([z.literal("0"), revision]);
const emptyOrID = z.union([z.literal(""), objectID]);
const emptyOrTime = z.union([z.literal(""), timestamp]);
const hash = z.string().regex(/^[a-f0-9]{64}$/);
export const walletApplicationInput = walletExpected.extend({ request_id: objectID, recommendation_id: count }).strict();
const requiredReasons = ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED", "WALLET_WRITE_CAPABILITY_UNVERIFIED", "TARGET_UNAVAILABLE"] as const;
const details = z.object({
  actor_id: objectID, input: walletApplicationInput, upstream_user_id: revision,
  evaluated_basis_hash: hash, recommendation_basis_hash: hash.nullable(),
  reasons: z.array(z.enum([...requiredReasons, "RECOMMENDATION_NOT_GENERATED", "RECOMMENDATION_STALE"])).min(8).max(9),
}).strict().refine(value => new Set(value.reasons).size === value.reasons.length && requiredReasons.every(reason => value.reasons.includes(reason)))
  .refine(value => (value.input.recommendation_id === "0") === (value.recommendation_basis_hash === null)
    && (value.input.recommendation_id === "0") === value.reasons.includes("RECOMMENDATION_NOT_GENERATED"));
export const walletApplication = z.object({
  id: revision, participant_id: objectID, participant_name: participant.shape.name, attempted_at: timestamp,
  state: z.literal("rejected"), dispatch_state: z.literal("not_dispatched"), reason: z.literal("FINANCIAL_APPLICATION_UNAVAILABLE"),
  target: z.null(), unit: z.literal("native_wallet"), visibility: z.enum(["administrator", "participant_summary"]), details: details.nullable(),
}).strict().refine(value => value.visibility === "administrator" ? value.details !== null && value.details.input.participant_id === value.participant_id : value.details === null);
export const walletApplicationQuery = z.object({
  participant_id: emptyOrID, start_at: emptyOrTime, end_at: emptyOrTime, after: count, limit: z.number().int().min(1).max(100),
}).strict().refine(value => {
  if (value.start_at === "" || value.end_at === "") return value.start_at === "" && value.end_at === "";
  const start = usageNanos(value.start_at), end = usageNanos(value.end_at);
  return start !== null && end !== null && end > start && end - start <= 31n * 86400n * 1000000000n;
});
const page = z.object({
  participant_id: emptyOrID, start_at: emptyOrTime, end_at: emptyOrTime,
  requests: z.array(walletApplication).max(100), next_after: revision.nullable(),
}).strict().refine(value => {
  if (!walletApplicationQuery.safeParse({ participant_id: value.participant_id, start_at: value.start_at, end_at: value.end_at, after: "0", limit: 100 }).success) return false;
  const start = value.start_at === "" ? null : usageNanos(value.start_at), end = value.end_at === "" ? null : usageNanos(value.end_at);
  return value.requests.every((row, index) => {
    const previous = value.requests[index - 1], at = usageNanos(row.attempted_at);
    return (!value.participant_id || row.participant_id === value.participant_id) && (previous === undefined || BigInt(row.id) > BigInt(previous.id) && row.visibility === previous.visibility)
      && at !== null && (start === null || at >= start) && (end === null || at < end);
  }) && (value.next_after === null || value.requests.at(-1)?.id === value.next_after);
});
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const walletAutomation = z.object({
  enabled: z.literal(false), can_enable: z.literal(false), state: z.literal("unavailable"),
  reason: z.literal("FINANCIAL_APPLICATION_UNAVAILABLE"), execution_state: z.literal("not_implemented"),
}).strict();
export const walletApplicationsOutputs = { request: walletApplication, get: walletApplication, list: page, automation: walletAutomation };
export const walletApplicationsContract = {
  request: base.route({ method: "POST", path: "/api/v1/wallet-applications/request" }).input(walletApplicationInput).output(walletApplication),
  get: base.route({ method: "POST", path: "/api/v1/wallet-applications/get" }).input(z.object({ id: revision }).strict()).output(walletApplication),
  list: base.route({ method: "POST", path: "/api/v1/wallet-applications/list" }).input(walletApplicationQuery).output(page),
  automation: base.route({ method: "POST", path: "/api/v1/wallet-applications/automation" }).input(z.object({}).strict()).output(walletAutomation),
};
export type WalletApplication = z.infer<typeof walletApplication>;
export type WalletApplicationInput = z.infer<typeof walletApplicationInput>;
