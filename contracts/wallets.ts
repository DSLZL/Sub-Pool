import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { participant } from "./participants";
import { usageNanos } from "./usage";

export const walletsVersion = "wallets-v3";
const timestamp = session.shape.created_at;
const count = z.union([z.literal("0"), revision]);
const decimal = z.string().max(76).regex(/^-?(0|[1-9][0-9]{0,37})(\.[0-9]{1,36})?$/)
  .refine(value => !/^-0(?:\.0+)?$/.test(value));
const field = z.object({
  value: decimal.nullable(), state: z.enum(["reported", "unverified", "invalid"]),
  reason: z.enum(["FIELD_NOT_RETURNED", "FIELD_PERMISSION_UNVERIFIED", "INVALID_RESPONSE", "NATIVE_FIELD_REPORTED"]),
}).strict().refine(value => (value.state === "reported") === (value.value !== null)
  && (value.state === "reported") === (value.reason === "NATIVE_FIELD_REPORTED")
  && (value.state === "invalid") === (value.reason === "INVALID_RESPONSE"));
const check = z.object({
  state: z.enum(["unverified", "available", "denied", "unsupported", "failed"]),
  reason: z.string().max(64).regex(/^[A-Z_]+$/),
  http_status: z.number().int().min(100).max(599).nullable(), latency_ms: z.number().int().nonnegative().nullable(),
}).strict();
const sourceLimitations = ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "WALLET_DEDUCTION_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED"] as const;
const usageSources = z.object({
  started_at: timestamp, captured_at: timestamp, state: z.enum(["failed", "partial", "exhausted"]),
  read: check.extend({ id: z.literal("usage.read") }), pages_read: z.number().int().min(0).max(10),
  upstream_total: count.nullable(), scanned_count: count.refine(value => count.safeParse(value).success && BigInt(value) <= 1000n),
  accounts: z.array(z.object({ upstream_account_id: revision, managed_account_id: objectID.nullable() }).strict()).max(1000),
  complete: z.literal(false), limitations: z.array(check.shape.reason).min(4).max(5),
}).strict().refine(value => (usageNanos(value.captured_at) ?? 0n) >= (usageNanos(value.started_at) ?? 1n))
  .refine(value => sourceLimitations.every(reason => value.limitations.includes(reason)) && new Set(value.limitations).size === value.limitations.length)
  .refine(value => count.safeParse(value.scanned_count).success && value.accounts.every(row => revision.safeParse(row.upstream_account_id).success)
    && value.accounts.length <= BigInt(value.scanned_count) && value.accounts.every((row, i) => {
    const previous = value.accounts[i - 1];
    return previous === undefined || BigInt(row.upstream_account_id) > BigInt(previous.upstream_account_id);
  }))
  .refine(value => count.safeParse(value.scanned_count).success && (value.upstream_total === null || count.safeParse(value.upstream_total).success)
    && (value.state === "failed" ? value.pages_read === 0 && value.scanned_count === "0" && value.upstream_total === null && value.read.state !== "available"
    : value.pages_read > 0 && value.upstream_total !== null && (value.state === "exhausted"
      ? value.read.state === "available" && value.scanned_count === value.upstream_total
      : BigInt(value.scanned_count) === BigInt(value.pages_read) * 100n && BigInt(value.scanned_count) < BigInt(value.upstream_total))));
export const walletRead = z.object({
  id: revision, participant_id: objectID, binding_id: revision, source_id: objectID, connection_revision: revision,
  attempted_at: timestamp, participant_revision: revision, upstream_user_id: revision, read_at: timestamp.nullable(),
  balance: field, frozen_balance: field.refine(value => value.value === null || !value.value.startsWith("-")),
  read: check.extend({ id: z.literal("users.read") }), field_authority: check.extend({ id: z.literal("users.balance.read") }),
  unit: z.literal("native_wallet"), usage_sources: usageSources.optional(),
}).strict().refine(value => (value.read_at !== null) === (value.read.state === "available"))
  .refine(value => value.read_at === null || (usageNanos(value.read_at) ?? 0n) >= (usageNanos(value.attempted_at) ?? 1n))
  .refine(value => value.balance.value === null && value.frozen_balance.value === null || value.read.state === "available" && value.field_authority.state === "available");
const estimateState = z.enum(["not_configured", "invalid_evidence", "insufficient_samples", "condition_changed", "evidence_incomplete", "non_identifying_delta", "period_unknown", "source_stale", "manual_range_unusable", "evidence_limit", "recalculation_required"]);
const contribution = z.object({
  account_id: objectID, account_revision: revision, current_contract_id: revision.nullable(), historical_contract_head: count,
  share_percent: z.string().regex(/^(?:0|[1-9]\d?|100)(?:\.\d{1,6})?$/).refine(value => !value.startsWith("100.") || /^100\.0+$/.test(value)).nullable(),
  estimate_id: revision.nullable(), estimate_state: estimateState, observation_head: count, capture_head: count,
  state: z.enum(["evidence_incomplete", "source_unavailable", "contract_unavailable"]), amount: z.null(),
}).strict().refine(value => value.current_contract_id !== null || value.share_percent === null);
const limitations = ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] as const;
export const walletRecommendation = z.object({
  id: revision, participant_id: objectID, binding_id: revision, generated_at: timestamp,
  rules_version: z.literal("gate-003-rules-v1"), trigger: z.enum(["wallet_refresh", "manual_recompute", "input_change"]),
  participant_revision: revision, basis_hash: z.string().regex(/^[a-f0-9]{64}$/), expires_at: timestamp.nullable(),
  global: z.object({ state: z.enum(["evidence_incomplete", "no_recorded_sources", "condition_changed", "evidence_limit"]), source_count: z.number().int().min(0).max(501),
    limitations: z.array(z.enum(limitations)).length(6).refine(values => new Set(values).size === 6), }).strict(),
  contributions: z.array(contribution).max(500), target: z.null(), complete: z.literal(false), applicable: z.literal(false),
}).strict().refine(value => value.global.source_count > 500 ? value.global.state === "evidence_limit" && value.contributions.length === 0 : value.global.source_count === value.contributions.length)
  .refine(value => new Set(value.contributions.map(row => row.account_id)).size === value.contributions.length);
