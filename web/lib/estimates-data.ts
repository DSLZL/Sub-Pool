import type { createEstimatesClient } from "@sub-pool/contracts/client";
import type { EstimateAssumptionInput, EstimateCurrent, EstimateRecord } from "@sub-pool/contracts/estimates";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";

type Client = ReturnType<typeof createEstimatesClient>;
export type EstimateAccount = Pick<ManagedAccount, "id" | "name">;
export type EstimateEntry = { account: EstimateAccount; current: EstimateCurrent };
export type EstimateData = { entries: EstimateEntry[]; history: EstimateRecord[] };
export type EstimateSave = EstimateAssumptionInput & { account_id: string; revision: string };
export type WindowScope = "short" | "seven_day" | "both";
type History = Awaited<ReturnType<Client["history"]>>;
export class EstimateReadError extends Error {}

export async function readEstimateHistory(account: string, head: string, read: (after: string) => Promise<History>) {
  const records: EstimateRecord[] = [];
  let after = "0", previous = 0n;
  for (;;) {
    const page = await read(after);
    if (page.account_id !== account) throw new EstimateReadError("估算历史账号不一致，未展示部分结果。");
    for (const record of page.records) {
      const id = BigInt(record.id);
      if (record.account_id !== account || id <= previous || id > BigInt(head)) throw new EstimateReadError("估算历史已变化或重复，请重新读取。");
      previous = id; records.push(record);
    }
    if (records.length > 2000) throw new EstimateReadError("已保留估算超过本页 2,000 条读取上限；未展示截断历史。");
    if (page.next_after === null) {
      if ((records.at(-1)?.id ?? "0") !== head) throw new EstimateReadError("估算历史与当前记录未完成核对，请重新读取。");
      return records;
    }
    if (page.next_after !== records.at(-1)?.id || BigInt(page.next_after) <= BigInt(after)) throw new EstimateReadError("估算历史续页无效，未展示部分结果。");
    after = page.next_after;
  }
}

export async function readEstimateData(accounts: EstimateAccount[], client: Pick<Client, "current" | "history">, history: boolean, signal: AbortSignal): Promise<EstimateData> {
  if (accounts.length > 500 || new Set(accounts.map(value => value.id)).size !== accounts.length) throw new EstimateReadError("账号范围重复或超过本页 500 个读取上限；未展示部分结果。");
  const entries: EstimateEntry[] = [], records: EstimateRecord[] = [];
  for (let offset = 0; offset < accounts.length; offset += 8) {
    const batch = await Promise.all(accounts.slice(offset, offset + 8).map(async account => {
      const current = await client.current({ account_id: account.id }, { signal });
      if (current.configuration.account_id !== account.id || current.latest && current.latest.account_id !== account.id) throw new EstimateReadError("估算当前状态账号不一致。");
      const rows = history ? await readEstimateHistory(account.id, current.latest?.id ?? "0", after => client.history({ account_id: account.id, after, limit: 100, window_kind: "all", period_end_at: "" }, { signal })) : [];
      return { entry: { account, current }, rows };
    }));
    for (const value of batch) { entries.push(value.entry); records.push(...value.rows); }
    if (records.length > 2000) throw new EstimateReadError("授权范围的估算历史超过本页 2,000 条读取上限；未展示部分结果。");
  }
  records.sort((left, right) => BigInt(left.id) < BigInt(right.id) ? 1 : -1);
  return { entries, history: records };
}

export const estimateStates = {
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
export function nativePeriodText(record: EstimateRecord | null) {
  return record?.windows.map(window => `${window.kind === "short" ? "短窗口" : "长窗口"}：${window.period ? window.period.start_at + " → " + window.period.end_at + "（" + window.period.duration_seconds + " 秒）" : "官方周期未知"}`).join("；") ?? "尚无生成记录";
}

type DisplayRange = { low: number; mid: number; high: number };
export function estimateCard(entry: EstimateEntry): { account: string; name: string; cycle: string; status: EstimateCurrent["current_state"]; capacity: DisplayRange | null; remaining: DisplayRange | null; basis: string; at: string; premise: string } {
  const current = entry.current, record = current.latest;
  const windows = record?.windows.map(window => `${window.kind === "short" ? "短窗口" : "长窗口"}：${estimateStates[window.state].label}；原始使用 ${window.current_used_percent === null ? "未知" : window.current_used_percent + "%"}；当前观测 ${window.current_observation_id ?? "未知"}；选用观测 ${window.observation_ids.join("、") || "无"}；源时间 ${window.source_at ?? "未知"}；最早选用 ${window.oldest_sample_at ?? "未知"}`).join("；");
  // shortcut: estimates-v1 has no qualified amount; enable this display slot only with a verified future provider.
  return { account: entry.account.id, name: entry.account.name, cycle: nativePeriodText(record), status: current.current_state, capacity: null, remaining: null,
    basis: estimateStates[current.current_state].reason + (windows ? " " + windows : "") + " 未具备完整费用与历史计价证明，不可作为钱包目标。",
    at: `生成 ${record?.generated_at ?? "尚无记录"}；当前核对 ${current.checked_at}（UTC）`, premise: current.configuration.assumptions?.premise ?? "尚未保存前提；原界面缺少明确保留比例和前提输入。" };
}

export function savedWindowScope(current: EstimateCurrent): WindowScope {
  const windows = current.configuration.assumptions?.windows;
  return windows?.length === 2 ? "both" : windows?.[0] ?? "short";
}
export function estimateSave(entry: EstimateEntry, scope: WindowScope): EstimateSave {
  const configuration = entry.current.configuration, a = configuration.assumptions;
  if (!a) throw new EstimateReadError("首次配置需要明确的保留比例和前提输入；原界面没有这些控件，未代填默认值。");
  return { account_id: entry.account.id, revision: configuration.revision, plan: a.plan, cost_basis: a.cost_basis,
    keep_ratio: a.keep_ratio, premise: a.premise, windows: scope === "both" ? ["seven_day", "short"] : [scope],
    source_id: a.source_id, connection_revision: a.connection_revision, account_revision: a.account_revision };
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
