"use client";

import { useState } from "react";
import { Badge, Callout, Card, Field, Grid, KV, Mono, PageHeader, Segmented, Select, Stat, Table, Tabs, Val } from "@/components/ui";
import { LineChart, CHART_COLORS } from "@/components/charts";
import { useScope } from "@/lib/scope";
import { accountName, BILLING_LABEL, costRows, dailyClose, estimates, participantName, usageDays, usageSummary, usageTrend } from "@/lib/mock";

const fmtTok = (n: number | null) => (n === null ? null : n / 1_000_000);

export default function UsagePage() {
  const { isAdmin, participants, canAccount, canParticipant } = useScope();
  const [tab, setTab] = useState("overview");
  const [range, setRange] = useState("14d");
  const [acct, setAcct] = useState("all");

  const rows = costRows.filter((r) => canAccount(r.account) && (isAdmin || canParticipant(r.participant)) && (acct === "all" || r.account === acct));
  const trendParticipants = participants.filter((p) => usageTrend[p.id]);
  const days = range === "7d" ? usageDays.slice(-7) : usageDays;
  const sliceVals = (v: (number | null)[]) => (range === "7d" ? v.slice(-7) : v);

  return (
    <>
      <PageHeader
        title="用量与费用"
        tag="S2A-USAGE-001 ~ 007"
        desc="请求、Token 与费用均来自 Sub2API 原生记录。标准费用、actual_cost 口径与账号成本分别列示，不可相加。"
      />

      <Callout tone="warn" className="mb-4" title="统计覆盖不完整">
        覆盖 {usageSummary.coverage.from} → {usageSummary.coverage.to}。{usageSummary.coverage.gap}。依赖完整周期数据的建议与结转已暂停。
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
            <Stat label="请求数" value={usageSummary.requests.toLocaleString()} sub="已记录请求" />
            <Stat label="输入 Token" value={<Val v={fmtTok(usageSummary.input)} unit="M" digits={1} />} />
            <Stat label="输出 Token" value={<Val v={fmtTok(usageSummary.output)} unit="M" digits={1} />} />
            <Stat
              label="缓存读 / 写"
              value={
                <span className="text-[18px]">
                  <Val v={fmtTok(usageSummary.cacheRead)} unit="M" digits={1} /> / <Val v={fmtTok(usageSummary.cacheWrite)} />
                </span>
              }
              sub="缓存写入未返回 → 未知"
            />
          </Grid>
          <Card
            title={isAdmin ? "参与者每日消费趋势" : "我的每日消费趋势"}
            desc="标准费用口径 · USD · 无记录不等于零使用"
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
            <LineChart labels={days} unit=" USD" series={trendParticipants.map((p, i) => ({ name: p.name, color: CHART_COLORS[i % 4], values: sliceVals(usageTrend[p.id]) }))} />
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
                options={[{ value: "all", label: "全部可见账号" }, ...["a1", "a2", "a3", "a4"].filter(canAccount).map((a) => ({ value: a, label: accountName(a) }))]}
              />
            </Field>
          </div>
          <Table
            rows={rows}
            rowKey={(r) => r.id}
            cols={[
              { key: "t", label: "时间", render: (r) => <span className="num whitespace-nowrap">{r.at}</span> },
              { key: "p", label: "参与者", render: (r) => (r.participant === "—" ? <Badge tone="warn">未归属</Badge> : participantName(r.participant)) },
              { key: "a", label: "账号", render: (r) => accountName(r.account) },
              {
                key: "m",
                label: "模型（请求 → 上游）",
                render: (r) => (
                  <Mono>
                    {r.model}
                    {r.upstreamModel !== r.model && ` → ${r.upstreamModel}`}
                  </Mono>
                ),
              },
              { key: "k", label: "Key ID", render: (r) => <Mono>{r.keyId}</Mono> },
              { key: "n", label: "请求", align: "right", render: (r) => <span className="num">{r.requests}</span> },
              { key: "s", label: "标准费用", align: "right", render: (r) => <Val v={r.standard} /> },
              { key: "ac", label: "actual_cost 口径", align: "right", render: (r) => <Val v={r.actual} /> },
              ...(isAdmin ? [{ key: "c", label: "账号成本", align: "right" as const, render: (r: (typeof rows)[number]) => <Val v={r.accountCost} /> }] : []),
              { key: "b", label: "扣除状态", render: (r) => <Badge tone={BILLING_LABEL[r.billing].tone}>{BILLING_LABEL[r.billing].label}</Badge> },
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
          <Callout tone="info">描述性统计（每日收盘、月汇总）与模型估计分开展示。描述性结果不随分配方案改变，也不直接作为余额建议。</Callout>
          <Grid cols={2}>
            <Card title="当前周期观测对照 · Alpha 主账号">
              <KV
                items={[
                  ["周期", estimates[0].cycle],
                  ["7 天窗口已用", "61%"],
                  ["本周期标准费用", <Val key="s" v={152.3} prefix="$" />],
                  ["今日新增", <Val key="d" v={14.2} prefix="$" />],
                ]}
              />
            </Card>
            <Card title="模型当前估计 · Alpha 主账号" desc="派生结果，单独标识">
              <KV
                items={[
                  ["等效周期容量", `$${estimates[0].capacity!.low} – $${estimates[0].capacity!.high}`],
                  ["剩余能力", `$${estimates[0].remaining!.low} – $${estimates[0].remaining!.high}`],
                  ["数据时间", estimates[0].at!],
                  ["状态", <Badge key="s" tone="ok">可估计</Badge>],
                ]}
              />
            </Card>
          </Grid>
          <Card title="每日收盘" pad={false}>
            <Table
              rows={dailyClose}
              rowKey={(r) => r.day}
              cols={[
                { key: "d", label: "日期", render: (r) => <span className="num">{r.day}</span> },
                { key: "u", label: "收盘已用", align: "right", render: (r) => (r.usedPct === null ? <Val v={null} /> : <span className="num">{r.usedPct}%</span>) },
                { key: "s", label: "累计标准费用", align: "right", render: (r) => <Val v={r.spend} prefix="$" /> },
                { key: "p", label: "每 1% 对应费用", align: "right", render: (r) => <Val v={r.perPct} prefix="$" /> },
                {
                  key: "n",
                  label: "样本",
                  render: (r) => (r.samples === 0 ? <Badge tone="muted">无观测</Badge> : r.samples < 48 ? <Badge tone="unk">样本不足 · {r.samples}</Badge> : <span className="num">{r.samples}</span>),
                },
              ]}
            />
          </Card>
        </div>
      )}
    </>
  );
}
