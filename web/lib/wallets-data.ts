import { glossary } from "@/lib/glossary";
import type { createWalletApplicationsClient, createWalletsClient } from "@sub-pool/contracts/client";
import { walletRead, type WalletView, type WalletRead, type WalletRecommendation } from "@sub-pool/contracts/wallets";
import { usageNanos } from "@sub-pool/contracts/usage";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { estimateAmountText } from "./estimates-data";
import { displayTime } from "./connection-data";

type WalletsClient = ReturnType<typeof createWalletsClient>;
type ApplicationsClient = ReturnType<typeof createWalletApplicationsClient>;
export type WalletAutomation = Awaited<ReturnType<ApplicationsClient["automation"]>>;
type WalletCatalog = Awaited<ReturnType<WalletsClient["list"]>>["participants"];
export type WalletOperationFilter = Omit<Parameters<ApplicationsClient["list"]>[0], "after" | "limit">;
type WalletHistoryWindow = Pick<WalletOperationFilter, "start_at" | "end_at">;
export type WalletData = {
  wallets: WalletView[];
  catalog: WalletCatalog;
  nextAfter: string | null;
  operations: WalletApplication[];
  automation: WalletAutomation | null;
};
export class WalletReadError extends Error {}

export async function readWalletOperationPage(applications: Pick<ApplicationsClient, "list">, filter: WalletOperationFilter, after: string, admin: boolean, signal?: AbortSignal) {
  const range = walletTimeRange(filter.start_at, filter.end_at);
  if (range.error) throw new WalletReadError(range.error);
  const page = await applications.list({ ...filter, after, limit: 100 }, { signal });
  if (page.participant_id !== filter.participant_id || page.start_at !== filter.start_at || page.end_at !== filter.end_at) {
    throw new WalletReadError("请求历史返回了不同的筛选范围。");
  }
  for (const [index, row] of page.requests.entries()) {
    if (filter.participant_id && row.participant_id !== filter.participant_id
      || BigInt(row.id) <= BigInt(page.requests[index - 1]?.id ?? after)
      || !walletTimeMatches(row.attempted_at, range)
      || (row.visibility === "administrator") !== admin) {
      throw new WalletReadError("请求历史顺序或当前身份范围不一致。");
    }
  }
  if (page.next_after !== null && (page.requests.at(-1)?.id !== page.next_after || BigInt(page.next_after) <= BigInt(after))) {
    throw new WalletReadError("请求历史分页没有前进。");
  }
  return page;
}

export async function readWalletRequests(applications: Pick<ApplicationsClient, "list">, participant: string, admin: boolean, signal?: AbortSignal) {
  const requests: WalletApplication[] = [];
  let after = "0";
  for (;;) {
    const page = await readWalletOperationPage(applications, { participant_id: participant, start_at: "", end_at: "" }, after, admin, signal);
    requests.push(...page.requests);
    if (page.next_after === null) return requests;
    after = page.next_after;
  }
}

export async function readWalletData(wallets: Pick<WalletsClient, "list" | "get">, applications: Pick<ApplicationsClient, "list" | "automation">, admin: boolean, signal?: AbortSignal, walletAfter = ""): Promise<WalletData> {
  const catalog: WalletCatalog = [], participantIDs = new Set<string>();
  let after = "";
  for (;;) {
    const page = await wallets.list({ after, limit: 100 }, { signal });
    for (const row of page.participants) {
      if (participantIDs.has(row.participant_id)) throw new WalletReadError("钱包列表包含重复的参与者。");
      participantIDs.add(row.participant_id); catalog.push(row);
    }
    if (page.next_after === null) break;
    if (page.participants.at(-1)?.participant_id !== page.next_after || page.next_after === after) {
      throw new WalletReadError("参与者分页游标与记录不一致。");
    }
    after = page.next_after;
  }
  const start = walletAfter ? catalog.findIndex(row => row.participant_id === walletAfter) + 1 : 0;
  if (walletAfter && start === 0) throw new WalletReadError("钱包分页位置已变化，请回到首页。");
  const summaries = catalog.slice(start, start + 25);
  const result: WalletView[] = [], operations: WalletApplication[] = [];
  for (let index = 0; index < summaries.length; index += 4) {
    const batch = await Promise.all(summaries.slice(index, index + 4).map(async summary => {
      const [view, requests] = await Promise.all([
        wallets.get({ participant_id: summary.participant_id }, { signal }),
        readWalletRequests(applications, summary.participant_id, admin, signal),
      ]);
      if (Object.entries(summary).some(([key, value]) => view[key as keyof WalletView] !== value)
        || (view.visibility === "all_recorded_sources") !== admin) throw new WalletReadError("钱包身份或版本已变化，请重新读取目录。");
      return { view, requests };
    }));
    for (const item of batch) {
      result.push(item.view);
      operations.push(...item.requests);
    }
  }
  const automation = admin ? await applications.automation({}, { signal }) : null;
  return { wallets: result, catalog, nextAfter: start + summaries.length < catalog.length ? summaries.at(-1)?.participant_id ?? null : null, operations, automation };
}

