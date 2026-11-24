import type { createEstimatesClient } from "@sub-pool/contracts/client";
import { estimateAssumptionInput, type EstimateAssumptionInput, type EstimateCurrent, type EstimateRecord } from "@sub-pool/contracts/estimates";
import type { ManagedAccount, ManagedAccountView } from "@sub-pool/contracts/accounts";
import { usageNanos } from "@sub-pool/contracts/usage";
import { session } from "@sub-pool/contracts/auth";

type Client = ReturnType<typeof createEstimatesClient>;
export type EstimateAccount = Pick<ManagedAccount, "id" | "name">;
export type EstimateEntry = { account: EstimateAccount; current: EstimateCurrent };
export type EstimateData = { entries: EstimateEntry[]; history: EstimateRecord[] };
export type EstimateSave = EstimateAssumptionInput & { account_id: string; revision: string };
export type WindowScope = "short" | "seven_day" | "both";
export type EstimateDraft = Pick<EstimateAssumptionInput, "keep_ratio" | "premise">;
export type EstimateHistoryFilter = Pick<Parameters<Client["history"]>[0], "window_kind" | "period_end_at">;
export type EstimateRecompute = Parameters<Client["recompute"]>[0];
type History = Awaited<ReturnType<Client["history"]>>;
export class EstimateReadError extends Error {}
const allHistory: EstimateHistoryFilter = { window_kind: "all", period_end_at: "" };

export function estimateHistoryFilter(window_kind: EstimateHistoryFilter["window_kind"], period_end_at: string): EstimateHistoryFilter {
  if (!["all", "short", "seven_day"].includes(window_kind) || period_end_at !== "" && (window_kind === "all" || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(period_end_at) || !session.shape.created_at.safeParse(period_end_at).success || usageNanos(period_end_at) === null || period_end_at.slice(0, 4) < "1970" || period_end_at.slice(0, 4) >= "9999")) {
    throw new EstimateReadError("筛选官方周期须先选择实际窗口，并填写有效的 UTC 结束时间；可留空读取该窗口全部保留历史。");
  }
  return { window_kind, period_end_at };
}

function historyMatches(record: EstimateRecord, filter: EstimateHistoryFilter) {
  return filter.window_kind === "all" || record.windows.some(window => window.kind === filter.window_kind && (filter.period_end_at === "" || window.period !== null && usageNanos(window.period.end_at) === usageNanos(filter.period_end_at)));
}

export async function readEstimateHistory(account: string, head: string, read: (after: string) => Promise<History>, filter: EstimateHistoryFilter = allHistory) {
  const records: EstimateRecord[] = [];
  let after = "0", previous = 0n;
  for (;;) {
    const page = await read(after);
    if (page.account_id !== account) throw new EstimateReadError("估算历史账号不一致，未展示部分结果。");
    for (const record of page.records) {
      const id = BigInt(record.id);
      if (record.account_id !== account || id <= previous || id > BigInt(head) || !historyMatches(record, filter)) throw new EstimateReadError("估算历史已变化、重复或不符筛选，请重新读取。");
      previous = id; records.push(record);
    }
    if (records.length > 2000) throw new EstimateReadError("已保留估算超过本页 2,000 条读取上限；未展示截断历史。");
    if (page.next_after === null) {
      if (filter.window_kind === "all" && (records.at(-1)?.id ?? "0") !== head) throw new EstimateReadError("估算历史与当前记录未完成核对，请重新读取。");
      return records;
    }
    if (page.next_after !== records.at(-1)?.id || BigInt(page.next_after) <= BigInt(after)) throw new EstimateReadError("估算历史续页无效，未展示部分结果。");
    after = page.next_after;
  }
}

