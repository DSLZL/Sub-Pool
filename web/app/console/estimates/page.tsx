"use client";

import { Timestamp } from "@/components/timestamp";
import { useEffect, useState } from "react";
import { Settings2 } from "lucide-react";
import { createEstimatesClient } from "@sub-pool/contracts/client";
import type { NativeWeeklyCurrent, NativeWeeklyRecord } from "@sub-pool/contracts/estimates";
import { AdminButton, Badge, Button, Callout, Card, Drawer, Field, Grid, Input, KV, Modal, PageHeader, Select, Table, Tabs, Toolbar } from "@/components/ui";
import { RangeBar } from "@/components/charts";
import { DateTimeField } from "@/components/date-time-field";
import { connectionFailure, displayTime } from "@/lib/connection-data";
import { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import { useEstimateData } from "@/lib/use-estimate-data";
import { estimateAmountText, estimateCard, estimateCostText, estimateStates, historyReason, nativePeriodText } from "@/lib/estimates-data";

export default function EstimatesPage() {
  const scope = useScope();
  const { auth, displayTimezone } = useApp();
  const time = (value: string | null) => displayTime(value, displayTimezone, false);
  const { isAdmin } = scope;
  const [tab, setTab] = useState("current");
  const [weeklyDetail, setWeeklyDetail] = useState<{ epoch: number; record: NativeWeeklyRecord } | null>(null);
  const data = useEstimateData(scope, tab === "legacy"), form = data.form;
  const openWeekly = (record: NativeWeeklyRecord) => setWeeklyDetail({ epoch: auth.epoch, record });
  const savedWeekly = weeklyDetail?.epoch === auth.epoch && data.entries.some(entry => entry.account.id === weeklyDetail.record.account_id) ? weeklyDetail.record : null;
  const currentDetail = tab === "current" && savedWeekly ? data.entries.find(entry => entry.account.id === savedWeekly.account_id)?.current.native_weekly : undefined;
  const allocation = currentDetail?.latest?.id === savedWeekly?.id ? currentDetail?.allocation : undefined;
  const one = data.entries.length === 1 ? data.entries[0] : undefined, record = one?.current.latest ?? null;
  const weekly = one?.current.native_weekly;
  const manual = record?.windows.find(window => window.basis === "manual");
  const targets = form && !data.simulationTargets.some(value => value.account.id === form.base.account.id) ? [form.base, ...data.simulationTargets] : data.simulationTargets;
  const simulated = data.simulationTargets.length > 0;
  const notice = data.error ?? data.pendingNotice ?? (!data.ready ? "正在读取当前授权范围的估算。" : data.entries.length === 0 ? "当前授权范围没有可读取的账号。" : null);
  const configuration = one?.current.configuration;
  const detail = data.detail, saved = detail?.record, recompute = data.recomputeForm;

  return (
    <>
      <PageHeader
        title="容量估算"
        desc={notice ?? "从账号的原生额度重置起点累计标准费用，随七天已用比例更新总量估算。"}
        actions={(simulated || data.pendingNotice) && (
          <>
          <AdminButton icon={<Settings2 size={14} />} disabled={!data.canOpen} onClick={data.openConfiguration}>
            估算假设
          </AdminButton>
          <AdminButton disabled={!data.canRecompute} onClick={data.openRecompute}>{data.recomputeUnresolved ? "核对重算结果" : "本地重新测算"}</AdminButton>
          </>
        )}
      />

      <Toolbar>
        <Field label="查看账号">
          <Select value={data.filters.account} onChange={event => { setWeeklyDetail(null); data.selectAccount(event.target.value); }} options={[{ value: "", label: "当前授权全部账号" }, ...data.accounts.map(account => ({ value: account.id, label: account.name }))]} />
        </Field>
        {tab === "legacy" && <>
          <Field label="原生窗口">
            <Select value={data.filters.window_kind} onChange={event => data.selectHistoryWindow(event.target.value)} options={[{ value: "all", label: "全部窗口" }, { value: "short", label: "短窗口" }, { value: "seven_day", label: "长窗口（不假定七天）" }]} />
          </Field>
          <DateTimeField label="官方周期结束" value={data.filters.period_end_at} disabled={data.filters.window_kind === "all"} hint="留空读取该窗口全部历史" onChange={data.editPeriod} />
        </>}
      </Toolbar>
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "current", label: "当前估计" },
          { id: "history", label: "七天估算历史" },
          { id: "cycles", label: "配额周期" },
          { id: "legacy", label: "旧估算记录" },
          ...(simulated || tab === "scheme" ? [{ id: "scheme", label: "模拟方案" }] : []),
        ]}
      />

      {tab === "current" && (
        <Grid cols={2}>
          {data.entries.map(entry => {
            if (entry.current.native_weekly) return <WeeklyCard key={entry.account.id} name={entry.account.name} weekly={entry.current.native_weekly} time={time} open={openWeekly} />;
            if (!entry.current.simulation_configurable) return <Card key={entry.account.id} title={entry.account.name} desc="七天估算适用于被动监控的 OpenAI OAuth 账号。" />;
            const e = estimateCard(entry, time);
            const st = estimateStates[e.status];
            return (
              <Card key={e.account} title={e.name} desc={`周期：${e.cycle}`} actions={<><Badge tone={st.tone} dot>{st.label}</Badge><Button size="sm" disabled={!entry.current.latest} onClick={() => { if (entry.current.latest) void data.openRecord(e.account, entry.current.latest.id); }}>查看依据</Button></>}>
                {e.capacity && e.remaining ? (
                  <div className="space-y-4">
                    <div>
                      <div className="mb-1.5 flex justify-between text-[12px]">
                        <span className="text-muted">约束窗口场景容量（模拟标准 USD）</span>
                        <span className="num font-medium">
                          ${e.capacity.mid}
                        </span>
                      </div>
                      <RangeBar low={e.capacity.low} mid={e.capacity.mid} high={e.capacity.high} max={Math.max(e.capacity.high, e.remaining.high)} />
                    </div>
                    <div>
                      <div className="mb-1.5 flex justify-between text-[12px]">
                        <span className="text-muted">场景剩余（模拟标准 USD）</span>
                        <span className="num font-medium">
                          ${e.remaining.mid}
                        </span>
                      </div>
                      <RangeBar low={e.remaining.low} mid={e.remaining.mid} high={e.remaining.high} max={Math.max(e.capacity.high, e.remaining.high)} />
                    </div>
                  </div>
                ) : (
                  <Callout tone={e.status === "source_stale" ? "warn" : "unk"} title="当前无法给出估计">
                    {e.basis}
                  </Callout>
                )}
                <div className="mt-4 border-t border-line pt-3 text-[12px] text-muted">
                  <div>依据：{e.basis}</div>
                  <div>数据时间：{e.at ?? "未知"}</div>
                  <div>前提：{e.premise}</div>
                </div>
              </Card>
            );
          })}
        </Grid>
      )}

      {tab === "history" && data.ready && (one ? (
        <WeeklyHistory key={`${auth.epoch}:${one.account.id}`} account={one.account} open={openWeekly} />
      ) : <Callout tone="info">选择单个账号即可查看其七天估算历史。</Callout>)}

      {tab === "legacy" && (
        <Card pad={false} desc="每条记录保留当时生成时间；重算另行记录。筛选实际原生窗口及官方周期结束时间；点击记录读取当时依据。" title="估算历史 · 当前授权账号">
          <Table
            rows={data.history}
            rowKey={(r) => r.id}
            onRow={(record) => { void data.openRecord(record.account_id, record.id); }}
            cols={[
              { key: "t", label: "生成时间", render: (r) => <Timestamp short className="num" value={r.generated_at} /> },
              { key: "a", label: "账号", render: (r) => data.entries.find(value => value.account.id === r.account_id)?.account.name ?? r.account_id },
              { key: "m", label: "场景剩余（标准 USD）", align: "right", render: (r) => <span className="num">{estimateAmountText(r.remaining)}</span> },
              { key: "c", label: "金额变化", align: "right", render: () => <span className="num">—</span> },
              { key: "r", label: "生成依据", render: (r) => <><span className="text-muted">{historyReason(r)}</span><Button size="sm" onClick={event => { event.stopPropagation(); void data.openRecord(r.account_id, r.id); }}>查看记录 {r.id}</Button></> },
            ]}
          />
        </Card>
      )}

      {tab === "cycles" && !one?.current.simulation_configurable && (
        <Card title={one ? `${one.account.name} · 七天配额` : "七天配额"}>
          {weekly ? <KV cols={1} items={[
            ["原生配额周期", weekly.period ? `${time(weekly.period.start_at)} → ${time(weekly.period.end_at)}` : weeklyStates[weekly.state]],
            ["当前已用", weekly.observation ? `${weekly.observation.used_percent}%` : "未知"],
            ["源观测时间", time(weekly.observation?.source_at ?? null)],
          ]} /> : <Callout tone="info">选择单个受监控的 OpenAI OAuth 账号查看配额周期。</Callout>}
        </Card>
      )}
      {tab === "cycles" && one?.current.simulation_configurable && (
        <Card title={`官方周期与本地测算段 · ${one?.account.name ?? "未选择单一账号"}`}>
          <div className="relative mt-2 mb-6">
            <div className="flex h-9 overflow-hidden rounded-md border border-line text-[11.5px]">
              <div className="flex w-[30%] items-center justify-center bg-surface-2 text-muted">历史周期未知</div>
              <div className="flex w-[70%] items-center justify-center bg-brand-soft text-brand">{record?.windows.some(window => window.period !== null) ? "已记录的原生官方窗口" : "当前官方周期未知"}</div>
            </div>
            <div className="mt-1.5 flex h-6 text-[11px]">
              <div className="w-[30%]" />
              <div className="w-[12%] rounded-l border border-dashed border-info bg-info-soft px-1 text-info">其它段未知</div>
              <div className="w-[58%] rounded-r border border-dashed border-info px-1 text-info">{manual ? "已保存人工测算段（不代表当前可用）" : "未记录人工测算段"}</div>
            </div>
            <div className="absolute top-0 left-[44%] h-[60px] w-px bg-warn" />
            <div className="mt-1 pl-[44%] text-[11px] text-warn">{record?.complete ? "模拟协议区间计价已核对；其它时段未知" : "历史计价与变更轨迹未获验证"}</div>
          </div>
          <KV
            cols={3}
            items={[
                ["官方重置证据", one ? nativePeriodText(record, time) : "请选择单一账号查看；多个账号的窗口不可合并。"],
                ["本地测算段", manual ? `${time(manual.manual_start_at)} → ${time(manual.manual_end_at)}；${estimateStates[manual.state].label}` : "未记录；人工起点不是官方重置"],
                ["声明前提", configuration?.assumptions?.premise ?? "尚无单一账号的已保存前提；价格改变不自动新建周期"],
            ]}
          />
          <Callout tone="info" className="mt-4">
            图中位置仅为示意，不按时长绘制。缺失周期不跨越结算；人工段不抹去官方周期内先前或后续消费。
          </Callout>
        </Card>
      )}

      {tab === "scheme" && simulated && (
        <Grid cols={2}>
          <Card title="当前方案" actions={<Badge tone="unk">{simulated ? "模拟条件场景" : "金额未获验证"}</Badge>}>
            <KV
              cols={1}
              items={[
                ["名称", data.entries.some(value => value.current.configuration.assumptions) ? "empirical_min_v1 · 唯一经验场景方案" : "尚无已保存方案"],
                ["适用前提", "同源原生周期、明确标准费用/保留比例、独立完整区间及历史计价证明"],
                ["独立验证", data.ready ? simulated ? "模拟协议区间费用已核对；原版能力与模型校准未获证明" : "仅条件算术已验证；实际费用完整性与模型校准未获证明" : "尚未完成读取"],
                ["保存时间", configuration?.saved_at ? time(configuration.saved_at) : data.entries.length > 1 ? "各账号分别保存；当前没有单一生效时间" : "尚无保存记录"],
              ]}
            />
          </Card>
          <Card title="可选方案">
            <Callout tone="info">目前没有第二种经独立验证的方案，因此不提供切换选项。新方案通过验证后会出现在这里；切换不会改变事实记录、已执行余额或描述性统计。</Callout>
          </Card>
        </Grid>
      )}

      <Drawer open={savedWeekly !== null} onClose={() => setWeeklyDetail(null)} title={`七天估算依据 · ${savedWeekly?.id ?? ""}`}>
        {savedWeekly && <WeeklyEvidence record={savedWeekly} allocation={allocation} time={time} />}
      </Drawer>
      <Drawer open={detail !== null} onClose={data.closeDetail} title={`估算依据 · ${detail?.id ?? ""}`}>
        {detail?.error && <Callout tone="warn">{detail.error}</Callout>}
        {detail && !saved && !detail.error && <Callout tone="info">正在读取所选账号的不可变估算记录…</Callout>}
        {saved && <div className="space-y-4">
          <Callout tone="info">以下为生成当时的保留依据，不代表当前有效金额、官方美元额度或已执行余额。</Callout>
          <KV cols={1} items={[
            ["记录 / 账号", `${saved.id} / ${saved.account_id}`],
            ["生成时间", <Timestamp key="generated" value={saved.generated_at} />],
            ["生成依据", historyReason(saved)],
            ["来源 / 连接 / 账号版本", `${saved.configuration.assumptions?.source_id} / ${saved.configuration.assumptions?.connection_revision} / ${saved.configuration.assumptions?.account_revision}`],
            ["策略 / 维护版本", `${saved.policy_revision} / ${saved.management_revision}`],
            ["观测 / 费用捕获头", `${saved.observation_head} / ${saved.capture_head}`],
            ["声明保留比例", saved.configuration.assumptions?.keep_ratio ?? "未知"],
            ["声明前提", saved.configuration.assumptions?.premise ?? "未知"],
            ["费用口径 / 资金资格", saved.complete ? "模拟协议标准 USD；所选区间完整，未证明结算资格或模型校准" : "标准 USD；完整性、结算资格和模型校准均未获证明"],
          ]} />
          {saved.windows.map(window => <Card key={window.kind} title={window.kind === "short" ? "短窗口依据" : "长窗口依据（不假定七天）"} actions={<Badge tone={estimateStates[window.state].tone}>{estimateStates[window.state].label}</Badge>}>
            <KV cols={1} items={[
              ["官方周期", window.period ? <span key="period" title={window.period.start_at + " → " + window.period.end_at}>{time(window.period.start_at)} → {time(window.period.end_at)}；{window.period.duration_seconds} 秒</span> : "未知"],
              ["原始使用百分比", window.current_used_percent === null ? "未知" : `${window.current_used_percent}%`],
              ["当前 / 选用观测", `${window.current_observation_id ?? "未知"} / ${window.observation_ids.join("、") || "无"}`],
              ["源时间 / 最早选用", `${window.source_at ? time(window.source_at) : "未知"} / ${window.oldest_sample_at ? time(window.oldest_sample_at) : "未知"}`],
              ["本地测算段", window.basis === "manual" ? <span key="manual" title={window.manual_start_at + " → " + window.manual_end_at}>{time(window.manual_start_at)} → {time(window.manual_end_at)}</span> : "未使用人工段；不改变官方周期"],
              ["状态说明", estimateStates[window.state].reason],
              ["完整费用依据（UTC）", estimateCostText(window)],
              ["场景容量 / 剩余（标准 USD）", `${estimateAmountText(window.capacity)} / ${estimateAmountText(window.remaining)}`],
            ]} />
          </Card>)}
        </div>}
      </Drawer>

      <Modal open={recompute !== null} onClose={data.closeRecompute} title="确认本地重新测算" footer={<>
        <Button disabled={recompute?.phase === "busy"} onClick={data.closeRecompute}>取消</Button>
        <AdminButton variant="primary" disabled={recompute?.phase !== "ready" && recompute?.phase !== "unknown"} onClick={() => { void data.recompute(); }}>{recompute?.phase === "busy" ? "正在提交并核对…" : recompute?.phase === "unknown" ? "只读核对结果" : "确认重新测算"}</AdminButton>
      </>}>
        <div className="space-y-4">
          <KV cols={1} items={[["账号", recompute?.base.account.name ?? "未知"], ["配置版本", recompute?.input.revision ?? "未知"], ["当前记录", recompute?.input.latest_id ?? "未知"]]} />
          <Callout tone={recompute?.error ? "warn" : "info"}>{recompute?.error ?? "只追加本地估算记录，保留原事实和历史；不写入上游余额，不会因为重算就取得资金资格。"}</Callout>
        </div>
      </Modal>

      <Modal
        open={form !== null}
        onClose={data.closeConfiguration}
        title="估算业务假设"
        footer={
          <>
            <Button disabled={form?.phase === "busy"} onClick={data.closeConfiguration}>取消</Button>
            <AdminButton
              variant="primary"
              disabled={!data.canSave}
              onClick={() => { void data.save(); }}
            >
              {form?.phase === "loading" ? "正在读取当前账号…" : form?.phase === "busy" ? "保存并核对中…" : form?.phase === "unknown" ? "核对保存结果" : "保存"}
            </AdminButton>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="账号与估计范围">
            <Select value={form ? `${form.base.account.id}:${form.scope}` : ""} disabled={form?.phase !== "ready" || !data.ready} onChange={(e) => data.selectTarget(e.target.value)} options={targets.flatMap(value => [{ value: `${value.account.id}:short`, label: `${value.account.name} · 短窗口` }, { value: `${value.account.id}:seven_day`, label: `${value.account.name} · 长窗口（不假定七天）` }, { value: `${value.account.id}:both`, label: `${value.account.name} · 两个原生窗口` }])} />
          </Field>
          <Field label="成本口径" hint="标准费用不直接等于钱包扣款。">
            <Select
              value="standard_usd"
              disabled
              options={[
                { value: "standard_usd", label: "标准费用（唯一已定义口径）" },
              ]}
            />
          </Field>
          <Field label="声明保留比例" hint="大于 0 且不超过 1；最多 18 位小数，不提供默认比例。">
            <Input value={form?.draft.keep_ratio ?? ""} disabled={form?.phase !== "ready"} inputMode="decimal" onChange={e => data.editDraft("keep_ratio", e.target.value)} placeholder="明确填写保留比例" />
          </Field>
          <Field label="声明前提">
            <Input value={form?.draft.premise ?? ""} disabled={form?.phase !== "ready"} onChange={e => data.editDraft("premise", e.target.value)} placeholder="明确声明本次经验测算的适用前提" />
          </Field>
          <Callout tone={form?.error ? "warn" : "info"}>{form?.error ?? (form?.phase === "loading" ? "正在只读核对所选账号的当前来源与版本。" : "提交前须明确填写保留比例与前提，并核对所选窗口。保存后按当时证据生成场景或不可用原因；不会修改账单或开放钱包操作。")}{!isAdmin && "当前为只读身份。"}</Callout>
        </div>
      </Modal>
    </>
  );
}

