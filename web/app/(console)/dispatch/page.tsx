"use client";

import { useState } from "react";
import { PauseCircle, Clock, Lock } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, Field, Input, Modal, Mono, Outcome, PageHeader, Select, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { dispatchObservationText } from "@/lib/dispatch-data";
import { useDispatchData } from "@/lib/use-dispatch-data";

const STATE: Record<string, { label: string; tone: "warn" | "ok" | "unk" | "muted" }> = {
  active: { label: "暂停中", tone: "warn" },
  restored: { label: "已恢复", tone: "ok" },
  reconcile: { label: "待人工核对", tone: "unk" },
};
type HistoryRow = { id: string; account: string; state: string; from: string; until: string; prev: string; result: Parameters<typeof Outcome>[0]["k"]; by: string; note: string };
// shortcut: Pause history has no qualified backend read; retain the original empty table until records exist.
const history: HistoryRow[] = [];

export default function DispatchPage() {
  const { auth, isAdmin, timezone } = useApp();
  const [dialog, setDialog] = useState({ epoch: -1, open: false, account: "", ack: false });
  const open = dialog.epoch === auth.epoch && dialog.open;
  const data = useDispatchData(open ? dialog.account : null);
  const close = () => setDialog({ epoch: auth.epoch, open: false, account: "", ack: false });
  const historyText = data.error ?? (!data.ready ? "正在读取可见账号与功能可用性；暂停历史尚未开放。" : data.accounts.length ? "暂停与恢复的操作历史尚未开放；此表不代表已核验历史为空。" : "当前授权范围没有纳管账号；暂停历史功能尚未开放。");
  const nativeText = data.error ?? (!data.ready ? "正在完整读取当前授权账号…" : !data.selected ? "当前没有可读取的授权账号。" : !data.selected.source_current ? "账号来源已变化，本次未读取原生配置。" : data.loading ? "正在读取所选账号的原生调度配置…" : dispatchObservationText(data.observation));

  return (
    <>
      <PageHeader
        title="账号暂停"
        tag="S2A-DIS-001 ~ 004"
        desc="原生调度配置与本系统临时暂停分别判断。整账号暂停、恢复与操作历史当前尚未开放。"
        actions={
          <Button variant="primary" icon={isAdmin ? <PauseCircle size={14} /> : <Lock size={13} />} disabled={!data.allowed}
            onClick={() => { data.reload(); setDialog({ epoch: auth.epoch, open: true, account: "", ack: false }); }}>
            查看调度
          </Button>
        }
      />
      <Callout tone="info" className="mb-4">
        原生调度开关不是本系统持有的暂停，也不代表实际可调度。尚未核验独立恢复控制，暂停、修改期限与自动恢复保持关闭；不提供单模型临时屏蔽。
      </Callout>
      <Card pad={false}>
        <Table
          rows={history}
          rowKey={(r) => r.id}
          empty={historyText}
          cols={[
            { key: "id", label: "编号", render: (r) => <Mono>{r.id}</Mono> },
            { key: "a", label: "账号", render: (r) => data.accounts.find(account => account.id === r.account)?.name ?? "账号不可确认" },
            { key: "s", label: "状态", render: (r) => <Badge tone={STATE[r.state]?.tone ?? "unk"} dot>{STATE[r.state]?.label ?? "未知"}</Badge> },
            { key: "f", label: "暂停起", render: (r) => <span className="num">{r.from}</span> },
            { key: "u", label: "计划恢复", render: (r) => <span className="num">{r.until}</span> },
            { key: "p", label: "暂停前状态", render: (r) => r.prev },
            { key: "r", label: "上游确认", render: (r) => <Outcome k={r.result} /> },
            ...(isAdmin ? [{ key: "by", label: "操作者", render: (r: HistoryRow) => r.by }] : []),
            { key: "n", label: "说明", render: (r) => <span className="text-[12px] text-muted">{r.note}</span> },
            { key: "x", label: "", render: (r) => isAdmin && r.state === "active" && <Button size="sm" icon={<Clock size={12} />} disabled>调整</Button> },
          ]}
        />
      </Card>

      <Modal
        open={open}
        onClose={close}
        title="原生调度与暂停条件"
        footer={
          <>
            <Button onClick={close}>取消</Button>
            <Button variant="primary" disabled title="独立恢复控制未通过核验，暂停操作尚未开放。">暂停</Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="账号">
            <Select aria-label="账号" value={data.selected?.id ?? ""} disabled={!data.ready || !data.accounts.length}
              onChange={(e) => setDialog(value => ({ ...value, account: e.target.value, ack: false }))}
              options={data.accounts.length ? data.accounts.map(account => ({ value: account.id, label: account.name + (account.source_current ? "" : "（来源已变化）") })) : [{ value: "", label: data.ready ? "没有授权账号" : "尚未完整读取" }]} />
          </Field>
          <Field label="恢复时间" hint="暂停与自动恢复尚未开放，当前不能保存恢复时间。">
            <Input type="datetime-local" value="" disabled />
          </Field>
          <Callout tone="warn">{nativeText}{data.observation && <>；读取时间：{displayTime(data.observation.checked_at, timezone)}</>}。原生配置不等于本系统临时暂停；整账号暂停影响全部实际使用者，且无法撤销已在途请求。</Callout>
          <Checkbox checked={dialog.ack} onChange={ack => setDialog(value => ({ ...value, ack }))} label="我了解暂停作用于整个上游账号；确认不会开启尚未开放的操作。" />
        </div>
      </Modal>
    </>
  );
}
