import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { participant } from "./participants";
import { walletExpected, walletRead, walletRecommendation } from "./wallets";
import { usageNanos } from "./usage";

export const walletApplicationsVersion = "wallet-applications-v2";
const timestamp = session.shape.created_at;
const count = z.union([z.literal("0"), revision]);
const emptyOrID = z.union([z.literal(""), objectID]);
const emptyOrTime = z.union([z.literal(""), timestamp]);
const hash = z.string().regex(/^[a-f0-9]{64}$/);
export const walletApplicationInput = walletExpected.extend({ request_id: objectID, recommendation_id: count }).strict();
const requiredReasons = ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED", "WALLET_WRITE_CAPABILITY_UNVERIFIED", "TARGET_UNAVAILABLE"] as const;
const details = z.object({
  actor_id: objectID, actor_api_key_id: objectID.optional(), input: walletApplicationInput, upstream_user_id: revision,
  evaluated_basis_hash: hash, recommendation_basis_hash: hash.nullable(),
  reasons: z.array(z.enum([...requiredReasons, "RECOMMENDATION_NOT_GENERATED", "RECOMMENDATION_STALE"])).min(8).max(9),
}).strict().refine(value => new Set(value.reasons).size === value.reasons.length && requiredReasons.every(reason => value.reasons.includes(reason)))
  .refine(value => (value.input.recommendation_id === "0") === (value.recommendation_basis_hash === null)
    && (value.input.recommendation_id === "0") === value.reasons.includes("RECOMMENDATION_NOT_GENERATED"));
const nativeApplication = z.object({
  id: revision, participant_id: objectID, participant_name: participant.shape.name, attempted_at: timestamp,
  state: z.literal("rejected"), dispatch_state: z.literal("not_dispatched"), reason: z.literal("FINANCIAL_APPLICATION_UNAVAILABLE"),
  target: z.null(), unit: z.literal("native_wallet"), visibility: z.enum(["administrator", "participant_summary"]), details: details.nullable(),
}).strict().refine(value => value.visibility === "administrator" ? value.details !== null && value.details.input.participant_id === value.participant_id : value.details === null);

