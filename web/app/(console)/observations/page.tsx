"use client";

import { useState } from "react";
import { Ban, RotateCcw, Flag } from "lucide-react";
import {
  AdminButton,
  Badge,
  Button,
  Callout,
  Card,
  Drawer,
  Field,
  KV,
  Modal,
  Mono,
  PageHeader,
  Select,
  Table,
  Textarea,
  Toolbar,
  Val,
} from "@/components/ui";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { accountName, observations as initial, pools, participantName, type Observation } from "@/lib/mock";

const STATE = {
  valid: { label: "有效", tone: "ok" as const },
  excluded: { label: "已排除", tone: "muted" as const },
  anchor: { label: "人工起点", tone: "info" as const },
};
const QUALITY = {
  good: { label: "完整", tone: "ok" as const },
  partial: { label: "部分缺失", tone: "unk" as const },
  stale: { label: "源已过期", tone: "warn" as const },
};

export default function ObservationsPage() {
  const { toast } = useApp();
  const { isAdmin, canAccount, canParticipant } = useScope();
  const [rows, setRows] = useState<Observation[]>(initial);
  const [fAcc, setFAcc] = useState("all");
  const [fState, setFState] = useState("all");
  const [fSrc, setFSrc] = useState("all");
  const [sel, setSel] = useState<Observation | null>(null);
  const [action, setAction] = useState<null | { kind: "exclude" | "restore" | "anchor" | "unanchor"; ob: Observation }>(null);
  const [reason, setReason] = useState("");

  const visible = rows.filter(
    (o) => canAccount(o.account) && (fAcc === "all" || o.account === fAcc) && (fState === "all" || o.state === fState) && (fSrc === "all" || o.source === fSrc),
  );

  const apply = () => {
    if (!action) return;
    const { kind, ob } = action;
    const next: Observation["state"] = kind === "exclude" ? "excluded" : kind === "anchor" ? "anchor" : "valid";
    setRows((rs) => rs.map((r) => (r.id === ob.id ? { ...r, state: next, excludeReason: kind === "exclude" ? reason : undefined } : r)));
    setSel(null);
    setAction(null);
    setReason("");
    toast({
      tone: "info",
      title: { exclude: "已排除观测", restore: "已恢复观测", anchor: "已设为人工测算起点", unanchor: "已撤销人工起点" }[kind],
      body: "原始值未删除。受影响的派生结果已加入重算队列，不会自动写入钱包或发送通知。",
    });
  };

  const pool = sel ? pools.find((p) => p.accounts.includes(sel.account)) : null;

  return (
    <>
      <PageHeader title="观测记录" tag="S2A-OBS-001 ~ 005" desc="每条观测是一次对上游额度窗口与费用的采样。排除或恢复只影响本地派生计算，不改动原始值或上游账单。" />
      <Card pad={false}>
        <div className="p-4 pb-0">
          <Toolbar>
            <Field label="账号" className="w-40">
              <Select value={fAcc} onChange={(e) => setFAcc(e.target.value)} options={[{ value: "all", label: "全部" }, ...["a1", "a2", "a3", "a4"].filter(canAccount).map((a) => ({ value: a, label: accountName(a) }))]} />
            </Field>
            <Field label="有效状态" className="w-32">
              <Select value={fState} onChange={(e) => setFState(e.target.value)} options={[{ value: "all", label: "全部" }, { value: "valid", label: "有效" }, { value: "excluded", label: "已排除" }, { value: "anchor", label: "人工起点" }]} />
            </Field>
            <Field label="来源" className="w-32">
              <Select value={fSrc} onChange={(e) => setFSrc(e.target.value)} options={[{ value: "all", label: "全部" }, { value: "active", label: "主动查询" }, { value: "passive", label: "被动快照" }]} />
            </Field>
            <span className="ml-auto pb-1.5 text-[12px] text-muted">共 {visible.length} 条</span>
          </Toolbar>
        </div>
        <Table
          rows={visible}
          rowKey={(o) => o.id}
          onRow={setSel}
          cols={[
            { key: "id", label: "编号", render: (o) => <Mono>{o.id}</Mono> },
            { key: "t", label: "采集时间", render: (o) => <span className="num">{o.at}</span> },
            { key: "a", label: "账号", render: (o) => accountName(o.account) },
            { key: "s", label: "短周期", align: "right", render: (o) => (o.shortPct === null ? <Val v={null} /> : <span className="num">{o.shortPct}%</span>) },
            { key: "w", label: "7 天", align: "right", render: (o) => (o.weekPct === null ? <Val v={null} /> : <span className="num">{o.weekPct}%</span>) },
            { key: "c", label: "周期累计费用", align: "right", render: (o) => <Val v={o.spend} prefix="$" /> },
            { key: "src", label: "来源", render: (o) => (o.source === "active" ? "主动" : "被动") },
            { key: "q", label: "质量", render: (o) => <Badge tone={QUALITY[o.quality].tone}>{QUALITY[o.quality].label}</Badge> },
            { key: "st", label: "状态", render: (o) => <Badge tone={STATE[o.state].tone} dot>{STATE[o.state].label}</Badge> },
          ]}
        />
      </Card>

      <Drawer open={!!sel} onClose={() => setSel(null)} title={sel ? `观测 ${sel.id}` : ""}>
        {sel && (
          <div className="space-y-4">
            <KV
              items={[
                ["账号", accountName(sel.account)],
                ["采集时间", sel.at],
                ["短周期已用", sel.shortPct === null ? <Val v={null} /> : `${sel.shortPct}%`],
                ["7 天已用", sel.weekPct === null ? <Val v={null} /> : `${sel.weekPct}%`],
                ["周期累计标准费用", <Val key="c" v={sel.spend} prefix="$" />],
                ["来源", sel.source === "active" ? "主动额度查询" : "被动快照"],
                ["数据质量", <Badge key="q" tone={QUALITY[sel.quality].tone}>{QUALITY[sel.quality].label}</Badge>],
                ["状态", <Badge key="s" tone={STATE[sel.state].tone}>{STATE[sel.state].label}</Badge>],
              ]}
            />
            {sel.state === "excluded" && <Callout tone="info" title="排除原因">{sel.excludeReason}</Callout>}
            <Card title="当时的份额依据">
              {pool ? (
                <ul className="space-y-1.5 text-[13px]">
                  {pool.shares
                    .filter((s) => isAdmin || canParticipant(s.participant))
                    .map((s) => (
                      <li key={s.participant} className="flex justify-between">
                        <span>{participantName(s.participant)}</span>
                        <span className="num">{s.pct}%</span>
                      </li>
                    ))}
                  {!isAdmin && <li className="text-[12px] text-faint">其他成员信息已按你的数据范围隐藏</li>}
                </ul>
              ) : (
                <span className="text-muted">账号未入池</span>
              )}
              <p className="mt-2 text-[12px] text-muted">{pool?.name} · v{pool?.version}（观测时生效的版本）</p>
            </Card>
            <Card title="关联结果">
              <ul className="space-y-1 text-[13px]">
                <li>估算更新 · {sel.account === "a1" ? "Alpha 主账号 11-04 14:15" : "—"}</li>
                <li>余额建议 · {sel.state === "excluded" ? "未使用（已排除）" : "rec-9902"}</li>
              </ul>
            </Card>
            {isAdmin && (
              <div className="flex flex-wrap gap-2 border-t border-line pt-4">
                {sel.state === "valid" && (
                  <>
                    <Button icon={<Ban size={14} />} onClick={() => setAction({ kind: "exclude", ob: sel })}>
                      排除此观测
                    </Button>
                    <Button icon={<Flag size={14} />} onClick={() => setAction({ kind: "anchor", ob: sel })}>
                      设为测算起点
                    </Button>
                  </>
                )}
                {sel.state === "excluded" && (
                  <Button icon={<RotateCcw size={14} />} onClick={() => setAction({ kind: "restore", ob: sel })}>
                    恢复参与计算
                  </Button>
                )}
                {sel.state === "anchor" && (
                  <Button icon={<RotateCcw size={14} />} onClick={() => setAction({ kind: "unanchor", ob: sel })}>
                    撤销人工起点
                  </Button>
                )}
              </div>
            )}
          </div>
        )}
      </Drawer>

      <Modal
        open={!!action}
        onClose={() => setAction(null)}
        title={action ? { exclude: "排除观测", restore: "恢复观测", anchor: "设为人工测算起点", unanchor: "撤销人工起点" }[action.kind] : ""}
        footer={
          <>
            <Button onClick={() => setAction(null)}>取消</Button>
            <AdminButton variant="primary" disabled={(action?.kind === "exclude" || action?.kind === "anchor") && !reason.trim()} onClick={apply}>
              确认
            </AdminButton>
          </>
        }
      >
        <div className="space-y-3">
          <p className="text-[13px]">
            观测 <Mono>{action?.ob.id}</Mono> · {action && accountName(action.ob.account)} · {action?.ob.at}
          </p>
          {(action?.kind === "exclude" || action?.kind === "anchor") && (
            <Field label="原因（必填）">
              <Textarea value={reason} onChange={(e) => setReason(e.target.value)} />
            </Field>
          )}
          <Callout tone="info">
            影响范围：{action && accountName(action.ob.account)} 当前周期的估算与余额建议将重新计算。不修改上游账单，不自动写回钱包，不发送业务通知。
          </Callout>
          {action?.kind === "anchor" && <Callout tone="warn">人工起点不是官方重置，不会抹去同一官方周期内的真实消费。</Callout>}
        </div>
      </Modal>
    </>
  );
}