export async function readEstimateData(accounts: EstimateAccount[], client: Pick<Client, "current" | "history">, history: boolean, signal: AbortSignal, filter: EstimateHistoryFilter = allHistory): Promise<EstimateData> {
  if (history) estimateHistoryFilter(filter.window_kind, filter.period_end_at);
  if (accounts.length > 500 || new Set(accounts.map(value => value.id)).size !== accounts.length) throw new EstimateReadError("账号范围重复或超过本页 500 个读取上限；未展示部分结果。");
  const entries: EstimateEntry[] = [], records: EstimateRecord[] = [];
  for (let offset = 0; offset < accounts.length; offset += 8) {
    const batch = await Promise.all(accounts.slice(offset, offset + 8).map(async account => {
      let current = await client.current({ account_id: account.id }, { signal });
      if (current.configuration.account_id !== account.id || current.latest && current.latest.account_id !== account.id) throw new EstimateReadError("估算当前状态账号不一致。");
      const rows = history ? await readEstimateHistory(account.id, current.latest?.id ?? "0", after => client.history({ account_id: account.id, after, limit: 100, ...filter }, { signal }), filter) : [];
      if (history && filter.window_kind !== "all") {
        const checked = await client.current({ account_id: account.id }, { signal });
        if (checked.configuration.account_id !== account.id || checked.configuration.revision !== current.configuration.revision || checked.latest?.id !== current.latest?.id) throw new EstimateReadError("筛选读取期间估算版本已变化，请重新读取。");
        current = checked;
      }
      return { entry: { account, current }, rows };
    }));
    for (const value of batch) { entries.push(value.entry); records.push(...value.rows); }
    if (records.length > 2000) throw new EstimateReadError("授权范围的估算历史超过本页 2,000 条读取上限；未展示部分结果。");
  }
  records.sort((left, right) => BigInt(left.id) < BigInt(right.id) ? 1 : -1);
  return { entries, history: records };
}

export async function readEstimateRecord(account: string, id: string, read: () => Promise<EstimateRecord>) {
  const record = await read();
  if (record.account_id !== account || record.id !== id) throw new EstimateReadError("详情回执与所选账号或记录不一致，未展示详情。");
  return record;
}

export function estimateRecomputeInput(entry: EstimateEntry): EstimateRecompute {
  if (entry.current.configuration.account_id !== entry.account.id || entry.current.configuration.assumptions === null) throw new EstimateReadError("须先明确保存该账号的估算假设，未提交重算。");
  return { account_id: entry.account.id, revision: entry.current.configuration.revision, latest_id: entry.current.latest?.id ?? "0" };
}

export function estimateRecomputeMatches(record: EstimateRecord, input: EstimateRecompute) {
  return record.account_id === input.account_id && record.configuration.revision === input.revision && record.trigger === "manual_recompute" && BigInt(record.id) > BigInt(input.latest_id);
}

export async function reconcileEstimateRecompute(input: EstimateRecompute, acknowledgement: EstimateRecord | null, client: Pick<Client, "current" | "history" | "record">, signal: AbortSignal) {
  const current = await client.current({ account_id: input.account_id }, { signal });
  if (current.configuration.account_id !== input.account_id) throw new EstimateReadError("重算回读账号不一致，结果继续待核对。");
  if (!acknowledgement) {
    const records = await readEstimateHistory(input.account_id, current.latest?.id ?? "0", after => client.history({ account_id: input.account_id, after, limit: 100, ...allHistory }, { signal }));
    const candidates = records.filter(record => estimateRecomputeMatches(record, input));
    throw new EstimateReadError(candidates.length ? `已读到可能相符的重算记录 ${candidates.slice(0, 5).map(record => record.id).join("、")}，但契约不能证明本次请求归属；结果继续待核对，不会重复提交。` : "尚未读到相符的重算记录；只可继续核对，不会重复提交。");
  }
  if (!estimateRecomputeMatches(acknowledgement, input)) throw new EstimateReadError("重算回执与提交版本不一致，结果继续待核对。");
  const persisted = await readEstimateRecord(input.account_id, acknowledgement.id, () => client.record({ id: acknowledgement.id }, { signal }));
  if (JSON.stringify(persisted) !== JSON.stringify(acknowledgement) || BigInt(current.configuration.revision) < BigInt(input.revision) || BigInt(current.latest?.id ?? "0") < BigInt(persisted.id)) throw new EstimateReadError("重算回执和持久记录未完成核对，只可继续读取。");
  return { persisted, changed: current.latest?.id !== persisted.id || current.configuration.revision !== input.revision };
}

