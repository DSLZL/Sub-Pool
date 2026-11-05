"use client";

import { Badge, FreshTag, Progress, Val, cx } from "@/components/ui";
import type { Account, Window } from "@/lib/mock";

export function WindowRow({ w, label }: { w: Window | null; label: string }) {
  if (!w)
    return (
      <div>
        <div className="mb-1 flex items-center justify-between text-[12px]">
          <span className="text-muted">{label}</span>
          <span className="text-faint">此账号未返回该窗口</span>
        </div>
        <Progress value={null} label={`${label} 未返回`} />
      </div>
    );
  const tone = w.usedPct === null ? "brand" : w.usedPct >= 90 ? "bad" : w.usedPct >= 70 ? "warn" : "brand";
  return (
    <div>
      <div className="mb-1 flex items-center justify-between gap-2 text-[12px]">
        <span className="text-muted">
          {w.kind}
          <span className="ml-1.5 text-faint">· {w.source === "active" ? "主动查询" : "被动快照"}</span>
        </span>
        <span className="num font-medium">{w.usedPct === null ? <Val v={null} /> : `${w.usedPct}% 已用`}</span>
      </div>
      <Progress value={w.usedPct} tone={tone} label={`${w.kind} 已用比例`} />
      <div className="mt-1 text-[11.5px] text-faint">重置：{w.resetAt ?? "未知"}</div>
    </div>
  );
}

export function StatusBadges({ a }: { a: Account }) {
  return (
    <div className="flex flex-wrap gap-1">
      {!a.monitoring && <Badge tone="muted">监控已暂停</Badge>}
      {a.enabled === null ? <Badge tone="unk">启用：未知</Badge> : a.enabled ? <Badge tone="ok">启用</Badge> : <Badge tone="muted">停用</Badge>}
      {a.schedulable === null ? (
        <Badge tone="unk">调度：未知</Badge>
      ) : a.schedulable ? (
        <Badge tone="ok">可调度</Badge>
      ) : (
        <Badge tone="warn">不可调度</Badge>
      )}
      {a.rateLimited === true && <Badge tone="warn">限流中</Badge>}
      {a.rateLimited === null && <Badge tone="unk">限流：未知</Badge>}
      {a.error && <Badge tone="bad">异常</Badge>}
    </div>
  );
}

export function AccountCard({ a, onClick }: { a: Account; onClick?: () => void }) {
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
          <div className="truncate text-[14px] font-semibold">{a.localName}</div>
          <div className="truncate font-mono text-[11.5px] text-faint">
            {a.upstreamId} · {a.plan}
          </div>
        </div>
        <FreshTag s={a.freshness} />
      </div>
      <div className="mt-2.5">
        <StatusBadges a={a} />
      </div>
      {a.error && <div className="mt-2 text-[12px] text-bad">{a.error}</div>}
      <div className="mt-3.5 mb-3 space-y-3">
        <WindowRow w={a.short} label="短周期窗口" />
        <WindowRow w={a.week} label="7 天窗口" />
      </div>
      <div className="mt-auto flex justify-between border-t border-line pt-2.5 text-[11.5px] text-muted">
        <span>最近获取 {a.lastFetch.slice(5)}</span>
        <span>源观测 {a.lastSourceAt ? a.lastSourceAt.slice(5) : "未知"}</span>
      </div>
    </button>
  );
}
