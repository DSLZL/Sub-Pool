"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { useEffect, useRef, useState } from "react";
import { createPoolsClient } from "@sub-pool/contracts/client";
import type { WeeklyWallet, WeeklyWalletRecommendation } from "@sub-pool/contracts/pools";
import { Badge, Button, Callout, Checkbox, Input, Modal, Table } from "@/components/ui";
import { connectionFailure } from "@/lib/connection-data";
import { useApp } from "@/lib/store";

const states = {
  enabled: "已开启", paused: "待重新确认", disabled: "已关闭", unavailable: "额度池未就绪",
} as const;
const entryStates = { applied: "已确认", pending: "待确认", unknown: uiCopy.pendingReview, rejected: "未派发", failed: "失败" } as const;
const recommendationStates = {
  unavailable: "启用池并绑定。", waiting_estimate: "等待估算。",
  waiting_actual_cost: "等待实付估算。", balance_unavailable: "核对钱包读数。",
  write_unavailable: "核对写入授权。", pending: `上一笔${glossary.balanceAdjustment}待确认，请到${glossary.adjustmentRecords}核对。`,
  settled: "本周已结算。", ready: "已算出建议。",
  not_due: "本周无需发放。",
  amount_precision_unverified: "精度未核实。",
  legacy_credit_unverified: "核对旧发放。",
  waiting_evidence: `钱包暂停，核对 ${glossary.balanceAdjustment}。`,
} as const;
const issueLabels = {
  account_unavailable: "绑定不可用", waiting_estimate: "等待估算",
  waiting_actual_cost: "核对实付费用",
  contract_changed: "核对合同", usage_unavailable: "缺少结算用量",
  usage_conflict: "用量冲突", balance_mismatch: "余额不符",
  amount_precision_unverified: "精度未核实",
  legacy_credit_unverified: "核对旧发放",
} as const;
const referenceReasons = {
  account_unavailable: "绑定不可用", condition_changed: "核对条件",
  source_stale: "等待观测", period_unavailable: "核对周期",
  period_changed: "等待新周期", waiting_estimate: "等待首份估算",
  waiting_actual_cost: "实付费待核对", waiting_latest_estimate: "等待最新费用",
  collecting_samples: "样本不足", below_minimum_usage: "已用不足 20%",
  unstable_estimate: "波动超过 10%", ready: "已达标",
} as const;

