"use client";

import { useState } from "react";
import { ScanSearch, Play } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, Drawer, Field, Input, KV, Modal, Mono, PageHeader, Select, Table } from "@/components/ui";
import { useApp } from "@/lib/store";
import { accounts, maintIssues, maintPlans as initial } from "@/lib/mock";

type Plan = (typeof initial)[number];

const STATUS: Record<string, { label: string; tone: "ok" | "info" | "muted" | "bad" | "warn" }> = {
  ready: { label: "可应用", tone: "info" },
  expired: { label: "已失效", tone: "muted" },
  applied: { label: "已应用", tone: "ok" },
  failed: { label: "失败（已回滚）", tone: "bad" },
  running: { label: "执行中", tone: "warn" },
};

export default function MaintenancePage() {
  const { toast } = useApp();
  const [plans, setPlans] = useState<Plan[]>(initial);
  const [sel, setSel] = useState<Plan | null>(null);
  const [newOpen, setNewOpen] = useState(false);
  const [checking, setChecking] = useState(false);
  const [kind, setKind] = useState("rebuild");
  const [acc, setAcc] = useState("a3");
  const [applyAck, setApplyAck] = useState(false);

  const precheck = () => {
    setChecking(true);
    setTimeout(() => {
      setChecking(false);
      setNewOpen(false);
      const p: Plan = {
        id: `MP-0${plans.length + 2}`,
        created: "2026-11-04 14:23",
        scope: `${accounts.find((a) => a.id === acc)?.localName} 10-01 → 11-03`,
        kind: kind === "rebuild" ? "派生结果重建" : kind === "cycles" ? "周期分段检查" : "规则升级重算",
        status: "ready",
        issues: 2,
        blockers: 0,
        basisAt: "2026-11-04 14:23",
        rules: kind === "upgrade" ? "估算规则 v2 → v3" : "估算规则 v2",
      };
      setPlans((ps) => [p, ...ps]);
      setSel(p);
      toast({ tone: "info", title: "预检完成，已生成维护计划", body: "预检只读取本系统已保存的事实，不访问上游。" });
    }, 1200);
  };

  return (
    <>
      <PageHeader
        title="历史维护"
        tag="S2A-MAINT-001 ~ 005"
        desc="对本系统保存的历史事实进行预检，并在确认后重建本地派生结果。维护不会改写原始事实、Sub2API 账单或钱包，也不会发送历史通知。"
        actions={
          <Button variant="primary" icon={<ScanSearch size={14} />} onClick={() => setNewOpen(true)}>
            新建预检
          </Button>
        }
      />
      <Card pad={false}>
        <Table
          rows={plans}
          rowKey={(p) => p.id}
          onRow={(p) => {
            setSel(p);
            setApplyAck(false);
          }}
          cols={[
            { key: "id", label: "计划", render: (p) => <Mono>{p.id}</Mono> },
            { key: "k", label: "类型", render: (p) => p.kind },
            { key: "s", label: "范围", render: (p) => p.scope },
            { key: "r", label: "规则版本", render: (p) => <span className="text-muted">{p.rules}</span> },
            { key: "i", label: "问题 / 阻塞", render: (p) => <span className="num">{p.issues} / <span className={p.blockers ? "text-bad" : ""}>{p.blockers}</span></span> },
            { key: "b", label: "依据时间", render: (p) => <span className="num text-muted">{p.basisAt}</span> },
            { key: "st", label: "状态", render: (p) => <Badge tone={STATUS[p.status].tone} dot>{STATUS[p.status].label}</Badge> },
          ]}
        />
      </Card>

      <Modal
        open={newOpen}
        onClose={() => setNewOpen(false)}
        title="新建预检"
        footer={
          <>
            <Button onClick={() => setNewOpen(false)}>取消</Button>
            <Button variant="primary" disabled={checking} onClick={precheck}>
              {checking ? "预检中…" : "开始预检"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field label="维护类型">
            <Select
              value={kind}
              onChange={(e) => setKind(e.target.value)}
              options={[
                { value: "rebuild", label: "派生结果重建" },
                { value: "cycles", label: "检查与纠正周期分段" },
                { value: "upgrade", label: "规则升级后重新测算" },
              ]}
            />
          </Field>
          <Field label="账号">
            <Select value={acc} onChange={(e) => setAcc(e.target.value)} options={accounts.map((a) => ({ value: a.id, label: a.localName }))} />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="开始日期">
              <Input type="date" defaultValue="2026-10-01" />
            </Field>
            <Field label="结束日期">
              <Input type="date" defaultValue="2026-11-03" />
            </Field>
          </div>
        </div>
      </Modal>

      <Drawer open={!!sel} onClose={() => setSel(null)} title={sel ? `维护计划 ${sel.id}` : ""}>
        {sel && (
          <div className="space-y-4">
            <KV
              items={[
                ["类型", sel.kind],
                ["状态", <Badge key="s" tone={STATUS[sel.status].tone}>{STATUS[sel.status].label}</Badge>],
                ["范围", sel.scope],
                ["规则版本", sel.rules],
                ["依据时间", sel.basisAt],
                ["创建", sel.created],
              ]}
            />
            {sel.status === "expired" && <Callout tone="warn" title="计划已失效">{"expiredReason" in sel ? sel.expiredReason : ""}。请重新预检，旧计划不能直接用于新数据。</Callout>}
            {sel.status === "failed" && <Callout tone="bad" title="执行失败，已完整回滚">第 2 阶段写入派生结果时发生冲突。当前数据保持维护前状态，没有留下部分结果。</Callout>}
            <Card title="发现的问题">
              <ul className="space-y-2.5">
                {maintIssues.map((i) => (
                  <li key={i.desc} className="flex items-start gap-2 text-[13px]">
                    <Badge tone={i.fixable ? "info" : "muted"}>{i.fixable ? "可修正" : "保持原状"}</Badge>
                    <div>
                      <div className="font-medium">{i.kind}</div>
                      <div className="text-muted">{i.desc}</div>
                    </div>
                  </li>
                ))}
              </ul>
            </Card>
            <Card title="影响预览">
              <ul className="space-y-1 text-[13px] text-muted">
                <li>· 重建 33 天的估算记录（新结果与旧结果并存，标记为重算）</li>
                <li>· 修正 1 处本地周期分段</li>
                <li>· 不修改：原始观测、上游账单、钱包、已执行余额</li>
                <li>· 不发送：任何历史业务通知</li>
              </ul>
            </Card>
            {sel.status === "ready" && (
              <div className="space-y-3 border-t border-line pt-4">
                <Checkbox checked={applyAck} onChange={setApplyAck} label="我已审阅影响范围。重算结果不代表过去已执行的金额，影响远端须另走应用流程。" />
                <Button
                  variant="primary"
                  icon={<Play size={14} />}
                  disabled={!applyAck}
                  onClick={() => {
                    setPlans((ps) => ps.map((p) => (p.id === sel.id ? { ...p, status: "applied" } : p)));
                    setSel(null);
                    toast({ tone: "ok", title: `${sel.id} 已应用`, body: "派生结果已重建。" });
                  }}
                >
                  应用计划
                </Button>
              </div>
            )}
          </div>
        )}
      </Drawer>
    </>
  );
}
