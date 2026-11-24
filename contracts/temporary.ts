import { oc } from "@orpc/contract";
import { z } from "zod";
import { authErrorData, session } from "./auth";
import { objectID, revision } from "./users";
import { usageNanos } from "./usage";
import { walletRecommendation, walletRead } from "./wallets";

export const temporaryVersion = "temporary-v2";
export const temporaryInput = z.object({}).strict();
const timestamp = session.shape.created_at, count = z.union([z.literal("0"), revision]);
const hash = z.string().regex(/^[a-f0-9]{64}$/), money = z.string().regex(/^(?:0|[1-9]\d{0,15})(?:\.\d{1,8})?$/);
const signed = z.union([money, z.string().regex(/^-(?:0|[1-9]\d{0,15})(?:\.\d{1,8})?$/).refine(value => !/^-0(?:\.0+)?$/.test(value))]);
const units = (value: string) => {
  const negative = value.startsWith("-"), [whole = "0", fraction = ""] = (negative ? value.slice(1) : value).split(".");
  const amount = BigInt(whole) * 100000000n + BigInt(fraction.padEnd(8, "0"));
  return negative ? -amount : amount;
};
const amount = walletRecommendation.shape.target.unwrap();
const simulation = walletRead.shape.simulation.unwrap();
const period = z.object({ kind: z.enum(["short", "seven_day"]), start_at: timestamp, end_at: timestamp,
  duration_seconds: z.string().regex(/^[1-9]\d{0,8}$/), basis: z.literal("native_reset_and_duration") }).strict().refine(value => {
  const start = usageNanos(value.start_at), end = usageNanos(value.end_at);
  return start !== null && end !== null && end > start && end - start === BigInt(value.duration_seconds) * 1000000000n;
});
const member = z.object({ participant_id: objectID, binding_id: revision, user_id: revision, baseline: amount }).strict();
const account = z.object({
  account_id: objectID, upstream_id: revision, account_revision: revision, contract_id: revision, contract_head: revision,
  member_head: z.string().max(256000), estimate_revision: revision, management_revision: revision,
  price_version: z.string().min(1).max(128), wallet_multiplier: money, periods: z.array(period).min(1).max(2),
  settlement_period: period, remaining: amount, reserve: amount, members: z.array(member).min(1).max(10_000),
}).strict().refine(value => new Set(value.members.map(row => row.participant_id)).size === value.members.length
  && new Set(value.members.map(row => row.user_id)).size === value.members.length
  && value.periods.some(row => JSON.stringify(row) === JSON.stringify(value.settlement_period)));
const wallet = z.object({ participant_id: objectID, participant_revision: revision, binding_id: revision, connection_revision: revision,
  source_id: objectID, latest_id: revision, user_id: revision, recommendation_id: revision, basis_hash: hash }).strict();
