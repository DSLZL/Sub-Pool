import type { createParticipantsClient, createPoolsClient } from "@sub-pool/contracts/client";
import type { Participant, AttributedUsage } from "@sub-pool/contracts/participants";
import type { PoolContract, PoolSummary } from "@sub-pool/contracts/pools";
import { sharePercent } from "@sub-pool/contracts/pools";
import { usageNanos } from "@sub-pool/contracts/usage";

type ParticipantsClient = ReturnType<typeof createParticipantsClient>;
type PoolsClient = ReturnType<typeof createPoolsClient>;
type ParticipantPage = Awaited<ReturnType<ParticipantsClient["list"]>>;
type CandidatePage = Awaited<ReturnType<ParticipantsClient["candidates"]>>;
type PoolPage = Awaited<ReturnType<PoolsClient["list"]>>;
type HistoryPage = Awaited<ReturnType<PoolsClient["history"]>>;
type CapturePage = Awaited<ReturnType<ParticipantsClient["captures"]>>;

export async function readParticipantPages(read: (after: string) => Promise<ParticipantPage>) {
  const participants: Participant[] = [], ids = new Set<string>(), cursors = new Set<string>();
  let after = "";
  do {
    const page = await read(after);
    for (const item of page.participants) {
      if (ids.has(item.id)) throw new Error("Repeated participant");
      ids.add(item.id); participants.push(item);
    }
    if (page.next_after && cursors.has(page.next_after)) throw new Error("Repeated participant cursor");
    if (page.next_after) cursors.add(page.next_after);
    after = page.next_after ?? "";
  } while (after);
  return participants;
}

export async function readPoolPages(read: (after: string) => Promise<PoolPage>) {
  const pools: PoolSummary[] = [], ids = new Set<string>(), cursors = new Set<string>();
  let after = "";
  do {
    const page = await read(after);
    for (const item of page.pools) {
      if (ids.has(item.pool_id) || !item.is_current) throw new Error("Invalid current pool");
      ids.add(item.pool_id); pools.push(item);
    }
    if (page.next_after && cursors.has(page.next_after)) throw new Error("Repeated pool cursor");
    if (page.next_after) cursors.add(page.next_after);
    after = page.next_after ?? "";
  } while (after);
  return pools;
}

export async function readPoolHistoryPages(read: (after: string) => Promise<HistoryPage>) {
  const contracts: PoolSummary[] = [];
  let after = "0";
  do {
    const page = await read(after);
    for (const item of page.contracts) {
      if (BigInt(item.id) <= BigInt(contracts.at(-1)?.id ?? after)) throw new Error("Invalid contract continuation");
      contracts.push(item);
    }
    if (page.next_after && (page.next_after !== contracts.at(-1)?.id || BigInt(page.next_after) <= BigInt(after))) throw new Error("Invalid history cursor");
    after = page.next_after ?? "0";
  } while (after !== "0");
  return contracts;
}

export async function readPoolContracts(summaries: PoolSummary[], read: (item: PoolSummary) => Promise<PoolContract>) {
  const contracts: PoolContract[] = [];
  for (let offset = 0; offset < summaries.length; offset += 4) {
    const batch = summaries.slice(offset, offset + 4);
    const results = await Promise.all(batch.map(read));
    for (let index = 0; index < batch.length; index++) {
      const item = batch[index], result = results[index];
      if (result.id !== item.id || result.pool_id !== item.pool_id || result.revision !== item.revision || result.effective_at !== item.effective_at) throw new Error("Pool changed during read");
      contracts.push(result);
    }
  }
  return contracts;
}

export async function readCandidatePages(read: (page: number) => Promise<CandidatePage>, source: string, revision: string) {
  const users: CandidatePage["users"] = [], ids = new Set<string>();
  let pageNumber = 1, total: string | null = null;
  for (;;) {
    const page = await read(pageNumber);
    if (page.source_id !== source || page.connection_revision !== revision || page.page !== pageNumber) throw new Error("Candidate source changed");
    if (page.read.state !== "available") return { users: [], read: page.read };
    if (page.total === null || total !== null && page.total !== total) throw new Error("Candidate coverage changed");
    total = page.total;
    for (const user of page.users) {
      if (ids.has(user.upstream_id)) throw new Error("Repeated candidate");
      ids.add(user.upstream_id); users.push(user);
    }
    if (page.next_page === null) {
      if (BigInt(users.length) !== BigInt(total)) throw new Error("Incomplete candidate coverage");
      return { users, read: page.read };
    }
    if (page.next_page !== pageNumber + 1) throw new Error("Invalid candidate continuation");
    pageNumber = page.next_page;
  }
}

