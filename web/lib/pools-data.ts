import type { createParticipantsClient, createPoolsClient } from "@sub-pool/contracts/client";
import type { Participant, ParticipantExit, ParticipantDeletion, AttributedUsage, UsageHistory } from "@sub-pool/contracts/participants";
import type { PoolContract, PoolSummary } from "@sub-pool/contracts/pools";
import { poolContract, sharePercent, shareUnits } from "@sub-pool/contracts/pools";
import { usageNanos } from "@sub-pool/contracts/usage";

type ParticipantsClient = ReturnType<typeof createParticipantsClient>;
type PoolsClient = ReturnType<typeof createPoolsClient>;
type ParticipantPage = Awaited<ReturnType<ParticipantsClient["list"]>>;
type CandidatePage = Awaited<ReturnType<ParticipantsClient["candidates"]>>;
type PoolPage = Awaited<ReturnType<PoolsClient["list"]>>;
type HistoryPage = Awaited<ReturnType<PoolsClient["history"]>>;
type CapturePage = Awaited<ReturnType<ParticipantsClient["captures"]>>;

export function checkParticipantSave(saved: Participant, input: Parameters<ParticipantsClient["create"]>[0], original: Participant | null, acknowledgement: Participant | null = null) {
  const sameBinding = original?.binding.source_id === input.source_id && original.binding.upstream_user_id === input.upstream_id;
  if (saved.revision !== (original ? String(BigInt(original.revision) + 1n) : "1") || original && (saved.id !== original.id || usageNanos(saved.created_at) !== usageNanos(original.created_at))
    || saved.name !== input.name || saved.contact !== input.contact || saved.notes !== input.notes || saved.enabled !== input.enabled
    || !saved.source_current || saved.binding.source_id !== input.source_id || saved.binding.upstream_user_id !== input.upstream_id
    || sameBinding && (saved.binding.id !== original.binding.id || usageNanos(saved.binding.started_at) !== usageNanos(original.binding.started_at))
    || acknowledgement && (saved.id !== acknowledgement.id || usageNanos(saved.created_at) !== usageNanos(acknowledgement.created_at)
      || usageNanos(saved.saved_at) !== usageNanos(acknowledgement.saved_at) || saved.binding.id !== acknowledgement.binding.id || usageNanos(saved.binding.started_at) !== usageNanos(acknowledgement.binding.started_at))) {
    throw new Error("参与者待核对。");
  }
}

export function checkPoolSave(saved: PoolContract, input: Parameters<PoolsClient["create"]>[0] | Parameters<PoolsClient["update"]>[0], acknowledgement: PoolContract | null = null) {
  const accounts = new Map(input.accounts.map(account => [account.id, account]));
  const participants = new Map(input.participants.map(member => [member.id, member]));
  const savedAccounts = new Map(acknowledgement?.accounts.map(account => [account.id, account]));
  const savedParticipants = new Map(acknowledgement?.participants.map(member => [member.id, member]));
  if (("id" in input ? saved.pool_id !== input.id || BigInt(saved.revision) !== BigInt(input.revision) + 1n : saved.revision !== "1") || !saved.is_current || saved.current_revision !== saved.revision
    || !saved.source_current || saved.visibility !== "all_members" || saved.source_id !== input.source_id || saved.connection_revision !== input.connection_revision
    || saved.name !== input.name || saved.enabled !== input.enabled || saved.accounts.length !== accounts.size || saved.participants.length !== participants.size
    || saved.accounts.some(account => accounts.get(account.id)?.revision !== account.revision)
    || saved.participants.some(member => { const expected = participants.get(member.id); return !expected || expected.revision !== member.revision || shareUnits(expected.share_percent) !== shareUnits(member.share_percent); })
    || acknowledgement && (saved.id !== acknowledgement.id || saved.pool_id !== acknowledgement.pool_id || usageNanos(saved.effective_at) !== usageNanos(acknowledgement.effective_at)
      || saved.accounts.some(account => { const prior = savedAccounts.get(account.id); return !prior || prior.source_id !== account.source_id || prior.upstream_id !== account.upstream_id || prior.name !== account.name; })
      || saved.participants.some(member => { const prior = savedParticipants.get(member.id); return !prior || prior.binding_id !== member.binding_id || prior.name !== member.name; }))) {
    throw new Error("额度池待核对。");
  }
}