const scope = z.object({ simulation, source_id: objectID, connection_revision: revision, policy_revision: revision,
  expires_at: timestamp, latest_deadline: timestamp, accounts: z.array(account).min(1).max(10_000), wallets: z.array(wallet).min(1).max(10_000) }).strict().refine(value => {
  const wallets = new Map(value.wallets.map(row => [row.participant_id, row]));
  return wallets.size === value.wallets.length && new Set(value.accounts.map(row => row.account_id)).size === value.accounts.length
    && value.wallets.every(row => row.source_id === value.source_id && row.connection_revision === value.connection_revision)
    && value.latest_deadline === value.accounts.flatMap(row => row.periods).reduce((earliest, row) => (usageNanos(row.end_at) ?? 0n) < (usageNanos(earliest) ?? 0n) ? row.end_at : earliest, value.latest_deadline)
    && value.wallets.every(wallet => value.accounts.some(account => account.members.some(member => member.participant_id === wallet.participant_id)))
    && value.accounts.every(row => new Set(row.periods.map(period => period.kind)).size === row.periods.length && row.members.every(member => {
      const owner = wallets.get(member.participant_id);
      return owner?.binding_id === member.binding_id && owner.user_id === member.user_id;
    }));
});
const unavailable = z.object({
  can_start: z.literal(false), state: z.literal("unavailable"), reason: z.literal("FINANCIAL_APPLICATION_UNAVAILABLE"),
  round_state: z.literal("not_implemented"), rights_state: z.literal("not_implemented"), reminders_available: z.literal(false),
}).strict();
export const temporaryPreflight = z.union([
  z.object({ state: z.literal("ready"), reason: z.literal("CONDITIONAL_SIMULATION_ONLY"), scope_hash: hash, scope }).strict(),
  z.object({ state: z.literal("unavailable"), reason: z.enum(["FINANCIAL_APPLICATION_UNAVAILABLE", "TEMPORARY_EVIDENCE_INCOMPLETE", "TEMPORARY_EVIDENCE_LIMIT", "TEMPORARY_WALLET_CHANGED", "TEMPORARY_INPUT_CHANGED",
    "SIMULATION_WRITE_UNAVAILABLE", "SIMULATION_UNRESOLVED_APPLICATION", "SIMULATION_TARGET_UNAVAILABLE", "ZERO_TARGET_PENDING"]), scope_hash: z.literal(""), scope: z.null() }).strict(),
]);
const cap = z.object({ account_id: objectID, participant_id: objectID, amount: money }).strict();
export const temporaryStart = z.object({ request_id: objectID, scope_hash: hash, mode: z.enum(["carry", "no_carry"]), deadline: timestamp,
  caps: z.array(cap).min(1).max(20_000), acknowledged_participants: z.array(objectID).min(1).max(10_000), informed: z.boolean(), confirmed: z.literal(true) }).strict()
  .refine(value => (value.mode !== "no_carry" || value.informed) && new Set(value.acknowledged_participants).size === value.acknowledged_participants.length
    && new Set(value.caps.map(row => row.account_id + ":" + row.participant_id)).size === value.caps.length);
const exit = z.object({ exited_at: timestamp, reason: z.enum(["deadline", "period_changed", "terminated"]), recorded_at: timestamp, actor_id: objectID.nullable() }).strict()
  .refine(value => (usageNanos(value.exited_at) ?? 1n) <= (usageNanos(value.recorded_at) ?? 0n) && (value.reason === "terminated") === (value.actor_id !== null));