export function shareDraft(draft: Record<string, string>) {
  let total = 0;
  for (const value of Object.values(draft)) {
    if (value === "") continue;
    if (!sharePercent.safeParse(value).success) return { valid: false, total: null, unallocated: null };
    const [whole, fraction = ""] = value.split(".");
    total += Number(whole) * 1000000 + Number(fraction.padEnd(6, "0"));
  }
  const format = (value: number) => `${Math.floor(value / 1000000)}.${String(value % 1000000).padStart(6, "0")}`.replace(/\.?0+$/, "");
  return { valid: total <= 100000000, total: format(total), unallocated: total <= 100000000 ? format(100000000 - total) : null };
}

export type RecordedUsage = { view: AttributedUsage | null; byParticipant: Record<string, AttributedUsage>; missingAccounts: string[] };
export class RecordedSelectionError extends Error {}

export async function readRecordedUsage(
  accounts: { id: string }[], participants: { id: string }[],
  captures: (account: string, after: string) => Promise<CapturePage>,
  usage: (ids: string[], participant: string) => Promise<AttributedUsage>,
): Promise<RecordedUsage> {
  if (accounts.length > 20) throw new RecordedSelectionError("当前界面缺少捕获选择入口；超过 20 个账号时不能静默截取部分账号。");
  const selections: CapturePage["captures"] = [], missingAccounts: string[] = [];
  for (let offset = 0; offset < accounts.length; offset += 4) {
    const results = await Promise.all(accounts.slice(offset, offset + 4).map(async account => {
      let after = "0", latest: CapturePage["captures"][number] | null = null;
      do {
        const page = await captures(account.id, after);
        let previous = after;
        for (const item of page.captures) {
          if (item.account_id !== account.id || BigInt(item.id) <= BigInt(previous)) throw new Error("Capture selection changed");
          previous = item.id;
          if (!latest || usageNanos(item.captured_at)! > usageNanos(latest.captured_at)! || usageNanos(item.captured_at) === usageNanos(latest.captured_at) && BigInt(item.id) > BigInt(latest.id)) latest = item;
        }
        if (page.next_after && (page.next_after !== previous || BigInt(page.next_after) <= BigInt(after))) throw new Error("Invalid capture continuation");
        after = page.next_after ?? "0";
      } while (after !== "0");
      return { account: account.id, latest };
    }));
    for (const result of results) {
      if (result.latest) selections.push(result.latest);
      else missingAccounts.push(result.account);
    }
  }
  if (!selections.length) return { view: null, byParticipant: {}, missingAccounts };
  if (selections.some(item => usageNanos(item.start_at) !== usageNanos(selections[0].start_at) || usageNanos(item.end_at) !== usageNanos(selections[0].end_at))) throw new RecordedSelectionError("各账号最近捕获的区间不同；当前界面缺少区间选择入口，不能拼成同一周期。");
  const ids = selections.map(item => item.id), view = await usage(ids, ""), byParticipant: RecordedUsage["byParticipant"] = {};
  const matches = (item: AttributedUsage) => usageNanos(item.start_at) === usageNanos(selections[0].start_at) && usageNanos(item.end_at) === usageNanos(selections[0].end_at)
    && item.groups.every(group => selections.some(selection => selection.account_id === group.account_id));
  if (!matches(view)) throw new Error("Usage interval changed");
  const assigned = participants.filter(item => view.groups.some(group => group.participant_id === item.id));
  for (let offset = 0; offset < assigned.length; offset += 4) {
    const batch = assigned.slice(offset, offset + 4), results = await Promise.all(batch.map(item => usage(ids, item.id)));
    for (let index = 0; index < batch.length; index++) {
      const item = results[index], id = batch[index].id;
      if (!matches(item) || item.groups.some(group => group.participant_id !== id)) throw new Error("Participant usage binding changed");
      byParticipant[id] = item;
    }
  }
  return { view, byParticipant, missingAccounts };
}