const weeklyStates: Record<NativeWeeklyCurrent["state"], string> = {
  ready: "已估算",
  condition_changed: "等待当前账号身份核实",
  connection_unavailable: "等待 Sub2API 连接可用",
  monitoring_paused: "被动监控已暂停",
  awaiting_observation: "等待七天配额观测",
  quota_unavailable: "七天配额读取失败",
  period_unknown: "缺少有效的七天用量、时长或重置时间",
  source_stale: "七天配额观测已过期",
  invalid_evidence: "七天配额观测相互冲突",
  quota_decreased: "本周期的已用比例回落，等待新的额度周期",
  observation_limit: "当前周期的观测超出可核对范围",
  awaiting_increase: "等待原生已用比例大于 0",
  collecting: "正在读取并核对使用记录",
  usage_unavailable: "读取使用记录失败，等待重试",
  usage_denied: "当前连接凭据无法读取使用记录",
  usage_rate_limited: "Sub2API 限制了读取频率",
  usage_invalid: "Sub2API 返回的使用记录无法核对",
  pagination_changed: "使用记录在分页期间发生变化，等待重新核对",
  usage_limit: "单日使用记录超出可核对范围",
  no_cost: "本周期累计标准费用尚未大于零",
  cost_unavailable: "本周期用量记录缺少有效标准费用",
};

