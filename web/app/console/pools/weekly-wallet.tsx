"use client";

import { glossary } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
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
  unavailable: "周额度发放需要启用额度池和当前绑定的专用钱包。", waiting_estimate: "等待同一周限周期的新鲜额度和达标估算。",
  waiting_actual_cost: "标准费用估算已达标，钱包实际费用估算仍在核对。", balance_unavailable: "当前钱包读数、冻结或余额变化需要核对。",
  write_unavailable: "当前原生模式或钱包写入条件尚未满足。", pending: `上一笔${glossary.balanceAdjustment}待确认，请到${glossary.adjustmentRecords}核对。`,
  settled: "这个周限周期已正式结算。", ready: "周额度建议已计算。",
  waiting_evidence: `部分结算依据需要核对，当前钱包暂停${glossary.balanceAdjustment}。`,
} as const;
const issueLabels = {
  account_unavailable: "账号或绑定不可用", waiting_estimate: "周周期或估算尚未就绪",
  contract_changed: "合同或历史周期需要核对", usage_unavailable: "用量证据未覆盖结算时点",
  usage_conflict: "存在冲突的用量记录", balance_mismatch: "用量和原生余额未对上",
  amount_precision_unverified: "用量精度无法核对原生扣减",
} as const;
const referenceReasons = {
  account_unavailable: "账号或绑定不可用", condition_changed: "当前来源或合同依据待核对",
  source_stale: "等待更新额度观测", period_unavailable: "周周期或额度观测待核对",
  period_changed: "等待新周期的费用估算", waiting_estimate: "等待同周期首份估算",
  waiting_actual_cost: "实际费用待核对", waiting_latest_estimate: "最新已用比例的费用仍在核对",
  collecting_samples: "等待同周期新增样本", below_minimum_usage: "等待已用比例达到 20%",
  unstable_estimate: "样本估值波动超过 10%", ready: "该账号估算已达标",
} as const;

