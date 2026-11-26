"use client";

import { uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { useEffect, useRef, useState } from "react";
import { Plus, Unlock } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Field, Grid, Input, Modal, Mono, Outcome, PageHeader, Stat, Switch, Table, Tabs } from "@/components/ui";
import { DateTimeField } from "@/components/date-time-field";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { useSecurityData } from "@/lib/use-security-data";
import { sourceRule, type RequestSource } from "@sub-pool/contracts/security";

const sourceKinds = { peer: "TCP 对端", trusted_forwarded: "可信代理链", legacy: "历史来源", unknown: "未知", local: "本机", session: "会话记录" } as const;
const sourceText = (source: RequestSource | null) => source === null ? "未取得" : `${source.ip ?? "未知"} · ${sourceKinds[source.kind]}${source.kind === "trusted_forwarded" ? `（对端 ${source.peer_ip ?? "未知"}）` : ""}`;
const loginOutcomes = { success: "成功", failure: "未通过", limited: "受限", blocked: "来源阻断" } as const;

export default function SecurityPage() {
  const { toast, isAdmin, displayTimezone } = useApp();
  const data = useSecurityData();
  const auditTime = (value: string) => displayTime(value, displayTimezone, false);
  const [tab, setTab] = useState("login");
  const [addOpen, setAddOpen] = useState(false);
  const [ip, setIp] = useState("");
  const [reason, setReason] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const sequence = useRef(0), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; sequence.current++; }; }, []);

  const blocks = data.rules?.filter(rule => rule.blocked) ?? [];
  const selfBlock = !!ip.trim() && [data.source?.ip, data.source?.peer_ip].includes(ip.trim());
  const alreadyBlocked = data.rules?.some(rule => rule.ip === ip.trim() && rule.blocked) === true;
  const explanation = reason.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
  const validReason = sourceRule.shape.reason.safeParse(explanation || null).success;
  const validExpiry = sourceRule.shape.expires_at.safeParse(expiresAt || null).success;
  const failed = data.login ? (BigInt(data.login.counts.failure) + BigInt(data.login.counts.limited) + BigInt(data.login.counts.blocked)).toString() : "未知";
  const closeAdd = () => { sequence.current++; setAddOpen(false); };
  const submitBlock = async () => {
    if (!data.canBlock || selfBlock || alreadyBlocked || !validReason || !validExpiry) return;
    const ticket = sequence.current, address = ip.trim(), revision = data.rules?.find(rule => rule.ip === address)?.revision ?? "0";
    const saved = await data.mutate({ kind: "source", ip: address, revision, blocked: true, reason: explanation || undefined, expires_at: expiresAt || undefined });
    if (saved && mounted.current && sequence.current === ticket) {
      closeAdd(); setIp(""); setReason(""); setExpiresAt(""); toast({ tone: "ok", title: "规则已保存" });
    }
  };

  return (
    <>
      <PageHeader title="登录与审计" desc={data.range ? <span title={auditTime(data.range.from) + " 至 " + auditTime(data.range.until)}>UTC {data.range.from.slice(0, 10)} 审计</span> : "读取审计…"} helpTopic="operations" />
      {data.pending === "read" && <Callout tone="info">点标签重读。</Callout>}
      {data.error && <Callout tone="bad">{data.error}</Callout>}
      {data.notice && <Callout tone={data.notice.needsReview || data.blocked ? "warn" : "info"}>{data.notice.text}</Callout>}
      <Tabs
        value={tab}
        onChange={(value) => { setTab(value); void data.load(); }}
        tabs={[
          { id: "login", label: "登录审计" },
          { id: "block", label: "来源阻断" },
          ...(isAdmin ? [{ id: "admin", label: "管理操作" }, { id: "hint", label: "浏览器线索" }] : []),
        ]}
      />

      {tab === "login" && (
        <div className="space-y-4">
          <Grid cols={3}>
            <Stat label="今日登录成功" value={data.login?.counts.success ?? "未知"} />
            <Stat label="今日登录失败" value={failed} tone="bad" />
            <Stat label="当前阻断" value={data.rules === null ? "未知" : blocks.length} />
          </Grid>
          <Card pad={false}>
            <Table
              rows={data.login?.events ?? []}
              rowKey={(l) => l.id}
              empty={data.login === null ? "登录记录未齐" : "暂无登录记录"}
              cols={[
                { key: "t", label: "时间", render: (l) => <Timestamp short className="num" value={l.occurred_at} /> },
                { key: "u", label: "用户名", render: (l) => <Mono>{l.username ?? "未匹配账户"}</Mono> },
                { key: "r", label: uiCopy.result, render: (l) => (l.outcome === "success" ? <Badge tone="ok" dot>成功</Badge> : <Badge tone="bad" dot>{loginOutcomes[l.outcome]}</Badge>) },
                { key: "ip", label: "服务端来源", render: (l) => <Mono>{sourceText(l.source)}</Mono> },
                { key: "h", label: "自报线索", render: (l) => (l.source.kind === "legacy" ? <span className="text-[12px] text-warn">历史未留存</span> : <span className="text-faint" title="无浏览器线索">—</span>) },
                { key: "why", label: "原因", render: (l) => <span className="text-muted">{l.outcome === "failure" ? "校验未通过" : l.outcome === "success" ? "—" : loginOutcomes[l.outcome]}</span> },
              ]}
            />
          </Card>
        </div>
      )}

      {tab === "block" && (
        <Card
          title="来源 IP 阻断"
          desc="按单个 IP 阻断。"
          actions={
            <AdminButton size="sm" variant="primary" icon={<Plus size={13} />} disabled={!data.canBlock} onClick={() => { sequence.current++; data.clearNotice(); setAddOpen(true); }}>
              添加阻断
            </AdminButton>
          }
          pad={false}
        >
          <Table
            rows={blocks}
            rowKey={(b) => b.ip}
            empty={data.rules === null ? "规则不齐" : "暂无阻断"}
            cols={[
              { key: "ip", label: "来源", render: (b) => <Mono>{b.ip}</Mono> },
              { key: "t", label: "阻断时间", render: (b) => <Timestamp short className="num" value={b.saved_at} /> },
              { key: "by", label: "操作者 ID", render: (b) => b.actor_id },
              { key: "u", label: "到期", render: (b) => <Timestamp short className="num" value={b.expires_at} empty="永久" /> },
              { key: "r", label: "原因", render: (b) => b.reason ?? <span className="text-muted">未填写</span> },
              {
                key: "x",
                label: "",
                render: (b) => (
                  <AdminButton
                    size="sm"
                    variant="ghost"
                    icon={<Unlock size={12} />}
                    disabled={!data.canSourceWrite}
                    onClick={async () => {
                      const saved = await data.mutate({ kind: "source", ip: b.ip, revision: b.revision, blocked: false });
                      if (saved && mounted.current) toast({ tone: "ok", title: `已解除 ${b.ip}` });
                    }}
                  >
                    解除
                  </AdminButton>
                ),
              },
            ]}
          />
        </Card>
      )}

      {isAdmin && tab === "admin" && (
        <Card pad={false} title="敏感管理操作">
          <Table
            rows={data.management ?? []}
            rowKey={(a) => a.id}
            empty={data.management === null ? "审计记录未齐" : "暂无审计记录"}
            cols={[
              { key: "t", label: "时间", render: (a) => <Timestamp short className="num" value={a.occurred_at} /> },
              { key: "a", label: "操作者 / 任务", render: (a) => a.actor_id ?? `任务 ${a.task_id}` },
              { key: "ac", label: "动作 / 操作 ID", render: (a) => `${a.action} · ${a.operation_id}` },
              { key: "tg", label: "对象", render: (a) => <span className="text-muted">{`${a.target_type}:${a.target_id}${a.target_revision ? ` · 修订 ${a.target_revision}` : ""}${a.source_id ? ` · 来源 ${a.source_id}` : ""}`}</span> },
              { key: "r", label: uiCopy.result, render: (a) => <Outcome k={a.outcome === "denied" ? "rejected" : a.outcome} /> },
            ]}
          />
        </Card>
      )}

      {isAdmin && tab === "hint" && (
        <Card title="网络线索" className="max-w-2xl">
          <Switch
            checked={data.config?.network_diagnostics_enabled ?? false}
            disabled={!data.canConfigWrite}
            onChange={async (value) => {
              if (!data.config) return;
              const saved = await data.mutate({ kind: "config", revision: data.config.revision, network_diagnostics_enabled: value });
              if (saved && mounted.current) toast({ tone: "ok", title: value ? "诊断已开启" : "诊断已关闭" });
            }}
            label="自愿诊断"
            desc={data.config === null ? "策略未知。" : "须本人同意。"}
          />
          <Callout tone="warn" className="mt-4">
            见登录审计。
          </Callout>
        </Card>
      )}

      <Modal
        open={addOpen}
        onClose={closeAdd}
        title="添加来源阻断"
        footer={
          <>
            <Button onClick={closeAdd}>取消</Button>
            <Button
              variant="primary"
              disabled={!ip.trim() || selfBlock || alreadyBlocked || !validReason || !validExpiry || !data.canBlock}
              onClick={submitBlock}
            >
              阻断
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          {data.notice && <Callout tone={data.notice.needsReview || data.blocked ? "warn" : "info"}>{data.notice.text}</Callout>}
          <Field label="精确 IPv4 / IPv6" error={selfBlock ? "连接受保护。" : alreadyBlocked ? "已阻断，可解除。" : undefined}>
            <Input value={ip} onChange={(e) => setIp(e.target.value)} placeholder="203.0.113.5 或 2001:db8::5" maxLength={64} disabled={data.pending === "source"} />
          </Field>
          <Field label="原因" error={validReason ? undefined : "原因最多 256 字符。"}>
            <Input value={reason} onChange={(e) => setReason(e.target.value)} disabled={data.pending === "source"} />
          </Field>
          <DateTimeField label="到期时间" value={expiresAt} onChange={setExpiresAt} disabled={data.pending === "source"}
            hint="留空表示永久。" />
          <p className="text-[12px] text-muted">
            当前来源 <Mono>{sourceText(data.source)}</Mono>
          </p>
        </div>
      </Modal>
    </>
  );
}
