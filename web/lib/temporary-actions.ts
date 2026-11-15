import type { z } from "zod";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import { temporaryPreflight, temporaryStart, temporaryRound, temporaryRoundDetails, temporaryRights, temporaryRightsChange, temporaryRightsReceipt, temporaryReminders, temporaryHistory } from "@sub-pool/contracts/temporary";
import { usageNanos } from "@sub-pool/contracts/usage";

export type TemporaryClient = ReturnType<typeof createTemporaryClient>;
export type TemporaryPreflight = z.infer<typeof temporaryPreflight>;
export type TemporaryScope = Extract<TemporaryPreflight, { state: "ready" }>["scope"];
export type TemporaryRound = z.infer<typeof temporaryRound>;
export type TemporaryDetails = z.infer<typeof temporaryRoundDetails>;
export type TemporaryRights = z.infer<typeof temporaryRights>;
export type TemporaryHistoryRow = z.infer<typeof temporaryHistory>["rounds"][number];
export type TemporaryStart = z.infer<typeof temporaryStart>;
export type TemporaryRightsChange = z.infer<typeof temporaryRightsChange>;
export type TemporaryReminderInput = Parameters<TemporaryClient["reminders"]>[0];

export class TemporaryReadError extends Error {}
const fail = (message: string): never => { throw new TemporaryReadError(message); };
export const temporarySame = (a: unknown, b: unknown): boolean => {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b)) return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((value, index) => temporarySame(value, b[index]));
  if (a && b && typeof a === "object" && typeof b === "object") {
    const left = Object.entries(a), right = Object.entries(b);
    return left.length === right.length && left.every(([key, value]) => Object.hasOwn(b, key) && temporarySame(value, Reflect.get(b, key)));
  }
  return false;
};
const sameTime = (a: string, b: string) => usageNanos(a) !== null && usageNanos(a) === usageNanos(b);
const units = (value: string) => {
  const negative = value.startsWith("-"), [whole = "0", fraction = ""] = (negative ? value.slice(1) : value).split(".");
  const amount = BigInt(whole) * 100000000n + BigInt(fraction.padEnd(8, "0"));
  return negative ? -amount : amount;
};

export async function readTemporaryHistory(client: Pick<TemporaryClient, "history">, signal?: AbortSignal) {
  const rounds: TemporaryHistoryRow[] = [], seen = new Set<string>();
  let after = "";
  for (;;) {
    const page = await client.history({ after, limit: 100 }, { signal });
    for (const row of page.rounds) {
      const previous = rounds.at(-1), before = previous ? usageNanos(previous.started_at) : null, now = usageNanos(row.started_at);
      if (seen.has(row.id) || now === null || previous && (before === null || before < now || before === now && previous.id <= row.id)) fail("轮次历史顺序或标识不一致，请重新读取。");
      seen.add(row.id); rounds.push(row);
    }
    if (rounds.length > 2000 || rounds.length === 2000 && page.next_after !== null) fail("轮次历史超过本页完整读取上限，不能显示截断列表。");
    if (page.next_after === null) return rounds;
    if (!page.rounds.length || page.next_after !== page.rounds.at(-1)?.id || page.next_after === after) fail("轮次历史游标未推进，请重新读取。");
    after = page.next_after;
  }
}

export function checkTemporaryDetails(value: TemporaryDetails, expected: TemporaryRound | TemporaryHistoryRow) {
  checkTemporaryRound(value.round, expected);
}

function checkTemporaryRound(round: TemporaryRound, expected: TemporaryRound | TemporaryHistoryRow) {
  const source = "scope" in expected ? expected.scope.source_id : expected.source_id;
  if (round.id !== expected.id || round.mode !== expected.mode || !sameTime(round.started_at, expected.started_at) || !sameTime(round.deadline, expected.deadline)
    || round.scope.source_id !== source || !temporarySame(round.scope.simulation, "scope" in expected ? expected.scope.simulation : expected.simulation)
    || !("scope" in expected) && round.scope.accounts.length !== expected.accounts
    || expected.exit && (!round.exit || !sameTime(round.exit.exited_at, expected.exit.exited_at) || round.exit.reason !== expected.exit.reason || round.exit.actor_id !== expected.exit.actor_id)) fail("轮次详情与当前选择不一致。");
  if ("scope" in expected && !temporarySame({ ...round, exit: null }, { ...expected, exit: null })) fail("轮次冻结依据发生不一致。");
}