type TimeLabel = (value: string | null) => string;
type WeeklyAllocation = NonNullable<NativeWeeklyCurrent["allocation"]>;
const weeklyConfidence = {
  collecting: "正在积累估算，达到分配门槛后显示份额",
  unstable: "最近三次总量估算波动较大，继续采集",
  trusted: "已达到分配门槛",
} as const;

function weeklyAmount(value: NativeWeeklyRecord["capacity"], decimals = 2) {
  const units = 10n ** BigInt(decimals);
  const numerator = BigInt(value.numerator), denominator = BigInt(value.denominator);
  const rounded = (2n * numerator * units + denominator) / (2n * denominator);
  if (rounded === 0n && numerator > 0n) return `<${(1 / Number(units)).toFixed(decimals)}`;
  return `${rounded / units}.${String(rounded % units).padStart(decimals, "0")}`;
}

function WeeklyCard({ name, weekly, time, open }: { name: string; weekly: NativeWeeklyCurrent; time: TimeLabel; open: (record: NativeWeeklyRecord) => void }) {
  const { latest, observation, period, progress, remaining, allocation } = weekly;
  const ready = latest !== null && remaining !== null;
  return <Card title={name} desc={period ? `原生七天周期 ${time(period.start_at)} → ${time(period.end_at)}` : "原生七天配额"}
    actions={<><Badge tone={ready ? "ok" : "unk"} dot>{ready ? weeklyStates.ready : "待估算"}</Badge>
      {latest && <Button size="sm" onClick={() => open(latest)}>{allocation ? "查看依据与分配" : "查看依据"}</Button>}</>}>
    {ready ? <div className="space-y-4">
      {([["七天总量估算（标准 USD）", latest.capacity], ["估算剩余（标准 USD）", remaining]] as const).map(([label, amount]) => {
        const point = Number(amount.numerator) / Number(amount.denominator);
        const capacity = Number(latest.capacity.numerator) / Number(latest.capacity.denominator);
        return <div key={label}>
          <div className="mb-1.5 flex justify-between text-[12px]">
            <span className="text-muted">{label}</span>
            <span className="num font-medium">{weeklyAmount(amount)}</span>
          </div>
          <RangeBar low={point} mid={point} high={point} max={capacity} />
        </div>;
      })}
    </div> : <Callout tone={weekly.state === "source_stale" ? "warn" : "unk"} title={weeklyStates[weekly.state]} />}
    <div className="mt-4 border-t border-line pt-3 text-[12px] text-muted">
      {observation && <div>已用 {observation.used_percent}% · 观测于 {time(observation.source_at)}</div>}
      {latest && <div>截至 {time(latest.observation.source_at)} · 累计 {weeklyAmount(latest.standard_cost, 8)} 标准 USD · {latest.records} 条记录</div>}
      {latest?.cumulative && <div>{allocation ? `已按 ${allocation.pool_name} 的当前份额计算分配` :
        observation?.used_percent === latest.observation.used_percent ? weeklyConfidence[latest.cumulative.confidence] : "正在核对最新已用比例对应的累计费用"}</div>}
      {progress && <div>{ready ? "更新中 · " : ""}{weeklyStates[progress.state]}{progress.total > 0 && ` · ${progress.day.slice(0, 10)} UTC · ${progress.rechecking ? "复核" : "读取"} ${progress.scanned} / ${progress.total}`}</div>}
    </div>
  </Card>;
}