export function checkParticipantExit(receipt: ParticipantExit, target: Participant, acknowledgement: ParticipantExit | null = null) {
  if (receipt.participant_id !== target.id || receipt.previous_revision !== target.revision || BigInt(receipt.revision) !== BigInt(target.revision) + 1n
    || receipt.binding_id !== target.binding.id || receipt.name !== target.name
    || acknowledgement && (receipt.effective_at !== acknowledgement.effective_at || JSON.stringify(receipt.pool_contract_ids) !== JSON.stringify(acknowledgement.pool_contract_ids))) {
    throw new Error("退出待核对。");
  }
}

export function checkParticipantDeletion(receipt: ParticipantDeletion, target: Participant) {
  const effective = usageNanos(receipt.effective_at), saved = usageNanos(target.saved_at);
  if (receipt.participant_id !== target.id || receipt.previous_revision !== target.revision || BigInt(receipt.revision) !== BigInt(target.revision) + 1n
    || receipt.binding_id !== target.binding.id || receipt.name !== target.name || effective === null || saved === null || effective < saved) {
    throw new Error("删除待核对。");
  }
}

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

export async function readPoolHistoryPage(read: (after: string) => Promise<HistoryPage>, after: string) {
  const page = await read(after);
  for (const [index, contract] of page.contracts.entries()) {
    if (BigInt(contract.id) <= BigInt(page.contracts[index - 1]?.id ?? after)) throw new Error("Invalid contract continuation");
  }
  if (page.next_after && (page.next_after !== page.contracts.at(-1)?.id || BigInt(page.next_after) <= BigInt(after))) throw new Error("Invalid history cursor");
  return page;
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

export async function readPoolMemberPages(client: Pick<PoolsClient, "members">, summary: PoolSummary, scopeRevision: string | undefined, signal: AbortSignal) {
  const members: PoolContract["participants"] = [], ids = new Set<string>();
  let after = "", expected: string | undefined;
  for (;;) {
    const { participants, next_after, scope_revision, ...contract } = await client.members({ id: summary.id, after, limit: 100 }, { signal });
    const signature = JSON.stringify({ ...contract, scope_revision });
    if (contract.id !== summary.id || contract.is_current !== summary.is_current
      || scopeRevision !== undefined && scopeRevision !== scope_revision || expected !== undefined && signature !== expected) {
      throw new Error("合同或权限变更。");
    }
    expected = signature;
    for (const member of participants) {
      if (ids.has(member.id)) throw new Error("成员重复。");
      ids.add(member.id);
      members.push(member);
    }
    if (next_after === null) return poolContract.parse({ ...contract, participants: members });
    if (next_after !== participants.at(-1)?.id || next_after === after) throw new Error("续页失败。");
    after = next_after;
  }
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

export type RecordedUsage = { view: AttributedUsage | UsageHistory | null; byParticipant: Record<string, AttributedUsage | UsageHistory>; missingAccounts: string[] };
export class RecordedSelectionError extends Error {}

export async function readCaptureSelections(
  accounts: { id: string }[],
  captures: (account: string, after: string) => Promise<CapturePage>,
) {
  if (accounts.length > 20) throw new RecordedSelectionError("最多 20 个账号。");
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
  return { selections, missingAccounts };
}

export async function readRecordedUsage(
  accounts: { id: string }[], participants: { id: string }[],
  captures: (account: string, after: string) => Promise<CapturePage>,
  usage: (ids: string[], participant: string) => Promise<AttributedUsage>,
): Promise<RecordedUsage> {
  const { selections, missingAccounts } = await readCaptureSelections(accounts, captures);
  if (!selections.length) return { view: null, byParticipant: {}, missingAccounts };
  if (selections.some(item => usageNanos(item.start_at) !== usageNanos(selections[0].start_at) || usageNanos(item.end_at) !== usageNanos(selections[0].end_at))) throw new RecordedSelectionError("选择时段。");
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
