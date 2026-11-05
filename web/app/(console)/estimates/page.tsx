"use client";

import { useState } from "react";
import { Settings2 } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Field, Grid, KV, Modal, PageHeader, Select, Table, Tabs } from "@/components/ui";
import { RangeBar } from "@/components/charts";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { accountName, estimateHistory, estimates, EST_STATUS } from "@/lib/mock";

export default function EstimatesPage() {
  const { toast } = useApp();
  const { canAccount, isAdmin } = useScope();
  const [tab, setTab] = useState("current");
  const [cfgOpen, setCfgOpen] = useState(false);
  const [cfg, setCfg] = useState({ costBasis: "standard", margin: "moderate", scope: "weekly" });
  const rows = estimates.filter((e) => canAccount(e.account));

  return (
    <>
      <PageHeader
        title="容量估算"
        tag="S2A-EST-001 ~ 006"
        desc="估算是本系统的派生推断，附带范围、前提和数据时间。不代表官方额度、未来精确可用量或已执行的写入。"
        actions={
          <AdminButton icon={<Settings2 size={14} />} onClick={() => setCfgOpen(true)}>
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
            const st = EST_STATUS[e.status];
            return (
              <Card key={e.account} title={accountName(e.account)} desc={`周期：${e.cycle}`} actions={<Badge tone={st.tone} dot>{st.label}</Badge>}>
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
                  <Callout tone={e.status === "stale" ? "warn" : "unk"} title="当前无法给出估计">
                    {e.basis}
                  </Callout>
                )}
                <div className="mt-4 border-t border-line pt-3 text-[12px] text-muted">
                  <div>依据：{e.capacity ? e.basis : "—"}</div>
                  <div>数据时间：{e.at ?? "未知"}</div>
                  <div>前提：Team/Plus 套餐窗口、仅本实例使用、计价规则未变</div>
                </div>
              </Card>
            );
          })}
        </Grid>
      )}

      {tab === "history" && (
        <Card pad={false} desc="每条记录是当时实际生成的结果；重算产生的新结果另行记录，不覆盖旧值。" title="Alpha 主账号 · 本周期">
          <Table
            rows={estimateHistory}
            rowKey={(r) => r.at}
            cols={[
              { key: "t", label: "生成时间", render: (r) => <span className="num">{r.at}</span> },
              { key: "a", label: "账号", render: (r) => accountName(r.account) },
              { key: "m", label: "容量中值", align: "right", render: (r) => <span className="num">${r.mid}</span> },
              { key: "c", label: "变化", align: "right", render: (r) => <span className="num">{r.change}</span> },
              { key: "r", label: "变化依据", render: (r) => <span className="text-muted">{r.reason}</span> },
            ]}
          />
        </Card>
      )}

      {tab === "cycles" && (
        <Card title="官方周期与本地测算段 · Alpha 主账号">
          <div className="relative mt-2 mb-6">
            <div className="flex h-9 overflow-hidden rounded-md border border-line text-[11.5px]">
              <div className="flex w-[30%] items-center justify-center bg-surface-2 text-muted">10-25 → 11-01 官方周期</div>
              <div className="flex w-[70%] items-center justify-center bg-brand-soft text-brand">11-01 09:00 → 11-08 09:00 官方周期（当前）</div>
            </div>
            <div className="mt-1.5 flex h-6 text-[11px]">
              <div className="w-[30%]" />
              <div className="w-[12%] rounded-l border border-dashed border-info bg-info-soft px-1 text-info">本地段 A</div>
              <div className="w-[58%] rounded-r border border-dashed border-info px-1 text-info">本地段 B（人工起点 ob-86001）</div>
            </div>
            <div className="absolute top-0 left-[44%] h-[60px] w-px bg-warn" />
            <div className="mt-1 pl-[44%] text-[11px] text-warn">11-02 10:30 计价前提变化（PC-12）</div>
          </div>
          <KV
            cols={3}
            items={[
              ["官方重置证据", "上游窗口 resetAt 变化"],
              ["本地测算段", "人工起点，非官方重置"],
              ["计价变化", "记录前提，不自动新建周期"],
            ]}
          />
          <Callout tone="info" className="mt-4">
            缺失多个周期时不会静默跨越结算；人工起点不会抹去同一官方周期内的真实消费。
          </Callout>
        </Card>
      )}

      {tab === "scheme" && (
        <Grid cols={2}>
          <Card title="当前方案" actions={<Badge tone="ok">生效中</Badge>}>
            <KV
              cols={1}
              items={[
                ["名称", "区间估计 v2"],
                ["适用前提", "OpenAI OAuth 窗口账号 · 标准费用口径 · 数据新鲜"],
                ["独立验证", "已通过（2026-10-20）"],
                ["生效时间", "2026-10-20 12:00"],
              ]}
            />
          </Card>
          <Card title="可选方案">
            <Callout tone="info">目前没有第二种经独立验证的方案，因此不提供切换选项。新方案通过验证后会出现在这里；切换不会改变事实记录、已执行余额或描述性统计。</Callout>
          </Card>
        </Grid>
      )}

      <Modal
        open={cfgOpen}
        onClose={() => setCfgOpen(false)}
        title="估算业务假设"
        footer={
          <>
            <Button onClick={() => setCfgOpen(false)}>取消</Button>
            <AdminButton
              variant="primary"
              onClick={() => {
                setCfgOpen(false);
                toast({ tone: "info", title: "假设已保存", body: "当前估计标记为“前提已变化”，将在下次采集后更新。" });
              }}
            >
              保存
            </AdminButton>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="估计范围">
            <Select value={cfg.scope} onChange={(e) => setCfg({ ...cfg, scope: e.target.value })} options={[{ value: "weekly", label: "7 天窗口" }, { value: "short", label: "短周期窗口" }]} />
          </Field>
          <Field label="成本口径" hint="标准费用不直接等于钱包扣款。">
            <Select
              value={cfg.costBasis}
              onChange={(e) => setCfg({ ...cfg, costBasis: e.target.value })}
              options={[
                { value: "standard", label: "标准费用" },
                { value: "actual", label: "actual_cost 口径" },
              ]}
            />
          </Field>
          <Field label="余额建议的保守程度">
            <Select
              value={cfg.margin}
              onChange={(e) => setCfg({ ...cfg, margin: e.target.value })}
              options={[
                { value: "conservative", label: "保守（取区间下沿）" },
                { value: "moderate", label: "适中" },
                { value: "loose", label: "宽松（取区间上沿）" },
              ]}
            />
          </Field>
          <Callout tone="info">配置不会修改真实账单。{!isAdmin && "当前为只读身份。"}</Callout>
        </div>
      </Modal>
    </>
  );
}
