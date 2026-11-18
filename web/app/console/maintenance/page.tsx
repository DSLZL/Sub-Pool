"use client";

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
  [maintenanceRulesVersion]: "按上游配额窗口重建周期索引",
  [maintenanceHistoryRulesVersion]: "按保留记录复核历史估算",
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
  const scope = (plan: MaintenancePlan) => <span title={plan.scope.from + " → " + plan.scope.until}>
    {data.accounts?.find(account => account.id === plan.scope.account_id)?.name ?? plan.scope.account_id} {time(plan.scope.from)} → {time(plan.scope.until)}
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
    } catch (error) { if (generation === form.current) setFormError(error instanceof Error ? error.message : "预检输入尚未核对。"); }
  }
  if (!data.allowed) return <Empty title="需要当前管理员权限">维护计划不能通过本地角色或账号筛选获得授权。</Empty>;

  return (
    <>
      <PageHeader
        title="历史维护"
        tag="S2A-MAINT-001 ~ 005"
        desc="对本系统保留的事实与历史估算进行离线预检，确认后保存新的本地周期或条件复算版本。旧结果保留，不改原始事实、Sub2API 账单或钱包，不发送历史通知；复算值不是过去已执行金额。"
        actions={
          <Button variant="primary" icon={<ScanSearch size={14} />} disabled={busy} onClick={() => readMode ? void data.load() : openNew()}>
            {data.pending === "read" ? "读取中…" : readMode ? "重新读取" : "新建预检"}
          </Button>
        }
      />
      {data.error && <Callout tone="bad" title="读取未完成">{data.error}。尚未取得完整维护列表，请重新读取。</Callout>}
      {data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"} title={data.notice.unknown ? "结果尚未确认" : "维护状态"}>{data.notice.text}</Callout>}
      <Card pad={false}>
        <Table
          rows={data.plans ?? []}
          rowKey={(p) => p.id}
          empty={data.pending === "read" ? "正在读取维护计划…" : !data.ready ? "维护记录未知，读取成功后显示完整列表。" : data.accounts?.length === 0 ? "尚无已纳管账号；请先在连接与采集页核对账号。" : "暂无维护计划"}
          onRow={(p) => { setApplyAck(false); void data.open(p.id); }}
          cols={[
            { key: "id", label: "计划", render: (p) => <Mono>{p.id}</Mono> },
            { key: "k", label: "类型", render: (p) => maintenanceKinds[p.kind] },
            { key: "s", label: "范围", render: scope },
            { key: "r", label: "维护依据", render: (p) => <span className="text-muted" title={p.rules_version}>{ruleLabels[p.rules_version] ?? "未知规则"}</span> },
            { key: "i", label: "问题 / 阻塞", render: (p) => <span className="num">{p.issues.length} / <span className={p.issues.some(issue => issue.blocking) ? "text-bad" : ""}>{p.issues.filter(issue => issue.blocking).length}</span></span> },
            { key: "b", label: "依据时间", render: (p) => <time className="num text-muted" dateTime={p.created_at} title={p.created_at}>{time(p.created_at)}</time> },
            { key: "st", label: "状态", render: (p) => <Badge tone={maintenanceStatus(p).tone} dot>{maintenanceStatus(p).label}</Badge> },
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
          {data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"} title={data.notice.unknown ? "结果尚未确认" : "预检未完成"}>{data.notice.text}</Callout>}
          <Field label="维护类型" hint={kind === "cycles" ? "按上游报告的配额窗口纠正本地周期索引。" : "按生成时间筛选保留的估算，复核所引用的观测与完整模拟费用证明。"}>
            <Select
              value={kind}
              disabled={busy}
              onChange={(e) => { const value = e.target.value; if (value === "rebuild" || value === "cycles" || value === "upgrade") setKind(value); }}
              options={[
                { value: "rebuild", label: "派生结果重建" },
                { value: "cycles", label: "检查与纠正周期分段" },
                { value: "upgrade", label: "规则升级后重新测算" },
              ]}
            />
          </Field>
          <Field label="账号">
            <Select value={acc} disabled={busy} onChange={(e) => setAcc(e.target.value)} options={[{ value: "", label: "请选择账号" }, ...(data.accounts ?? []).map((a) => ({ value: a.id, label: a.name }))]} />
          </Field>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <DateTimeField label="开始时间（含）" value={from} disabled={busy} onChange={setFrom} />
            <DateTimeField label="结束时间（不含）" value={until} disabled={busy} onChange={setUntil} />
          </div>
        </div>
      </Modal>

      <Drawer open={data.selectionID !== null} onClose={() => { data.close(); setApplyAck(false); }} title={data.selectionID ? `维护计划 ${data.selectionID}` : ""}>
        <div className="space-y-4">
          {data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"} title={data.notice.unknown ? "结果尚未确认" : "维护状态"}>{data.notice.text}</Callout>}
          {!sel && <Callout tone="info" title={busy ? "正在读取预检依据" : "预检依据未知"}>只有当前计划的实际读取成功后才能确认应用；可关闭查看后重新读取。</Callout>}
          {sel && <>
            <KV
              items={[
                ["类型", maintenanceKinds[sel.kind]],
                ["状态", <Badge key="s" tone={maintenanceStatus(sel).tone}>{maintenanceStatus(sel).label}</Badge>],
                ["范围", scope(sel)],
                ["维护依据", <span key="rule" title={sel.rules_version}>{ruleLabels[sel.rules_version] ?? "未知规则"}</span>],
                ["依据时间", <time key="basis-time" dateTime={sel.created_at} title={sel.created_at}>{time(sel.created_at)}</time>],
                ["创建", time(sel.created_at)],
                ["失效时间", <time key="expires" dateTime={sel.expires_at} title={sel.expires_at}>{time(sel.expires_at)}</time>],
                ["依据指纹", <span key="basis" title={sel.basis.fingerprint}>{sel.basis.fingerprint.slice(0, 16)}…</span>],
                ["先前结果", sel.basis.previous_applied_plan_id ?? "无先前已应用索引"],
                ["本次结果", sel.result_id ?? "尚无已应用结果"],
              ]}
            />
            {(sel.state === "expired" || sel.state === "ready" && sel.validity !== "valid") && <Callout tone="warn" title="计划已失效">{sel.reason === "BASIS_CHANGED" || sel.validity === "basis_changed" ? "原始事实、配置或派生依据已变化" : "预检有效期已过"}。请重新预检，旧计划不能直接用于新数据。</Callout>}
            {sel.state === "failed" && <Callout tone="bad" title="后台执行失败">{sel.reason === "WORKER_INTERRUPTED" ? "执行在服务切换前中断，后台未重放任务。" : "受理人的当前会话或管理权限不可用。"}该计划未登记已应用结果，原始事实保留；请核对当前记录后重新预检。</Callout>}
            {(sel.state === "queued" || sel.state === "running") && <Callout tone="info" title="后台已受理">当前为{maintenanceStatus(sel).label}，尚未登记已应用结果。页面会查询真实状态；关闭查看不会取消任务。</Callout>}
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
                <li>· 扫描 {sel.preview.observations} 条已收到观测，排除 {sel.preview.excluded} 条；{sel.preview.scan_complete ? "扫描完整" : "扫描已截断，不能应用"}。收到记录不代表完整周期覆盖。{sel.preview.financial && "包含历史估算显式引用的范围外保留观测。"}</li>
                <li>· 原生周期索引 {sel.preview.periods.length} 段；与先前结果相比新增 {sel.difference.added}、移除 {sel.difference.removed}、变化 {sel.difference.changed} 段。旧结果保留。{sel.preview.financial && `历史估算 ${sel.preview.financial.records} 条；${sel.preview.financial.scan_complete ? "扫描完整" : "扫描已截断"}；复算版本新增 ${sel.difference.financial?.added ?? 0}、移除 ${sel.difference.financial?.removed ?? 0}、变化 ${sel.difference.financial?.changed ?? 0} 条；计算规则 ${sel.preview.financial.calculation_rules}。`}</li>
                <li>· 不修改：原始观测、原估算、上游账单、钱包、已执行余额。{sel.preview.financial ? "新金额仅为依赖原声明前提的模拟条件值，未校准；不代表过去执行金额或当前钱包目标。" : "本计划仅处理周期索引，不重算历史金额。"}</li>
                <li>· 不发送：任何历史业务通知</li>
                {sel.preview.periods.map((period, index) => <li key={index} title={period.start_at + " → " + period.end_at}>· {period.kind === "short" ? "原生短窗" : "原生长窗"}：{time(period.start_at)} → {time(period.end_at)}，原生时长 {period.duration_seconds} 秒；来源样本 {period.first_used_percent}% → {period.last_used_percent}%（{period.observation_ids.length} 条观测）。</li>)}
                {sel.preview.financial?.results.map(result => <li key={`estimate-${result.estimate_id}`} title={result.generated_at}>· 估算 {result.estimate_id}（{time(result.generated_at)}）：原 {result.original_rules} / {estimateStates[result.original_state].label} / {estimateAmountText(result.original_remaining)} USD → 条件复算 {estimateStates[result.state].label} / {estimateAmountText(result.remaining)} USD。{result.windows.map(window => `${window.kind === "short" ? "短窗" : "长窗"} ${estimateAmountText(window.remaining)} USD`).join("；")}</li>)}
              </ul>
            </Card>
            {(sel.state === "ready" || data.blocked && data.notice?.unknown) && (
              <div className="space-y-3 border-t border-line pt-4">
                <Checkbox checked={applyAck} onChange={setApplyAck} disabled={!canApply} label={sel.preview.financial ? "我已审阅此账号、UTC 范围、原始证明与旧新结果。仅保存本地条件复算版本，不代表过去已执行金额，不执行远端操作。" : "我已审阅此账号、UTC 范围与依据。仅重建本地周期索引，不重算历史金额，不执行远端操作。"} />
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
