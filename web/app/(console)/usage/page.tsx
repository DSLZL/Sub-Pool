"use client";

import { useEffect, useState } from "react";
import { Badge, Callout, Card, Field, Grid, KV, Mono, PageHeader, Segmented, Select, Stat, Table, Tabs, Val } from "@/components/ui";
import { LineChart, CHART_COLORS } from "@/components/charts";
import { useScope } from "@/lib/scope";
import { millionTokens, usageTrend } from "@/lib/usage-data";
import { useUsageData } from "@/lib/use-usage-data";

const limitations: Record<string, string> = {
  RETENTION_UNVERIFIED: "历史保留范围未核实", OFFSET_SNAPSHOT_UNVERIFIED: "上游分页不是原子快照",
  HISTORICAL_BILLING_PATH_UNKNOWN: "历史扣款路径未知", REQUEST_START_TIME_UNAVAILABLE: "时间是日志记录时间，非已证明的请求开始时间",
  ONLY_RECORDED_BINDING_INTERVALS: "归属仅依据已记录绑定", ONLY_SELECTED_CAPTURES: "仅包含所选捕获",
  CAPTURE_PARTIAL: "捕获不完整", CAPTURE_FAILED: "捕获失败", SCAN_LIMIT: "达到采集扫描上限",
};

export default function UsagePage() {
  const scope = useScope(), { isAdmin } = scope;
  const [tab, setTab] = useState("overview");
  const [range, setRange] = useState("14d");
  const [acct, setAcct] = useState("all");

  const usage = useUsageData(scope, range === "7d" ? 7 : 14, acct), view = usage.data?.view;
  const rows = usage.data?.records ?? [];
  const accountName = (id: string) => usage.accounts.find(account => account.id === id)?.name ?? id;
  const trendParticipants = Object.entries(usage.data?.byParticipant ?? {});
  const trend = (buckets: NonNullable<typeof view>["buckets"]) => usage.window ? usageTrend(usage.window, buckets) : { labels: [], values: [] };
  const days = trend([]).labels;
  const dailyClose: { day: string }[] = [];
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
          采集时间 {view.oldest_capture_at} → {view.newest_capture_at}；明细已读 {rows.length} / {view.summary.request_count} 原生行。
          {view.limitations.map(reason => limitations[reason] ?? "另有采集限制").join("；")}。</> : usage.ready ? "没有可见的已记录捕获；这不证明用量为零。" : "正在核对全部授权页面；未展示部分结果。")}
        {!!usage.data?.missingAccounts.length && <> 缺少捕获：{usage.data.missingAccounts.map(accountName).join("、")}。</>}
        只读已保存记录，不会自动采集；7/14 天含今日、按 UTC 日历分桶，账号选择适用于明细、汇总和趋势。观察结果不完整，不能用于结算、完整周期建议或结转。
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
            desc="actual_cost 原生口径 · USD · UTC · 无记录不等于零使用，非已证明的实际扣款"
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
            <LineChart labels={days} unit=" USD" series={trendParticipants.map(([id, item], i) => ({ name: `${rows.find(row => row.attribution.participant_id === id)?.attribution.participant_name ?? id} · ${id}`, color: CHART_COLORS[i % 4], values: trend(item.buckets).values }))} />
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
          </div>
          <Table
            rows={rows}
            rowKey={(r) => `${r.capture_id}.${r.fact.id}`}
            empty={usage.error ?? (!usage.ready ? "正在读取全部明细…" : view ? "所选窗口内没有可见记录；不等于零使用。" : "没有已记录捕获。")}
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
              { key: "k", label: "Key ID", render: (r) => <Mono>{r.fact.api_key_id ?? "未知"}</Mono> },
              { key: "n", label: "原生行", align: "right", render: () => <span className="num">1</span> },
              { key: "s", label: "标准费用 · USD", align: "right", render: (r) => <Val v={r.fact.metrics.total_cost} /> },
              { key: "ac", label: "actual_cost · USD", align: "right", render: (r) => <Val v={r.fact.metrics.actual_cost} /> },
              ...(isAdmin ? [{ key: "c", label: "账号统计费用 · USD", align: "right" as const, render: (r: (typeof rows)[number]) => <Val v={r.fact.metrics.account_stats_cost} /> }] : []),
              { key: "b", label: "扣除状态 / 原生候选", render: (r) => <Badge tone="unk">未知 · {r.fact.billing_candidate === "wallet" ? "钱包候选" : r.fact.billing_candidate === "subscription" ? "订阅候选" : "候选未知"} · {r.fact.billing_mode ?? "模式未知"}</Badge> },
            ]}
          />
          <div className="space-y-2 border-t border-line p-4 text-[12px] text-muted">
            <p>· 日志费用不总是实际扣款：simple 模式可能只记录费用或只更新 Key 窗口；零费用不证明未计价。</p>
            <p>· 历史运行模式无法确认时，不将日志费用宣称为已发生的钱包或订阅扣款。</p>
            <p>· 不展示 Key 原文，也不采集提示词与回答内容。</p>
          </div>
        </Card>
      )}

      {tab === "history" && (
        <div className="space-y-4">
          <Callout tone="info">账号历史汇总、每日收盘与模型估计尚无可用服务；这里保持未知，不使用日志日桶代替精确周期，也不以当前价格重建历史或计算余额建议。</Callout>
          <Grid cols={2}>
            <Card title="当前周期观测对照 · 暂不可用">
              <KV
                items={[
                  ["周期", <Val key="cycle" v={null} />],
                  ["7 天窗口已用", <Val key="used" v={null} />],
                  ["本周期标准费用", <Val key="s" v={null} prefix="$" />],
                  ["今日新增", <Val key="d" v={null} prefix="$" />],
                ]}
              />
            </Card>
            <Card title="模型当前估计 · 暂不可用" desc="派生结果，单独标识">
              <KV
                items={[
                  ["等效周期容量", <Val key="capacity" v={null} />],
                  ["剩余能力", <Val key="remaining" v={null} />],
                  ["数据时间", <Val key="at" v={null} />],
                  ["状态", <Badge key="s" tone="unk">暂不可用</Badge>],
                ]}
              />
            </Card>
          </Grid>
          <Card title="每日收盘" pad={false}>
            <Table
              rows={dailyClose}
              rowKey={(r) => r.day}
              empty="每日收盘服务暂不可用；暂无可确认结果。"
              cols={[
                { key: "d", label: "日期", render: (r) => <span className="num">{r.day}</span> },
                { key: "u", label: "收盘已用", align: "right", render: () => <span className="num"><Val v={null} /></span> },
                { key: "s", label: "累计标准费用", align: "right", render: () => <Val v={null} prefix="$" /> },
                { key: "p", label: "每 1% 对应费用", align: "right", render: () => <Val v={null} prefix="$" /> },
                {
                  key: "n",
                  label: "样本",
                  render: () => <span className="num">未知</span>,
                },
              ]}
            />
          </Card>
        </div>
      )}
    </>
  );
}
