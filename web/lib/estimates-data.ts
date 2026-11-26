import { createEstimatesClient } from "@sub-pool/contracts/client";
import type { EstimateCurrent, EstimateRecord } from "@sub-pool/contracts/estimates";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import { usageNanos } from "@sub-pool/contracts/usage";
import { session } from "@sub-pool/contracts/auth";
import { appendRequestId, contractMismatchMessage, requestIdHeader, validRequestId } from "./request-id";

type Client = ReturnType<typeof createEstimatesClient>;
export type EstimateAccount = Pick<ManagedAccount, "id" | "name">;
export type EstimateEntry = { account: EstimateAccount; current: EstimateCurrent };
export type EstimateHistoryFilter = Pick<Parameters<Client["history"]>[0], "window_kind" | "period_end_at">;
export class EstimateReadError extends Error {}


export function estimateHistoryFilter(window_kind: EstimateHistoryFilter["window_kind"], period_end_at: string): EstimateHistoryFilter {
  if (!["all", "short", "seven_day"].includes(window_kind) || period_end_at !== "" && (window_kind === "all" || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(period_end_at) || !session.shape.created_at.safeParse(period_end_at).success || usageNanos(period_end_at) === null || period_end_at.slice(0, 4) < "1970" || period_end_at.slice(0, 4) >= "9999")) {
    throw new EstimateReadError("选窗口、UTC 截止。");
  }
  return { window_kind, period_end_at };
}

function historyMatches(record: EstimateRecord, filter: EstimateHistoryFilter) {
  return filter.window_kind === "all" || record.windows.some(window => window.kind === filter.window_kind && (filter.period_end_at === "" || window.period !== null && usageNanos(window.period.end_at) === usageNanos(filter.period_end_at)));
}

export async function readEstimatePage(account: string, after: string, read: () => ReturnType<Client["history"]>, filter: EstimateHistoryFilter) {
  estimateHistoryFilter(filter.window_kind, filter.period_end_at);
  const page = await read();
  if (page.account_id !== account) throw new EstimateReadError("账号不符。");
  let previous = BigInt(after);
  for (const record of page.records) {
    const id = BigInt(record.id);
    if (record.account_id !== account || id <= previous || !historyMatches(record, filter)) {
      throw new EstimateReadError("筛选不符。");
    }
    previous = id;
  }
  if (page.next_after !== null && (page.next_after !== page.records.at(-1)?.id || BigInt(page.next_after) <= BigInt(after))) {
    throw new EstimateReadError("无法续页。");
  }
  return page;
}

export async function readEstimateArchive(origin: string, input: Parameters<Client["history"]>[0], signal: AbortSignal, fetcher: typeof fetch = fetch) {
  let responseId: string | null = null;
  const estimates = createEstimatesClient(origin, async (request, options) => {
    const response = await fetcher(request, options);
    if (response.ok) responseId = validRequestId(response.headers.get(requestIdHeader));
    return response;
  });
  try {
    return await readEstimatePage(input.account_id, input.after, () => estimates.history(input, { signal }), input);
  } catch (error) {
    if (!responseId) throw error;
    throw new EstimateReadError(appendRequestId(error instanceof EstimateReadError ? error.message : contractMismatchMessage, responseId));
  }
}

export async function readEstimateSummaries(accounts: EstimateAccount[], client: Pick<Client, "current">, signal: AbortSignal) {
  const entries: EstimateEntry[] = [];
  for (let offset = 0; offset < accounts.length; offset += 8) {
    const batch = await Promise.all(accounts.slice(offset, offset + 8).map(async account => {
      const current = await client.current({ account_id: account.id }, { signal });
      if (current.configuration.account_id !== account.id || current.native_weekly?.latest &&
        current.native_weekly.latest.account_id !== account.id) throw new EstimateReadError("账号不符。");
      return { account, current };
    }));
    entries.push(...batch);
  }
  return entries;
}

export async function readEstimateRecord(account: string, id: string, read: () => Promise<EstimateRecord>) {
  const record = await read();
  if (record.account_id !== account || record.id !== id) throw new EstimateReadError("记录不符。");
  return record;
}

export const estimateStates = {
  scenario: { tone: "info", label: "模拟条件", reason: "旧模拟估算" },
  not_configured: { tone: "unk", label: "未配置", reason: "缺估算设置" },
  invalid_evidence: { tone: "bad", label: "证据无效", reason: "比例或费用有误" },
  insufficient_samples: { tone: "unk", label: "样本不足", reason: "样本不足" },
  condition_changed: { tone: "warn", label: "条件已变", reason: "条件已变" },
  evidence_incomplete: { tone: "unk", label: "费用不足", reason: "费用缺项" },
  non_identifying_delta: { tone: "unk", label: "区间待核对", reason: "无有效差值" },
  period_unknown: { tone: "unk", label: "周期未知", reason: "周期未知" },
  source_stale: { tone: "warn", label: "依据已过期", reason: "估算已过期" },
  manual_range_unusable: { tone: "warn", label: "人工区间不可用", reason: "区间无效" },
  evidence_limit: { tone: "warn", label: "超出范围", reason: "观测超限" },
  recalculation_required: { tone: "warn", label: "需重新核对", reason: "设置已变" },
} as const;
const triggers: Record<EstimateRecord["trigger"], string> = { configuration: "假设配置", manual_recompute: "明确重算", observation: "新观测", usage: "新费用捕获", review: "有效性维护", range: "本地段维护" };
export function historyReason(record: EstimateRecord) {
  return `${triggers[record.trigger]} · 配置 ${record.configuration.revision} · ${estimateStates[record.state].label} · ${record.rules_version}`;
}
export function nativePeriodText(record: EstimateRecord | null, time = (value: string) => value) {
  return record?.windows.map(window => `${window.kind === "short" ? "短窗口" : "长窗口"}：${window.period ? time(window.period.start_at) + " → " + time(window.period.end_at) + "（" + window.period.duration_seconds + " 秒）" : "周期未知"}`).join("；") ?? "暂无估算";
}

type Amount = NonNullable<EstimateRecord["remaining"]>;
export function estimateAmountText(value: Amount | null) {
  return value === null ? "未知" : value.denominator === "1" ? value.numerator : `${value.numerator}/${value.denominator}`;
}
export function estimateCostText(window: EstimateRecord["windows"][number]) {
  const proof = window.cost_proof;
  return proof ? `模拟协议 ${proof.protocol}；记录批次 ${proof.epoch}；账本 ${proof.ledger_revision}；历史计价 ${proof.price_version}（${proof.standard_rate} × ${proof.wallet_multiplier}，${proof.billing_type}）；关闭水位 ${proof.closed_through}；` + proof.intervals.map(interval => `[${interval.start_at}, ${interval.end_at})：标准 ${estimateAmountText(interval.standard_cost)} USD，实际 ${estimateAmountText(interval.actual_cost)} USD，${interval.entry_count} 项`).join("；") : "缺费用或计价";
}
