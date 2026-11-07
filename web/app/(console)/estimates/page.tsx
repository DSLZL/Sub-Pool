"use client";

import { useState } from "react";
import { Settings2 } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Field, Grid, KV, Modal, PageHeader, Select, Table, Tabs } from "@/components/ui";
import { RangeBar } from "@/components/charts";
import { useScope } from "@/lib/scope";
import { useEstimateData } from "@/lib/use-estimate-data";
import { estimateCard, estimateStates, historyReason, nativePeriodText } from "@/lib/estimates-data";

export default function EstimatesPage() {
  const scope = useScope();
  const { isAdmin } = scope;
  const [tab, setTab] = useState("current");
  const data = useEstimateData(scope, tab === "history"), rows = data.entries.map(estimateCard), form = data.form;
  const one = data.entries.length === 1 ? data.entries[0] : undefined, record = one?.current.latest ?? null;
  const manual = record?.windows.find(window => window.basis === "manual");
  const targets = form && !data.entries.some(value => value.account.id === form.base.account.id) ? [form.base, ...data.entries] : data.entries;
  const notice = data.error ?? data.pendingNotice ?? (!data.ready ? "正在核对当前授权范围的估算…" : data.entries.length === 0 ? "当前授权范围没有可读取的账号。" : "金额、费用覆盖与模型校准仍未获完整证据；未知不会补零。");
  const configuration = one?.current.configuration;

  return (
    <>
      <PageHeader
        title="容量估算"
        tag="S2A-EST-001 ~ 006"
        desc={`估算是本系统的派生推断，不代表官方美元额度、未来保证或已执行写入。${notice}`}
        actions={
          <AdminButton icon={<Settings2 size={14} />} disabled={!data.canOpen} onClick={data.openConfiguration}>
            估算假设
          </AdminButton>
        }
      />
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
              <Card key={e.account} title={e.name} desc={`周期：${e.cycle}`} actions={<Badge tone={st.tone} dot>{st.label}</Badge>}>
                {e.capacity && e.remaining ? (
                  <div className="space-y-4">
                    <div>
                      <div className="mb-1.5 flex justify-between text-[12px]">
                        <span className="text-muted">等效周期容量（标准费用口径）</span>
                        <span className="num font-medium">
                          ${e.capacity.low} – ${e.capacity.high}
                        </span>
                      </div>
                      <RangeBar low={e.capacity.low} mid={e.capacity.mid} high={e.capacity.high} max={320} />
                    </div>
                    <div>
                      <div className="mb-1.5 flex justify-between text-[12px]">
                        <span className="text-muted">剩余能力</span>
                        <span className="num font-medium">
                          ${e.remaining.low} – ${e.remaining.high}
                        </span>
                      </div>
                      <RangeBar low={e.remaining.low} mid={e.remaining.mid} high={e.remaining.high} max={320} />
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
        <Card pad={false} desc="每条记录保留当时生成时间；重算另行记录。此处读取当前授权账号的保留历史，原界面没有账号/周期筛选控件。" title="估算历史 · 当前授权账号">
          <Table
            rows={data.history}
            rowKey={(r) => r.id}
            cols={[
              { key: "t", label: "生成时间（UTC）", render: (r) => <span className="num">{r.generated_at}</span> },
              { key: "a", label: "账号", render: (r) => data.entries.find(value => value.account.id === r.account_id)?.account.name ?? r.account_id },
              { key: "m", label: "容量参考（USD）", align: "right", render: () => <span className="num">未知</span> },
              { key: "c", label: "金额变化", align: "right", render: () => <span className="num">—</span> },
              { key: "r", label: "生成依据", render: (r) => <span className="text-muted">{historyReason(r)}</span> },
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
            <div className="mt-1 pl-[44%] text-[11px] text-warn">历史计价与变更轨迹未获验证</div>
          </div>
          <KV
            cols={3}
            items={[
                ["官方重置证据", one ? nativePeriodText(record) : "多个账号的窗口不可合并；原界面没有只读账号选择器。"],
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
          <Card title="当前方案" actions={<Badge tone="unk">金额未获验证</Badge>}>
            <KV
              cols={1}
              items={[
                ["名称", data.entries.some(value => value.current.configuration.assumptions) ? "empirical_min_v1 · 唯一经验场景方案" : "尚无已保存方案"],
                ["适用前提", "同源原生周期、明确标准费用/保留比例、独立完整区间及历史计价证明"],
                ["独立验证", data.ready ? "仅条件算术已验证；实际费用完整性与模型校准未获证明" : "尚未完成读取"],
                ["保存时间", configuration?.saved_at ?? (data.entries.length > 1 ? "各账号分别保存；当前没有单一生效时间" : "尚无保存记录")],
              ]}
            />
          </Card>
          <Card title="可选方案">
            <Callout tone="info">目前没有第二种经独立验证的方案，因此不提供切换选项。新方案通过验证后会出现在这里；切换不会改变事实记录、已执行余额或描述性统计。</Callout>
          </Card>
        </Grid>
      )}

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
              {form?.phase === "busy" ? "保存并核对中…" : form?.phase === "unknown" ? "核对保存结果" : "保存"}
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
          <Field label="原声明保留比例（本页不可改）">
            <Select
              value={form?.base.current.configuration.assumptions?.keep_ratio ?? ""}
              disabled
              options={[
                { value: form?.base.current.configuration.assumptions?.keep_ratio ?? "", label: form?.base.current.configuration.assumptions ? `keep = ${form.base.current.configuration.assumptions.keep_ratio}（保留原值）` : "尚未声明，不能代填默认值" },
              ]}
            />
          </Field>
          <Callout tone={form?.error ? "warn" : "info"}>{form?.error ?? (form?.base.current.configuration.assumptions ? `声明前提：${form.base.current.configuration.assumptions.premise}。此页只修改窗口；缺少填写比例/前提的原有控件，不能首次配置。配置不会修改账单或开放钱包操作。` : "首次配置需要明确保留比例和前提；原界面缺少输入控件，保存保持关闭，没有默认金额或比例。")}{!isAdmin && "当前为只读身份。"}</Callout>
        </div>
      </Modal>
    </>
  );
}