const simulation = walletRead.shape.simulation.unwrap();
const scope = walletRead.shape.simulation_scope.unwrap();
const amount = walletRecommendation.shape.target.unwrap();
const money = scope.shape.balance;
const quantum = z.literal("0.00000001");
export const simulatedWalletApplicationInput = walletApplicationInput.extend({ simulation }).strict();
function units(value: string) {
  if (!money.safeParse(value).success) return null;
  const [whole, fraction = ""] = value.split(".");
  return BigInt(whole + fraction.padEnd(8, "0"));
}
function sameAccounts(left: string[], right: string[]) {
  return left.length === right.length && left.every(id => right.includes(id));
}
type Scope = z.infer<typeof scope>;
function closed(value: Scope) {
  return units(value.frozen_balance) === 0n && value.pending_requests === "0" && value.scope_complete
    && value.ledger_closed && value.billing_type === "standard_wallet";
}
const writeRequest = simulation.extend({
  operation_id: hash, user_id: revision, expected_wallet_version: revision, expected_ledger_revision: revision,
  expected_closed_through: timestamp, source_account_ids: scope.shape.source_account_ids,
  target: money.refine(value => /^[0-9]+\.[0-9]{8}$/.test(value)), quantum,
}).strict();
type WriteRequest = z.infer<typeof writeRequest>;
function precondition(before: Scope, request: WriteRequest) {
  return closed(before) && before.epoch === request.epoch && before.user_id === request.user_id
    && before.wallet_version === request.expected_wallet_version && before.ledger_revision === request.expected_ledger_revision
    && before.closed_through === request.expected_closed_through && sameAccounts(before.source_account_ids, request.source_account_ids);
}
const writeIntent = z.object({ request: writeRequest, before: scope, original_target: amount, remainder: amount }).strict().refine(value => {
  if (!amount.safeParse(value.original_target).success || !amount.safeParse(value.remainder).success) return false;
  const target = units(value.request.target), original = value.original_target, remainder = value.remainder;
  return target !== null && precondition(value.before, value.request)
    && target === BigInt(original.numerator) * 100000000n / BigInt(original.denominator)
    && (BigInt(original.numerator) * 100000000n - target * BigInt(original.denominator)) * BigInt(remainder.denominator)
      === BigInt(remainder.numerator) * BigInt(original.denominator) * 100000000n;
});
const writeProof = simulation.extend({
  operation_id: hash, user_id: revision, request_hash: hash, request: writeRequest, state: z.enum(["applied", "rejected"]),
  reason: z.enum(["APPLIED", "PRECONDITION_CHANGED", "WRITE_DENIED"]), before: scope, after: scope, occurred_at: timestamp,
}).strict().refine(value => {
  const before = value.before, after = value.after, at = usageNanos(value.occurred_at);
  if (value.epoch !== value.request.epoch || value.user_id !== value.request.user_id || value.operation_id !== value.request.operation_id
    || [before, after].some(row => row.epoch !== value.epoch || row.user_id !== value.user_id)
    || at === null || (usageNanos(before.closed_through) ?? at + 1n) > at) return false;
  if (value.state === "rejected") return value.reason !== "APPLIED" && JSON.stringify(before) === JSON.stringify(after);
  if (![before.wallet_version, before.ledger_revision, after.wallet_version, after.ledger_revision].every(id => revision.safeParse(id).success)) return false;
  return value.reason === "APPLIED" && precondition(before, value.request) && closed(after)
    && sameAccounts(before.source_account_ids, after.source_account_ids) && after.closed_through === value.occurred_at
    && BigInt(after.wallet_version) === BigInt(before.wallet_version) + 1n && BigInt(after.ledger_revision) === BigInt(before.ledger_revision) + 1n
    && units(after.balance) === units(value.request.target);
});
const simulatedReasons = ["SIMULATION_TARGET_UNAVAILABLE", "SIMULATION_WRITE_UNAVAILABLE", "ZERO_TARGET_PENDING", "RECOMMENDATION_STALE",
  "SIMULATION_PRECONDITION_CHANGED", "SIMULATED_APPLY_PENDING", "SIMULATED_WRITE_RESULT_UNKNOWN", "PROCESS_INTERRUPTED_RESULT_UNKNOWN",
  "SIMULATED_APPLIED", "SIMULATED_PRECONDITION_CHANGED", "SIMULATED_WRITE_DENIED"] as const;
