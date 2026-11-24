import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { usageNanos } from "./usage";

export const poolsVersion = "pools-v1";
export const poolMemberLimit = 10_000;
const timestamp = session.shape.created_at;
const text = (max: number) => z.string().max(max).refine(v => new TextEncoder().encode(v).length <= max && !/[\u0000-\u001f\u007f-\u009f]/.test(v));
const name = text(512).min(1).refine(v => v.trim() === v && [...v].length <= 128);
const percentPattern = /^(?:0|[1-9][0-9]{0,2})(?:\.[0-9]{1,6})?$/;
const units = (v: string) => {
  if (!percentPattern.test(v)) return NaN;
  const [whole, fraction = ""] = v.split(".");
  return Number(whole) * 1000000 + Number(fraction.padEnd(6, "0"));
};
export const sharePercent = z.string().max(10).regex(percentPattern).refine(v => units(v) <= 100000000);
export { units as shareUnits };
const member = z.object({ id: objectID, revision }).strict();
const share = z.object({ id: objectID, revision, share_percent: sharePercent }).strict();
const unique = (v: { id: string }[]) => new Set(v.map(r => r.id)).size === v.length;
const fields = {
  source_id: objectID, connection_revision: revision, name, enabled: z.boolean(),
  accounts: z.array(member).min(1).max(100).refine(unique),
  participants: z.array(share).max(poolMemberLimit).refine(unique).refine(v => v.reduce((n, p) => n + units(p.share_percent), 0) <= 100000000),
};
export const poolCreateInput = z.object(fields).strict();
export const poolUpdateInput = z.object({ id: objectID, revision, ...fields }).strict();
const summaryFields = {
  id: revision, pool_id: objectID, revision, name, enabled: z.boolean(), source_id: objectID, connection_revision: revision,
  effective_at: timestamp, ended_at: timestamp.nullable(), current_revision: revision.nullable(), is_current: z.boolean(),
  source_current: z.boolean(), visibility: z.enum(["all_members", "scoped_members"]),
};
const validSummary = (v: { revision: string; current_revision: string | null; is_current: boolean; ended_at: string | null; effective_at: string; visibility: string }) =>
  (v.current_revision === null || v.is_current === (v.revision === v.current_revision)) && v.is_current === (v.ended_at === null)
  && (v.current_revision === null) === (v.visibility === "scoped_members")
  && (v.ended_at === null || usageNanos(v.ended_at)! >= usageNanos(v.effective_at)!);
export const poolSummary = z.object(summaryFields).strict().refine(validSummary);
const account = z.object({
  id: objectID, revision, name, source_id: objectID, upstream_id: revision, identity_current: z.boolean(),
}).strict();
const participant = z.object({
  id: objectID, revision, name, binding_id: revision, share_percent: sharePercent,
  enabled: z.boolean(), binding_current: z.boolean(),
}).strict();
const contractFields = {
  ...summaryFields, accounts: z.array(account).min(1).max(100).refine(unique),
  unallocated_percent: sharePercent.nullable(),
};
export const poolContract = z.object({
  ...contractFields, participants: z.array(participant).max(poolMemberLimit).refine(unique),
}).strict().refine(validSummary)
  .refine(v => v.accounts.every(a => a.source_id === v.source_id))
  .refine(v => v.visibility === "scoped_members" ? v.unallocated_percent === null && v.participants.length > 0
    : v.unallocated_percent !== null && units(v.unallocated_percent) + v.participants.reduce((n, p) => n + units(p.share_percent), 0) === 100000000);