export function walletOperation(row: WalletApplication) {
  const native = row.details && "native_adjustment" in row.details ? row.details : null;
  return {
    id: row.id, at: row.attempted_at, actor: (row.details?.actor_id ?? "—") + (row.unit === "simulated_wallet_usd" && row.details?.automation ? ` · 自动 #${row.details.automation.control_revision}` : ""),
    participant: row.participant_id, participantName: row.participant_name,
    rec: native?.native_adjustment.weekly_issuance ? "周额度发放" : row.details?.input.recommendation_id === "0" ? "未生成" : row.details?.input.recommendation_id ?? "—",
    before: row.unit === "simulated_wallet_usd" ? row.details?.simulation_write?.before.balance ?? null : native?.native_adjustment.balance_before ?? null,
    target: row.target === null ? null : row.details?.simulation_write?.request.target ?? estimateAmountText(row.target),
    adjustment: native?.input.native_delta ?? null,
    after: row.unit === "simulated_wallet_usd" && ["applied", "failed"].includes(row.state) ? row.details?.simulation_proof?.after.balance ?? null : native?.native_adjustment.balance_after ?? null,
    outcome: row.state === "applied" ? "confirmed" : row.state,
    note: row.state === "applied" ? row.unit === "native_wallet" ? `原生${glossary.balanceAdjustment}已核对流水${native?.native_adjustment.ledger_id ? " #" + native.native_adjustment.ledger_id : ""}和独立余额读数。` : "仅模拟协议：操作证明与独立钱包读取已核对，已确认生效。"
      : row.state === "failed" ? "仅模拟协议：操作证明已确认拒绝；本次操作未改变钱包。"
      : row.state === "unknown" ? "结果未知，禁止重复派发；须只读核对操作证明与钱包读数。"
      : row.state === "pending" ? "申请已保存，等待独立结果核对。"
      : row.reason === "NATIVE_WALLET_WRITE_UNAVAILABLE" ? `${glossary.sub2api} 管理员身份、standard 模式或钱包读取未能确认，${glossary.balanceAdjustment}请求未派发。`
      : row.reason === "SOURCE_WRITE_GRANT_REQUIRED" ? `当前钱包写入授权缺失或已变化，${glossary.balanceAdjustment}请求未派发。`
      : row.reason === "UPSTREAM_WRITES_DISABLED" ? `紧急禁写已开启，${glossary.balanceAdjustment}请求未派发。`
      : row.reason === "NATIVE_WALLET_PRECONDITION_CHANGED" ? `当前绑定或连接版本已变化，${glossary.balanceAdjustment}请求未派发。`
      : row.reason === "NATIVE_WALLET_INSUFFICIENT_BALANCE" ? "可用余额不足以扣减该金额。"
      : row.reason === "SIMULATION_UNRESOLVED_APPLICATION" ? "原操作结果未核实，本次自动建议已跳过；没有重复派发。"
      : row.reason === "ZERO_TARGET_PENDING" ? "目标按来源精度量化后为零，本次未派发。"
      : row.reason === "SIMULATION_PRECONDITION_CHANGED" ? "当前权限、来源或钱包前提已变化，本次未派发。"
      : row.details?.reasons.some(reason => reason === "RECOMMENDATION_STALE") ? `依据已失效；请求已拒绝，未向 ${glossary.sub2api} 派发。` : `请求已拒绝，未向 ${glossary.sub2api} 派发；没有远端生效凭证。`,
    record: row,
  };
}
export type WalletOperation = ReturnType<typeof walletOperation>;

