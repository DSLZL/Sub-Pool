"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { useEffect, useRef, useState } from "react";
import { ScanSearch, Play } from "lucide-react";
import { maintenanceHistoryRulesVersion, maintenanceRulesVersion, type MaintenanceInput, type MaintenancePlan } from "@sub-pool/contracts/maintenance";
import { Badge, Button, Callout, Card, Checkbox, Drawer, Empty, Field, KV, Modal, Mono, PageHeader, Select, Table } from "@/components/ui";
import { DateTimeField } from "@/components/date-time-field";
import { displayTime } from "@/lib/connection-data";
import { maintenanceCanApply, maintenanceRange, maintenanceIssue, maintenanceKinds, maintenanceStatus } from "@/lib/maintenance-data";
import { useMaintenanceData } from "@/lib/use-maintenance-data";
import { useApp } from "@/lib/store";
import { estimateAmountText, estimateStates } from "@/lib/estimates-data";

const ruleLabels: Record<string, string> = {
  [maintenanceRulesVersion]: `按 ${glossary.sub2api} 周期索引`,
  [maintenanceHistoryRulesVersion]: "复核估算",
};

export default function MaintenancePage() {
  const data = useMaintenanceData(), sel = data.selected;
  const { displayTimezone } = useApp();
  const time = (value: string) => displayTime(value, displayTimezone, false);
  const [newOpen, setNewOpen] = useState(false);
  const [kind, setKind] = useState<MaintenanceInput["kind"]>("rebuild");
  const [acc, setAcc] = useState(""), [from, setFrom] = useState(""), [until, setUntil] = useState("");
  const [applyAck, setApplyAck] = useState(false), [formError, setFormError] = useState<string | null>(null);
  const form = useRef(0);
  useEffect(() => { setApplyAck(false); }, [data.selectionID, sel?.basis.fingerprint, sel?.validity, sel?.state]);
  const checking = data.pending === "preflight", busy = data.pending !== null;
  const readMode = data.blocked || !!data.error || !data.ready || data.accounts?.length === 0;
  const canApply = sel !== null && maintenanceCanApply(sel) && data.ready && !data.blocked && !busy;
  const scope = (plan: MaintenancePlan, short = false) => <span>
    {data.accounts?.find(account => account.id === plan.scope.account_id)?.name ?? plan.scope.account_id} <Timestamp short={short} value={plan.scope.from} /> → <Timestamp short={short} value={plan.scope.until} />
  </span>;
  function closeNew() { form.current++; setNewOpen(false); setAcc(""); setFrom(""); setUntil(""); setKind("rebuild"); setFormError(null); }
  function openNew() {
    if (busy || !data.ready || data.blocked) return;
    form.current++; const now = new Date().toISOString();
    setAcc(""); setKind("rebuild"); setFrom(now.slice(0, 10) + "T00:00:00Z"); setUntil(now); setFormError(null); setNewOpen(true);
  }
  async function precheck() {
    if (busy || !newOpen) return;
    const generation = form.current; setFormError(null);
    try {
      const input = maintenanceRange(acc, kind, from, until), plan = await data.preflight(input);
      if (generation !== form.current || plan === null) return;
      closeNew(); await data.open(plan.id);
    } catch (error) { if (generation === form.current) setFormError(error instanceof Error ? error.message : "输入待核对。"); }
  }
  if (!data.allowed) return <Empty title="仅管理员" />;

  return (
    <>
      <PageHeader
        title={uiCopy.maintenance}
        helpTopic="operations"
        actions={
          <Button variant="primary" icon={<ScanSearch size={14} />} disabled={busy} onClick={() => readMode ? void data.load() : openNew()}>
            {data.pending === "read" ? uiCopy.loading : readMode ? uiCopy.reload : "新建预检"}
          </Button>
        }
      />
      {data.error && <Callout tone="bad" title="读取不全">{data.error}。请重读。</Callout>}
      {data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"} title={data.notice.unknown ? "结果待核对" : "维护状态"}>{data.notice.text}</Callout>}
      <Card pad={false}>
        <Table
          rows={data.plans ?? []}
          rowKey={(p) => p.id}
          empty={data.pending === "read" ? "读取计划…" : !data.ready ? "记录待读取。" : data.accounts?.length === 0 ? "先纳管账号。" : "暂无计划"}
          onRow={(p) => { setApplyAck(false); void data.open(p.id); }}
          cols={[
            { key: "id", label: "计划", render: (p) => <Mono>{p.id}</Mono> },
            { key: "k", label: "类型", render: (p) => maintenanceKinds[p.kind] },
            { key: "s", label: "范围", render: p => scope(p, true) },
            { key: "i", label: "问题 / 阻塞", render: (p) => <span className="num">{p.issues.length} / <span className={p.issues.some(issue => issue.blocking) ? "text-bad" : ""}>{p.issues.filter(issue => issue.blocking).length}</span></span> },
            { key: "b", label: "创建", render: (p) => <Timestamp short className="num text-muted" value={p.created_at} /> },
            { key: "st", label: uiCopy.status, render: (p) => <Badge tone={maintenanceStatus(p).tone} dot>{maintenanceStatus(p).label}</Badge> },
          ]}
        />
      </Card>

      <Modal
        open={newOpen}
        onClose={closeNew}
        title="新建预检"
        footer={
          <>
            <Button onClick={closeNew}>取消</Button>
            <Button variant="primary" disabled={busy || !acc} onClick={() => void precheck()}>
              {checking ? "预检中…" : "开始预检"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          {formError && <Callout tone="warn" title="无法预检">{formError}</Callout>}
          {data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"} title={data.notice.unknown ? "结果待核对" : "预检未完成"}>{data.notice.text}</Callout>}
          <Field label="类型">
            <Select
              value={kind}
              disabled={busy}
              onChange={(e) => { const value = e.target.value; if (value === "rebuild" || value === "cycles" || value === "upgrade") setKind(value); }}
              options={[
                { value: "rebuild", label: "重建索引" },
                { value: "cycles", label: "核对周期" },
                { value: "upgrade", label: "复算估算" },
              ]}
            />
          </Field>
          <Field label={uiCopy.account}>
            <Select value={acc} disabled={busy} onChange={(e) => setAcc(e.target.value)} options={[{ value: "", label: "选择账号" }, ...(data.accounts ?? []).map((a) => ({ value: a.id, label: a.name }))]} />
          </Field>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <DateTimeField label="开始（含）" value={from} disabled={busy} onChange={setFrom} />
            <DateTimeField label="结束（不含）" value={until} disabled={busy} onChange={setUntil} />
          </div>
        </div>
      </Modal>

      <Drawer open={data.selectionID !== null} onClose={() => { data.close(); setApplyAck(false); }} title={data.selectionID ? `维护计划 ${data.selectionID}` : ""}>
        <div className="space-y-4">
          {data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"} title={data.notice.unknown ? "结果待核对" : "维护状态"}>{data.notice.text}</Callout>}
          {!sel && <Callout tone="info" title={busy ? "读取预检…" : "缺少预检"}>先读计划。</Callout>}
          {sel && <>
            <KV
              items={[
                ["类型", maintenanceKinds[sel.kind]],
                [uiCopy.status, <Badge key="s" tone={maintenanceStatus(sel).tone}>{maintenanceStatus(sel).label}</Badge>],
                ["范围", scope(sel)],
                ["维护依据", <span key="rule" title={sel.rules_version}>{ruleLabels[sel.rules_version] ?? "未知规则"}</span>],
                ["创建", <Timestamp key="basis-time" value={sel.created_at} />],
                ["失效时间", <Timestamp key="expires" value={sel.expires_at} />],
                ["先前结果", sel.basis.previous_applied_plan_id ?? "暂无旧索引"],
                ["本次结果", sel.result_id ?? "暂无结果"],
              ]}
            />
            {(sel.state === "expired" || sel.state === "ready" && sel.validity !== "valid") && <Callout tone="warn" title="计划失效">{sel.reason === "BASIS_CHANGED" || sel.validity === "basis_changed" ? "资料已变" : "预检过期"}。请重做预检。</Callout>}
            {sel.state === "failed" && <Callout tone="bad" title="执行失败">{sel.reason === "WORKER_INTERRUPTED" ? "执行中断。" : "授权已失效。"}请重做预检。</Callout>}
            <Card title="发现的问题">
              <ul className="space-y-2.5">
                {sel.issues.map((i, index) => (
                  <li key={index} className="flex items-start gap-2 text-[13px]">
                    <Badge tone={!i.blocking ? "info" : "muted"}>{i.blocking ? "阻塞" : "本地派生"}</Badge>
                    <div>
                      <div className="font-medium">{maintenanceIssue(i).title}</div>
                      <div className="text-muted">{maintenanceIssue(i).description}</div>
                    </div>
                  </li>
                ))}
              </ul>
            </Card>
            <Card title="影响预览">
              <ul className="space-y-1 text-[13px] text-muted">
                <li>观测 {sel.preview.observations} · 排除 {sel.preview.excluded}{!sel.preview.scan_complete && " · 超限"}</li>
                <li>周期 {sel.preview.periods.length}（+{sel.difference.added} / −{sel.difference.removed} / 改 {sel.difference.changed}）</li>
                {sel.preview.financial && <li>旧估算 {sel.preview.financial.records}（+{sel.difference.financial?.added ?? 0} / −{sel.difference.financial?.removed ?? 0} / 改 {sel.difference.financial?.changed ?? 0}）{!sel.preview.financial.scan_complete && " · 超限"}</li>}
                <li></li>
                {sel.preview.periods.map((period, index) => <li key={index} title={period.start_at + " → " + period.end_at}>{period.kind === "short" ? "短窗" : "长窗"} · {time(period.start_at)} → {time(period.end_at)} · {period.first_used_percent}% → {period.last_used_percent}%</li>)}
                {sel.preview.financial?.results.map(result => <li key={`estimate-${result.estimate_id}`} title={result.generated_at}>估算 #{result.estimate_id} · {estimateStates[result.original_state].label} {estimateAmountText(result.original_remaining)} → {estimateStates[result.state].label} {estimateAmountText(result.remaining)} USD</li>)}
              </ul>
            </Card>
            {(sel.state === "ready" || data.blocked && data.notice?.unknown) && (
              <div className="space-y-3 border-t border-line pt-4">
                <Checkbox checked={applyAck} onChange={setApplyAck} disabled={!canApply} label={sel.preview.financial ? "确认复算。" : "确认重建。"} />
                <Button
                  variant="primary"
                  icon={<Play size={14} />}
                  disabled={busy || !data.blocked && (!canApply || !applyAck)}
                  onClick={() => data.blocked ? void data.load() : void data.apply(sel, applyAck)}
                >
                  {data.blocked ? "只读核对" : data.pending === "apply" ? "提交中…" : "应用计划"}
                </Button>
              </div>
            )}
          </>}
        </div>
      </Drawer>
    </>
  );
}