const numericPaging = { after: z.union([z.literal("0"), revision]), limit: z.number().int().min(1).max(100) };
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData },
  POOL_CHANGE_BLOCKED: { status: 409, data: authErrorData },
  POOL_MEMBER_LIMIT: { status: 409, data: authErrorData },
  TOO_MANY_REQUESTS: { status: 429, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
const evidence = z.object({
  upstream_id: revision, participant_id: objectID.nullable(), contract_id: revision.nullable(),
  basis: z.enum(["mapping_not_recorded", "recorded_contract", "unassigned_participant", "no_recorded_contract", "contract_member_missing"]),
}).strict().refine(v => (v.basis === "recorded_contract") === (v.contract_id !== null) && (v.contract_id === null || v.participant_id !== null))
  .refine(v => v.basis === "mapping_not_recorded" || (v.basis === "unassigned_participant") === (v.participant_id === null));
const increasing = (a: string, b: string) => a.length < b.length || a.length === b.length && a < b;
const weeklyMoney = z.string().regex(/^(0|[1-9][0-9]{0,37})(\.[0-9]{1,8})?$/);
const weeklyBalance = z.string().refine(value => weeklyMoney.safeParse(value.replace(/^-/, "")).success);
const weeklyWalletInput = z.object({
  id: objectID, pool_revision: revision, revision, enabled: z.boolean(), auto_allocate: z.boolean(),
}).strict();
const weeklyWalletPlan = z.object({
  gross: weeklyMoney, spent: weeklyMoney, opening_debt: weeklyMoney, debt: weeklyMoney,
  balance_before: weeklyBalance, target: weeklyMoney, contract_id: revision, estimate_id: revision.nullable(), share_percent: sharePercent.optional(),
}).strict();
const weeklyWalletEntryFields = {
  id: revision, participant_id: objectID, name, phase: z.enum(["advance", "settlement"]), period_end: timestamp, recorded_at: timestamp,
  application_id: revision.nullable(), state: z.enum(["pending", "unknown", "applied", "rejected", "failed"]),
};
const weeklyWalletEntry = z.object({ ...weeklyWalletEntryFields, plan: weeklyWalletPlan })
  .strict().refine(value => (value.phase === "settlement") === (value.plan.estimate_id !== null)
  && (value.application_id !== null || value.state === "applied"));
const groupMoney = z.string().regex(/^(?:0|[1-9]\d{0,37})(?:\.\d{1,36})?$/);
const groupBalance = z.string().regex(/^-?(?:0|[1-9]\d{0,37})(?:\.\d{1,36})?$/);
const weeklyWalletCycle = z.object({ pool_id: objectID, account_id: objectID, period_start: timestamp, period_end: timestamp }).strict();
const weeklyWalletContribution = weeklyWalletCycle.extend({
  name, contract_id: revision, phase: z.enum(["unfunded", "advance", "settlement"]), estimate_id: revision.nullable(),
  share_percent: z.union([sharePercent, z.literal("")]), gross: groupMoney, spent: groupMoney,
  unallocated_percent: sharePercent.optional(),
  opening_debt: groupMoney, covered: groupMoney, used_to_cover: groupMoney, debt: groupMoney,
  remaining: groupMoney, last_formal_gross: groupMoney,
}).strict().refine(value => (value.phase === "settlement") === (value.estimate_id !== null));
const weeklyWalletGroup = z.object({
  cutoff: timestamp, balance_before: groupBalance, target: groupMoney, delta: weeklyBalance,
  contributions: z.array(weeklyWalletContribution).min(1).max(500),
  closed: z.array(weeklyWalletContribution).max(500),
  offsets: z.array(z.object({ from: weeklyWalletCycle, to: weeklyWalletCycle, amount: groupMoney, at: timestamp }).strict()).max(250000),
}).strict();
const weeklyWalletGroupEntry = z.object({ ...weeklyWalletEntryFields, group: weeklyWalletGroup }).strict()
  .refine(value => value.application_id !== null ? value.group.delta !== "0" : value.state === "applied" && value.group.delta === "0");
const weeklyWallet = z.object({
  pool_id: objectID, pool_revision: revision, revision, enabled: z.boolean(), auto_allocate: z.boolean(),
  state: z.enum(["unavailable", "disabled", "paused", "enabled"]), entries: z.array(weeklyWalletEntry).max(100),
  groups: z.array(weeklyWalletGroupEntry).max(100).optional(),
}).strict().refine(value => (value.enabled || value.auto_allocate) === (value.state === "enabled" || value.state === "paused")
  && value.entries.every((item, i) => i === 0 || increasing(item.id, value.entries[i - 1]!.id)));
const weeklyWalletBasis = z.string().regex(/^[0-9a-f]{64}$/);
const weeklyWalletProposalFields = { basis: weeklyWalletBasis, period_end: timestamp, delta: weeklyBalance };
const weeklyWalletIssue = z.object({
  pool_id: objectID, account_id: z.union([objectID, z.literal("")]),
  reason: z.enum(["account_unavailable", "waiting_estimate", "contract_changed", "usage_unavailable", "usage_conflict", "balance_mismatch", "amount_precision_unverified"]),
}).strict();
const weeklyWalletRecommendation = z.object({
  participant_id: objectID, pool: z.object({ id: objectID, name }).strict().nullable(),
  state: z.enum(["unavailable", "waiting_estimate", "waiting_actual_cost", "balance_unavailable", "write_unavailable", "waiting_evidence", "pending", "settled", "ready"]),
  proposal: z.union([
    z.object({ ...weeklyWalletProposalFields, plan: weeklyWalletPlan }).strict(),
    z.object({ ...weeklyWalletProposalFields, group: weeklyWalletGroup }).strict().refine(value => value.delta === value.group.delta),
  ]).nullable(),
  issues: z.array(weeklyWalletIssue).min(1).max(500).optional(),
}).strict().refine(value => (value.state === "ready") === (value.proposal !== null) && (value.state === "unavailable" || value.pool !== null)
  && (value.state === "waiting_evidence") === (value.issues !== undefined)
  && (!value.proposal || "group" in value.proposal || value.proposal.plan.estimate_id !== null && value.proposal.plan.share_percent !== undefined));
const weeklyWalletSettlement = z.union([
  weeklyWalletEntry.refine(value => value.phase === "settlement" && value.plan.share_percent !== undefined),
  weeklyWalletGroupEntry.refine(value => value.phase === "settlement"),
]);
export const poolsOutputs = {
  contract: poolContract,
  members: z.object({
    ...contractFields, participants: z.array(participant).max(100).refine(unique), next_after: objectID.nullable(), scope_revision: revision,
  }).strict().refine(validSummary)
    .refine(v => v.accounts.every(a => a.source_id === v.source_id))
    .refine(v => (v.unallocated_percent === null) === (v.visibility === "scoped_members"))
    .refine(v => v.next_after === null || v.participants.at(-1)?.id === v.next_after),
  weeklyWallet,
  weeklyWalletRecommendation,
  weeklyWalletSettlement,
  list: z.object({ pools: z.array(poolSummary).max(100), next_after: objectID.nullable() }).strict()
    .refine(v => v.next_after === null || v.pools.at(-1)?.pool_id === v.next_after),
  history: z.object({ contracts: z.array(poolSummary).max(100), next_after: revision.nullable() }).strict()
    .refine(v => v.contracts.every((c, i) => i === 0 || increasing(v.contracts[i - 1]!.id, c.id)))
    .refine(v => v.next_after === null || v.contracts.at(-1)?.id === v.next_after),
  evidence: z.object({ capture_id: revision, records: z.array(evidence).max(100), next_after: revision.nullable() }).strict()
    .refine(v => v.records.every((r, i) => i === 0 || increasing(v.records[i - 1]!.upstream_id, r.upstream_id)))
    .refine(v => v.next_after === null || v.records.at(-1)?.upstream_id === v.next_after),
};
export const poolsContract = {
  create: base.route({ method: "POST", path: "/api/v1/pools/create" }).input(poolCreateInput).output(poolContract),
  update: base.route({ method: "POST", path: "/api/v1/pools/update" }).input(poolUpdateInput).output(poolContract),
  disable: base.route({ method: "POST", path: "/api/v1/pools/disable" }).input(z.object({ id: objectID, revision }).strict()).output(poolContract),
  list: base.route({ method: "POST", path: "/api/v1/pools/list" }).input(z.object({ after: z.union([z.literal(""), objectID]), search: text(256), limit: numericPaging.limit }).strict()).output(poolsOutputs.list),
  get: base.route({ method: "POST", path: "/api/v1/pools/get" }).input(z.object({ id: objectID }).strict()).output(poolContract),
  contract: base.route({ method: "POST", path: "/api/v1/pools/contract" }).input(z.object({ id: revision }).strict()).output(poolContract),
  members: base.route({ method: "POST", path: "/api/v1/pools/members" }).input(z.object({ id: revision, after: z.union([z.literal(""), objectID]), limit: numericPaging.limit }).strict()).output(poolsOutputs.members),
  history: base.route({ method: "POST", path: "/api/v1/pools/history" }).input(z.object({ id: z.union([z.literal(""), objectID]), ...numericPaging }).strict()).output(poolsOutputs.history),
  evidence: base.route({ method: "POST", path: "/api/v1/pools/usage-evidence" }).input(z.object({ capture_id: revision, ...numericPaging }).strict()).output(poolsOutputs.evidence),
  weeklyWallet: base.route({ method: "POST", path: "/api/v1/pools/weekly-wallet" }).input(z.object({ id: objectID }).strict()).output(weeklyWallet),
  configureWeeklyWallet: base.route({ method: "POST", path: "/api/v1/pools/weekly-wallet/configure" }).input(weeklyWalletInput).output(weeklyWallet),
  weeklyWalletRecommendation: base.route({ method: "POST", path: "/api/v1/pools/weekly-wallet/recommendation" }).input(z.object({ id: objectID }).strict()).output(weeklyWalletRecommendation),
  applyWeeklyWallet: base.route({ method: "POST", path: "/api/v1/pools/weekly-wallet/apply" })
    .input(z.object({ pool_id: objectID, participant_id: objectID, basis: weeklyWalletBasis, cutoff: timestamp.optional() }).strict()).output(weeklyWalletSettlement),
};
export type PoolContract = z.infer<typeof poolContract>;
export type PoolSummary = z.infer<typeof poolSummary>;
export type WeeklyWallet = z.infer<typeof weeklyWallet>;
export type WeeklyWalletRecommendation = z.infer<typeof weeklyWalletRecommendation>;
