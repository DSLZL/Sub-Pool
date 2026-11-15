import type { createWalletApplicationsClient, createWalletsClient } from "@sub-pool/contracts/client";
import { walletRead, type WalletView, type WalletRead, type WalletRecommendation } from "@sub-pool/contracts/wallets";
import { usageNanos } from "@sub-pool/contracts/usage";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { estimateAmountText } from "./estimates-data";

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
    id: row.id, at: row.attempted_at, actor: (row.details?.actor_id ?? "—") + (row.unit === "simulated_wallet_usd" && row.details?.automation ? ` · 自动 #${row.details.automation.control_revision}` : ""),
    participant: row.participant_id, participantName: row.participant_name,
    rec: row.details?.input.recommendation_id === "0" ? "未生成" : row.details?.input.recommendation_id ?? "—",
    before: row.unit === "simulated_wallet_usd" ? row.details?.simulation_write?.before.balance ?? null : null,
    target: row.target === null ? null : row.details?.simulation_write?.request.target ?? estimateAmountText(row.target),
    after: row.unit === "simulated_wallet_usd" && ["applied", "failed"].includes(row.state) ? row.details?.simulation_proof?.after.balance ?? null : null,
    outcome: row.state === "applied" ? "confirmed" : row.state,
    note: row.state === "applied" ? "仅模拟协议：操作证明与独立钱包读取已核对，已确认生效。"
      : row.state === "failed" ? "仅模拟协议：操作证明已确认拒绝；本次操作未改变钱包。"
      : row.state === "unknown" ? "结果未知，禁止重复派发；须只读核对操作证明与钱包读数。"
      : row.state === "pending" ? "模拟应用意图已持久化，等待独立结果核对。"
      : row.reason === "SIMULATION_UNRESOLVED_APPLICATION" ? "原操作结果未核实，本次自动建议已跳过；没有重复派发。"
      : row.reason === "ZERO_TARGET_PENDING" ? "目标按来源精度量化后为零，本次未派发。"
      : row.reason === "SIMULATION_PRECONDITION_CHANGED" ? "当前权限、来源或钱包前提已变化，本次未派发。"
      : row.details?.reasons.includes("RECOMMENDATION_STALE") ? "依据已失效；请求已拒绝，未向上游派发。" : "请求已拒绝，未向上游派发；没有远端生效凭证。",
    record: row,
  };
}
export type WalletOperation = ReturnType<typeof walletOperation>;

