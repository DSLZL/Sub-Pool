"use client";

import { UsageDayCollection } from "@/components/usage-day-collection";

import { useEffect, useState } from "react";
import { usageCalendar } from "@sub-pool/contracts/participants";
import { Badge, Callout, Card, Empty, Field, Grid, KV, Mono, PageHeader, Segmented, Select, Stat, Table, Tabs, Val } from "@/components/ui";
import { LineChart, CHART_COLORS } from "@/components/charts";
import { useScope } from "@/lib/scope";
import { millionTokens, usageDayCost, usageTrend } from "@/lib/usage-data";
import { useUsageData } from "@/lib/use-usage-data";
import { useUsageAccount } from "@/lib/use-usage-account";
import { usageKeyCosts } from "@/lib/usage-account-data";
import { estimateStates } from "@/lib/estimates-data";

const limitations: Record<string, string> = {
  RETENTION_UNVERIFIED: "历史保留范围未核实", OFFSET_SNAPSHOT_UNVERIFIED: "上游分页不是原子快照",
  HISTORICAL_BILLING_PATH_UNKNOWN: "历史扣款路径未知", REQUEST_START_TIME_UNAVAILABLE: "时间是日志记录时间，非已证明的请求开始时间",
  ONLY_RECORDED_BINDING_INTERVALS: "归属仅依据已记录绑定", ONLY_SELECTED_CAPTURES: "仅包含所选捕获",
  CAPTURE_PARTIAL: "捕获不完整", CAPTURE_FAILED: "捕获失败", SCAN_LIMIT: "达到采集扫描上限", DAILY_COVERAGE_INCOMPLETE: "所选日期含未完成或缺失采集",
};

