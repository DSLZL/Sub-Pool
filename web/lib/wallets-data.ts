import { glossary } from "@/lib/glossary";
import type { createWalletApplicationsClient, createWalletsClient } from "@sub-pool/contracts/client";
import { walletRead, type WalletView, type WalletRead, type WalletRecommendation } from "@sub-pool/contracts/wallets";
import { usageNanos } from "@sub-pool/contracts/usage";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { estimateAmountText } from "./estimates-data";
import { displayTime } from "./connection-data";

type WalletsClient = ReturnType<typeof createWalletsClient>;
type ApplicationsClient = ReturnType<typeof createWalletApplicationsClient>;
type WalletCatalog = Awaited<ReturnType<WalletsClient["list"]>>["participants"];
export type WalletOperationFilter = Omit<Parameters<ApplicationsClient["list"]>[0], "after" | "limit">;
type WalletHistoryWindow = Pick<WalletOperationFilter, "start_at" | "end_at">;
export type WalletData = {
  wallets: WalletView[];
  catalog: WalletCatalog;
  nextAfter: string | null;
  operations: WalletApplication[];
};
export class WalletReadError extends Error {}

export async function readWalletOperationPage(applications: Pick<ApplicationsClient, "list">, filter: WalletOperationFilter, after: string, admin: boolean, signal?: AbortSignal) {
  const range = walletTimeRange(filter.start_at, filter.end_at);
  if (range.error) throw new WalletReadError(range.error);
  const page = await applications.list({ ...filter, after, limit: 100 }, { signal });
  if (page.participant_id !== filter.participant_id || page.start_at !== filter.start_at || page.end_at !== filter.end_at) {
    throw new WalletReadError("历史不符。");
  }
  for (const [index, row] of page.requests.entries()) {
    if (filter.participant_id && row.participant_id !== filter.participant_id
      || BigInt(row.id) <= BigInt(page.requests[index - 1]?.id ?? after)
      || !walletTimeMatches(row.attempted_at, range)
      || (row.visibility === "administrator") !== admin) {
      throw new WalletReadError("历史不符。");
    }
  }
  if (page.next_after !== null && (page.requests.at(-1)?.id !== page.next_after || BigInt(page.next_after) <= BigInt(after))) {
    throw new WalletReadError("历史无法续页。");
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

export async function readWalletData(wallets: Pick<WalletsClient, "list" | "get">, applications: Pick<ApplicationsClient, "list">, admin: boolean, signal?: AbortSignal, walletAfter = ""): Promise<WalletData> {
  const catalog: WalletCatalog = [], participantIDs = new Set<string>();
  let after = "";
  for (;;) {
    const page = await wallets.list({ after, limit: 100 }, { signal });
    for (const row of page.participants) {
      if (participantIDs.has(row.participant_id)) throw new WalletReadError("参与者重复。");
      participantIDs.add(row.participant_id); catalog.push(row);
    }
    if (page.next_after === null) break;
    if (page.participants.at(-1)?.participant_id !== page.next_after || page.next_after === after) {
      throw new WalletReadError("分页不符。");
    }
    after = page.next_after;
  }
  const start = walletAfter ? catalog.findIndex(row => row.participant_id === walletAfter) + 1 : 0;
  if (walletAfter && start === 0) throw new WalletReadError("分页已变，回首页。");
  const summaries = catalog.slice(start, start + 25);
  const result: WalletView[] = [], operations: WalletApplication[] = [];
  for (let index = 0; index < summaries.length; index += 4) {
    const batch = await Promise.all(summaries.slice(index, index + 4).map(async summary => {
      const [view, requests] = await Promise.all([
        wallets.get({ participant_id: summary.participant_id }, { signal }),
        readWalletRequests(applications, summary.participant_id, admin, signal),
      ]);
      if (Object.entries(summary).some(([key, value]) => view[key as keyof WalletView] !== value)
        || (view.visibility === "all_recorded_sources") !== admin) throw new WalletReadError("钱包已变，请重读。");
      return { view, requests };
    }));
    for (const item of batch) {
      result.push(item.view);
      operations.push(...item.requests);
    }
  }
  return { wallets: result, catalog, nextAfter: start + summaries.length < catalog.length ? summaries.at(-1)?.participant_id ?? null : null, operations };
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
    note: row.state === "applied" ? row.unit === "native_wallet" ? `已确认流水${native?.native_adjustment.ledger_id ? " #" + native.native_adjustment.ledger_id : ""}及余额。` : "旧模拟调整已确认。"
      : row.state === "failed" ? "旧模拟调整被拒。"
      : row.state === "unknown" ? "查原流水，勿重试。"
      : row.state === "pending" ? "请求已保存。"
      : row.reason === "NATIVE_WALLET_WRITE_UNAVAILABLE" ? "权限或读数待核对，未发送。"
      : row.reason === "SOURCE_WRITE_GRANT_REQUIRED" ? "未授权，未发送。"
      : row.reason === "UPSTREAM_WRITES_DISABLED" ? "紧急禁写，未发送。"
      : row.reason === "NATIVE_WALLET_PRECONDITION_CHANGED" ? "钱包已变，未发送。"
      : row.reason === "NATIVE_WALLET_INSUFFICIENT_BALANCE" ? "余额不足。"
      : row.reason === "SIMULATION_UNRESOLVED_APPLICATION" ? "有未决操作，已跳过。"
      : row.reason === "ZERO_TARGET_PENDING" ? "零金额，已跳过。"
      : row.reason === "SIMULATION_PRECONDITION_CHANGED" ? "条件变更，已跳过。"
      : row.details?.reasons.some(reason => reason === "RECOMMENDATION_STALE") ? "建议过期，未发送。" : "拒绝，未发送。",
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
  const attempt = latest ? `尝试 #${latest.id} · ${displayTime(latest.attempted_at, timezone, false)}${latest.read.state === "available" ? latest.simulation ? " · 旧模拟" : "" : " · 未取得读数"}。` : "暂无读数。";
  let sourceNote = "";
  if (view.visibility === "all_recorded_sources") {
    const sources = latest?.usage_sources;
    if (latest?.simulation_scope) sourceNote = ` 模拟来源 ${latest.simulation_scope.source_account_ids.length} · ${latest.simulation_scope.scope_complete && latest.simulation_scope.ledger_closed ? "账本完整" : "账本待核对"}。`;
    else if (!sources) sourceNote = "暂无来源。";
    else if (sources.state === "failed") sourceNote = "来源不可读。";
    else {
      const managed = sources.accounts.filter(row => row.managed_account_id !== null).length;
      sourceNote = `${sources.state === "exhausted" ? "已查来源" : "部分来源"} ${sources.accounts.length} · 纳管 ${managed} · 未纳管 ${sources.accounts.length - managed}。`;
    }
    if (!latest?.simulation) sourceNote += "";
  }
  return {
    id: view.participant_id, name: view.name, upstreamUserId: view.upstream_user_id, enabled: view.enabled,
    balance: receipt?.balance.state === "reported" ? receipt.balance.value : null,
    frozen: receipt?.frozen_balance.state === "reported" ? receipt.frozen_balance.value : null,
    balanceReadAt: receipt?.read_at ?? null, recommendation: view.current_target === null ? null : estimateAmountText(view.current_target),
    recState, recReason: view.current_state === "scenario" ? "旧模拟建议。" : evidenceLimited ? "记录超限，金额未知。" : view.current_state === "recalculation_required" ? "建议过期。" : recommendation ? "待补数据。" : "暂无建议。",
    readNote: `${view.last_confirmed_read ? `最近确认 #${view.last_confirmed_read.id}。` : "无确认读数。"}${attempt}${view.read_references_current ? "" : "读数待核对。"} ${simulated ? "模拟钱包（USD）。" : "钱包单位。"}${sourceNote}`,
    simulated,
    contributions: view.contributions.map(row => {
      const effect = row.temporary_effect;
      const personal = effect?.state === "active" ? `临时上限 ${effect.cap}（${effect.mode === "carry" ? "结转" : "不结转"}）· 截止 ${effect.deadline}（UTC）`
        : effect?.state === "rights" ? `结转 ${effect.adjustment}`
        : effect?.state === "ordinary" ? "普通分配" : "无临时记录";
      return { account: row.account_id, amount: row.amount === null ? null : estimateAmountText(row.amount), state: row.state === "scenario" ? `旧模拟分配 · ${personal}`
        : row.state === "evidence_limit" ? "记录超限" : row.state === "allocation_deficit" ? "成员份额透支" : row.state === "source_unavailable" ? "来源不可用" : row.state === "contract_unavailable" ? "缺份额依据" : "缺少扣费依据" };
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
      throw new WalletReadError("历史不符。");
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
  return { start: startAt, end: endAt, error: invalid ? "填写 UTC 起止时间。" : null };
}
export function walletTimeMatches(at: string, range: ReturnType<typeof walletTimeRange>) {
  const instant = usageNanos(at);
  return !range.error && instant !== null && (range.start === null || instant >= range.start) && (range.end === null || instant < range.end);
}
