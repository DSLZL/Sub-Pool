"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import Link from "next/link";
import { UsageDayCollection } from "@/components/usage-day-collection";

import { useEffect, useState } from "react";
import { usageCalendar } from "@sub-pool/contracts/participants";
import { Badge, Callout, Card, Empty, Field, Grid, KV, Mono, PageHeader, Segmented, Select, Stat, Table, Tabs, Val } from "@/components/ui";
import { LineChart, CHART_COLORS } from "@/components/charts";
import { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { Timestamp } from "@/components/timestamp";
import { millionTokens, usageDayCost, usageTrend } from "@/lib/usage-data";
import { useUsageData } from "@/lib/use-usage-data";
import { useUsageAccount } from "@/lib/use-usage-account";
import { usageKeyCosts } from "@/lib/usage-account-data";
import { estimateAmountText } from "@/lib/estimates-data";

export default function UsagePage() {
  const scope = useScope(), { isAdmin } = scope;
  const { displayTimezone } = useApp();
  const time = (value: string | null | undefined) => value ? displayTime(value, displayTimezone, false) : "未知";
  const [tab, setTab] = useState("overview");
  const [range, setRange] = useState("14d");
  const [acct, setAcct] = useState("all");

  const usage = useUsageData(scope, range === "7d" ? 7 : 14, acct), view = usage.data?.view;
  const history = useUsageAccount(scope, acct, tab, usage.window, view ?? null);
  const current = history.daily?.current;
  const snapshot = current?.observation.snapshot ?? history.observation?.snapshot;
  const longWindow = snapshot?.windows.find(window => window.kind === "seven_day");
  const currentUsage = current?.usage.windows.find(window => window.kind === "seven_day");
  const calendar = usage.timezone ? usageCalendar(usage.timezone) : null;
  const todayCost = acct === "all" || !calendar || !usage.window || !usage.timezone ? null : usageDayCost(view?.buckets ?? [], calendar.day(usage.window.end_at), "total_cost", usage.timezone);
  const estimate = history.model;
  const rows = usage.data?.records ?? [];
  const accountName = (id: string) => usage.accounts.find(account => account.id === id)?.name ?? id;
  const trendParticipants = Object.entries(usage.data?.byParticipant ?? {});
  const trend = usage.ready && usage.window && usage.timezone
    ? usageTrend(usage.window, trendParticipants.map(([, item]) => item.buckets), usage.timezone)
    : null;
  const dailyRows = history.daily?.records ?? [];
  const keyCost = usageKeyCosts(history.keys);
  useEffect(() => {
    if (usage.catalogReady && acct !== "all" && !usage.accounts.some(account => account.id === acct)) setAcct("all");
  }, [acct, usage.accounts, usage.catalogReady]);

  return (
    <>
      <PageHeader
        title={uiCopy.usage}
        helpTopic="usage"
      />

      <Callout tone="warn" className="mb-4" title={usage.error ? uiCopy.readFailed : !usage.ready ? "读取中" : view ? "统计范围" : "暂无用量"}>
        {usage.error ? <>{usage.error} <button type="button" className="underline" onClick={usage.reload}>刷新</button></> : !usage.ready ? null : view ? <>
          <Timestamp value={view.query_start_at} /> → <Timestamp value={view.query_end_at} />
          {!!usage.data?.missingAccounts.length && <p>待采集：{usage.data.missingAccounts.map(accountName).join("、")}</p>}
          {view.limitations.some(reason => reason === "CAPTURE_PARTIAL" || reason === "DAILY_COVERAGE_INCOMPLETE" || reason === "SCAN_LIMIT") && <p>采集不全。</p>}
        </> : isAdmin ? !usage.accounts.length ? <Link href="/console/connection" className="underline">去连接</Link>
          : tab !== "detail" ? <button type="button" className="underline" onClick={() => setTab("detail")}>去采集</button> : "选账号采集。" : "联系管理员。"}
      </Callout>

      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "overview", label: "用量概览" },
          { id: "detail", label: "费用明细" },
          { id: "history", label: "账号历史" },
        ]}
      />

      {tab === "overview" && (
        <div className="space-y-4">
          <Grid cols={4}>
            <Stat label="请求数" value={<Val v={view?.summary.request_count} digits={0} />} />
            <Stat label="输入 Token" value={<Val v={millionTokens(view?.summary.metrics.input_tokens)} unit="M" digits={1} />} />
            <Stat label="输出 Token" value={<Val v={millionTokens(view?.summary.metrics.output_tokens)} unit="M" digits={1} />} />
            <Stat
              label="缓存读 / 写"
              value={
                <span className="text-[18px]">
                  <Val v={millionTokens(view?.summary.metrics.cache_read_tokens)} unit="M" digits={1} /> / <Val v={millionTokens(view?.summary.metrics.cache_creation_tokens)} unit="M" />
                </span>
              }
            />
          </Grid>
          <Card
            title={isAdmin ? "每日消费趋势" : "授权消费趋势"}
            desc={`日志实际费用 · USD · ${usage.timezone ?? "时区待确认"}`}
            actions={
              <Segmented
                value={range}
                onChange={setRange}
                options={[
                  { id: "7d", label: "7 天" },
                  { id: "14d", label: "14 天" },
                ]}
              />
            }
          >
            {usage.error || !usage.ready ? <Empty title={usage.error ?? uiCopy.loading} />
              : trend ? <LineChart labels={trend.labels} unit=" USD" series={trendParticipants.map(([id, item], i) => ({
                name: `${item.records.find(row => row.attribution.participant_id === id)?.attribution.participant_name ?? id} · ${id}`,
                color: CHART_COLORS[i % CHART_COLORS.length],
                values: trend.values[i],
              }))} />
              : <Empty title="暂无归属费用">
                {isAdmin && view && <button type="button" className="underline" onClick={() => setTab("detail")}>去采集</button>}
              </Empty>}
          </Card>
        </div>
      )}

      {tab === "detail" && (
        <Card pad={false}>
          <div className="flex flex-wrap items-end gap-3 p-4 pb-3">
            <Field label={uiCopy.account} className="w-48">
              <Select
                value={acct}
                onChange={(e) => setAcct(e.target.value)}
                options={[{ value: "all", label: "全部账号" }, ...usage.accounts.map(account => ({ value: account.id, label: account.name }))]}
              />
            </Field>
            <UsageDayCollection account={scope.accounts.find(item => item.id === acct)} connectionRevision={scope.connectionRevision}
              range={usage.window} onChange={usage.reload} />
          </div>
          <Table
            rows={rows}
            rowKey={(r) => `${r.capture_id}.${r.fact.id}`}
            empty={usage.error ?? (!usage.ready ? uiCopy.loading : view ? "暂无记录。" : `没有${glossary.recordedConsumption}。`)}
            cols={[
              { key: "t", label: "时间", render: (r) => <Timestamp short className="num whitespace-nowrap" value={r.fact.created_at} note={`日志 #${r.fact.id}${r.fact.request_id ? " · " + r.fact.request_id : ""}`} /> },
              { key: "p", label: "归属", render: (r) => (r.attribution.participant_id === null ? <Badge tone="warn">未归属</Badge> : r.attribution.participant_name) },
              { key: "a", label: uiCopy.account, render: (r) => accountName(r.account_id) },
              {
                key: "m",
                label: `模型（请求 → ${glossary.sub2api}）`,
                render: (r) => (
                  <Mono>
                    {r.fact.model} → {r.fact.upstream_model ?? <Val v={null} reason="缺少模型" />}
                    {r.fact.upstream_response_model !== null && `（响应：${r.fact.upstream_response_model}）`}
                  </Mono>
                ),
              },
              { key: "k", label: "Key ID", render: (r) => <Mono>{r.fact.api_key_id ?? <Val v={null} reason="未返回 Key ID" />} · 标准费 <Val v={keyCost(r)} prefix="$" reason="缺少窗口费用" /></Mono> },
              { key: "s", label: "标准费用 · USD", align: "right", render: (r) => <Val v={r.fact.metrics.total_cost} /> },
              { key: "ac", label: "日志实际费用 · USD", align: "right", render: (r) => <Val v={r.fact.metrics.actual_cost} /> },
              ...(isAdmin ? [{ key: "c", label: "账号统计费用 · USD", align: "right" as const, render: (r: (typeof rows)[number]) => <Val v={r.fact.metrics.account_stats_cost} /> }] : []),
              { key: "b", label: "扣费", render: () => <Val v={null} reason="日志缺少扣费确认" /> },
            ]}
          />
          <div className="flex flex-wrap gap-2 px-4 py-3">
            <button type="button" className="text-xs underline" onClick={usage.reload}>回到首页</button>
            {view?.next_after && <button type="button" className="text-xs underline" onClick={usage.nextPage}>下一页</button>}
          </div>
          <details className="space-y-2 border-t border-line p-4 text-[12px] text-muted">
            <summary className="cursor-pointer">详情</summary>
            {acct === "all" ? uiCopy.chooseAccount : history.keyError ?? (history.keys ? <>
              <p>Key 费用区间 {time(history.keys.query_start_at)} → {time(history.keys.query_end_at)}</p>
              {history.keys.key_groups?.map(group => <p key={`${group.participant_id}:${group.api_key_id}`}>
                {group.participant_id ?? "未归属"} / {group.api_key_id ?? "—"} · <Val v={group.metrics.total_cost} prefix="$" /> · {group.request_count} 条
              </p>)}
            </> : !history.ready ? uiCopy.loading : <Val v={null} reason="缺少 Key 窗口费用" />)}
          </details>
        </Card>
      )}

      {tab === "history" && (
        <div className="space-y-4">
          {history.selected && (
            <details className="text-[12px] text-muted">
              <summary className="cursor-pointer">月费用</summary>
              {history.monthlyError ?? (history.monthly ? history.monthly.buckets.length ? history.monthly.buckets.map(bucket => (
                <p key={bucket.start_at}>{calendar?.day(bucket.start_at).slice(0, 7)} · 标准 <Val v={bucket.metrics.total_cost} prefix="$" /></p>
              )) : "暂无月费用" : uiCopy.loading)}
            </details>
          )}
          <Grid cols={2}>
            <Card title="账号观测">
              <KV
                items={[
                  ["配额重置", longWindow?.reset_at ? <Timestamp key="reset" value={longWindow.reset_at} /> : <Val key="reset" v={null} />],
                  ["已用", <Val key="used" v={longWindow?.used_percent} unit="%" />],
                  ["窗口标准费", <Val key="s" v={currentUsage?.capture?.summary.metrics.total_cost} prefix="$" />],
                  ["今日标准费", <Val key="d" v={todayCost} prefix="$" />],
                ]}
              />
            </Card>
          <Card title="七天估算" desc="标准费用 USD">
              <KV
                items={[
                  ["七天容量", estimateAmountText(estimate?.latest?.capacity ?? null)],
                  ["估算剩余", estimateAmountText(estimate?.remaining ?? null)],
                  ["观测时间", time(estimate?.observation?.source_at)],
                  [uiCopy.status, history.modelError ?? <Badge key="s" tone={estimate?.state === "ready" ? "ok" : "unk"}>{estimate?.state === "ready" ? "已估算" : history.selected && !history.ready ? "读取中" : "待估算"}</Badge>],
                ]}
              />
            </Card>
          </Grid>
          <Card title="每日最后快照" pad={false}>
            <Table
              rows={dailyRows}
              rowKey={(r) => r.day}
              empty={!history.selected ? uiCopy.chooseAccount : history.dailyError ?? (history.daily ? "暂无观测。" : "快照读取中。")}
              cols={[
                { key: "d", label: "接收日期", render: (r) => <span className="num" title={`观测 ${r.observation.id}；源 ${r.observation.snapshot.source_at ?? "未知"}；接收 ${r.observation.snapshot.received_at}；${r.observation.excluded ? "已排除" : "未排除"}；费用捕获 ${r.usage.windows.find(window => window.kind === "seven_day")?.capture?.id ?? "未知"}`}>{r.day}{r.observation.excluded ? "（已排除）" : ""}</span> },
                { key: "u", label: "已用", align: "right", render: (r) => <span className="num"><Val v={r.observation.snapshot.windows.find(window => window.kind === "seven_day")?.used_percent} unit="%" /></span> },
                { key: "s", label: "窗口标准费", align: "right", render: (r) => <Val v={r.usage.windows.find(window => window.kind === "seven_day")?.capture?.summary.metrics.total_cost} prefix="$" /> },
                { key: "p", label: "每 1% 对应费用", align: "right", render: () => <Val v={null} prefix="$" /> },
                {
                  key: "n",
                  label: "区间快照数",
                  render: (r) => <span className="num">{r.receipt_count}</span>,
                },
              ]}
            />
          </Card>
        </div>
      )}
    </>
  );
}