export default function UsagePage() {
  const scope = useScope(), { isAdmin } = scope;
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
  const modelState = history.model ? estimateStates[history.model.status] : null;
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
        title="用量与费用"
        tag="S2A-USAGE-001 ~ 007"
        desc="请求、Token 与费用均来自 Sub2API 原生记录。标准费用、actual_cost 口径与账号成本分别列示，不可相加。"
      />

      <Callout tone="warn" className="mb-4" title={usage.error ? "无法完整读取用量" : !usage.ready ? "正在读取已记录用量…" : "统计覆盖不完整"}>
        {usage.error ?? (view ? <>查询 {view.query_start_at} → {view.query_end_at}（UTC，结束不含）。捕获区间 {view.start_at} → {view.end_at}。
          采集时间 {view.oldest_capture_at} → {view.newest_capture_at}；本页 {rows.length} 条，共 {view.summary.request_count} 条原生行。
          {view.history && `按 ${view.history.selections.length} 个已记录时段汇总。`}
          {view.limitations.map(reason => limitations[reason] ?? "另有采集限制").join("；")}。</> : usage.ready ? "没有可见的已记录捕获；这不证明用量为零。" : "正在核对全部授权页面；未展示部分结果。")}
        {!!usage.data?.missingAccounts.length && <> 缺少捕获：{usage.data.missingAccounts.map(accountName).join("、")}。</>}
        展示已记录数据。7/14 天含今日，按 {usage.timezone ?? "待确认时区"} 日历分桶，账号选择适用于明细、汇总和趋势。观察结果不完整，不能用于结算、完整周期建议或结转。
      </Callout>

      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "overview", label: "用量概览" },
          { id: "detail", label: "费用明细" },
          { id: "history", label: "账号历史对照" },
        ]}
      />

      {tab === "overview" && (
        <div className="space-y-4">
          <Grid cols={4}>
            <Stat label="请求数" value={<Val v={view?.summary.request_count} />} sub="所选窗口内已记录原生行" />
            <Stat label="输入 Token" value={<Val v={millionTokens(view?.summary.metrics.input_tokens)} unit="M" digits={1} />} />
            <Stat label="输出 Token" value={<Val v={millionTokens(view?.summary.metrics.output_tokens)} unit="M" digits={1} />} />
            <Stat
              label="缓存读 / 写"
              value={
                <span className="text-[18px]">
                  <Val v={millionTokens(view?.summary.metrics.cache_read_tokens)} unit="M" digits={1} /> / <Val v={millionTokens(view?.summary.metrics.cache_creation_tokens)} unit="M" />
                </span>
              }
              sub="原生未返回即未知；无记录不补零"
            />
          </Grid>
          <Card
            title={isAdmin ? "参与者每日消费趋势" : "授权参与者每日消费趋势"}
            desc={`actual_cost 原生口径 · USD · ${usage.timezone ?? "时区待确认"} · 无记录不等于零使用，非已证明的实际扣款`}
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
            {usage.error || !usage.ready ? <Empty title={usage.error ?? "正在读取已记录用量…"} />
              : trend ? <LineChart labels={trend.labels} unit=" USD" series={trendParticipants.map(([id, item], i) => ({
                name: `${item.records.find(row => row.attribution.participant_id === id)?.attribution.participant_name ?? id} · ${id}`,
                color: CHART_COLORS[i % CHART_COLORS.length],
                values: trend.values[i],
              }))} />
              : <Empty title="暂无可用的已归属费用">
                {isAdmin && <button type="button" className="underline" onClick={() => setTab("detail")}>去采集</button>}
              </Empty>}
          </Card>
        </div>
      )}

      {tab === "detail" && (
        <Card pad={false}>
          <div className="flex flex-wrap items-end gap-3 p-4 pb-3">
            <Field label="账号" className="w-48">
              <Select
                value={acct}
                onChange={(e) => setAcct(e.target.value)}
                options={[{ value: "all", label: "全部可见账号" }, ...usage.accounts.map(account => ({ value: account.id, label: account.name }))]}
              />
            </Field>
            <UsageDayCollection account={scope.accounts.find(item => item.id === acct)} connectionRevision={scope.connectionRevision}
              range={usage.window} onChange={usage.reload} />
          </div>
          <Table
            rows={rows}
            rowKey={(r) => `${r.capture_id}.${r.fact.id}`}
            empty={usage.error ?? (!usage.ready ? "正在读取当前页明细…" : view ? "所选窗口内没有可见记录；不等于零使用。" : "没有已记录捕获。")}
            cols={[
              { key: "t", label: "日志时间（UTC）", render: (r) => <span className="num whitespace-nowrap" title={`原生行 ${r.fact.id}；请求 ${r.fact.request_id ?? "未知"}；绑定 ${r.attribution.binding_id ?? "未归属"}；原生倍率 ${r.fact.rate_multiplier ?? "未知"}；账号倍率 ${r.fact.account_rate_multiplier ?? "未知"}`}>{r.fact.created_at}</span> },
              { key: "p", label: "记录时归属", render: (r) => (r.attribution.participant_id === null ? <Badge tone="warn">未归属</Badge> : r.attribution.participant_name) },
              { key: "a", label: "账号", render: (r) => accountName(r.account_id) },
              {
                key: "m",
                label: "模型（请求 → 上游）",
                render: (r) => (
                  <Mono>
                    {r.fact.model} → {r.fact.upstream_model ?? "未知"}
                    {r.fact.upstream_response_model !== null && `（响应：${r.fact.upstream_response_model}）`}
                  </Mono>
                ),
              },
              { key: "k", label: "Key ID", render: (r) => <Mono>{r.fact.api_key_id ?? "未知"} · 当前窗口标准费用 {keyCost(r) === null ? "未知" : `$${keyCost(r)}`}</Mono> },
              { key: "n", label: "原生行", align: "right", render: () => <span className="num">1</span> },
              { key: "s", label: "标准费用 · USD", align: "right", render: (r) => <Val v={r.fact.metrics.total_cost} /> },
              { key: "ac", label: "actual_cost · USD", align: "right", render: (r) => <Val v={r.fact.metrics.actual_cost} /> },
              ...(isAdmin ? [{ key: "c", label: "账号统计费用 · USD", align: "right" as const, render: (r: (typeof rows)[number]) => <Val v={r.fact.metrics.account_stats_cost} /> }] : []),
              { key: "b", label: "扣除状态 / 原生候选", render: (r) => <Badge tone="unk">未知 · {r.fact.billing_candidate === "wallet" ? "钱包候选" : r.fact.billing_candidate === "subscription" ? "订阅候选" : "候选未知"} · {r.fact.billing_mode ?? "模式未知"}</Badge> },
            ]}
          />
          <div className="flex flex-wrap gap-2 px-4 py-3">
            <button type="button" className="text-xs underline" onClick={usage.reload}>回到首页</button>
            {view?.next_after && <button type="button" className="text-xs underline" onClick={usage.nextPage}>下一页</button>}
          </div>
          <div className="space-y-2 border-t border-line p-4 text-[12px] text-muted">
            <p>· 日志费用不总是实际扣款：simple 模式可能只记录费用或只更新 Key 窗口；零费用不证明未计价。</p>
            <p>· 历史运行模式无法确认时，不将日志费用宣称为已发生的钱包或订阅扣款。{acct === "all" ? "请选择单一账号核对当前 Key 窗口。" : history.keyError ?? (history.keys ? `当前 Key 标准费用按账号、记录时成员和 Key ID 分组；仅限最新保留观测仍有效的原生长窗口，区间 ${history.keys.query_start_at} → ${history.keys.query_end_at}（UTC，结束不含，截至源时刻），捕获 ${history.keys.history?.selections[0]?.id}，采集于 ${history.keys.newest_capture_at}。窗口成员 / Key 汇总：${history.keys.key_groups?.map(group => `成员 ${group.participant_id ?? "未归属"}，Key ID ${group.api_key_id ?? "未知（不代表同一 Key）"}，标准费用 ${group.metrics.total_cost === null ? "未知" : `$${group.metrics.total_cost}`}，原生行 ${group.request_count}`).join("；") ?? "未返回 Key 分组，费用未知"}。只含当前授权与保留原生行，缺少 Key 或分组仍为未知，不证明完整周期或实际扣款。` : !history.ready ? "正在核对当前 Key 窗口。" : "当前原生长窗口、对应捕获或有效期无法确认，Key 窗口费用未知。")}</p>
            <p>· 不展示 Key 原文，也不采集提示词与回答内容。</p>
          </div>
        </Card>
      )}

      {tab === "history" && (
        <div className="space-y-4">
          <Callout tone="info">
            {acct === "all" ? "请在费用明细中选择一个账号，再查看其观测和模型状态。" : `当前账号：${accountName(acct)}。`}
            今日费用仅为所选采集中、截至查询时刻的 {usage.timezone ?? "待确认时区"} 当日标准费用；没有日桶仍为未知，不证明全天完整。完整本周期费用和金融日收盘尚无可确认结果，不以当前价格重建历史或计算余额建议。
            {history.selected && (history.observationError ?? (history.observation ? `账号观测读取状态 ${history.observation.freshness}；该读源时间 ${history.observation.snapshot?.source_at ?? "未知"}；接收时间 ${history.observation.snapshot?.received_at ?? "未知"}。` : !history.ready ? "正在读取账号观测。" : "没有已保存窗口。"))}
            {history.selected && (history.modelError ?? history.model?.basis)}
            {current && `最新保留观测 ${current.observation.id}${current.observation.excluded ? "（已排除）" : ""}；源时间 ${current.observation.snapshot.source_at ?? "未知"}；接收时间 ${current.observation.snapshot.received_at}。窗口费用只覆盖该快照的原生区间截至源时刻，不能当作完整周期费用。`}
            {history.selected && (history.dailyError ?? (history.daily ? `每日行按 ${history.daily.timezone} 接收日期取区间内最后保留快照，包含已排除观测；费用来自各自真实原生窗口的一个保留捕获，仅含当前授权。并非财务收盘，不能推算每1%费用或完整容量。` : "正在核对每日保留快照。"))}
            {history.selected && (history.monthlyError ?? (history.monthly ? `月度标准费用按 ${history.monthly.history?.timezone} 日历分桶，仅覆盖所选 7/14 天与捕获交集：${history.monthly.buckets.length ? history.monthly.buckets.map(bucket => `${calendar?.day(bucket.start_at).slice(0, 7)} 月，${bucket.start_at} → ${bucket.end_at}（UTC，结束不含），标准费用 ${bucket.metrics.total_cost === null ? "未知" : `$${bucket.metrics.total_cost}`}，原生行 ${bucket.request_count}`).join("；") : "没有可见原生行，费用未知"}。重复月份的 UTC 分段分别保留；不是完整月账单，不以缺失值补零。` : "正在核对月度汇总。"))}
          </Callout>
          <Grid cols={2}>
            <Card title="当前账号观测对照">
              <KV
                items={[
                  ["原生长窗口", longWindow ? `${longWindow.duration_seconds ?? "未知"} 秒；重置 ${longWindow.reset_at ?? "未知"}${longWindow.reset_expired ? "（已到期）" : ""}` : <Val key="cycle" v={null} />],
                  ["长窗口原始已用", <Val key="used" v={longWindow?.used_percent} unit="%" />],
                  ["最新快照窗口已记录标准费用", <Val key="s" v={currentUsage?.capture?.summary.metrics.total_cost} prefix="$" />],
                  ["今日已记录标准费用", <Val key="d" v={todayCost} prefix="$" />],
                ]}
              />
            </Card>
            <Card title="模型当前估计" desc="派生结果，单独标识">
              <KV
                items={[
                  ["等效周期容量", <Val key="capacity" v={null} />],
                  ["剩余能力", <Val key="remaining" v={null} />],
                  ["数据时间", history.model?.at ?? <Val key="at" v={null} />],
                  ["状态", <Badge key="s" tone={modelState?.tone ?? "unk"}>{modelState?.label ?? (history.selected && !history.ready ? "正在读取" : "未知")}</Badge>],
                ]}
              />
            </Card>
          </Grid>
          <Card title="每日最后保留快照" pad={false}>
            <Table
              rows={dailyRows}
              rowKey={(r) => r.day}
              empty={!history.selected ? "请选择单一账号。" : history.dailyError ?? (history.daily ? "所选接收区间没有保留观测；不代表用量为零。" : "每日快照尚未完整读取。")}
              cols={[
                { key: "d", label: "接收日期", render: (r) => <span className="num" title={`观测 ${r.observation.id}；源 ${r.observation.snapshot.source_at ?? "未知"}；接收 ${r.observation.snapshot.received_at}；${r.observation.excluded ? "已排除" : "未排除"}；费用捕获 ${r.usage.windows.find(window => window.kind === "seven_day")?.capture?.id ?? "未知"}`}>{r.day}{r.observation.excluded ? "（已排除）" : ""}</span> },
                { key: "u", label: "长窗口原始已用", align: "right", render: (r) => <span className="num"><Val v={r.observation.snapshot.windows.find(window => window.kind === "seven_day")?.used_percent} unit="%" /></span> },
                { key: "s", label: "原生窗口已记录标准费用", align: "right", render: (r) => <Val v={r.usage.windows.find(window => window.kind === "seven_day")?.capture?.summary.metrics.total_cost} prefix="$" /> },
                { key: "p", label: "每 1% 对应费用", align: "right", render: () => <Val v={null} prefix="$" /> },
                {
                  key: "n",
                  label: "区间内保留快照数",
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
