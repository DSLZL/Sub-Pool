import type { z } from "zod";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import type { temporaryRound, temporaryRoundDetails, temporaryHistory } from "@sub-pool/contracts/temporary";
import { usageNanos } from "@sub-pool/contracts/usage";

export type TemporaryClient = ReturnType<typeof createTemporaryClient>;
export type TemporaryRound = z.infer<typeof temporaryRound>;
export type TemporaryDetails = z.infer<typeof temporaryRoundDetails>;
export type TemporaryHistoryRow = z.infer<typeof temporaryHistory>["rounds"][number];

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

export async function readTemporaryHistory(client: Pick<TemporaryClient, "history">, signal?: AbortSignal, boundary: TemporaryHistoryRow | null = null) {
  const after = boundary?.id ?? "";
  const page = await client.history({ after, limit: 100 }, { signal });
  const seen = new Set<string>(after ? [after] : []);
  let previous = boundary;
  for (const row of page.rounds) {
    const before = previous ? usageNanos(previous.started_at) : null, now = usageNanos(row.started_at);
    if (seen.has(row.id) || now === null || previous && (before === null || before < now || before === now && previous.id <= row.id)) fail("历史顺序异常。");
    seen.add(row.id);
    previous = row;
  }
  if (page.next_after !== null && (page.next_after !== page.rounds.at(-1)?.id || page.next_after === after)) fail("历史无法续页。");
  return page;
}

export function checkTemporaryDetails(value: TemporaryDetails, expected: TemporaryRound | TemporaryHistoryRow) {
  checkTemporaryRound(value.round, expected);
}

function checkTemporaryRound(round: TemporaryRound, expected: TemporaryRound | TemporaryHistoryRow) {
  const source = "scope" in expected ? expected.scope.source_id : expected.source_id;
  if (round.id !== expected.id || round.mode !== expected.mode || !sameTime(round.started_at, expected.started_at) || !sameTime(round.deadline, expected.deadline)
    || round.scope.source_id !== source || !temporarySame(round.scope.simulation, "scope" in expected ? expected.scope.simulation : expected.simulation)
    || !("scope" in expected) && round.scope.accounts.length !== expected.accounts
    || expected.exit && (!round.exit || !sameTime(round.exit.exited_at, expected.exit.exited_at) || round.exit.reason !== expected.exit.reason || round.exit.actor_id !== expected.exit.actor_id)) fail("轮次不符。");
  if ("scope" in expected && !temporarySame({ ...round, exit: null }, { ...expected, exit: null })) fail("冻结记录不符。");
}