export const temporaryRound = z.object({ id: objectID, actor_id: objectID, started_at: timestamp, deadline: timestamp, mode: z.enum(["carry", "no_carry"]),
  scope, caps: z.array(cap).min(1).max(250000), acknowledged_participants: z.array(objectID).min(1).max(10_000), informed: z.boolean(), exit: exit.nullable() }).strict().refine(value => {
  const start = usageNanos(value.started_at), deadline = usageNanos(value.deadline), last = usageNanos(value.scope.latest_deadline);
  const caps = new Map(value.caps.map(row => [row.account_id + ":" + row.participant_id, units(row.amount)]));
  const acknowledged = new Set(value.acknowledged_participants), targets = new Map<string, bigint>();
  if (caps.size !== value.caps.length || acknowledged.size !== value.acknowledged_participants.length || acknowledged.size !== value.scope.wallets.length
    || !value.scope.wallets.every(row => acknowledged.has(row.participant_id))
    || value.caps.length !== value.scope.accounts.reduce((count, row) => count + row.members.length, 0)) return false;
  for (const account of value.scope.accounts) {
    let total = 0n;
    for (const member of account.members) {
      const cap = caps.get(account.account_id + ":" + member.participant_id);
      if (cap === undefined) return false;
      total += cap; targets.set(member.participant_id, (targets.get(member.participant_id) ?? 0n) + cap);
    }
    if (total * BigInt(account.remaining.denominator) > BigInt(account.remaining.numerator) * 100000000n) return false;
  }
  return [...targets.values()].every(value => value > 0n)
    && start !== null && deadline !== null && last !== null && start < deadline && deadline <= last && (value.mode !== "no_carry" || value.informed)
    && (value.exit === null || (usageNanos(value.exit.exited_at) ?? start - 1n) >= start && (usageNanos(value.exit.exited_at) ?? deadline + 1n) <= deadline);
});
const adjustments = z.record(objectID, signed);
export const temporarySettlement = z.object({ round_id: objectID, account_id: objectID,
  state: z.enum(["pending_evidence", "settled", "no_carry", "no_borrowing", "cancelled"]), reason: z.string().min(1).max(128),
  original_period: period, next_period: period.nullable(), ledger_revision: z.union([z.literal(""), revision]), closed_through: timestamp.nullable(),
  round_spent: z.record(objectID, money), period_spent: z.record(objectID, money), adjustments, residuals: z.record(objectID, amount),
}).strict().refine(value => {
  if (value.state === "settled") {
    if (value.next_period === null || value.closed_through === null || value.ledger_revision === ""
      || value.next_period.kind !== value.original_period.kind || value.next_period.start_at !== value.original_period.end_at
      || (usageNanos(value.closed_through) ?? 0n) < (usageNanos(value.original_period.end_at) ?? 1n)
      || Object.keys(value.round_spent).length !== Object.keys(value.period_spent).length
      || !Object.entries(value.round_spent).every(([id, spent]) => value.period_spent[id] !== undefined && units(spent) <= units(value.period_spent[id]))
      || !Object.keys(value.adjustments).every(id => value.round_spent[id] !== undefined)
      || Object.values(value.adjustments).reduce((sum, value) => sum + units(value), 0n) !== 0n) return false;
    return true;
  }
  return Object.keys(value.adjustments).length === 0;
});
export const temporaryReminders = z.object({ revision: count, enabled: z.boolean(), threshold: money.nullable() }).strict().refine(value =>
  value.enabled ? value.revision !== "0" && value.threshold !== null && Number(value.threshold) > 0 && Number(value.threshold) < 100 : value.threshold === null);
export const temporaryRights = z.object({ round_id: objectID, account_id: objectID, revision: count, period: period.nullable(), adjustments }).strict();
export const temporaryRightsChange = z.object({ request_id: objectID, round_id: objectID, account_id: objectID, participant_id: objectID,
  binding_id: revision, revision: count, period, after: signed, reason: z.string().min(1).max(500).refine(value => value.trim().length > 0) }).strict();
export const temporaryRightsReceipt = temporaryRightsChange.extend({ before: signed, delta: signed, recorded_at: timestamp }).strict()
  .refine(value => units(value.after) - units(value.before) === units(value.delta));
const configured = z.object({
  can_start: z.literal(false), state: z.enum(["idle", "active", "exited"]), reason: z.enum(["TEMPORARY_PREFLIGHT_REQUIRED", "CONDITIONAL_SIMULATION_ONLY"]),
  round_state: z.enum(["idle", "active", "exited"]), rights_state: z.enum(["none", "pending_evidence", "processed"]),
  reminders_available: z.boolean(), simulation, round: temporaryRound.optional(), settlements: z.array(temporarySettlement).max(10_000).optional(),
  restorations: z.array(z.object({ participant_id: objectID, advice_state: z.enum(["temporary_active", "recalculation_required", "ordinary_restored"]),
    remote_state: z.enum(["temporary_active", "remote_restore_pending", "not_required", "remote_restored"]), application_id: revision.nullable() }).strict()).max(10_000).optional(),
  reminders: temporaryReminders.optional(),
}).strict().refine(value => value.state === value.round_state && (value.state === "idle" ? value.round === undefined && !value.reminders_available
  : value.round !== undefined && value.round.scope.simulation.epoch === value.simulation.epoch && (value.state === "active") === (value.round.exit === null)));
export const temporaryStatus = z.union([unavailable, configured]);
export const temporaryRoundDetails = z.object({ round: temporaryRound, settlements: z.array(temporarySettlement).max(10_000), reminders: temporaryReminders }).strict()
  .refine(value => new Set(value.settlements.map(row => row.account_id)).size === value.settlements.length
    && value.settlements.every(row => row.round_id === value.round.id && value.round.scope.accounts.some(account => account.account_id === row.account_id)));
