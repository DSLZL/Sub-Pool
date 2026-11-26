"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import Link from "next/link";
import { Timestamp } from "@/components/timestamp";
import { CursorNavigation } from "@/components/cursor-navigation";
import { useEffect, useState } from "react";
import { createEstimatesClient } from "@sub-pool/contracts/client";
import type { NativeWeeklyCurrent, NativeWeeklyRecord } from "@sub-pool/contracts/estimates";
import { Badge, Button, Callout, Card, Drawer, Empty, Field, Grid, KV, PageHeader, Select, Table, Tabs, Toolbar, Val } from "@/components/ui";
import { RangeBar } from "@/components/charts";
import { DateTimeField } from "@/components/date-time-field";
import { connectionFailure, displayTime } from "@/lib/connection-data";
import { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import { useEstimateData } from "@/lib/use-estimate-data";
import { estimateAmountText, estimateCostText, estimateStates, historyReason } from "@/lib/estimates-data";

export default function EstimatesPage() {
  const scope = useScope();
  const { auth, displayTimezone } = useApp();
  const time = (value: string | null) => displayTime(value, displayTimezone, false);
  const [tab, setTab] = useState("current");
  const [weeklyDetail, setWeeklyDetail] = useState<{ epoch: number; record: NativeWeeklyRecord } | null>(null);
  const data = useEstimateData(scope, tab === "legacy");
  const openWeekly = (record: NativeWeeklyRecord) => setWeeklyDetail({ epoch: auth.epoch, record });
  const savedWeekly = weeklyDetail?.epoch === auth.epoch && data.entries.some(entry => entry.account.id === weeklyDetail.record.account_id) ? weeklyDetail.record : null;
  const currentDetail = tab === "current" && savedWeekly ? data.entries.find(entry => entry.account.id === savedWeekly.account_id)?.current.native_weekly : undefined;
  const allocation = currentDetail?.latest?.id === savedWeekly?.id ? currentDetail?.allocation : undefined;
  const one = data.entries.length === 1 ? data.entries[0] : undefined;
  const weekly = one?.current.native_weekly;
  const notice = data.error ?? (!data.ready ? uiCopy.loading : null);
  const empty = tab === "current" && data.ready && !data.error && data.accounts.length === 0;
  const detail = data.detail, saved = detail?.record;

  return (
    <>
      <PageHeader title={uiCopy.estimates} helpTopic="estimates" />
      {notice && <Callout tone={data.error ? "bad" : "unk"}>{notice}</Callout>}
      {empty && <Empty title={uiCopy.noAccounts}>
        {scope.isAdmin && <Link href="/console/connection" className="underline">去连接</Link>}
      </Empty>}

      <Toolbar>
        <Field label={uiCopy.account}>
          <Select value={data.filters.account} onChange={event => { setWeeklyDetail(null); data.selectAccount(event.target.value); }} options={[{ value: "", label: "全部账号" }, ...data.accounts.map(account => ({ value: account.id, label: account.name }))]} />
        </Field>
        {tab === "legacy" && <>
          <Field label="原生窗口">
            <Select value={data.filters.window_kind} onChange={event => data.selectHistoryWindow(event.target.value)} options={[{ value: "all", label: "全部窗口" }, { value: "short", label: "短窗口" }, { value: "seven_day", label: "长窗口" }]} />
          </Field>
          <DateTimeField label="周期结束" value={data.filters.period_end_at} disabled={data.filters.window_kind === "all"} hint="留空查全部" onChange={data.editPeriod} />
        </>}
      </Toolbar>
      <Tabs value={tab} onChange={value => { setWeeklyDetail(null); data.closeDetail(); setTab(value); }}
        tabs={[
          { id: "current", label: "当前估计" },
          { id: "history", label: "七天估算历史" },
          { id: "cycles", label: "配额周期" },
          { id: "legacy", label: "旧估算记录" },
        ]}
      />
      {tab === "current" && <>
        <Grid cols={2}>
          {data.entries.map(({ account, current }) => current.native_weekly ?
            <WeeklyCard key={account.id} name={account.name} weekly={current.native_weekly} time={time} open={openWeekly} /> :
            <Card key={account.id} title={account.name} desc="等待配额。" />)}
        </Grid>
        <CursorNavigation after={data.accountAfter} next={data.accountNext} onChange={data.changeAccountPage} disabled={!data.ready} />
      </>}
      {tab === "history" && data.ready && (one ? (
        <WeeklyHistory key={`${auth.epoch}:${one.account.id}`} account={one.account} open={openWeekly} />
      ) : <Callout tone="info">{uiCopy.chooseAccount}</Callout>)}
      {tab === "legacy" && (
        <Card pad={false} title="旧估算记录">
          <Table
            rows={data.history}
            empty={data.filters.account ? "暂无匹配记录。" : uiCopy.chooseAccount}
            rowKey={(r) => r.id}
            onRow={(record) => { void data.openRecord(record.account_id, record.id); }}
            cols={[
              { key: "t", label: "生成于", render: (r) => <Timestamp short className="num" value={r.generated_at} /> },
              { key: "a", label: uiCopy.account, render: (r) => data.accounts.find(value => value.id === r.account_id)?.name ?? r.account_id },
              { key: "m", label: "场景剩余（标准 USD）", align: "right", render: (r) => <span className="num">{estimateAmountText(r.remaining)}</span> },
              { key: "r", label: uiCopy.status, render: (r) => <><span className="text-muted">{historyReason(r)}</span><Button size="sm" onClick={event => { event.stopPropagation(); void data.openRecord(r.account_id, r.id); }}>查看记录 {r.id}</Button></> },
            ]}
          />
          <CursorNavigation first="0" after={data.after} next={data.next} onChange={data.changeHistoryPage} disabled={!data.ready} />
        </Card>
      )}
      {tab === "cycles" && (
        <Card title={one ? `${one.account.name} · 七天配额` : "七天配额"}>
          {weekly ? <KV cols={1} items={[
            ["周期", weekly.period ? `${time(weekly.period.start_at)} → ${time(weekly.period.end_at)}` : weeklyStates[weekly.state]],
            ["已用", <Val key="used" v={weekly.observation?.used_percent} unit="%" reason="缺少配额观测" />],
            ["源观测时间", time(weekly.observation?.source_at ?? null)],
          ]} /> : <Callout tone="info">选择监控账号。</Callout>}
        </Card>
      )}
      <Drawer open={savedWeekly !== null} onClose={() => setWeeklyDetail(null)} title={`七天估算 #${savedWeekly?.id ?? ""}`}>
        {savedWeekly && <WeeklyEvidence record={savedWeekly} allocation={allocation} time={time} />}
      </Drawer>
      <Drawer open={detail !== null} onClose={data.closeDetail} title={`旧估算 #${saved?.id ?? ""}`}>
        {detail?.error && <Callout tone="warn">{detail.error}</Callout>}
        {detail && !saved && !detail.error && <Callout tone="info">{uiCopy.loading}</Callout>}
        {saved && <div className="space-y-4">
          <KV cols={1} items={[
            ["记录 / 账号", `${saved.id} / ${saved.account_id}`],
            ["生成于", <Timestamp key="generated" value={saved.generated_at} />],
            [uiCopy.status, historyReason(saved)],
            ["观测 / 采集", `${saved.observation_head} / ${saved.capture_head}`],
            ["声明保留比例", saved.configuration.assumptions?.keep_ratio ?? "未知"],
            ["声明前提", saved.configuration.assumptions?.premise ?? "未知"],
            ["单位", saved.complete ? "模拟标准 USD" : "标准 USD"],
          ]} />
          {saved.windows.map(window => <Card key={window.kind} title={window.kind === "short" ? "短窗口" : "长窗口"} actions={<Badge tone={estimateStates[window.state].tone}>{estimateStates[window.state].label}</Badge>}>
            <KV cols={1} items={[
              ["官方周期", window.period ? <span key="period" title={window.period.start_at + " → " + window.period.end_at}>{time(window.period.start_at)} → {time(window.period.end_at)}；{window.period.duration_seconds} 秒</span> : "未知"],
              ["已用", <Val key="used" v={window.current_used_percent} unit="%" />],
              ["本地测算段", window.basis === "manual" ? <span key="manual" title={window.manual_start_at + " → " + window.manual_end_at}>{time(window.manual_start_at)} → {time(window.manual_end_at)}</span> : "采用官方周期"],
              ["说明", estimateStates[window.state].reason],
              ["费用（UTC）", estimateCostText(window)],
              ["容量 / 剩余（USD）", `${estimateAmountText(window.capacity)} / ${estimateAmountText(window.remaining)}`],
            ]} />
          </Card>)}
        </div>}
      </Drawer>
    </>
  );
}

const weeklyStates: Record<NativeWeeklyCurrent["state"], string> = {
  ready: "已估算",
  condition_changed: "待核实账号",
  connection_unavailable: `等待 ${glossary.sub2api} 连接可用`,
  monitoring_paused: "监控暂停",
  awaiting_observation: "等待观测",
  quota_unavailable: uiCopy.readFailed,
  period_unknown: "七天周期缺项",
  source_stale: "观测已过期",
  invalid_evidence: "观测冲突",
  quota_decreased: "用量回落",
  observation_limit: "观测超限",
  awaiting_increase: "等待用量增长",
  collecting: "读取用量中",
  usage_unavailable: "待读取",
  usage_denied: "无权读取用量",
  usage_rate_limited: `${glossary.sub2api} 限流`,
  usage_invalid: `${glossary.sub2api}用量待核对`,
  pagination_changed: "分页已变",
  usage_limit: "单日记录超限",
  no_cost: "标准费为零",
  cost_unavailable: "缺少标准费用",
};

type TimeLabel = (value: string | null) => string;
type WeeklyAllocation = NonNullable<NativeWeeklyCurrent["allocation"]>;
const weeklyConfidence = {
  collecting: "待更多样本",
  unstable: "估算波动",
  trusted: "可分配",
} as const;

function weeklyAmount(value: NativeWeeklyRecord["capacity"] | null, decimals = 2) {
  if (value === null) return "待更新";
  const units = 10n ** BigInt(decimals);
  const numerator = BigInt(value.numerator), denominator = BigInt(value.denominator);
  const rounded = (2n * numerator * units + denominator) / (2n * denominator);
  if (rounded === 0n && numerator > 0n) return `<${(1 / Number(units)).toFixed(decimals)}`;
  return `${rounded / units}.${String(rounded % units).padStart(decimals, "0")}`;
}

function WeeklyCard({ name, weekly, time, open }: { name: string; weekly: NativeWeeklyCurrent; time: TimeLabel; open: (record: NativeWeeklyRecord) => void }) {
  const { latest, observation, period, progress, remaining, allocation } = weekly;
  const ready = latest !== null && remaining !== null;
  return <Card title={name} desc={period && `${time(period.start_at)} → ${time(period.end_at)}`}
    actions={<><Badge tone={ready ? "ok" : latest ? "warn" : "unk"} dot>{ready ? weeklyStates.ready : latest ? "观测已过期" : "待估算"}</Badge>
      {latest && <Button size="sm" onClick={() => open(latest)}>详情</Button>}</>}>
    {latest ? <div className="space-y-4">
      {([["周容量（USD）", latest.capacity], ["剩余（标准 USD）", remaining]] as const).map(([label, amount]) => {
        const point = amount ? Number(amount.numerator) / Number(amount.denominator) : null;
        const capacity = Number(latest.capacity.numerator) / Number(latest.capacity.denominator);
        return <div key={label}>
          <div className="mb-1.5 flex justify-between text-[12px]">
            <span className="text-muted">{label}</span>
            <span className="num font-medium">{weeklyAmount(amount)}</span>
          </div>
          {point !== null && <RangeBar low={point} mid={point} high={point} max={capacity} />}
        </div>;
      })}
    </div> : <Callout tone={weekly.state === "source_stale" ? "warn" : "unk"} title={weeklyStates[weekly.state]} />}
    <div className="mt-4 border-t border-line pt-3 text-[12px] text-muted">
      {observation && <div>已用 {observation.used_percent}% · 观测于 {time(observation.source_at)}</div>}
      {latest?.cumulative && <div>{allocation ? `已按 ${allocation.pool_name} 的份额` :
        weeklyConfidence[latest.cumulative.confidence]}</div>}
      {progress && <div>{ready ? "更新中 · " : ""}{weeklyStates[progress.state]}{progress.total > 0 && ` · ${progress.day.slice(0, 10)} UTC · ${progress.rechecking ? "复核" : "读取"} ${progress.scanned} / ${progress.total}`}</div>}
    </div>
  </Card>;
}

function WeeklyEvidence({ record, allocation, time }: { record: NativeWeeklyRecord; allocation?: WeeklyAllocation; time: TimeLabel }) {
  const { cumulative } = record;
  return <div className="space-y-4">
    <KV cols={1} items={[
      ["生成于", time(record.generated_at)],
      ["周期", `${time(record.period.start_at)} → ${time(record.period.end_at)}`],
      ["样本用量", `${record.observation.used_percent}% · ${time(record.observation.source_at)} · 观测 #${record.observation.observation_id}`],
      ...(record.percentage_point_increase ? [["用量变化", `${estimateAmountText(record.percentage_point_increase)} 个百分点`] as [string, string]] : []),
      ["标准费", `${weeklyAmount(record.standard_cost, 8)} USD · ${record.records} 条记录`],
      ["容量 / 剩余", `${weeklyAmount(record.capacity)} / ${weeklyAmount(record.remaining)} 标准 USD`],
    ]} />
    {cumulative &&
      <Table rows={cumulative.checkpoints} rowKey={point => point.observation.observation_id} cols={[
        { key: "time", label: "截至", render: point => <Timestamp short value={point.observation.source_at} /> },
        { key: "used", label: "已用", render: point => `${point.observation.used_percent}%` },
        { key: "cost", label: "累计标准费（USD）", render: point => weeklyAmount(point.standard_cost, 8) },
        { key: "total", label: "周容量（标准 USD）", render: point => weeklyAmount(point.capacity) },
      ]} />
    }
    {record.samples && <Table rows={record.samples} rowKey={sample => sample.end.observation_id} cols={[
      { key: "from", label: "起点", render: sample => <><Timestamp short value={sample.start.source_at} /> · {sample.start.used_percent}% · #{sample.start.observation_id}</> },
      { key: "to", label: "终点", render: sample => <><Timestamp short value={sample.end.source_at} /> · {sample.end.used_percent}% · #{sample.end.observation_id}</> },
      { key: "cost", label: "标准费（USD）", render: sample => <span title={estimateAmountText(sample.standard_cost)}>{weeklyAmount(sample.standard_cost, 8)} · {sample.records} 条</span> },
      { key: "read", label: "日志核对时间", render: sample => <Timestamp short value={sample.read_at} /> },
    ]} />}
    {allocation && <Card title={`${allocation.pool_name} · 当前比例分配`} desc={`按池合同 #${allocation.contract_id}（标准 USD）`}>
      <Table rows={allocation.members} rowKey={member => member.participant_id} cols={[
        { key: "name", label: uiCopy.participant, render: member => member.name },
        { key: "share", label: "份额", render: member => `${member.share_percent}%` },
        { key: "capacity", label: "总量份额", render: member => weeklyAmount(member.capacity) },
        { key: "remaining", label: "剩余份额", render: member => weeklyAmount(member.remaining) },
      ]} />
      {allocation.reserve && <div className="mt-3 text-[12px] text-muted">未分配 {allocation.reserve.share_percent}% · 总量 {weeklyAmount(allocation.reserve.capacity)} · 剩余 {weeklyAmount(allocation.reserve.remaining)}</div>}
    </Card>}
  </div>;
}

function WeeklyHistory({ account, open }: { account: { id: string; name: string }; open: (record: NativeWeeklyRecord) => void }) {
  const { refreshSession } = useApp();
  const [before, setBefore] = useState("0");
  const [page, setPage] = useState<{ before: string; records: NativeWeeklyRecord[]; next: string | null; error: string | null }>();
  useEffect(() => {
    const abort = new AbortController();
    void createEstimatesClient(window.location.origin).weeklyHistory({ account_id: account.id, before }, { signal: abort.signal }).then(result => {
      if (abort.signal.aborted) return;
      if (result.account_id !== account.id || before !== "0" && result.records.some(record => BigInt(record.id) >= BigInt(before))) {
        setPage({ before, records: [], next: null, error: "历史不符。" });
        return;
      }
      setPage({ before, records: result.records, next: result.next_before, error: null });
    }).catch(error => {
      if (abort.signal.aborted) return;
      const failure = connectionFailure(error);
      setPage({ before, records: [], next: null, error: failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    });
    return () => abort.abort();
  }, [account.id, before, refreshSession]);
  const visible = page?.before === before ? page : undefined;
  const next = visible?.next;
  return <Card title={`七天估算历史 · ${account.name}`} actions={<>
    {before !== "0" && <Button size="sm" onClick={() => setBefore("0")}>最新记录</Button>}
    {next && <Button size="sm" onClick={() => setBefore(next)}>更早记录</Button>}
  </>}>
    {!visible || visible.error || visible.records.length === 0 ? <Callout tone={visible?.error ? "warn" : "info"}>{visible?.error ?? (visible ? "暂无历史。" : uiCopy.loading)}</Callout> :
      <Table rows={visible.records} rowKey={record => record.id} cols={[
        { key: "time", label: "生成于", render: record => <Timestamp short value={record.generated_at} /> },
        { key: "total", label: "周容量（标准 USD）", align: "right", render: record => weeklyAmount(record.capacity) },
        { key: "remaining", label: "剩余（标准 USD）", align: "right", render: record => weeklyAmount(record.remaining) },
        { key: "record", label: "依据", render: record => <Button size="sm" onClick={() => open(record)}>查看记录 {record.id}</Button> },
      ]} />}
  </Card>;
}
