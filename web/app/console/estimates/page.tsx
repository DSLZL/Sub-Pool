"use client";

import { useState } from "react";
import { Settings2 } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Drawer, Field, Grid, Input, KV, Modal, PageHeader, Select, Table, Tabs, Toolbar } from "@/components/ui";
import { RangeBar } from "@/components/charts";
import { useScope } from "@/lib/scope";
import { useEstimateData } from "@/lib/use-estimate-data";
import { estimateAmountText, estimateCard, estimateCostText, estimateStates, historyReason, nativePeriodText } from "@/lib/estimates-data";

export default function EstimatesPage() {
  const scope = useScope();
  const { isAdmin } = scope;
  const [tab, setTab] = useState("current");
  const data = useEstimateData(scope, tab === "history"), rows = data.entries.map(estimateCard), form = data.form;
  const one = data.entries.length === 1 ? data.entries[0] : undefined, record = one?.current.latest ?? null;
  const manual = record?.windows.find(window => window.basis === "manual");
  const targets = form && !data.entries.some(value => value.account.id === form.base.account.id) ? [form.base, ...data.entries] : data.entries;
  const simulated = data.entries.some(value => value.current.latest?.windows.some(window => window.cost_proof));
  const notice = data.error ?? data.pendingNotice ?? (!data.ready ? "正在核对当前授权范围的估算…" : data.entries.length === 0 ? "当前授权范围没有可读取的账号。" : simulated ? "模拟协议的场景已标明来源；模型未校准，未知金额保持为空。" : "金额、费用覆盖与模型校准仍未获完整证据；未知不会补零。");
  const configuration = one?.current.configuration;
  const detail = data.detail, saved = detail?.record, recompute = data.recomputeForm;

  return (
    <>
      <PageHeader
        title="容量估算"
        tag="S2A-EST-001 ~ 006"
        desc={`估算是本系统的派生推断，不代表官方美元额度、未来保证或已执行写入。${notice}`}
        actions={
          <>
          <AdminButton icon={<Settings2 size={14} />} disabled={!data.canOpen} onClick={data.openConfiguration}>
            估算假设
          </AdminButton>
          <AdminButton disabled={!data.canRecompute} onClick={data.openRecompute}>{data.recomputeUnresolved ? "核对重算结果" : "本地重新测算"}</AdminButton>
          </>
        }
      />

      <Toolbar>
        <Field label="查看账号">
          <Select value={data.filters.account} onChange={event => data.selectAccount(event.target.value)} options={[{ value: "", label: "当前授权全部账号" }, ...data.accounts.map(account => ({ value: account.id, label: account.name }))]} />
        </Field>
        {tab === "history" && <>
          <Field label="原生窗口">
            <Select value={data.filters.window_kind} onChange={event => data.selectHistoryWindow(event.target.value)} options={[{ value: "all", label: "全部窗口" }, { value: "short", label: "短窗口" }, { value: "seven_day", label: "长窗口（不假定七天）" }]} />
          </Field>
          <Field label="官方周期结束（UTC）">
            <Input value={data.filters.period_end_at} onChange={event => data.editPeriod(event.target.value)} disabled={data.filters.window_kind === "all"} placeholder="留空读取该窗口全部历史" />
          </Field>
        </>}
      </Toolbar>
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "current", label: "当前估计" },
          { id: "history", label: "估算历史" },
          { id: "cycles", label: "周期与测算段" },
          { id: "scheme", label: "估算方案" },
        ]}
      />

      {tab === "current" && (
        <Grid cols={2}>
          {rows.map((e) => {
            const st = estimateStates[e.status];
            return (
              <Card key={e.account} title={e.name} desc={`周期：${e.cycle}`} actions={<><Badge tone={st.tone} dot>{st.label}</Badge><Button size="sm" disabled={!data.entries.find(entry => entry.account.id === e.account)?.current.latest} onClick={() => { const latest = data.entries.find(entry => entry.account.id === e.account)?.current.latest; if (latest) void data.openRecord(e.account, latest.id); }}>查看依据</Button></>}>
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

      {tab === "history" && (
        <Card pad={false} desc="每条记录保留当时生成时间；重算另行记录。筛选实际原生窗口及官方周期结束时间；点击记录读取当时依据。" title="估算历史 · 当前授权账号">
          <Table
            rows={data.history}
            rowKey={(r) => r.id}
            onRow={(record) => { void data.openRecord(record.account_id, record.id); }}
            cols={[
              { key: "t", label: "生成时间（UTC）", render: (r) => <span className="num">{r.generated_at}</span> },
              { key: "a", label: "账号", render: (r) => data.entries.find(value => value.account.id === r.account_id)?.account.name ?? r.account_id },
              { key: "m", label: "场景剩余（标准 USD）", align: "right", render: (r) => <span className="num">{estimateAmountText(r.remaining)}</span> },
              { key: "c", label: "金额变化", align: "right", render: () => <span className="num">—</span> },
              { key: "r", label: "生成依据", render: (r) => <><span className="text-muted">{historyReason(r)}</span><Button size="sm" onClick={event => { event.stopPropagation(); void data.openRecord(r.account_id, r.id); }}>查看记录 {r.id}</Button></> },
            ]}
          />
        </Card>
      )}

      {tab === "cycles" && (
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
                ["官方重置证据", one ? nativePeriodText(record) : "请选择单一账号查看；多个账号的窗口不可合并。"],
                ["本地测算段", manual ? `${manual.manual_start_at} → ${manual.manual_end_at}；${estimateStates[manual.state].label}` : "未记录；人工起点不是官方重置"],
                ["声明前提", configuration?.assumptions?.premise ?? "尚无单一账号的已保存前提；价格改变不自动新建周期"],
            ]}
          />
          <Callout tone="info" className="mt-4">
            图中位置仅为示意，不按时长绘制。缺失周期不跨越结算；人工段不抹去官方周期内先前或后续消费。所有时间保留 UTC 原值。
          </Callout>
        </Card>
      )}

      {tab === "scheme" && (
        <Grid cols={2}>
          <Card title="当前方案" actions={<Badge tone="unk">{simulated ? "模拟条件场景" : "金额未获验证"}</Badge>}>
            <KV
              cols={1}
              items={[
                ["名称", data.entries.some(value => value.current.configuration.assumptions) ? "empirical_min_v1 · 唯一经验场景方案" : "尚无已保存方案"],
                ["适用前提", "同源原生周期、明确标准费用/保留比例、独立完整区间及历史计价证明"],
                ["独立验证", data.ready ? simulated ? "模拟协议区间费用已核对；原版能力与模型校准未获证明" : "仅条件算术已验证；实际费用完整性与模型校准未获证明" : "尚未完成读取"],
                ["保存时间", configuration?.saved_at ?? (data.entries.length > 1 ? "各账号分别保存；当前没有单一生效时间" : "尚无保存记录")],
              ]}
            />
          </Card>
          <Card title="可选方案">
            <Callout tone="info">目前没有第二种经独立验证的方案，因此不提供切换选项。新方案通过验证后会出现在这里；切换不会改变事实记录、已执行余额或描述性统计。</Callout>
          </Card>
        </Grid>
      )}

      <Drawer open={detail !== null} onClose={data.closeDetail} title={`估算依据 · ${detail?.id ?? ""}`}>
        {detail?.error && <Callout tone="warn">{detail.error}</Callout>}
        {detail && !saved && !detail.error && <Callout tone="info">正在读取所选账号的不可变估算记录…</Callout>}
        {saved && <div className="space-y-4">
          <Callout tone="info">以下为生成当时的保留依据，不代表当前有效金额、官方美元额度或已执行余额。</Callout>
          <KV cols={1} items={[
            ["记录 / 账号", `${saved.id} / ${saved.account_id}`],
            ["生成时间（UTC）", saved.generated_at],
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
              ["官方周期（UTC）", window.period ? `${window.period.start_at} → ${window.period.end_at}；${window.period.duration_seconds} 秒` : "未知"],
              ["原始使用百分比", window.current_used_percent === null ? "未知" : `${window.current_used_percent}%`],
              ["当前 / 选用观测", `${window.current_observation_id ?? "未知"} / ${window.observation_ids.join("、") || "无"}`],
              ["源时间 / 最早选用（UTC）", `${window.source_at ?? "未知"} / ${window.oldest_sample_at ?? "未知"}`],
              ["本地测算段（UTC）", window.basis === "manual" ? `${window.manual_start_at} → ${window.manual_end_at}` : "未使用人工段；不改变官方周期"],
              ["状态说明", estimateStates[window.state].reason],
              ["完整费用依据", estimateCostText(window)],
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