const summary = z.object({id: objectID, source_id: objectID, simulation, started_at: timestamp, deadline: timestamp, mode: z.enum(["carry", "no_carry"]),
  exit: exit.nullable(), accounts: z.number().int().min(1).max(10_000), rights_state: z.enum(["none", "pending_evidence", "processed"])}).strict()
  .refine(value => (usageNanos(value.started_at) ?? 1n) < (usageNanos(value.deadline) ?? 0n)
    && (value.exit === null ? value.rights_state === "none" : (usageNanos(value.exit.exited_at) ?? 0n) >= (usageNanos(value.started_at) ?? 1n)
      && (usageNanos(value.exit.exited_at) ?? 1n) <= (usageNanos(value.deadline) ?? 0n)));
export const temporaryHistory = z.object({rounds: z.array(summary).max(100), next_after: objectID.nullable()}).strict()
  .refine(value => value.rounds.every((row, index) => { const before = value.rounds[index-1]; if (!before) return true;
    const a = usageNanos(before.started_at), b = usageNanos(row.started_at);
    return a !== null && b !== null && (a > b || a === b && before.id > row.id);
  }) && (value.next_after === null || value.rounds.at(-1)?.id === value.next_after));
const base = oc.errors({
  BAD_REQUEST: { status: 400, data: authErrorData }, UNAUTHORIZED: { status: 401, data: authErrorData },
  FORBIDDEN: { status: 403, data: authErrorData }, SOURCE_BLOCKED: { status: 403, data: authErrorData },
  NOT_FOUND: { status: 404, data: authErrorData }, CONFLICT: { status: 409, data: authErrorData }, TOO_MANY_REQUESTS: { status: 429, data: authErrorData },
  SECRET_STORAGE_UNAVAILABLE: { status: 503, data: authErrorData }, INTERNAL_SERVER_ERROR: { status: 500, data: authErrorData },
});
export const temporaryOutputs = { status: temporaryStatus, preflight: temporaryPreflight, round: temporaryRoundDetails, history: temporaryHistory, stop: temporaryRound,
  start: temporaryRound, rights: temporaryRights, rightsChange: temporaryRightsReceipt, reminders: temporaryReminders };
export const temporaryContract = {
  status: base.route({ method: "POST", path: "/api/v1/temporary/status" }).input(temporaryInput).output(temporaryStatus),
  preflight: base.route({ method: "POST", path: "/api/v1/temporary/preflight" }).input(temporaryInput).output(temporaryPreflight),
  start: base.route({ method: "POST", path: "/api/v1/temporary/start" }).input(temporaryStart).output(temporaryRound),
  round: base.route({ method: "POST", path: "/api/v1/temporary/round" }).input(z.object({ round_id: objectID }).strict()).output(temporaryRoundDetails),
  history: base.route({ method: "POST", path: "/api/v1/temporary/history" }).input(z.object({after: z.union([z.literal(""), objectID]), limit: z.number().int().min(1).max(100)}).strict()).output(temporaryHistory),
  stop: base.route({ method: "POST", path: "/api/v1/temporary/stop" }).input(z.object({ round_id: objectID }).strict()).output(temporaryRound),
  rights: base.route({ method: "POST", path: "/api/v1/temporary/rights" }).input(z.object({ round_id: objectID, account_id: objectID }).strict()).output(temporaryRights),
  rightsChange: base.route({ method: "POST", path: "/api/v1/temporary/rights-change" }).input(temporaryRightsChange).output(temporaryRightsReceipt),
  reminders: base.route({ method: "POST", path: "/api/v1/temporary/reminders" }).input(z.object({ round_id: objectID, revision: count, enabled: z.boolean(), threshold: z.string() }).strict()
    .refine(value => value.enabled ? money.safeParse(value.threshold).success && Number(value.threshold) > 0 && Number(value.threshold) < 100 : value.threshold === "")).output(temporaryReminders),
};
export type TemporaryStatus = z.infer<typeof temporaryStatus>;