const simulatedDetails = z.object({
  actor_id: objectID, actor_api_key_id: objectID.optional(), input: simulatedWalletApplicationInput, upstream_user_id: revision,
  evaluated_basis_hash: hash, recommendation_basis_hash: hash.nullable(),
  reasons: z.tuple([z.literal("CONDITIONAL_SIMULATION_ONLY"), z.enum(simulatedReasons)]),
  simulation_write: writeIntent.optional(), simulation_proof: writeProof.optional(),
}).strict();
const simulatedApplication = z.object({
  id: revision, participant_id: objectID, participant_name: participant.shape.name, attempted_at: timestamp, updated_at: timestamp, simulation,
  state: z.enum(["pending", "unknown", "applied", "failed", "rejected"]),
  dispatch_state: z.enum(["pending", "unknown", "confirmed", "confirmed_failed", "not_dispatched"]),
  reason: z.enum([...simulatedReasons, "SIMULATED_APPLICATION_UNAVAILABLE", "SIMULATED_APPLICATION_FAILED"]),
  target: amount.nullable(), unit: z.literal("simulated_wallet_usd"), visibility: z.enum(["administrator", "participant_summary"]), details: simulatedDetails.nullable(),
}).strict().refine(value => {
  const attempted = usageNanos(value.attempted_at), updated = usageNanos(value.updated_at);
  if (attempted === null || updated === null || updated < attempted) return false;
  const expected = { pending: "pending", unknown: "unknown", applied: "confirmed", failed: "confirmed_failed", rejected: "not_dispatched" } as const;
  if (value.dispatch_state !== expected[value.state]) return false;
  if (value.visibility === "participant_summary") {
    return value.details === null && value.target === null && value.reason === ({ pending: "SIMULATED_APPLY_PENDING", unknown: "SIMULATED_WRITE_RESULT_UNKNOWN", applied: "SIMULATED_APPLIED", failed: "SIMULATED_APPLICATION_FAILED", rejected: "SIMULATED_APPLICATION_UNAVAILABLE" } as const)[value.state];
  }
  const detail = value.details;
  if (detail === null || detail.input.participant_id !== value.participant_id || detail.input.simulation.epoch !== value.simulation.epoch || detail.reasons[1] !== value.reason) return false;
  const intent = detail.simulation_write, proof = detail.simulation_proof;
  if (intent && (intent.request.epoch !== value.simulation.epoch || intent.request.user_id !== detail.upstream_user_id)) return false;
  if (proof && (!intent || JSON.stringify(proof.request) !== JSON.stringify(intent.request)
    || proof.state === "applied" && units(proof.before.balance) !== units(intent.before.balance)
    || (usageNanos(proof.occurred_at) ?? updated + 1n) > updated)) return false;
  if (value.state !== "rejected" && (!intent || value.target === null)) return false;
  if (value.target !== null) {
    if (!amount.safeParse(value.target).success || !intent) return false;
    const target = units(intent.request.target);
    if (target === null || target <= 0n || BigInt(value.target.numerator) * 100000000n !== target * BigInt(value.target.denominator)) return false;
  }
  if (value.state === "applied" || value.state === "failed") return proof?.state === (value.state === "applied" ? "applied" : "rejected")
    && value.reason === (value.state === "applied" ? "SIMULATED_APPLIED" : `SIMULATED_${proof.reason}`);
  if (value.state === "pending") return value.reason === "SIMULATED_APPLY_PENDING" && proof === undefined;
  if (value.state === "unknown") return ["SIMULATED_WRITE_RESULT_UNKNOWN", "PROCESS_INTERRUPTED_RESULT_UNKNOWN"].includes(value.reason);
  return ["SIMULATION_TARGET_UNAVAILABLE", "SIMULATION_WRITE_UNAVAILABLE", "ZERO_TARGET_PENDING", "RECOMMENDATION_STALE", "SIMULATION_PRECONDITION_CHANGED"].includes(value.reason) && proof === undefined;
});
export const walletApplication = z.union([nativeApplication, simulatedApplication]);
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
    return revision.safeParse(row.id).success && (previous === undefined || revision.safeParse(previous.id).success)
      && (!value.participant_id || row.participant_id === value.participant_id) && (previous === undefined || BigInt(row.id) > BigInt(previous.id) && row.visibility === previous.visibility)
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
export const walletApplicationsOutputs = { request: nativeApplication, simulate: simulatedApplication, reconcile: simulatedApplication, get: walletApplication, list: page, automation: walletAutomation };
export const walletApplicationsContract = {
  request: base.route({ method: "POST", path: "/api/v1/wallet-applications/request" }).input(walletApplicationInput).output(nativeApplication),
  simulate: base.route({ method: "POST", path: "/api/v1/wallet-applications/simulate" }).input(simulatedWalletApplicationInput).output(simulatedApplication),
  reconcile: base.route({ method: "POST", path: "/api/v1/wallet-applications/reconcile" }).input(z.object({ id: revision }).strict()).output(simulatedApplication),
  get: base.route({ method: "POST", path: "/api/v1/wallet-applications/get" }).input(z.object({ id: revision }).strict()).output(walletApplication),
  list: base.route({ method: "POST", path: "/api/v1/wallet-applications/list" }).input(walletApplicationQuery).output(page),
  automation: base.route({ method: "POST", path: "/api/v1/wallet-applications/automation" }).input(z.object({}).strict()).output(walletAutomation),
};
export type WalletApplication = z.infer<typeof walletApplication>;
export type WalletApplicationInput = z.infer<typeof walletApplicationInput>;