function WeeklyEvidence({ record, allocation, time }: { record: NativeWeeklyRecord; allocation?: WeeklyAllocation; time: TimeLabel }) {
  const { cumulative } = record;
  return <div className="space-y-4">
    <KV cols={1} items={[
      ["生成时间", time(record.generated_at)],
      ["原生七天周期", `${time(record.period.start_at)} → ${time(record.period.end_at)}`],
      ["估算采用的原生占比", `${record.observation.used_percent}% · ${time(record.observation.source_at)} · 观测 #${record.observation.observation_id}`],
      ["来源 / 连接 / 纳管版本", `${record.source_id} / ${record.connection_revision} / ${record.account_revision}`],
      ...(record.percentage_point_increase ? [["计入的用量增长", `${estimateAmountText(record.percentage_point_increase)} 个百分点`] as [string, string]] : []),
      ["计入的标准费用", `${weeklyAmount(record.standard_cost, 8)} USD · ${record.records} 条记录`],
      ["七天总量 / 当时剩余", `${weeklyAmount(record.capacity)} / ${weeklyAmount(record.remaining)} 标准 USD`],
    ]} />
    {cumulative && <>
      <Table rows={cumulative.checkpoints} rowKey={point => point.observation.observation_id} cols={[
        { key: "time", label: "累计截止时间", render: point => <Timestamp short value={point.observation.source_at} /> },
        { key: "used", label: "已用比例", render: point => `${point.observation.used_percent}%` },
        { key: "cost", label: "累计标准费用（USD）", render: point => weeklyAmount(point.standard_cost, 8) },
        { key: "total", label: "总量估算（标准 USD）", render: point => weeklyAmount(point.capacity) },
      ]} />
      <Callout tone="info">
        累计费用从周期起点算到该次原生观测，除以已用比例得到周总量。已用达到 20%，且最近三次不同占比的总量估算最大差距不超过最新估值的 10% 时，按池合同计算参考分配。
      </Callout>
    </>}
    {record.samples && <Table rows={record.samples} rowKey={sample => sample.end.observation_id} cols={[
      { key: "from", label: "样本起点", render: sample => <><Timestamp short value={sample.start.source_at} /> · {sample.start.used_percent}% · #{sample.start.observation_id}</> },
      { key: "to", label: "样本终点", render: sample => <><Timestamp short value={sample.end.source_at} /> · {sample.end.used_percent}% · #{sample.end.observation_id}</> },
      { key: "cost", label: "标准费用（USD）", render: sample => <span title={estimateAmountText(sample.standard_cost)}>{weeklyAmount(sample.standard_cost, 8)} · {sample.records} 条</span> },
      { key: "read", label: "日志核对时间", render: sample => <Timestamp short value={sample.read_at} /> },
    ]} />}
    {allocation && <Card title={`${allocation.pool_name} · 当前比例分配`} desc={`按池合同 #${allocation.contract_id} 计算，单位为标准 USD。`}>
      <Table rows={allocation.members} rowKey={member => member.participant_id} cols={[
        { key: "name", label: "参与者", render: member => member.name },
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
        setPage({ before, records: [], next: null, error: "七天估算历史与所选账号或分页不一致。" });
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
    {!visible || visible.error || visible.records.length === 0 ? <Callout tone={visible?.error ? "warn" : "info"}>{visible?.error ?? (visible ? "尚无七天估算记录。" : "正在读取七天估算历史。")}</Callout> :
      <Table rows={visible.records} rowKey={record => record.id} cols={[
        { key: "time", label: "生成时间", render: record => <Timestamp short value={record.generated_at} /> },
        { key: "total", label: "总量估算（标准 USD）", align: "right", render: record => weeklyAmount(record.capacity) },
        { key: "remaining", label: "当时剩余（标准 USD）", align: "right", render: record => weeklyAmount(record.remaining) },
        { key: "record", label: "依据", render: record => <Button size="sm" onClick={() => open(record)}>查看记录 {record.id}</Button> },
      ]} />}
  </Card>;
}