export function buildTemporaryStart(preflight: TemporaryPreflight, input: Omit<TemporaryStart, "scope_hash" | "confirmed">, now = Date.now()) {
  if (preflight.state !== "ready") return fail("启用前核验尚未就绪。");
  const result = temporaryStart.parse({ ...input, scope_hash: preflight.scope_hash, confirmed: true });
  const deadline = usageNanos(result.deadline), latest = usageNanos(preflight.scope.latest_deadline), expiry = usageNanos(preflight.scope.expires_at);
  if (deadline === null || latest === null || expiry === null || deadline <= BigInt(now) * 1000000n || deadline > latest || expiry <= BigInt(now) * 1000000n) fail("核验已过期，或截止时间不在允许范围内。");
  const pairs = new Set(preflight.scope.accounts.flatMap(account => account.members.map(member => account.account_id + ":" + member.participant_id)));
  if (result.caps.length !== pairs.size || result.caps.some(cap => !pairs.has(cap.account_id + ":" + cap.participant_id))
    || result.acknowledged_participants.length !== preflight.scope.wallets.length || !preflight.scope.wallets.every(wallet => result.acknowledged_participants.includes(wallet.participant_id))) fail("必须填写全部受影响成员的上限并逐一确认范围。");
  return result;
}

export function checkTemporaryStart(input: TemporaryStart, scope: TemporaryScope, actor: string, result: TemporaryRound) {
  if (result.actor_id !== actor || result.mode !== input.mode || !sameTime(result.deadline, input.deadline) || result.informed !== input.informed
    || !temporarySame(result.scope, scope) || !temporarySame(result.caps, input.caps) || !temporarySame(result.acknowledged_participants, input.acknowledged_participants)) fail("开启回执与提交依据不一致，结果待核对。");
}

export function checkTemporaryStop(before: TemporaryRound, result: TemporaryRound) {
  checkTemporaryRound(result, before);
  if (!result.exit) fail("终止回执尚未确认退出。");
}

export function checkTemporaryRights(value: TemporaryRights, round: TemporaryRound, accountID: string) {
  if (value.round_id !== round.id || value.account_id !== accountID || !round.scope.accounts.some(account => account.account_id === accountID)) fail("当前权益不属于所选轮次和账号。");
}

export function buildTemporaryRights(round: TemporaryRound, rights: TemporaryRights, participantID: string, after: string, reason: string, requestID: string) {
  checkTemporaryRights(rights, round, rights.account_id);
  const member = round.scope.accounts.find(account => account.account_id === rights.account_id)?.members.find(member => member.participant_id === participantID);
  if (!round.exit || !member || !rights.period) return fail("所选轮次尚未退出，或当前周期、冻结成员尚未确认，不能修订权益。");
  return temporaryRightsChange.parse({ request_id: requestID, round_id: round.id, account_id: rights.account_id, participant_id: participantID,
    binding_id: member.binding_id, revision: rights.revision, period: rights.period, after, reason: reason.trim() });
}

export function checkTemporaryRightsReceipt(input: TemporaryRightsChange, before: TemporaryRights, value: z.infer<typeof temporaryRightsReceipt>) {
  const actual = Object.fromEntries(Object.keys(input).map(key => [key, Reflect.get(value, key)]));
  if (!temporarySame(input, actual) || units(value.before) !== units(before.adjustments[input.participant_id] ?? "0")) fail("权益回执与提交或原权益不一致，结果待核对。");
}

export function checkTemporaryReminders(input: TemporaryReminderInput, value: z.infer<typeof temporaryReminders>) {
  if (BigInt(value.revision) <= BigInt(input.revision) || value.enabled !== input.enabled || value.threshold !== (input.enabled ? input.threshold : null)) fail("本轮提醒回执与提交不一致，结果待核对。");
}
