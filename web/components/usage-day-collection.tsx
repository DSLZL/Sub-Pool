"use client";

import { glossary } from "@/lib/glossary";
import { useEffect, useMemo, useRef, useState } from "react";
import { createUsageClient } from "@sub-pool/contracts/client";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import { usageNanos, type UsageDay } from "@sub-pool/contracts/usage";
import { Button, Callout } from "@/components/ui";
import { connectionFailure, displayTime } from "@/lib/connection-data";
import type { UsageWindow } from "@/lib/usage-data";
import { useApp } from "@/lib/store";

const dayNanos = 86400000000000n;
const labels: Record<UsageDay["state"], string> = {
  queued: "已排队", collecting: "采集中", partial: "当日仍在记录", waiting: "等待重试", complete: "采集完成", blocked: "采集暂停",
};
const reasons: Record<string, string> = {
  PAGINATION_CHANGED: "分页变化，重新核对当天",
  RATE_LIMITED: `${glossary.sub2api} 限流`,
  NETWORK_ERROR: `${glossary.sub2api} 连接失败`,
  TIMEOUT: `${glossary.sub2api} 读取超时`,
  UPSTREAM_ERROR: `${glossary.sub2api} 读取失败`,
  INVALID_RESPONSE: `${glossary.sub2api} 返回的用量资料无法核对`,
  FORBIDDEN: `${glossary.sub2api} 凭据缺少用量读取权限`,
  UNAUTHORIZED: `${glossary.sub2api} 凭据未通过验证`,
  VERIFYING: "正在复核分页",
  AUTHORITY_CHANGED: "管理员会话或权限已变化",
  BINDING_CHANGED: "连接或账号身份已变化",
  COLLECTION_DISABLED: "当前采集条件未满足",
  RESTORE_INVALIDATED: "恢复后等待重新核对",
};

type Props = { account?: ManagedAccount; connectionRevision: string | null; range: UsageWindow | null; onChange: () => void };

export function UsageDayCollection({ account, connectionRevision, range, onChange }: Props) {
  const { auth, isAdmin } = useApp();
  if (!isAdmin || auth.status !== "authenticated") return null;
  if (!account) return <div className="text-xs text-muted">选择账号后可以立即采集用量。</div>;
  if (!range || !connectionRevision) return null;
  const from = usageNanos(range.start_at), to = usageNanos(range.end_at);
  if (from === null || to === null || from < 0n || to <= from) return null;
  const first = from / dayNanos, today = BigInt(Date.now()) * 1000000n / dayNanos;
  const endDay = (to - 1n) / dayNanos, last = endDay > today ? today : endDay;
  if (first > last) return <div className="text-xs text-muted">可提交至当前 UTC 日期的用量采集。</div>;
  if (last - first >= 31n) return <div className="text-xs text-muted">立即采集支持最多 31 个 UTC 日期。</div>;
  const start = new Date(Number(first * 86400000n)).toISOString();
  const end = new Date(Number((last + 1n) * 86400000n)).toISOString();
  const key = [auth.epoch, account.id, account.revision, account.source_id, connectionRevision, start, end].join(":");
  return <UsageDayControls key={key} account={account} connectionRevision={connectionRevision} start={start} end={end} onChange={onChange} />;
}

function UsageDayControls({ account, connectionRevision, start, end, onChange }: {
  account: ManagedAccount; connectionRevision: string; start: string; end: string; onChange: () => void;
}) {
  const { refreshSession, displayTimezone } = useApp();
  const input = useMemo(() => ({
    id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: connectionRevision,
    start_at: start, end_at: end,
  }), [account.id, account.revision, account.source_id, connectionRevision, start, end]);
  const request = useRef<{ abort: AbortController; busy: boolean; mutation: number; previous: string | null } | null>(null);
  const [days, setDays] = useState<UsageDay[]>([]);
  const [pending, setPending] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ticket = { abort: new AbortController(), busy: false, mutation: 0, previous: null as string | null };
    request.current = ticket;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const read = async () => {
      const mutation = ticket.mutation;
      try {
        if (ticket.busy) return;
        const result = await createUsageClient(window.location.origin).dayStatus(input, { signal: ticket.abort.signal });
        if (request.current !== ticket || mutation !== ticket.mutation) return;
        const signature = JSON.stringify(result.days.map(day => [day.day, day.state, day.records, day.completed_at]));
        const changed = ticket.previous !== null && ticket.previous !== signature;
        ticket.previous = signature;
        setDays(result.days);
        setError(null);
        setReady(true);
        if (changed) onChange();
      } catch (cause) {
        if (request.current !== ticket || mutation !== ticket.mutation) return;
        const failure = connectionFailure(cause);
        setError(failure.text);
        setDays([]);
        setReady(false);
        if ([401, 403].includes(failure.status)) void refreshSession();
      } finally {
        if (request.current === ticket) timer = setTimeout(read, 2500);
      }
    };
    void read();
    return () => {
      request.current = null;
      ticket.abort.abort();
      clearTimeout(timer);
    };
  }, [input, onChange, refreshSession]);

  const queue = async () => {
    const ticket = request.current;
    if (!ticket || ticket.busy || !ready) return;
    ticket.busy = true;
    ticket.mutation++;
    setPending(true);
    setError(null);
    try {
      const result = await createUsageClient(window.location.origin).queueDays(input, { signal: ticket.abort.signal });
      if (request.current !== ticket) return;
      setDays(result.days);
      onChange();
    } catch (cause) {
      if (request.current !== ticket) return;
      const failure = connectionFailure(cause);
      setReady(false);
      setError(failure.status === 0 ? "采集请求结果未知，先查看任务状态。" : failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    } finally {
      if (request.current === ticket) {
        ticket.busy = false;
        setPending(false);
      }
    }
  };
  const latest = days.reduce<string | null>((last, day) => {
    const at = day.completed_at;
    return at && (last === null || usageNanos(at)! > usageNanos(last)!) ? at : last;
  }, null);
  return <div className="space-y-2">
    <Button variant="secondary" disabled={!ready || pending || !account.monitoring || !account.source_current} onClick={() => void queue()}>
      {pending ? "正在排队…" : "立即采集"}
    </Button>
    {error && <Callout tone="warn" title="用量采集状态">{error}</Callout>}
    <div className="text-xs text-muted" aria-live="polite">
      最近完成 {latest ? displayTime(latest, displayTimezone) : "—"}
    </div>
    {days.length > 0 && <ul className="space-y-1 text-xs text-muted">
      {days.map(day => {
        const reason = reasons[day.reason] ?? (day.state === "waiting" || day.state === "blocked" ? day.reason : null);
        return <li key={day.day}>{day.day} UTC · {labels[day.state]} · {day.records} / {day.total ?? "未知"} 条
          {reason && " · " + reason}
          {day.state === "waiting" && day.next_attempt_at ? " · " + displayTime(day.next_attempt_at, displayTimezone) : ""}
        </li>;
      })}
    </ul>}
  </div>;
}