export function walletRow(view: WalletView, operations: WalletApplication[]) {
  const receipt = view.last_confirmed_read ?? view.latest_read;
  const latest = view.latest_read, recommendation = view.latest_recommendation;
  const last = operations.filter(row => row.participant_id === view.participant_id).at(-1);
  const recState = view.current_state === "recalculation_required" ? "stale" : view.current_state === "scenario" ? "scenario" : recommendation ? "blocked" : "insufficient";
  const simulated = receipt?.simulation !== undefined;
  const attempt = latest ? `最近尝试 #${latest.id} · ${latest.attempted_at}；${latest.read.state === "available" ? latest.simulation ? "模拟钱包字段已独立读取" : "字段按原生权限单独判断" : "未取得完整读数"}。` : "尚无钱包读取记录。";
  let sourceNote = "";
  if (view.visibility === "all_recorded_sources") {
    const sources = latest?.usage_sources;
    if (latest?.simulation_scope) sourceNote = ` 模拟钱包声明 ${latest.simulation_scope.source_account_ids.length} 个来源；${latest.simulation_scope.scope_complete && latest.simulation_scope.ledger_closed ? "已封闭账本范围" : "整体来源或账本尚未核验"}。`;
    else if (!sources) sourceNote = " 尚未记录原生用量来源扫描。";
    else if (sources.state === "failed") sourceNote = " 原生用量来源扫描失败，未确认任何来源。";
    else {
      const managed = sources.accounts.filter(row => row.managed_account_id !== null).length;
      sourceNote = ` 原生用量来源扫描${sources.state === "exhausted" ? "已结束" : "未完成，仅保留已核验部分"}；记录 ${sources.accounts.length} 个来源（读取时已纳管 ${managed} 个、未纳管 ${sources.accounts.length - managed} 个）。`;
    }
    if (!latest?.simulation) sourceNote += " 来源扫描不代表完整消费或钱包扣款。";
  }
  return {
    id: view.participant_id, name: view.name, upstreamUserId: view.upstream_user_id, enabled: view.enabled,
    balance: receipt?.balance.state === "reported" ? receipt.balance.value : null,
    frozen: receipt?.frozen_balance.state === "reported" ? receipt.frozen_balance.value : null,
    balanceReadAt: receipt?.read_at ?? null, recommendation: view.current_target === null ? null : estimateAmountText(view.current_target),
    recState, recReason: view.current_state === "scenario" ? "模拟协议条件化建议：完整周期实际扣款、历史份额与全部来源已核对；金额仅适用于该模拟实例，提交时仍需服务端核对当前绑定与全部前提。" : `${view.current_state === "recalculation_required" ? "建议依据已变化，历史解释保留。" : recommendation ? "当前建议依据不足，无法形成可应用金额。" : "尚未生成可确认建议。"} 钱包整体归属、扣费覆盖、冻结并发及币种精度条件未通过；应用保持不可用。`,
    readNote: `${view.last_confirmed_read ? `展示最近完整确认读数 #${view.last_confirmed_read.id}。` : "没有完整确认读数。"}${attempt}${view.read_references_current ? "" : "读取来源或版本未确认当前有效。"} ${simulated ? "模拟钱包 USD 扣款单位。" : "原生钱包单位，币种未核验。"}${sourceNote}`,
    simulated,
    contributions: view.contributions.map(row => ({ account: row.account_id, amount: row.amount === null ? null : estimateAmountText(row.amount), state: row.state === "scenario" ? "模拟协议条件贡献" : row.state === "allocation_deficit" ? "成员份额透支" : row.state === "source_unavailable" ? "来源不可用" : row.state === "contract_unavailable" ? "份额依据不可用" : "费用与钱包扣费依据不足" })),
    lastApplied: last ? walletOperation(last) : null, view,
  };
}
export type WalletRow = ReturnType<typeof walletRow>;

export type WalletHistory = { walletReads: WalletRead[]; recommendations: WalletRecommendation[] };
export async function readWalletHistory(client: Pick<WalletsClient, "history">, participant: string, signal?: AbortSignal): Promise<WalletHistory> {
  const history: WalletHistory = { walletReads: [], recommendations: [] };
  for (const kind of ["wallet", "recommendation"] as const) {
    let after = "0";
    for (;;) {
      const page = await client.history({ participant_id: participant, kind, after, limit: 100 }, { signal });
      const rows = kind === "wallet" ? page.wallet_reads : page.recommendations;
      if (page.participant_id !== participant || page.kind !== kind
        || (kind === "wallet" ? page.recommendations : page.wallet_reads).length
        || rows.some((row, i) => row.participant_id !== participant || BigInt(row.id) <= BigInt(i ? rows[i - 1]!.id : after))
        || page.next_after !== null && (rows.length === 0 || page.next_after !== rows.at(-1)!.id)) {
        throw new WalletReadError("历史分页或参与者不一致，未显示不完整记录。");
      }
      if ((kind === "wallet" ? history.walletReads : history.recommendations).length + rows.length > operationLimit) {
        throw new WalletReadError("历史超过 2000 条，无法确认完整范围；没有截断展示。");
      }
      if (kind === "wallet") history.walletReads.push(...page.wallet_reads);
      else history.recommendations.push(...page.recommendations);
      if (page.next_after === null) break;
      after = page.next_after;
    }
  }
  return history;
}

export function walletTimeRange(start: string, end: string) {
  const from = start.trim(), to = end.trim(), stamp = walletRead.shape.attempted_at;
  const startAt = from ? usageNanos(from) : null, endAt = to ? usageNanos(to) : null;
  const invalid = from !== "" && (!stamp.safeParse(from).success || startAt === null)
    || to !== "" && (!stamp.safeParse(to).success || endAt === null)
    || startAt !== null && endAt !== null && startAt >= endAt;
  return { start: startAt, end: endAt, error: invalid ? "请输入有效 UTC 时间（以 Z 结尾），结束时间须晚于开始时间。" : null };
}
export function walletTimeMatches(at: string, range: ReturnType<typeof walletTimeRange>) {
  const instant = usageNanos(at);
  return !range.error && instant !== null && (range.start === null || instant >= range.start) && (range.end === null || instant < range.end);
}
