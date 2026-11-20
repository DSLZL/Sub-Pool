"use client";

import { useEffect, useRef, useState } from "react";
import { createPoolsClient } from "@sub-pool/contracts/client";
import type { WeeklyWallet, WeeklyWalletRecommendation } from "@sub-pool/contracts/pools";
import { Badge, Button, Callout, Checkbox, Modal, Table } from "@/components/ui";
import { connectionFailure } from "@/lib/connection-data";
import { useApp } from "@/lib/store";

const states = {
  enabled: "已开启", paused: "待重新确认", disabled: "已关闭", unavailable: "当前额度池不符合发放条件",
} as const;
const entryStates = { applied: "已确认", pending: "正在确认", unknown: "待核对", rejected: "未派发", failed: "失败" } as const;
const recommendationStates = {
  unavailable: "周额度发放需要属于单个启用额度池的专用钱包。", waiting_estimate: "等待同一周限周期的新鲜额度和达标估算。",
  waiting_actual_cost: "标准费用估算已达标，钱包实际费用估算仍在核对。", balance_unavailable: "当前钱包读数、冻结或余额变化需要核对。",
  write_unavailable: "当前原生模式或钱包写入条件尚未满足。", pending: "上一笔调账待确认，请到应用记录核对。",
  settled: "这个周限周期已正式结算。", ready: "周额度建议已计算。",
} as const;