type WeeklyWalletSubject = { id: string; name: string };
type WeeklyWalletDialogProps = (
  { mode: "settings"; pool: WeeklyWalletSubject; member?: never } |
  { mode: "settings" | "recommendation"; member: WeeklyWalletSubject; pool?: never }
) & {
  time: (value: string) => string; close: () => void; onApplied?: () => void;
};
type GroupPlan = NonNullable<WeeklyWallet["groups"]>[number]["group"];
type IssuancePlan = NonNullable<WeeklyWallet["issuances"]>[number]["issuance"];
function UsageAnomalies({ usage }: { usage?: IssuancePlan["contributions"][number]["usage"] }) {
  return usage && (usage.unattributed_rows > 0 || usage.cost_issue_rows > 0)
    ? <> · 归属不明 {usage.unattributed_rows} 条 · 费用异常 {usage.cost_issue_rows} 条 · <Timestamp short value={usage.observed_at} /></> : null;
}
function WeeklyIssuanceBreakdown({ issuance, confirmed }: { issuance: IssuancePlan; confirmed?: boolean }) {
  return <>
    <Table rows={issuance.contributions} rowKey={row => row.pool_id + ":" + row.account_id} cols={[
      { key: "account", label: uiCopy.account, render: row => row.name },
      { key: "end", label: "重置", render: row => <Timestamp short value={row.period_end} /> },
      { key: "gross", label: "应得", render: row => <span title={"合同 #" + row.contract_id}>{row.gross} · {row.share_percent}%</span> },
      { key: "prior", label: "本周期已到账", render: row => row.prior_issued },
      { key: "amount", label: confirmed ? "本次已到账" : "本次发放额", render: row => row.amount },
      { key: "debt", label: "新增待抵扣", render: row => row.debt },
    ]} />
    {issuance.contributions.flatMap(row => row.offsets.map(offset => <p key={row.account_id + ":" + offset.origin} className="text-sm text-muted">
      {row.name} 抵扣 {offset.amount} · {offset.origin} · <Timestamp short value={offset.cycle.period_end} />
    </p>))}
    {issuance.contributions.filter(row => row.tail !== "0").map(row => <p key={"tail:" + row.account_id} className="text-sm text-muted">
      {row.name} 精度尾差 {row.tail}
    </p>)}
    {issuance.contributions.filter(row => row.usage && (row.usage.unattributed_rows || row.usage.cost_issue_rows)).map(row => <p key={"usage:" + row.account_id} className="text-sm text-muted">
      {row.name}<UsageAnomalies usage={row.usage} />
    </p>)}
  </>;
}
function WeeklyWalletBreakdown({ group, time }: { group: GroupPlan; time: WeeklyWalletDialogProps["time"] }) {
  const rows = [...group.contributions, ...group.closed];
  const accountName = (cycle: GroupPlan["offsets"][number]["from"]) =>
    rows.find(row => row.pool_id === cycle.pool_id && row.account_id === cycle.account_id && row.period_end === cycle.period_end)?.name ?? cycle.account_id;
  return <>
    <Table rows={rows} rowKey={row => `${row.pool_id}:${row.account_id}:${row.period_end}`} cols={[
      { key: "account", label: uiCopy.account, render: row => row.name },
      { key: "end", label: "重置", render: row => <Timestamp short value={row.period_end} note={`周期开始 ${time(row.period_start)} · ${row.period_start}`} /> },
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
  const [accounts, setAccounts] = useState<WeeklyWalletSubject[]>([]);
  const [capacities, setCapacities] = useState<Record<string, string>>({});
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
  const issuance = proposal && "issuance" in proposal ? proposal.issuance : null;
  const entries = saved ? [...(saved.issuances ?? []), ...(saved.groups ?? []), ...saved.entries] : [];
  const entryKey = (row: (typeof entries)[number]) => ("issuance" in row ? "issuance:" : "group" in row ? "wallet:" : "account:") + row.id;
  const history = entries.find(entry => entryKey(entry) === historyID);
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
        const [result, contract] = await Promise.all([client.weeklyWallet({ id }, options), client.get({ id }, options)]);
        if (abort.signal.aborted) return;
        if (result.pool_id !== id || contract.pool_id !== id || result.pool_revision !== contract.revision) throw new Error("unexpected weekly pool");
        setSaved(result); setEnabled(result.enabled); setAutoAllocate(result.auto_allocate);
        setAccounts(contract.accounts); setCapacities(result.first_week_capacity ?? {});
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
    const first_week_capacity = Object.fromEntries(accounts.flatMap(account => {
      const amount = capacities[account.id]?.trim();
      if (!amount) return [];
      const normalized = amount.includes(".") ? amount.replace(/0+$/, "").replace(/\.$/, "") : amount;
      return [[account.id, normalized]];
    }));
    const input = { id: target.id, pool_revision: saved.pool_revision, revision: saved.revision, enabled, auto_allocate: autoAllocate, first_week_capacity };
    try {
      const result = await client.configureWeeklyWallet(input, { signal });
      if (signal.aborted) return;
      if (result.pool_id !== input.id || result.pool_revision !== input.pool_revision ||
        BigInt(result.revision) !== BigInt(input.revision) + 1n ||
        result.enabled !== input.enabled || result.auto_allocate !== input.auto_allocate ||
        accounts.some(({ id }) => result.first_week_capacity?.[id] !== first_week_capacity[id])) {
        throw new Error("unexpected weekly wallet receipt");
      }
      setSaved(result); setFeedback("设置已保存。");
      if (memberID) readAgain();
    } catch (error) {
      if (signal.aborted) return;
      const failure = connectionFailure(error);
      setSaved(null); setConfirmed(false);
      setFeedback(failure.status === 409 ? "设置已变。" : uiCopy.pendingResult + failure.text);
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
      const samePlan = "issuance" in receipt
        ? issuance && receipt.issuance.amount === issuance.amount &&
          receipt.issuance.outstanding_debt === issuance.outstanding_debt &&
          JSON.stringify(receipt.issuance.contributions) === JSON.stringify(issuance.contributions)
        : "group" in receipt
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
      setFeedback(failure.status === 409 ? "条件已变。" : ` 待核对，查看${glossary.adjustmentRecords}。` + failure.text);
      if ([401, 403].includes(failure.status)) void refreshSession();
    } finally {
      busy.current = false;
      if (!signal.aborted) setSaving(false);
    }
  }

  const title = member ? `${settings ? "周额度设置" : uiCopy.weeklyRecommendation} · ${member.name}` : `周额度发放 · ${pool?.name}`;
  return <Modal open onClose={() => { if (!busy.current) close(); }} title={title} footer={<>
    <Button disabled={saving} onClick={() => { setFeedback(null); readAgain(); }}>{settings ? uiCopy.reload : "重算"}</Button>
    {settings && target && <Button disabled={!saved || saving || saved.state === "unavailable"} onClick={() => void save()}>保存设置</Button>}
    {basis && <Button variant="primary" disabled={saving || !confirmed} onClick={() => void apply()}>{issuance ? "确认发放" : "应用建议余额"}</Button>}
  </>}>
    <div className="space-y-3">
      {feedback && <Callout tone="info">{feedback}</Callout>}
      {!settings && !recommendation && !feedback && <p role="status" className="text-sm text-muted">计算中…</p>}
      {!settings && recommendation && !proposal && (!reference || recommendation.issues) && <Callout tone="info">
        {recommendationStates[recommendation.state]}
        {recommendation.issues?.map((issue, index) => <div key={index}>{issue.account_id || issue.pool_id} · {issueLabels[issue.reason]}<UsageAnomalies usage={issue.usage} /></div>)}
      </Callout>}
      {reference && <>
        {reference.truncated && <Callout tone="warn">只显示部分。</Callout>}
        {reference.accounts.length > 1 && <Callout tone="info">参考合计  {reference.amount ?? uiCopy.pendingReview}</Callout>}
        <Table rows={reference.accounts} rowKey={row => `${row.pool_id}:${row.account_id}`} cols={[
          { key: "name", label: uiCopy.account, render: row => row.name },
          { key: "share", label: "合同份额", render: row => row.share_percent === null ? "—" : `${row.share_percent}%` },
          { key: "amount", label: "参考周额度", align: "right", render: row => row.amount ?? "—" },
          { key: "basis", label: "依据", render: row => row.basis_at
            ? <><Timestamp short value={row.basis_at} /> · 已用 {row.used_percent}%</> : "—" },
          { key: "samples", label: "样本", render: row => `${row.samples}/3` },
          { key: "state", label: uiCopy.status, render: row => referenceReasons[row.reason] },
        ]} />
      </>}
      {plan && proposal && <Callout tone="info">
        份额 {plan.share_percent}% · 应得 {plan.gross} · 已用 {plan.spent} · 期初欠额 {plan.opening_debt} · 结转 {plan.debt}
        <div>余额 {plan.balance_before} → {plan.target} · 增减 {proposal.delta} · 重置 {time(proposal.period_end)}</div>
      </Callout>}
      {group && <>
        <Callout tone="info">余额 {group.balance_before} → {group.target} · 增减 {group.delta}</Callout>
        <WeeklyWalletBreakdown group={group} time={time} />
      </>}
      {issuance && <>
        <Callout tone="info">本次发放 {issuance.amount} · 当前余额 {issuance.balance ?? "未报告"} · 冻结 {issuance.frozen ?? "未报告"} · 待抵扣余额 {issuance.outstanding_debt}</Callout>
        <WeeklyIssuanceBreakdown issuance={issuance} />
        {recommendation?.issues?.map(issue => <p key={issue.account_id + issue.reason} className="text-sm text-muted">
          {issue.account_id || issue.pool_id} · {issueLabels[issue.reason]}<UsageAnomalies usage={issue.usage} />
        </p>)}
      </>}
      {basis && <Checkbox label="确认专用钱包" checked={confirmed} disabled={saving} onChange={setConfirmed} />}
      {proposal && !basis && <Callout tone="info">{recommendationStates.write_unavailable}</Callout>}
      {settings && recommendation && !target && <Callout tone="info">暂无启用池。</Callout>}
      {saved && <>
        {member && <p className="text-sm text-muted">当前发放设置 · {target?.name}</p>}
        <Badge tone={saved.state === "enabled" ? "ok" : "muted"}>{states[saved.state]}</Badge>
        {entries.some(row => row.state === "unknown" || row.state === "pending") && <Callout tone="warn">待核对 · 查{glossary.adjustmentRecords}</Callout>}
        <Checkbox label="开启半额预发" checked={enabled} disabled={saving || saved.state === "unavailable"} onChange={setEnabled} />
        <Checkbox label="自动正式发放" checked={autoAllocate} disabled={saving || saved.state === "unavailable"} onChange={setAutoAllocate} />
        {accounts.map(account => <label key={account.id} className="block space-y-1 text-sm">
          <span>{account.name}   · 首周容量 </span>
          <Input inputMode="decimal" placeholder="等待正式估算" value={capacities[account.id] ?? ""} disabled={saving || saved.state === "unavailable"}
            onChange={event => setCapacities({ ...capacities, [account.id]: event.target.value })} />
        </label>)}
        <Table rows={memberID ? entries.filter(row => row.participant_id === memberID) : entries} rowKey={entryKey} cols={[
          { key: "member", label: uiCopy.participant, render: row => row.name },
          { key: "time", label: "重置", render: row => <Timestamp short value={row.period_end} /> },
          { key: "phase", label: "阶段", render: row => row.phase === "advance" ? "预发" : "正式分配" },
          { key: "gross", label: "应得", render: row => "issuance" in row
            ? row.issuance.contributions.filter(source => source.pool_id === saved.pool_id).map(source => `${source.name} ${source.gross}`).join(" · ") : "group" in row
            ? row.group.contributions.filter(source => source.pool_id === saved.pool_id).map(source => `${source.name} ${source.gross}`).join(" · ") : row.plan.gross },
          { key: "used", label: "旧账已用", render: row => "issuance" in row ? "—" : "group" in row
            ? row.group.contributions.filter(source => source.pool_id === saved.pool_id).map(source => source.spent).join(" · ") : row.plan.spent },
          { key: "target", label: "到账 / 旧余额", render: row => "issuance" in row
            ? row.state === "applied" ? row.issuance.amount : "—" : "group" in row ? row.group.target : row.plan.target },
          { key: "debt", label: "待抵扣 / 旧欠额", render: row => "issuance" in row
            ? row.issuance.contributions.filter(source => source.pool_id === saved.pool_id).map(source => source.debt).join(" · ") : "group" in row
            ? row.group.contributions.filter(source => source.pool_id === saved.pool_id).map(source => source.debt).join(" · ") : row.plan.debt },
          { key: "state", label: uiCopy.status, render: row => `${entryStates[row.state]}${row.application_id ? ` · ${glossary.balanceAdjustment} #${row.application_id}` : ""}` },
          { key: "detail", label: "分账", render: row => "group" in row || "issuance" in row
            ? <Button size="sm" onClick={() => setHistoryID(historyID === entryKey(row) ? null : entryKey(row))}>{historyID === entryKey(row) ? "收起分账" : "查看分账"}</Button> : "—" },
        ]} />
        {history && "group" in history && <>
          <Callout tone="info">账目 #{history.id} · {entryStates[history.state]} · 本次增减 {history.group.delta}</Callout>
          <WeeklyWalletBreakdown group={history.group} time={time} />
        </>}
        {history && "issuance" in history && <>
          <Callout tone="info">账目 #{history.id} · {entryStates[history.state]}</Callout>
          <WeeklyIssuanceBreakdown issuance={history.issuance} confirmed={history.state === "applied"} />
        </>}
      </>}
    </div>
  </Modal>;
}
