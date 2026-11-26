"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { useState } from "react";
import type { DispatchPause } from "@sub-pool/contracts/dispatch";
import { PauseCircle, Clock, Lock } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, Field, Input, Modal, Mono, Outcome, PageHeader, Select, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { UpstreamWriteConditions } from "@/components/upstream-write-conditions";
import { dispatchObservationText } from "@/lib/dispatch-data";
import { useDispatchData } from "@/lib/use-dispatch-data";
import { dispatchDeadlineInput, dispatchReasonText, dispatchStatusText, dispatchTimeInput } from "@/lib/dispatch-control-data";

const STATE: Record<string, { label: string; tone: "warn" | "ok" | "unk" | "muted" }> = {
  pausing: { label: "暂停待确认", tone: "unk" },
  paused: { label: "暂停中", tone: "warn" },
  restoring: { label: "恢复待确认", tone: "unk" },
  restored: { label: "已恢复", tone: "ok" },
  needs_review: { label: uiCopy.pendingReview, tone: "unk" },
  reviewed: { label: "已核对", tone: "muted" },
  failed: { label: "未执行暂停", tone: "muted" },
};
type HistoryRow = { id: string; account: string; state: string; prev: string; result: Parameters<typeof Outcome>[0]["k"]; by: string; note: string; pause: DispatchPause; current: boolean };
type Dialog = { epoch: number; open: boolean; account: string; ack: boolean; until: string; pause: DispatchPause | null; action: "reschedule" | "restore" | "review" };
const actions: Record<string, string> = { pause: "登记暂停", pause_result: "暂停结果", reschedule: "修改期限", restore: "登记恢复", restore_result: "恢复结果", review: "核对", interrupted: "进程中断", retry: "等待读取" };

