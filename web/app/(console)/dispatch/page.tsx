"use client";

import { useState } from "react";
import { PauseCircle, Clock, PlayCircle } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Checkbox, Field, Input, KV, Modal, Mono, Outcome, PageHeader, Select, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { accountName, pauses as initial } from "@/lib/mock";

const STATE: Record<string, { label: string; tone: "warn" | "ok" | "unk" | "muted" }> = {
  active: { label: "暂停中", tone: "warn" },
  restored: { label: "已恢复", tone: "ok" },
  reconcile: { label: "待人工核对", tone: "unk" },
};

export default function DispatchPage() {
  const { toast } = useApp();
  const { isAdmin, accounts, canAccount } = useScope();
  const [rows, setRows] = useState(initial);
  const [newOpen, setNewOpen] = useState(false);
  const [acc, setAcc] = useState(accounts[0]?.id ?? "");
  const [until, setUntil] = useState("2026-11-04T18:00");
  const [ack, setAck] = useState(false);
  const [edit, setEdit] = useState<(typeof initial)[number] | null>(null);
  const [newUntil, setNewUntil] = useState("");

  const visible = rows.filter((r) => canAccount(r.account));

  return (
    <>
      <PageHeader
        title="账号暂停"
        tag="S2A-DIS-001 ~ 004"
        desc="在明确时限内暂停整个上游账号的调度。这与暂停本地监控不同，也不会修改余额、份额或消费。"
        actions={
          <AdminButton variant="primary" icon={<PauseCircle size={14} />} onClick={() => setNewOpen(true)}>
            暂停账号
          </AdminButton>
        }
      />
      <Callout tone="info" className="mb-4">
        单模型临时屏蔽属于能力缺口，不提供此选项。恢复时未发现可靠 CAS，若检测到外部改动将停止并转为人工核对。
      </Callout>
      <Card pad={false}>
        <Table
          rows={visible}
          rowKey={(r) => r.id}
          cols={[
            { key: "id", label: "编号", render: (r) => <Mono>{r.id}</Mono> },
            { key: "a", label: "账号", render: (r) => accountName(r.account) },
            { key: "s", label: "状态", render: (r) => <Badge tone={STATE[r.state].tone} dot>{STATE[r.state].label}</Badge> },
            { key: "f", label: "暂停起", render: (r) => <span className="num">{r.from}</span> },
            { key: "u", label: "计划恢复", render: (r) => <span className="num">{r.until}</span> },
            { key: "p", label: "暂停前状态", render: (r) => r.prev },
            { key: "r", label: "上游确认", render: (r) => <Outcome k={r.result} /> },
            ...(isAdmin ? [{ key: "by", label: "操作者", render: (r: (typeof initial)[number]) => r.by }] : []),
            { key: "n", label: "说明", render: (r) => <span className="text-[12px] text-muted">{r.note}</span> },
            {
              key: "x",
              label: "",
              render: (r) =>
                isAdmin &&
                r.state === "active" && (
                  <Button
                    size="sm"
                    icon={<Clock size={12} />}
                    onClick={() => {
                      setEdit(r);
                      setNewUntil(r.until.replace(" ", "T"));
                    }}
                  >
                    调整
                  </Button>
                ),
            },
          ]}
        />
      </Card>

      <Modal
        open={newOpen}
        onClose={() => setNewOpen(false)}
        title="临时暂停上游账号调度"
        footer={
          <>
            <Button onClick={() => setNewOpen(false)}>取消</Button>
            <Button
              variant="primary"
              disabled={!ack}
              onClick={() => {
                setRows((rs) => [
                  { id: `ds-${42 + rs.length}`, account: acc, state: "active", from: "2026-11-04 14:22", until: until.replace("T", " "), prev: "可调度", by: "admin", result: "confirmed", note: "" },
                  ...rs,
                ]);
                setNewOpen(false);
                setAck(false);
                toast({ tone: "ok", title: "上游已确认暂停", body: `${accountName(acc)} 将于 ${until.replace("T", " ")} 自动恢复。` });
              }}
            >
              暂停
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="账号">
            <Select value={acc} onChange={(e) => setAcc(e.target.value)} options={accounts.map((a) => ({ value: a.id, label: a.localName }))} />
          </Field>
          <Field label="恢复时间" hint="到期由后台自动恢复，不依赖浏览器保持打开。">
            <Input type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value)} />
          </Field>
          <Callout tone="warn">暂停影响该账号的全部实际使用者，且无法撤销已在途的请求。</Callout>
          <Checkbox checked={ack} onChange={setAck} label="我了解暂停作用于整个上游账号。" />
        </div>
      </Modal>

      <Modal
        open={!!edit}
        onClose={() => setEdit(null)}
        title={`调整暂停 ${edit?.id ?? ""}`}
        footer={
          <>
            <Button onClick={() => setEdit(null)}>取消</Button>
            <Button
              icon={<PlayCircle size={14} />}
              onClick={() => {
                setRows((rs) => rs.map((r) => (r.id === edit?.id ? { ...r, state: "restored", until: "2026-11-04 14:22", note: "提前恢复，读回一致" } : r)));
                setEdit(null);
                toast({ tone: "ok", title: "已提前恢复", body: "恢复为暂停前状态：可调度。" });
              }}
            >
              立即恢复
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                setRows((rs) => rs.map((r) => (r.id === edit?.id ? { ...r, until: newUntil.replace("T", " ") } : r)));
                setEdit(null);
                toast({ tone: "ok", title: "恢复时间已更新" });
              }}
            >
              保存新时间
            </Button>
          </>
        }
      >
        {edit && (
          <div className="space-y-4">
            <KV items={[["账号", accountName(edit.account)], ["暂停前状态", edit.prev]]} />
            <Field label="新的恢复时间">
              <Input type="datetime-local" value={newUntil} onChange={(e) => setNewUntil(e.target.value)} />
            </Field>
            <Callout tone="info">恢复前会读取当前调度状态；若与暂停时记录不一致，将停止并转为待人工核对。</Callout>
          </div>
        )}
      </Modal>
    </>
  );
}