type WeeklyWalletSubject = { id: string; name: string };
type WeeklyWalletDialogProps = ({ pool: WeeklyWalletSubject; member?: never } | { member: WeeklyWalletSubject; pool?: never }) & {
  time: (value: string) => string; close: () => void; onApplied?: () => void;
};
export function WeeklyWalletDialog({ pool, member, time, close, onApplied }: WeeklyWalletDialogProps) {
  const { refreshSession } = useApp();
  const [version, refresh] = useState(0);
  const [saved, setSaved] = useState<WeeklyWallet | null>(null);
  const [recommendation, setRecommendation] = useState<WeeklyWalletRecommendation | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [enabled, setEnabled] = useState(false), [autoAllocate, setAutoAllocate] = useState(false);
  const [saving, setSaving] = useState(false), [feedback, setFeedback] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);
  const busy = useRef(false);
  const memberID = member?.id, poolID = pool?.id;
  const target = pool ?? recommendation?.pool;
  const proposal = recommendation?.proposal;
  useEffect(() => {
    const abort = new AbortController();
    request.current = abort;
    void (async () => {
      try {
        const client = createPoolsClient(window.location.origin), options = { signal: abort.signal };
        const view = memberID ? await client.weeklyWalletRecommendation({ id: memberID }, options) : null;
        if (abort.signal.aborted) return;
        if (view && view.participant_id !== memberID) throw new Error("unexpected weekly member");
        setRecommendation(view);
        const id = poolID ?? view?.pool?.id;
        if (!id) return;
        const result = await client.weeklyWallet({ id }, options);
        if (abort.signal.aborted) return;
        if (result.pool_id !== id) throw new Error("unexpected weekly pool");
        setSaved(result); setEnabled(result.enabled); setAutoAllocate(result.auto_allocate);
      } catch (error) {
        if (abort.signal.aborted) return;
        const failure = connectionFailure(error);
        setFeedback(failure.text);
        if ([401, 403].includes(failure.status)) void refreshSession();
      }
    })();
    return () => abort.abort();
  }, [memberID, poolID, version, refreshSession]);

  function readAgain() {
    setSaved(null); setRecommendation(null); setConfirmed(false); refresh(value => value + 1);
  }

  async function save() {
    if (!saved || !target || busy.current || !request.current) return;
    busy.current = true; setSaving(true); setFeedback(null);
    const signal = request.current.signal;
    const client = createPoolsClient(window.location.origin);
    const input = { id: target.id, pool_revision: saved.pool_revision, revision: saved.revision, enabled, auto_allocate: autoAllocate };
    try {
      const result = await client.configureWeeklyWallet(input, { signal });
      if (signal.aborted) return;
      if (result.pool_id !== input.id || result.pool_revision !== input.pool_revision ||
        BigInt(result.revision) !== BigInt(input.revision) + 1n ||
        result.enabled !== input.enabled || result.auto_allocate !== input.auto_allocate) {
        throw new Error("unexpected weekly wallet receipt");
      }
      setSaved(result); setFeedback("设置已保存。");
      if (memberID) readAgain();
    } catch (error) {
      if (signal.aborted) return;
      const failure = connectionFailure(error);
      setSaved(null); setConfirmed(false);
      setFeedback(failure.status === 409 ? "池合同或发放设置已变化，请重新读取。" : "保存结果待核对，请重新读取当前设置。" + failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    } finally {
      busy.current = false;
      if (!signal.aborted) setSaving(false);
    }
  }

  async function apply() {
    if (!memberID || !target || !proposal || !saved || !confirmed || busy.current || !request.current) return;
    busy.current = true; setSaving(true); setFeedback(null); setConfirmed(false);
    const signal = request.current.signal;
    try {
      const receipt = await createPoolsClient(window.location.origin).applyWeeklyWallet({
        pool_id: target.id, participant_id: memberID, basis: proposal.basis,
      }, { signal });
      if (signal.aborted) return;
      if (receipt.participant_id !== memberID || receipt.period_end !== proposal.period_end || JSON.stringify(receipt.plan) !== JSON.stringify(proposal.plan)) {
        throw new Error("unexpected weekly settlement receipt");
      }
      const operation = receipt.application_id ? `（调账 #${receipt.application_id}）` : "";
      setFeedback(`正式结算${entryStates[receipt.state]}${operation}。`);
      onApplied?.();
      readAgain();
    } catch (error) {
      if (signal.aborted) return;
      const failure = connectionFailure(error);
      setSaved(null); setRecommendation(null);
      setFeedback(failure.status === 409 ? "周期、份额、余额或调账状态已变化，请重新读取建议。" : "结算结果待核对，请先读取应用记录。" + failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    } finally {
      busy.current = false;
      if (!signal.aborted) setSaving(false);
    }
  }

  return <Modal open onClose={() => { if (!busy.current) close(); }} title={member ? `周额度建议 · ${member.name}` : `周额度发放 · ${pool?.name}`} footer={<>
    <Button disabled={saving} onClick={() => { setFeedback(null); readAgain(); }}>重新读取</Button>
    {target && <Button disabled={!saved || saving || saved.state === "unavailable"} onClick={() => void save()}>保存设置</Button>}
    {proposal && <Button variant="primary" disabled={!saved || saving || !confirmed} onClick={() => void apply()}>应用本周建议</Button>}
  </>}>
    <div className="space-y-3">
      {feedback && <Callout tone="info">{feedback}</Callout>}
      {recommendation && !proposal && <Callout tone="info">{recommendationStates[recommendation.state]}</Callout>}
      {proposal && <Callout tone="info">
        本周份额 {proposal.plan.share_percent}%，正式应得 {proposal.plan.gross}。扣除临时用量 {proposal.plan.spent} 和期初欠额 {proposal.plan.opening_debt} 后，建议余额为 {proposal.plan.target}。
        当前余额 {proposal.plan.balance_before}，本次增减 {proposal.delta}，结转欠额 {proposal.plan.debt}。周限重置于 {time(proposal.period_end)}。
      </Callout>}
      {proposal && <Checkbox label="确认这是此池的专用钱包，并应用上述建议" checked={confirmed} disabled={saving} onChange={setConfirmed} />}
      {saved && <>
        {member && <p className="text-sm text-muted">所属额度池 {target?.name}</p>}
        <Badge tone={saved.state === "enabled" ? "ok" : "muted"}>{states[saved.state]}</Badge>
        <Callout tone="info">使用专用 Sub2API 钱包。单账号池在原生周限重置后按上次正式额度的一半预发，并用后续额度抵扣未清欠额。</Callout>
        {saved.entries.some(row => row.state === "unknown" || row.state === "pending") && <Callout tone="warn">待确认的调账可在「余额建议与应用」核对原生记录。</Callout>}
        <Checkbox label="开启半额预发" checked={enabled} disabled={saving || saved.state === "unavailable"} onChange={setEnabled} />
        <Checkbox label="估算达标后自动应用正式分配" checked={autoAllocate} disabled={saving || saved.state === "unavailable"} onChange={setAutoAllocate} />
        <Table rows={memberID ? saved.entries.filter(row => row.participant_id === memberID) : saved.entries} rowKey={row => row.id} cols={[
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