const summary = z.object({
  participant_id: objectID, name: participant.shape.name, participant_revision: revision, binding_id: revision,
  source_id: objectID, upstream_user_id: revision, enabled: z.boolean(), source_current: z.boolean(),
}).strict();
const view = summary.extend({
  connection_revision: revision, checked_at: timestamp, latest_read: walletRead.nullable(), last_confirmed_read: walletRead.nullable(),
  read_references_current: z.boolean(), latest_recommendation: walletRecommendation.nullable(), contributions: z.array(contribution).max(500),
  visibility: z.enum(["all_recorded_sources", "authorized_sources"]), current_state: z.enum(["not_generated", "unavailable", "recalculation_required"]),
  needs_recalculation: z.boolean().nullable(), current_target: z.null(), applicable: z.literal(false), difference: z.null(), last_application: z.null(), application_state: z.literal("not_implemented"),
}).strict().refine(value => [value.latest_read, value.last_confirmed_read, value.latest_recommendation].every(row => row === null || row.participant_id === value.participant_id && row.binding_id === value.binding_id))
  .refine(value => value.last_confirmed_read === null || value.last_confirmed_read.balance.state === "reported" && value.last_confirmed_read.frozen_balance.state === "reported"
    && value.latest_read !== null && revision.safeParse(value.last_confirmed_read.id).success && revision.safeParse(value.latest_read.id).success
    && BigInt(value.last_confirmed_read.id) <= BigInt(value.latest_read.id))
  .refine(value => !value.read_references_current || value.source_current && value.latest_read !== null && value.latest_read.connection_revision === value.connection_revision)
  .refine(value => value.visibility === "all_recorded_sources" ? value.needs_recalculation === (value.current_state === "recalculation_required")
    : value.needs_recalculation === null && value.latest_recommendation === null && value.current_state === "unavailable"
      && [value.latest_read, value.last_confirmed_read].every(read => read?.usage_sources === undefined))
  .refine(value => new Set(value.contributions.map(row => row.account_id)).size === value.contributions.length);
export const walletExpected = z.object({
  participant_id: objectID, participant_revision: revision, binding_id: revision, connection_revision: revision, source_id: objectID, latest_id: count,
}).strict();
const history = z.object({
  participant_id: objectID, kind: z.enum(["wallet", "recommendation"]), wallet_reads: z.array(walletRead).max(100), recommendations: z.array(walletRecommendation).max(100), next_after: revision.nullable(),
}).strict().refine(value => value.kind === "wallet" ? value.recommendations.length === 0 : value.wallet_reads.length === 0)
  .refine(value => {
    const rows = value.kind === "wallet" ? value.wallet_reads : value.recommendations;
    return rows.every((row, index) => {
      const previous = rows[index - 1];
      return revision.safeParse(row.id).success && row.participant_id === value.participant_id && (previous === undefined || BigInt(row.id) > BigInt(previous.id));
    }) && (value.next_after === null || rows.at(-1)?.id === value.next_after);
  });
export const walletsOutputs = {
  list: z.object({ participants: z.array(summary).max(100), next_after: objectID.nullable() }).strict()
    .refine(value => value.next_after === null || value.participants.at(-1)?.participant_id === value.next_after),
  get: view, history, refresh: walletRead, recompute: walletRecommendation,
};
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData }, FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, CONNECTION_NOT_CONFIGURED: { status: 409, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const walletsContract = {
  list: base.route({ method: "POST", path: "/api/v1/wallets/list" }).input(z.object({ after: z.union([z.literal(""), objectID]), limit: z.number().int().min(1).max(100) }).strict()).output(walletsOutputs.list),
  get: base.route({ method: "POST", path: "/api/v1/wallets/get" }).input(z.object({ participant_id: objectID }).strict()).output(view),
  history: base.route({ method: "POST", path: "/api/v1/wallets/history" }).input(z.object({ participant_id: objectID, kind: z.enum(["wallet", "recommendation"]), after: count, limit: z.number().int().min(1).max(100) }).strict()).output(history),
  refresh: base.route({ method: "POST", path: "/api/v1/wallets/refresh" }).input(walletExpected).output(walletRead),
  recompute: base.route({ method: "POST", path: "/api/v1/wallets/recompute" }).input(walletExpected).output(walletRecommendation),
};
export type WalletView = z.infer<typeof view>;
export type WalletRead = z.infer<typeof walletRead>;
export type WalletRecommendation = z.infer<typeof walletRecommendation>;
export type WalletExpected = z.infer<typeof walletExpected>;
