"use client";

import { useEffect, useRef, useState } from "react";
import { createPoolsClient } from "@sub-pool/contracts/client";
import type { WeeklyWallet } from "@sub-pool/contracts/pools";
import { Badge, Button, Callout, Checkbox, Modal, Table } from "@/components/ui";
import { connectionFailure } from "@/lib/connection-data";
import { useApp } from "@/lib/store";

const states = {
  enabled: "已开启", paused: "待重新确认", disabled: "已关闭", unavailable: "当前额度池不符合发放条件",
} as const;
const entryStates = { applied: "已确认", pending: "正在确认", unknown: "待核对", rejected: "未派发", failed: "失败" } as const;

export function WeeklyWalletDialog({ pool, time, close }: { pool: { id: string; name: string }; time: (value: string) => string; close: () => void }) {
  const { refreshSession } = useApp();
  const [version, refresh] = useState(0);
  const [saved, setSaved] = useState<WeeklyWallet | null>(null);
  const [enabled, setEnabled] = useState(false), [autoAllocate, setAutoAllocate] = useState(false);
  const [saving, setSaving] = useState(false), [feedback, setFeedback] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);
  const busy = useRef(false);
  useEffect(() => {
    const abort = new AbortController();
    request.current = abort;
    void createPoolsClient(window.location.origin).weeklyWallet({ id: pool.id }, { signal: abort.signal }).then(result => {
      if (abort.signal.aborted) return;
      if (result.pool_id !== pool.id) { setFeedback("发放状态与当前池不一致。"); return; }
      setSaved(result); setEnabled(result.enabled); setAutoAllocate(result.auto_allocate);
    }).catch(error => {
      if (abort.signal.aborted) return;
      const failure = connectionFailure(error);
      setFeedback(failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    });
    return () => abort.abort();
  }, [pool.id, version, refreshSession]);

  async function save() {
    if (!saved || busy.current || !request.current) return;
    busy.current = true; setSaving(true); setFeedback(null);
    const signal = request.current.signal;
    const client = createPoolsClient(window.location.origin);
    const input = { id: pool.id, pool_revision: saved.pool_revision, revision: saved.revision, enabled, auto_allocate: enabled && autoAllocate };
    try {
      const result = await client.configureWeeklyWallet(input, { signal });
      if (signal.aborted) return;
      if (result.pool_id !== pool.id || result.pool_revision !== input.pool_revision ||
        BigInt(result.revision) !== BigInt(input.revision) + 1n ||
        result.enabled !== input.enabled || result.auto_allocate !== input.auto_allocate) {
        throw new Error("unexpected weekly wallet receipt");
      }
      setSaved(result); setFeedback("设置已保存。");
    } catch (error) {
      if (signal.aborted) return;
      const failure = connectionFailure(error);
      setSaved(null);
      setFeedback(failure.status === 409 ? "池合同或发放设置已变化，请重新读取。" : "保存结果待核对，请重新读取当前设置。" + failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    } finally {
      busy.current = false;
      if (!signal.aborted) setSaving(false);
    }
  }

  return <Modal open onClose={close} title={`周额度发放 · ${pool.name}`} footer={<>
    <Button disabled={saving} onClick={() => { setFeedback(null); setSaved(null); refresh(value => value + 1); }}>重新读取</Button>
    <Button variant="primary" disabled={!saved || saving || saved.state === "unavailable"}
      onClick={() => void save()}>{saving ? "保存中…" : "保存设置"}</Button>
  </>}>
    <div className="space-y-3">
      {feedback && <Callout tone="info">{feedback}</Callout>}
      {saved && <>
        <Badge tone={saved.state === "enabled" ? "ok" : "muted"}>{states[saved.state]}</Badge>
        <Callout tone="info">使用专用 Sub2API 钱包。单账号池在原生周限重置后按上次正式额度的一半预发，并用后续额度抵扣未清欠额。</Callout>
        {saved.entries.some(row => row.state === "unknown" || row.state === "pending") && <Callout tone="warn">待确认的调账可在「余额建议与应用」核对原生记录。</Callout>}
        <Checkbox label="开启半额预发" checked={enabled} disabled={saving || saved.state === "unavailable"} onChange={setEnabled} />
        <Checkbox label="估算达标后自动按份额正式分配" checked={enabled && autoAllocate} disabled={!enabled || saving} onChange={setAutoAllocate} />
        <Table rows={saved.entries} rowKey={row => row.id} cols={[
          { key: "member", label: "参与者", render: row => row.name },
          { key: "time", label: "周限重置", render: row => time(row.period_end) },
          { key: "phase", label: "阶段", render: row => row.phase === "advance" ? "预发" : "正式分配" },
          { key: "gross", label: "应得", render: row => row.plan.gross },
          { key: "used", label: "临时用量", render: row => row.plan.spent },
          { key: "target", label: "结算余额", render: row => row.plan.target },
          { key: "debt", label: "结转欠额", render: row => row.plan.debt },
          { key: "state", label: "状态", render: row => `${entryStates[row.state]}${row.application_id ? ` · 调账 #${row.application_id}` : ""}` },
        ]} />
      </>}
    </div>
  </Modal>;
}