export const estimateStates = {
  scenario: { tone: "info", label: "模拟条件场景", reason: "已核对模拟协议的完整相邻费用区间；场景依赖声明前提，未作模型校准或资金资格证明。" },
  not_configured: { tone: "unk", label: "未配置", reason: "尚未声明估算前提、明确保留比例及窗口范围。" },
  invalid_evidence: { tone: "bad", label: "证据无效", reason: "原始百分比或费用不满足精确计算条件。" },
  insufficient_samples: { tone: "unk", label: "样本不足", reason: "尚无三个不同源时间端点和两个可验证的相邻费用区间。" },
  condition_changed: { tone: "warn", label: "条件已变化", reason: "来源、身份、版本或同周期额度变化使原前提失效。" },
  evidence_incomplete: { tone: "unk", label: "费用证据不足", reason: "已记录观测不能证明区间费用完整性与历史计价，金额仍未知。" },
  non_identifying_delta: { tone: "unk", label: "区间不可识别", reason: "零额度变化或零费用不能推断容量。" },
  period_unknown: { tone: "unk", label: "官方周期未知", reason: "缺少可验证的源时间、原生重置或实际时长，不能按窗口名称猜测。" },
  source_stale: { tone: "warn", label: "依据已过期", reason: "当前或选用端点已过期，或实际官方窗口已结束；历史记录保留。" },
  manual_range_unusable: { tone: "warn", label: "本地测算段不可用", reason: "已保存的人工区间当前不满足有效端点或官方周期条件。" },
  evidence_limit: { tone: "warn", label: "证据超出读取范围", reason: "周期保留观测超出后端有界扫描，未以部分数据估计。" },
  recalculation_required: { tone: "warn", label: "需重新核对", reason: "配置、维护策略或输入版本已变化，历史结果不代表当前有效值。" },
} as const;
const triggers: Record<EstimateRecord["trigger"], string> = { configuration: "假设配置", manual_recompute: "明确重算", observation: "新观测", usage: "新费用捕获", review: "有效性维护", range: "本地段维护" };
export function historyReason(record: EstimateRecord) {
  return `${triggers[record.trigger]} · 配置 ${record.configuration.revision} · ${estimateStates[record.state].label} · ${record.rules_version}`;
}
export function nativePeriodText(record: EstimateRecord | null, time = (value: string) => value) {
  return record?.windows.map(window => `${window.kind === "short" ? "短窗口" : "长窗口"}：${window.period ? time(window.period.start_at) + " → " + time(window.period.end_at) + "（" + window.period.duration_seconds + " 秒）" : "官方周期未知"}`).join("；") ?? "尚无生成记录";
}

type DisplayRange = { low: number; mid: number; high: number };
type Amount = NonNullable<EstimateRecord["remaining"]>;
export function estimateAmountText(value: Amount | null) {
  return value === null ? "未知" : value.denominator === "1" ? value.numerator : `${value.numerator}/${value.denominator}`;
}
export function estimateCostText(window: EstimateRecord["windows"][number]) {
  const proof = window.cost_proof;
  return proof ? `模拟协议 ${proof.protocol}；记录批次 ${proof.epoch}；账本 ${proof.ledger_revision}；历史计价 ${proof.price_version}（${proof.standard_rate} × ${proof.wallet_multiplier}，${proof.billing_type}）；关闭水位 ${proof.closed_through}；` + proof.intervals.map(interval => `[${interval.start_at}, ${interval.end_at})：标准 ${estimateAmountText(interval.standard_cost)} USD，实际 ${estimateAmountText(interval.actual_cost)} USD，${interval.entry_count} 项`).join("；") : "未取得完整区间费用与历史计价证明";
}
function displayPoint(value: Amount | null): DisplayRange | null {
  if (value === null) return null;
  const number = Number(value.numerator) / Number(value.denominator);
  return Number.isFinite(number) ? { low: number, mid: number, high: number } : null;
}
export function estimateCard(entry: EstimateEntry, formatTime?: (value: string) => string): { account: string; name: string; cycle: string; status: EstimateCurrent["current_state"]; capacity: DisplayRange | null; remaining: DisplayRange | null; basis: string; at: string; premise: string } {
  const current = entry.current, record = current.latest;
  const timestamp = (value: string | null) => value === null ? "未知" : formatTime?.(value) ?? value;
  const windows = record?.windows.map(window => `${window.kind === "short" ? "短窗口" : "长窗口"}：${estimateStates[window.state].label}；原始使用 ${window.current_used_percent === null ? "未知" : window.current_used_percent + "%"}；当前观测 ${window.current_observation_id ?? "未知"}；选用观测 ${window.observation_ids.join("、") || "无"}；源时间 ${timestamp(window.source_at)}；最早选用 ${timestamp(window.oldest_sample_at)}`).join("；");
  const remaining = current.current_state === "scenario" && record?.complete ? current.current_remaining : null;
  const limiting = remaining === null ? undefined : record?.windows.find(window => window.remaining !== null && BigInt(window.remaining.numerator) * BigInt(remaining.denominator) === BigInt(remaining.numerator) * BigInt(window.remaining.denominator));
  const evidence = record?.windows.map(window => window.cost_proof ? `模拟协议完整区间；历史计价 ${window.cost_proof.price_version}；标准 ${window.cost_proof.intervals.map(interval => estimateAmountText(interval.standard_cost)).join(" + ")} USD；实际 ${window.cost_proof.intervals.map(interval => estimateAmountText(interval.actual_cost)).join(" + ")} USD；经验容量范围 ${estimateAmountText(window.range_min)} – ${estimateAmountText(window.range_max)} USD` : "未取得完整区间费用与历史计价证明").join("；");
  return { account: entry.account.id, name: entry.account.name, cycle: nativePeriodText(record, formatTime), status: current.current_state, capacity: displayPoint(limiting?.capacity ?? null), remaining: displayPoint(remaining),
    basis: estimateStates[current.current_state].reason + (windows ? " " + windows : "") + (evidence ? " " + evidence : "") + " 标准费用场景不可作为钱包目标。",
    at: `生成 ${record ? timestamp(record.generated_at) : "尚无记录"}；当前核对 ${timestamp(current.checked_at)}${formatTime ? "" : "（UTC）"}`, premise: current.configuration.assumptions?.premise ?? "尚未保存前提；管理员可在估算假设中明确声明。" };
}

