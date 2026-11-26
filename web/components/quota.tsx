"use client";

import { glossary } from "@/lib/glossary";
import { Badge, FreshTag, Outcome, Progress, Val, cx } from "@/components/ui";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { CollectionJob, QuotaWindow } from "@sub-pool/contracts/monitoring";
import { accountStates, compactTime, quotaSource, quotaWindow, windowTitle, type AccountRow } from "@/lib/accounts-data";
import { displayTime } from "@/lib/connection-data";
import { useApp } from "@/lib/store";

export function WindowRow({ w, label, source }: { w: QuotaWindow | null; label: string; source: string }) {
  const { displayTimezone } = useApp();
  if (!w)
    return (
      <div>
        <div className="mb-1 flex items-center justify-between text-[12px]">
          <span className="text-muted">{label}</span>
          <span className="text-faint">窗口未知</span>
        </div>
        <Progress value={null} label={`${label} 未返回`} />
      </div>
    );
  const value = w.used_percent === null ? null : Number(w.used_percent);
  const tone = value === null ? "brand" : value >= 90 ? "bad" : value >= 70 ? "warn" : "brand";
  return (
    <div>
      <div className="mb-1 flex items-center justify-between gap-2 text-[12px]">
        <span className="text-muted">
          {windowTitle(w)}
          <span className="ml-1.5 text-faint">· {source}</span>
        </span>
        <span className="num font-medium">{w.used_percent === null ? <Val v={null} /> : `${w.used_percent}% 已用`}</span>
      </div>
      <Progress value={value} tone={tone} label={`${label} 已用`} />
      <div className="mt-1 text-[11.5px] text-faint" title={w.reset_at ?? undefined}>重置：{w.reset_at ? displayTime(w.reset_at, displayTimezone, false) : "未知"}{w.reset_expired ? "（已过期）" : ""}</div>
    </div>
  );
}

export function StatusBadges({ a, attempt }: { a: ManagedAccount; attempt?: CollectionJob | null }) {
  const { account, runtime } = a.snapshot;
  return (
    <div className="flex flex-wrap gap-1">
      {!a.monitoring && <Badge tone="muted">监控已暂停</Badge>}
      {account.status === "active" ? <Badge tone="ok">启用</Badge> : account.status === "disabled" ? <Badge tone="muted">停用</Badge> : <Badge tone="unk">{glossary.sub2api} 状态：{account.status}</Badge>}
      {account.schedulable ? (
        <Badge tone="ok">可调度</Badge>
      ) : (
        <Badge tone="warn">不可调度</Badge>
      )}
      {runtime.rate_limited_at !== null && <Badge tone="warn">曾报告限流</Badge>}
      {runtime.error_present === true && <Badge tone="bad">{glossary.sub2api} 报告异常</Badge>}
      {runtime.error_present === null && <Badge tone="unk">异常：未知</Badge>}
      {a.collection_state !== "awaiting_collector" && <Badge tone="unk">{accountStates[a.collection_state]}</Badge>}
      {attempt && <span className="inline-flex items-center gap-1 text-[11.5px]" title="最近采集尝试">最近尝试 <Outcome k={attempt.state} /></span>}
    </div>
  );
}

export function AccountCard({ a, onClick }: { a: AccountRow; onClick?: () => void }) {
  const { displayTimezone } = useApp();
  const { account, observation, error } = a;
  return (
    <button
      onClick={onClick}
      className={cx(
        "flex h-full w-full flex-col rounded-lg border border-line bg-surface p-4 text-left shadow-[var(--shadow)] transition-colors",
        onClick && "hover:border-brand/50",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate text-[14px] font-semibold">{account.name}</div>
          <div className="truncate font-mono text-[11.5px] text-faint">
            {account.upstream_id}
          </div>
        </div>
        <FreshTag s={observation?.freshness ?? "unknown"} />
      </div>
      <div className="mt-2.5">
        <StatusBadges a={account} attempt={observation?.latest_attempt} />
      </div>
      {error && <div className="mt-2 text-[12px] text-bad">{error}</div>}
      <div className="mt-3.5 mb-3 space-y-3">
        <WindowRow w={quotaWindow(a, "short")} label="短窗口" source={quotaSource(a)} />
        <WindowRow w={quotaWindow(a, "seven_day")} label="7 天窗口" source={quotaSource(a)} />
      </div>
      <div className="mt-auto flex justify-between border-t border-line pt-2.5 text-[11.5px] text-muted">
        <span title={observation?.snapshot?.received_at}>最近获取 {compactTime(observation?.snapshot?.received_at, displayTimezone)}</span>
        <span title={observation?.snapshot?.source_at ?? undefined}>源观测 {observation?.snapshot?.source_at ? compactTime(observation.snapshot.source_at, displayTimezone) : "未知"}</span>
      </div>
    </button>
  );
}