export default function DispatchPage() {
  const { auth, isAdmin, displayTimezone } = useApp();
  const [dialog, setDialog] = useState<Dialog>({ epoch: -1, open: false, account: "", ack: false, until: "", pause: null, action: "reschedule" });
  const open = dialog.epoch === auth.epoch && dialog.open;
  const data = useDispatchData(open ? dialog.account : null);
  const control = data.control;
  const close = () => setDialog({ epoch: auth.epoch, open: false, account: "", ack: false, until: "", pause: null, action: "reschedule" });
  const latest = new Map<string, string>();
  for (const event of control.events) latest.set(event.pause.id, event.pause.revision);
  const history: HistoryRow[] = control.events.map(event => ({ id: event.id, account: event.pause.account_id, state: event.pause.state,
    prev: event.pause.previous_schedulable ? "允许调度" : "已停止调度", by: event.actor === "worker" ? "后台任务" : "管理员",
    result: event.pause.state === "needs_review" ? "reconcile" : ["pausing", "restoring"].includes(event.pause.state) ? "pending"
      : ["VERIFIED_PAUSE", "VERIFIED_RESTORE"].includes(event.pause.reason) ? "confirmed" : "skipped",
    note: `${actions[event.action]}：${dispatchReasonText(event.pause)}`, pause: event.pause, current: latest.get(event.pause.id) === event.pause.revision }));
  const historyText = control.error ?? data.error ?? (!control.ready ? "读取暂停记录…" : "暂无暂停。");
  const nativeText = data.error ?? (!data.ready ? uiCopy.loading : !data.selected ? "暂无账号。" : !data.selected.source_current ? "账号连接已变。" : data.loading ? "读取账号配置…" : dispatchObservationText(data.observation));
  const restoreAt = dispatchDeadlineInput(dialog.until);
  const canSubmit = isAdmin && !control.pending && control.ready && dialog.ack && (dialog.pause
    && latest.get(dialog.pause.id) !== dialog.pause.revision ? false : dialog.pause
    ? dialog.action === "review" ? dialog.pause.state === "needs_review"
      : dialog.pause.state === "paused" && (dialog.action === "reschedule" ? restoreAt !== null : !!control.control?.can_restore)
    : !!control.control?.can_pause && restoreAt !== null && !!data.selected?.source_current && data.observation?.read.state === "available"
      && control.control.source_id === data.selected.source_id && control.control.connection_revision === data.observation.connection_revision);
  const submit = async () => {
    if (!canSubmit) return;
    let result: DispatchPause | null = null;
    if (dialog.pause) {
      const input = { id: dialog.pause.id, revision: dialog.pause.revision };
      if (dialog.action === "review") result = await control.review({ ...input, acknowledge_result_unknown: true });
      else if (dialog.action === "restore") result = await control.restore(input);
      else if (restoreAt) result = await control.reschedule({ ...input, restore_at: restoreAt });
    } else if (data.observation && restoreAt) {
      const requestID = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16)))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
      result = await control.pause({ id: data.observation.id, revision: data.observation.revision, source_id: data.observation.source_id,
        connection_revision: data.observation.connection_revision, request_id: requestID, restore_at: restoreAt, acknowledge_account_wide: true });
    }
    data.reload();
    if (result) close();
  };

  return (
    <>
      <PageHeader
        title={uiCopy.dispatch}
        helpTopic="connection"
        actions={
          <Button variant="primary" icon={isAdmin ? <PauseCircle size={14} /> : <Lock size={13} />} disabled={!data.allowed}
            onClick={() => { data.reload(); setDialog({ epoch: auth.epoch, open: true, account: "", ack: false, until: dispatchTimeInput(new Date(Date.now() + 3600_000)), pause: null, action: "reschedule" }); }}>
            查看调度
          </Button>
        }
      />
      <UpstreamWriteConditions capability="scheduling" status={
        control.message || control.error || (control.control?.can_pause ? "暂停已授权。"
          : control.control ? dispatchStatusText(control.control, []) : "读取状态…")
      } />
      <Card pad={false}>
        <Table
          rows={history}
          rowKey={(r) => r.id}
          empty={historyText}
          cols={[
            { key: "id", label: "编号", render: (r) => <Mono>{r.id}</Mono> },
            { key: "a", label: uiCopy.account, render: (r) => data.accounts.find(account => account.id === r.account)?.name ?? "账号不可确认" },
            { key: "s", label: uiCopy.status, render: (r) => <Badge tone={STATE[r.state]?.tone ?? "unk"} dot>{STATE[r.state]?.label ?? "未知"}</Badge> },
            { key: "f", label: "暂停起", render: (r) => <Timestamp short className="num" value={r.pause.created_at} /> },
            { key: "u", label: "计划恢复", render: (r) => <Timestamp short className="num" value={r.pause.restore_at} /> },
            { key: "p", label: "暂停前状态", render: (r) => r.prev },
            { key: "r", label: `${glossary.sub2api} 确认`, render: (r) => <Outcome k={r.result} /> },
            ...(isAdmin ? [{ key: "by", label: "操作者", render: (r: HistoryRow) => r.by }] : []),
            { key: "n", label: "说明", render: (r) => <span className="text-[12px] text-muted">{r.note}</span> },
            { key: "x", label: "", render: (r) => isAdmin && r.current && ["paused", "needs_review"].includes(r.state) && <Button size="sm" icon={<Clock size={12} />} disabled={control.pending}
              onClick={() => setDialog({ epoch: auth.epoch, open: true, account: r.account, ack: false, until: dispatchTimeInput(r.pause.restore_at), pause: r.pause, action: r.state === "needs_review" ? "review" : "reschedule" })}>{r.state === "needs_review" ? "核对" : "调整"}</Button> },
          ]}
        />
      </Card>

      <Modal
        open={open}
        onClose={close}
        title="暂停条件"
        footer={
          <>
            <Button onClick={close}>取消</Button>
            <Button variant="primary" disabled={!canSubmit} onClick={() => void submit()} title={!control.control?.can_pause ? "暂停未开放。" : undefined}>
              {control.pending ? "确认中…" : !dialog.pause ? "暂停" : dialog.action === "review" ? "确认人工核对" : dialog.action === "restore" ? "提前恢复" : "保存期限"}
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label={dialog.pause ? `操作（${data.accounts.find(account => account.id === dialog.account)?.name ?? "原账号"}）` : uiCopy.account}>
            <Select aria-label={dialog.pause ? "暂停记录操作" : uiCopy.account} value={dialog.pause ? dialog.action : data.selected?.id ?? ""} disabled={control.pending || !data.ready || !data.accounts.length}
              onChange={(e) => { const selected = e.target.value; setDialog(value => value.pause && (selected === "reschedule" || selected === "restore" || selected === "review")
                ? { ...value, action: selected, ack: false } : { ...value, account: selected, ack: false }); }}
              options={dialog.pause ? dialog.pause.state === "needs_review" ? [{ value: "review", label: "结束跟踪" }] : [{ value: "reschedule", label: "修改恢复时间" }, { value: "restore", label: "恢复先前状态" }]
                : data.accounts.length ? data.accounts.map(account => ({ value: account.id, label: account.name + (account.source_current ? "" : "（来源已变）") })) : [{ value: "", label: data.ready ? "没有授权账号" : "读取不全" }]} />
          </Field>
          <Field label="恢复时间（本机）" hint="最长 30 天。">
            <Input aria-label="恢复时间" type="datetime-local" value={dialog.until} disabled={control.pending || !isAdmin || !!dialog.pause && dialog.action !== "reschedule"}
              onChange={event => setDialog(value => ({ ...value, until: event.target.value, ack: false }))} />
          </Field>
          <Callout tone="warn">
            {control.message || nativeText}
            <div>{dialog.action === "review" && dialog.pause ? `请先在 ${glossary.sub2api} 核对结果。此处仅结束跟踪。`
              : dialog.pause ? "更改对整个账号生效。" : "暂停整个账号。已在途的请求继续执行。"}</div>
          </Callout>
          <Checkbox checked={dialog.ack} onChange={ack => setDialog(value => ({ ...value, ack }))} label={dialog.action === "review" && dialog.pause ? "确认结束跟踪" : "确认影响范围"} />
        </div>
      </Modal>
    </>
  );
}