export function savedWindowScope(current: EstimateCurrent): WindowScope {
  const windows = current.configuration.assumptions?.windows;
  return windows?.length === 2 ? "both" : windows?.[0] ?? "short";
}
export function estimateSave(entry: EstimateEntry, scope: WindowScope, draft: EstimateDraft, reference: ManagedAccountView): EstimateSave {
  const account = reference.account;
  if (account.id !== entry.account.id || !account.source_current || !account.connection_available || account.identity_state !== "verified" || account.read.state !== "available" || account.observed_connection_revision !== reference.connection_revision) {
    throw new EstimateReadError("当前账号身份或来源未完成核对，请重新打开假设配置读取。");
  }
  const input = estimateAssumptionInput.safeParse({ plan: "empirical_min_v1", cost_basis: "standard_usd", ...draft,
    windows: scope === "both" ? ["seven_day", "short"] : [scope], source_id: account.source_id,
    connection_revision: reference.connection_revision, account_revision: account.revision });
  if (!input.success) throw new EstimateReadError("须明确填写 (0, 1] 内至多 18 位小数的保留比例及有效前提，不能代填默认值。");
  return { ...input.data, account_id: entry.account.id, revision: entry.current.configuration.revision };
}
export function estimateConfigurationMatches(record: EstimateRecord, input: EstimateSave) {
  const a = record.configuration.assumptions;
  return record.trigger === "configuration" && record.account_id === input.account_id && record.configuration.revision === (BigInt(input.revision) + 1n).toString()
    && a !== null && a.plan === input.plan && a.cost_basis === input.cost_basis && a.keep_ratio === input.keep_ratio && a.premise === input.premise
    && a.source_id === input.source_id && a.connection_revision === input.connection_revision && a.account_revision === input.account_revision
    && JSON.stringify([...a.windows].sort()) === JSON.stringify([...input.windows].sort());
}
export async function reconcileEstimateConfiguration(input: EstimateSave, acknowledgement: EstimateRecord | null, client: Pick<Client, "current" | "history" | "record">, signal: AbortSignal) {
  const current = await client.current({ account_id: input.account_id }, { signal });
  if (current.configuration.account_id !== input.account_id) throw new EstimateReadError("回读账号不一致，结果继续待核对。");
  const records = acknowledgement ? [await client.record({ id: acknowledgement.id }, { signal })]
    : await readEstimateHistory(input.account_id, current.latest?.id ?? "0", after => client.history({ account_id: input.account_id, after, limit: 100, window_kind: "all", period_end_at: "" }, { signal }));
  const persisted = records.find(record => estimateConfigurationMatches(record, input));
  if (!persisted || acknowledgement && (persisted.id !== acknowledgement.id || !estimateConfigurationMatches(acknowledgement, input))) throw new EstimateReadError("尚未读到相符的保存记录；只可继续核对，不会重复提交。");
  if (BigInt(current.configuration.revision) < BigInt(persisted.configuration.revision)) throw new EstimateReadError("当前配置和历史回读版本不一致，请再次只读核对。");
  return { persisted, current, changed: current.configuration.revision !== persisted.configuration.revision };
}