type WeeklyWalletSubject = { id: string; name: string };
type WeeklyWalletDialogProps = (
  { mode: "settings"; pool: WeeklyWalletSubject; member?: never } |
  { mode: "settings" | "recommendation"; member: WeeklyWalletSubject; pool?: never }
) & {
  time: (value: string) => string; close: () => void; onApplied?: () => void;
};
type GroupPlan = NonNullable<WeeklyWallet["groups"]>[number]["group"];
function WeeklyWalletBreakdown({ group, time }: { group: GroupPlan; time: WeeklyWalletDialogProps["time"] }) {
  const rows = [...group.contributions, ...group.closed];
  const accountName = (cycle: GroupPlan["offsets"][number]["from"]) =>
    rows.find(row => row.pool_id === cycle.pool_id && row.account_id === cycle.account_id && row.period_end === cycle.period_end)?.name ?? cycle.account_id;
  return <>
    <Table rows={rows} rowKey={row => `${row.pool_id}:${row.account_id}:${row.period_end}`} cols={[
      { key: "account", label: "账号", render: row => row.name },
      { key: "end", label: "周限重置", render: row => <Timestamp short value={row.period_end} note={`周期开始 ${time(row.period_start)} · ${row.period_start}`} /> },
      { key: "share", label: "合同份额", render: row => <span title={`合同 #${row.contract_id}`}>{row.share_percent === "" ? "—" : `${row.share_percent}%`}</span> },
      { key: "gross", label: "应得", render: row => row.gross },
      { key: "used", label: "已用", render: row => row.spent },
      { key: "offset", label: "获抵 / 承担", render: row => `${row.covered} / ${row.used_to_cover}` },
      { key: "remaining", label: "剩余 / 未清", render: row => `${row.remaining} / ${row.debt}` },
    ]} />
    {group.offsets.map(item => <p key={`${item.from.account_id}:${item.from.period_end}:${item.to.account_id}:${item.to.period_end}:${item.at}`} className="text-sm text-muted">
      {accountName(item.from)} 为 {accountName(item.to)} 抵扣 {item.amount} · {time(item.at)}
    </p>)}
  </>;
}
export function WeeklyWalletDialog({ mode, pool, member, time, close, onApplied }: WeeklyWalletDialogProps) {
  const { refreshSession } = useApp();
  const [version, refresh] = useState(0);
  const [saved, setSaved] = useState<WeeklyWallet | null>(null);
  const [recommendation, setRecommendation] = useState<WeeklyWalletRecommendation | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [historyID, setHistoryID] = useState<string | null>(null);
  const [enabled, setEnabled] = useState(false), [autoAllocate, setAutoAllocate] = useState(false);
  const [saving, setSaving] = useState(false), [feedback, setFeedback] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);
  const busy = useRef(false);
  const memberID = member?.id, poolID = pool?.id;
  const settings = mode === "settings";
  const target = pool ?? recommendation?.pool;
  const proposal = settings ? null : recommendation?.proposal;
  const reference = settings ? undefined : recommendation?.reference;
  const basis = proposal?.basis;
  const group = proposal && "group" in proposal ? proposal.group : null;
  const plan = proposal && "plan" in proposal ? proposal.plan : null;
  const entries = saved ? [...(saved.groups ?? []), ...saved.entries] : [];
  const history = saved?.groups?.find(entry => entry.id === historyID);
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
        if (!settings) return;
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
  }, [memberID, poolID, settings, version, refreshSession]);

  function readAgain() {
    setSaved(null); setRecommendation(null); setConfirmed(false); setHistoryID(null); refresh(value => value + 1);
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
    if (!memberID || !target || !proposal || !basis || !confirmed || busy.current || !request.current) return;
    busy.current = true; setSaving(true); setFeedback(null); setConfirmed(false);
    const signal = request.current.signal;
    try {
      const receipt = await createPoolsClient(window.location.origin).applyWeeklyWallet({
        pool_id: target.id, participant_id: memberID, basis,
        ...(group ? { cutoff: group.cutoff } : {}),
      }, { signal });
      if (signal.aborted) return;
      const samePlan = "group" in receipt
        ? group && JSON.stringify(receipt.group) === JSON.stringify(group)
        : plan && JSON.stringify(receipt.plan) === JSON.stringify(plan);
      if (receipt.participant_id !== memberID || receipt.period_end !== proposal.period_end || !samePlan) {
        throw new Error("unexpected weekly settlement receipt");
      }
      const operation = receipt.application_id ? `（${glossary.balanceAdjustment} #${receipt.application_id}）` : "";
      setFeedback(`正式结算${entryStates[receipt.state]}${operation}。`);
      onApplied?.();
      readAgain();
    } catch (error) {
      if (signal.aborted) return;
      const failure = connectionFailure(error);
      setSaved(null); setRecommendation(null);
      setFeedback(failure.status === 409 ? `周期、份额、余额或${glossary.balanceAdjustment}状态已变化，请重新读取建议。` : `结算结果待核对，请先读取${glossary.adjustmentRecords}。` + failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    } finally {
      busy.current = false;
      if (!signal.aborted) setSaving(false);
    }
  }

  const title = member ? `${settings ? "周额度设置" : "周额度建议"} · ${member.name}` : `周额度发放 · ${pool?.name}`;
  return <Modal open onClose={() => { if (!busy.current) close(); }} title={title} footer={<>
    <Button disabled={saving} onClick={() => { setFeedback(null); readAgain(); }}>{settings ? "重新读取" : "重新计算"}</Button>
    {settings && target && <Button disabled={!saved || saving || saved.state === "unavailable"} onClick={() => void save()}>保存设置</Button>}
    {basis && <Button variant="primary" disabled={saving || !confirmed} onClick={() => void apply()}>应用建议余额</Button>}
  </>}>
    <div className="space-y-3">
      {feedback && <Callout tone="info">{feedback}</Callout>}
      {!settings && !recommendation && !feedback && <p role="status" className="text-sm text-muted">正在计算周额度建议…</p>}
      {!settings && recommendation && !proposal && (!reference || recommendation.issues) && <Callout tone="info">
        {recommendationStates[recommendation.state]}
        {recommendation.issues?.map((issue, index) => <div key={index}>{issue.account_id || issue.pool_id} · {issueLabels[issue.reason]}</div>)}
      </Callout>}
      {reference && <>
        {reference.accounts.length > 1 && <Callout tone="info">参考周额度合计 {reference.amount ?? "待核对"}</Callout>}
        <Table rows={reference.accounts} rowKey={row => `${row.pool_id}:${row.account_id}`} cols={[
          { key: "name", label: "账号", render: row => row.name },
          { key: "share", label: "合同份额", render: row => row.share_percent === null ? "—" : `${row.share_percent}%` },
          { key: "amount", label: "参考周额度", align: "right", render: row => row.amount ?? "—" },
          { key: "basis", label: "依据", render: row => row.basis_at
            ? <><Timestamp short value={row.basis_at} /> · 已用 {row.used_percent}%</> : "—" },
          { key: "samples", label: "样本", render: row => `${row.samples}/3` },
          { key: "state", label: "状态", render: row => referenceReasons[row.reason] },
        ]} />
      </>}
      {plan && proposal && <Callout tone="info">
        本周份额 {plan.share_percent}%，正式应得 {plan.gross}。扣除临时用量 {plan.spent} 和期初欠额 {plan.opening_debt} 后，建议余额为 {plan.target}。
        当前余额 {plan.balance_before}，本次增减 {proposal.delta}，结转欠额 {plan.debt}。周限重置于 {time(proposal.period_end)}。
      </Callout>}
      {group && <>
        <Callout tone="info">当前余额 {group.balance_before}，建议余额 {group.target}，本次增减 {group.delta}。</Callout>
        <WeeklyWalletBreakdown group={group} time={time} />
      </>}
      {basis && <Checkbox label="确认这是该参与者的专用钱包，并应用建议余额" checked={confirmed} disabled={saving} onChange={setConfirmed} />}
      {proposal && !basis && <Callout tone="info">{recommendationStates.write_unavailable}</Callout>}
      {settings && recommendation && !target && <Callout tone="info">未找到可配置周额度的启用池。</Callout>}
      {saved && <>
        {member && <p className="text-sm text-muted">当前发放设置 · {target?.name}</p>}
        <Badge tone={saved.state === "enabled" ? "ok" : "muted"}>{states[saved.state]}</Badge>
        <Callout tone="info">各账号在原生周限重置后按上次正式额度预发一半。同一专用钱包可用当期剩余额度抵扣超额。</Callout>
        {entries.some(row => row.state === "unknown" || row.state === "pending") && <Callout tone="warn">待确认的{glossary.balanceAdjustment}可在「{glossary.balances}」核对原生记录。</Callout>}
        <Checkbox label="开启半额预发" checked={enabled} disabled={saving || saved.state === "unavailable"} onChange={setEnabled} />
        <Checkbox label="估算达标后自动调整正式分配" checked={autoAllocate} disabled={saving || saved.state === "unavailable"} onChange={setAutoAllocate} />
        <Table rows={memberID ? entries.filter(row => row.participant_id === memberID) : entries} rowKey={row => ("group" in row ? "wallet:" : "account:") + row.id} cols={[
          { key: "member", label: "参与者", render: row => row.name },
          { key: "time", label: "周限重置", render: row => <Timestamp short value={row.period_end} /> },
          { key: "phase", label: "阶段", render: row => row.phase === "advance" ? "预发" : "正式分配" },
          { key: "gross", label: "应得", render: row => "group" in row
            ? row.group.contributions.filter(source => source.pool_id === saved.pool_id).map(source => `${source.name} ${source.gross}`).join(" · ") : row.plan.gross },
          { key: "used", label: "已用", render: row => "group" in row
            ? row.group.contributions.filter(source => source.pool_id === saved.pool_id).map(source => source.spent).join(" · ") : row.plan.spent },
          { key: "target", label: "结算余额", render: row => "group" in row ? row.group.target : row.plan.target },
          { key: "debt", label: "未清欠额", render: row => "group" in row
            ? row.group.contributions.filter(source => source.pool_id === saved.pool_id).map(source => source.debt).join(" · ") : row.plan.debt },
          { key: "state", label: "状态", render: row => `${entryStates[row.state]}${row.application_id ? ` · ${glossary.balanceAdjustment} #${row.application_id}` : ""}` },
          { key: "detail", label: "分账", render: row => "group" in row
            ? <Button size="sm" onClick={() => setHistoryID(historyID === row.id ? null : row.id)}>{historyID === row.id ? "收起分账" : "查看分账"}</Button> : "—" },
        ]} />
        {history && <>
          <Callout tone="info">账目 #{history.id} · {entryStates[history.state]} · 本次增减 {history.group.delta}</Callout>
          <WeeklyWalletBreakdown group={history.group} time={time} />
        </>}
      </>}
    </div>
  </Modal>;
}