export function walletRow(view: WalletView, operations: WalletApplication[], timezone?: string | null) {
  const receipt = view.last_confirmed_read ?? view.latest_read;
  const latest = view.latest_read, recommendation = view.latest_recommendation;
  const evidenceLimited = recommendation?.global.state === "evidence_limit" || view.contributions.some(row => row.state === "evidence_limit");
  const last = operations.filter(row => row.participant_id === view.participant_id).at(-1);
  const recState = view.current_state === "recalculation_required" ? "stale" : view.current_state === "scenario" ? "scenario" : recommendation ? "blocked" : "insufficient";
  const simulated = receipt?.simulation !== undefined;
  const attempt = latest ? `最近尝试 #${latest.id} · ${displayTime(latest.attempted_at, timezone, false)}，${latest.read.state === "available" ? latest.simulation ? "模拟钱包字段已独立读取" : "字段按原生权限单独判断" : "未取得完整读数"}。` : "尚无钱包读取记录。";
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
    recState, recReason: view.current_state === "scenario" ? "模拟协议条件化建议：完整周期实际扣款、历史份额与全部来源已核对；金额仅适用于该模拟实例，提交时仍需服务端核对当前绑定与全部前提。" : evidenceLimited ? "完整的来源或成员证据超过计算容量，建议金额未知。" : `${view.current_state === "recalculation_required" ? "建议依据已变化，历史解释保留。" : recommendation ? "当前建议依据不足，无法形成可应用金额。" : "尚未生成可确认建议。"} 钱包整体归属、扣费覆盖、冻结并发及币种精度条件未通过；应用保持不可用。`,
    readNote: `${view.last_confirmed_read ? `展示最近完整确认读数 #${view.last_confirmed_read.id}。` : "没有完整确认读数。"}${attempt}${view.read_references_current ? "" : "读取来源或版本未确认当前有效。"} ${simulated ? "模拟钱包 USD 扣款单位。" : "原生钱包单位，币种未核验。"}${sourceNote}`,
    simulated,
    contributions: view.contributions.map(row => {
      const effect = row.temporary_effect;
      const personal = effect?.state === "active" ? `本人临时上限 ${effect.cap}（${effect.mode === "carry" ? "结转" : "不结转"}）；截止 ${effect.deadline}（UTC）`
        : effect?.state === "rights" ? `本人当前周期结转调整 ${effect.adjustment}（分配输入）`
        : effect?.state === "ordinary" ? "普通分配（无临时调整）" : "临时影响未记录";
      return { account: row.account_id, amount: row.amount === null ? null : estimateAmountText(row.amount), state: row.state === "scenario" ? `模拟协议条件贡献 · ${personal}`
        : row.state === "evidence_limit" ? "完整证据超过计算容量" : row.state === "allocation_deficit" ? "成员份额透支" : row.state === "source_unavailable" ? "来源不可用" : row.state === "contract_unavailable" ? "份额依据不可用" : "费用与钱包扣费依据不足" };
    }),
    lastApplied: last ? walletOperation(last) : null, view,
  };
}
export type WalletRow = ReturnType<typeof walletRow>;

type WalletHistoryKind = "wallet" | "recommendation";
export type WalletHistory = {
  walletReads: WalletRead[];
  recommendations: WalletRecommendation[];
  after: Record<WalletHistoryKind, string>;
  nextAfter: Record<WalletHistoryKind, string | null>;
  window: WalletHistoryWindow;
};
export async function readWalletHistory(client: Pick<WalletsClient, "history">, participant: string, signal?: AbortSignal, after = { wallet: "0", recommendation: "0" }, window: WalletHistoryWindow = { start_at: "", end_at: "" }): Promise<WalletHistory> {
  const range = walletTimeRange(window.start_at, window.end_at);
  if (range.error) throw new WalletReadError(range.error);
  const history: WalletHistory = { walletReads: [], recommendations: [], after, window, nextAfter: { wallet: null, recommendation: null } };
  for (const kind of ["wallet", "recommendation"] as const) {
    const page = await client.history({ participant_id: participant, kind, after: after[kind], limit: 100,
      ...(window.start_at ? { start_at: window.start_at } : {}), ...(window.end_at ? { end_at: window.end_at } : {}) }, { signal });
    const rows = kind === "wallet" ? page.wallet_reads : page.recommendations;
    if (page.participant_id !== participant || page.kind !== kind
      || (kind === "wallet" ? page.recommendations : page.wallet_reads).length
      || rows.some((row, index) => row.participant_id !== participant || BigInt(row.id) <= BigInt(rows[index - 1]?.id ?? after[kind])
        || !walletTimeMatches("attempted_at" in row ? row.attempted_at : row.generated_at, range))
      || page.next_after !== null && (rows.length === 0 || page.next_after !== rows.at(-1)?.id)) {
      throw new WalletReadError("历史分页或参与者不一致，请重新读取。");
    }
    if (kind === "wallet") history.walletReads = page.wallet_reads;
    else history.recommendations = page.recommendations;
    history.nextAfter[kind] = page.next_after;
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
