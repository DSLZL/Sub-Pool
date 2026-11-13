import type { createWalletApplicationsClient, createWalletsClient } from "@sub-pool/contracts/client";
import type { WalletView } from "@sub-pool/contracts/wallets";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";

type WalletsClient = ReturnType<typeof createWalletsClient>;
type ApplicationsClient = ReturnType<typeof createWalletApplicationsClient>;
export type WalletAutomation = Awaited<ReturnType<ApplicationsClient["automation"]>>;
export type WalletData = { wallets: WalletView[]; operations: WalletApplication[]; automation: WalletAutomation | null };
export class WalletReadError extends Error {}
const participantLimit = 500, operationLimit = 2000;

export async function readWalletData(wallets: Pick<WalletsClient, "list" | "get">, applications: Pick<ApplicationsClient, "list" | "automation">, admin: boolean, signal?: AbortSignal): Promise<WalletData> {
  const summaries: Awaited<ReturnType<WalletsClient["list"]>>["participants"] = [];
  const participantIDs = new Set<string>(), cursors = new Set<string>();
  let after = "";
  for (;;) {
    if (cursors.has(after)) throw new WalletReadError("参与者分页重复，钱包列表未完整读取。");
    cursors.add(after);
    const page = await wallets.list({ after, limit: 100 }, { signal });
    for (const row of page.participants) {
      if (participantIDs.has(row.participant_id) || summaries.length >= participantLimit) throw new WalletReadError("钱包列表重复或超过 500 位参与者的读取上限。");
      participantIDs.add(row.participant_id); summaries.push(row);
    }
    if (page.next_after === null) break;
    if (page.participants.at(-1)?.participant_id !== page.next_after) throw new WalletReadError("参与者分页游标与记录不一致。");
    after = page.next_after;
  }
  const operations: WalletApplication[] = [];
  let cursor = "0";
  for (;;) {
    const page = await applications.list({ participant_id: "", start_at: "", end_at: "", after: cursor, limit: 100 }, { signal });
    if (page.participant_id || page.start_at || page.end_at) throw new WalletReadError("请求历史返回了不同的筛选范围。");
    for (const row of page.requests) {
      if (BigInt(row.id) <= BigInt(operations.at(-1)?.id ?? cursor) || operations.length >= operationLimit) throw new WalletReadError("请求历史顺序重复或超过 2000 条的读取上限。");
      if ((row.visibility === "administrator") !== admin) throw new WalletReadError("请求历史与当前身份范围不一致。");
      operations.push(row);
    }
    if (page.next_after === null) break;
    if (page.requests.at(-1)?.id !== page.next_after || BigInt(page.next_after) <= BigInt(cursor)) throw new WalletReadError("请求历史分页没有前进。");
    cursor = page.next_after;
  }
  const result: WalletView[] = [];
  // Bound concurrent reads; a changed reference or failed later page invalidates the whole view.
  for (let index = 0; index < summaries.length; index += 4) {
    const batch = await Promise.all(summaries.slice(index, index + 4).map(async summary => {
      const view = await wallets.get({ participant_id: summary.participant_id }, { signal });
      if (Object.entries(summary).some(([key, value]) => view[key as keyof WalletView] !== value)
        || (view.visibility === "all_recorded_sources") !== admin) throw new WalletReadError("钱包身份或版本已变化，请重新读取完整列表。");
      return view;
    }));
    result.push(...batch);
  }
  const automation = admin ? await applications.automation({}, { signal }) : null;
  return { wallets: result, operations, automation };
}

export function walletOperation(row: WalletApplication) {
  return {
    id: row.id, at: row.attempted_at, actor: row.details?.actor_id ?? "—",
    participant: row.participant_id, participantName: row.participant_name,
    rec: row.details?.input.recommendation_id === "0" ? "未生成" : row.details?.input.recommendation_id ?? "—",
    before: null, target: row.target, after: null, outcome: row.state,
    note: row.details?.reasons.includes("RECOMMENDATION_STALE") ? "依据已失效；请求已拒绝，未向上游派发。" : "请求已拒绝，未向上游派发；没有远端生效凭证。",
    record: row,
  };
}
export type WalletOperation = ReturnType<typeof walletOperation>;

export function walletRow(view: WalletView, operations: WalletApplication[]) {
  const receipt = view.last_confirmed_read ?? view.latest_read;
  const latest = view.latest_read, recommendation = view.latest_recommendation;
  const last = operations.filter(row => row.participant_id === view.participant_id).at(-1);
  const recState = view.current_state === "recalculation_required" ? "stale" : recommendation ? "blocked" : "insufficient";
  const attempt = latest ? `最近尝试 #${latest.id} · ${latest.attempted_at}；${latest.read.state === "available" ? "字段按原生权限单独判断" : "未取得完整原生读数"}。` : "尚无钱包读取记录。";
  let sourceNote = "";
  if (view.visibility === "all_recorded_sources") {
    const sources = latest?.usage_sources;
    if (!sources) sourceNote = " 尚未记录原生用量来源扫描。";
    else if (sources.state === "failed") sourceNote = " 原生用量来源扫描失败，未确认任何来源。";
    else {
      const managed = sources.accounts.filter(row => row.managed_account_id !== null).length;
      sourceNote = ` 原生用量来源扫描${sources.state === "exhausted" ? "已结束" : "未完成，仅保留已核验部分"}；记录 ${sources.accounts.length} 个来源（读取时已纳管 ${managed} 个、未纳管 ${sources.accounts.length - managed} 个）。`;
    }
    sourceNote += " 来源扫描不代表完整消费或钱包扣款。";
  }
  return {
    id: view.participant_id, name: view.name, upstreamUserId: view.upstream_user_id, enabled: view.enabled,
    balance: receipt?.balance.state === "reported" ? receipt.balance.value : null,
    frozen: receipt?.frozen_balance.state === "reported" ? receipt.frozen_balance.value : null,
    balanceReadAt: receipt?.read_at ?? null, recommendation: view.current_target,
    recState, recReason: `${view.current_state === "recalculation_required" ? "建议依据已变化，历史解释保留。" : recommendation ? "当前建议依据不足，无法形成可应用金额。" : "尚未生成可确认建议。"} 钱包整体归属、扣费覆盖、冻结并发及币种精度条件未通过；应用保持不可用。`,
    readNote: `${view.last_confirmed_read ? `展示最近完整确认读数 #${view.last_confirmed_read.id}。` : "没有完整确认读数。"}${attempt}${view.read_references_current ? "" : "读取来源或版本未确认当前有效。"} 原生钱包单位，币种未核验。${sourceNote}`,
    contributions: view.contributions.map(row => ({ account: row.account_id, amount: row.amount, state: row.state === "source_unavailable" ? "来源不可用" : row.state === "contract_unavailable" ? "份额依据不可用" : "费用与钱包扣费依据不足" })),
    lastApplied: last ? walletOperation(last) : null, view,
  };
}
export type WalletRow = ReturnType<typeof walletRow>;
