"use client";

import { useEffect, useRef, useState } from "react";
import { Plus, Unlock } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Field, Grid, Input, Modal, Mono, Outcome, PageHeader, Stat, Switch, Table, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { useSecurityData } from "@/lib/use-security-data";
import type { RequestSource } from "@sub-pool/contracts/security";

const sourceKinds = { peer: "TCP 对端", trusted_forwarded: "可信代理链", legacy: "历史来源", unknown: "未知", local: "本机", session: "会话记录" } as const;
const sourceText = (source: RequestSource | null) => source === null ? "尚未取得" : `${source.ip ?? "未知"} · ${sourceKinds[source.kind]}${source.kind === "trusted_forwarded" ? `（对端 ${source.peer_ip ?? "未知"}）` : ""}`;
const loginOutcomes = { success: "成功", failure: "未通过", limited: "受限", blocked: "来源阻断" } as const;

export default function SecurityPage() {
  const { toast, isAdmin, displayTimezone } = useApp();
  const data = useSecurityData();
  const auditTime = (value: string) => displayTime(value, displayTimezone, false);
  const [tab, setTab] = useState("login");
  const [addOpen, setAddOpen] = useState(false);
  const [ip, setIp] = useState("");
  const sequence = useRef(0), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; sequence.current++; }; }, []);

  const blocks = data.rules?.filter(rule => rule.blocked) ?? [];
  const selfBlock = !!ip.trim() && [data.source?.ip, data.source?.peer_ip].includes(ip.trim());
  const alreadyBlocked = data.rules?.some(rule => rule.ip === ip.trim() && rule.blocked) === true;
  const failed = data.login ? (BigInt(data.login.counts.failure) + BigInt(data.login.counts.limited) + BigInt(data.login.counts.blocked)).toString() : "未知";
  const closeAdd = () => { sequence.current++; setAddOpen(false); };
  const submitBlock = async () => {
    if (!data.canBlock || selfBlock || alreadyBlocked) return;
    const ticket = sequence.current, address = ip.trim(), revision = data.rules?.find(rule => rule.ip === address)?.revision ?? "0";
    const saved = await data.mutate({ kind: "source", ip: address, revision, blocked: true });
    if (saved && mounted.current && sequence.current === ticket) { closeAdd(); setIp(""); toast({ tone: "ok", title: "阻断规则已保存" }); }
  };

  return (
    <>
      <PageHeader title="登录与审计" tag="S2A-SEC-001 ~ 004" desc={`登录来源由服务端观察，受代理信任配置影响；TCP 对端可能是应用代理。审计不含秘密。${data.range ? `按 UTC 日期查询，当前范围 ${auditTime(data.range.from)} 至 ${auditTime(data.range.until)}。` : "正在读取 UTC 今日范围。"}`} />
      {data.pending === "read" && <Callout tone="info">正在读取完整安全记录，点击标签可只读刷新。</Callout>}
      {data.error && <Callout tone="bad">{data.error}</Callout>}
      {data.notice && <Callout tone={data.notice.needsReview || data.blocked ? "warn" : "info"}>{data.notice.text}</Callout>}
      <Tabs
        value={tab}
        onChange={(value) => { setTab(value); void data.load(); }}
        tabs={[
          { id: "login", label: "登录审计" },
          { id: "block", label: "来源阻断" },
          ...(isAdmin ? [{ id: "admin", label: "管理操作留痕" }, { id: "hint", label: "浏览器线索" }] : []),
        ]}
      />

      {tab === "login" && (
        <div className="space-y-4">
          <Grid cols={3}>
            <Stat label="今日登录成功" value={data.login?.counts.success ?? "未知"} />
            <Stat label="今日未成功登录" value={failed} tone="bad" />
            <Stat label="当前阻断来源" value={data.rules === null ? "未知" : blocks.length} />
          </Grid>
          <Card pad={false}>
            <Table
              rows={data.login?.events ?? []}
              rowKey={(l) => l.id}
              empty={data.login === null ? "尚未取得完整登录记录" : "UTC 当前范围内无登录记录"}
              cols={[
                { key: "t", label: "时间", render: (l) => <time className="num" dateTime={l.occurred_at} title={l.occurred_at}>{auditTime(l.occurred_at)}</time> },
                { key: "u", label: "用户名", render: (l) => <Mono>{l.username ?? "未匹配账户"}</Mono> },
                { key: "r", label: "结果", render: (l) => (l.outcome === "success" ? <Badge tone="ok" dot>成功</Badge> : <Badge tone="bad" dot>{loginOutcomes[l.outcome]}</Badge>) },
                { key: "ip", label: "服务端来源", render: (l) => <Mono>{sourceText(l.source)}</Mono> },
                { key: "h", label: "浏览器自报线索", render: (l) => (l.source.kind === "legacy" ? <span className="text-[12px] text-warn">历史未留存</span> : <span className="text-faint" title="浏览器线索未留存，不影响登录">—</span>) },
                { key: "why", label: "原因", render: (l) => <span className="text-muted">{l.outcome === "failure" ? "校验未通过；无细分原因" : l.outcome === "success" ? "—" : loginOutcomes[l.outcome]}</span> },
              ]}
            />
          </Card>
        </div>
      )}

      {tab === "block" && (
        <Card
          title="来源 IP 阻断"
          desc="仅支持精确 IPv4/IPv6，需手动解除；不保存原因或到期时间，不能替代网络防火墙"
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
            empty={data.rules === null ? "尚未取得完整来源规则" : "当前无阻断来源"}
            cols={[
              { key: "ip", label: "来源", render: (b) => <Mono>{b.ip}</Mono> },
              { key: "t", label: "阻断时间", render: (b) => <time className="num" dateTime={b.saved_at} title={b.saved_at}>{auditTime(b.saved_at)}</time> },
              { key: "by", label: "操作者 ID", render: (b) => b.actor_id },
              { key: "u", label: "到期", render: () => "手动解除" },
              { key: "r", label: "原因", render: () => <span className="text-muted">未保存</span> },
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
        <Card pad={false} title="敏感管理操作" desc="仅显示已有操作留痕；成功表示本地记录。余额、临时业务与恢复能力以各页状态为准。">
          <Table
            rows={data.management ?? []}
            rowKey={(a) => a.id}
            empty={data.management === null ? "尚未取得完整管理审计" : "UTC 当前范围内无管理操作记录"}
            cols={[
              { key: "t", label: "时间", render: (a) => <time className="num" dateTime={a.occurred_at} title={a.occurred_at}>{auditTime(a.occurred_at)}</time> },
              { key: "a", label: "操作者 / 任务", render: (a) => a.actor_id ?? `任务 ${a.task_id}` },
              { key: "ac", label: "动作 / 操作 ID", render: (a) => `${a.action} · ${a.operation_id}` },
              { key: "tg", label: "对象", render: (a) => <span className="text-muted">{`${a.target_type}:${a.target_id}${a.target_revision ? ` · 修订 ${a.target_revision}` : ""}${a.source_id ? ` · 来源 ${a.source_id}` : ""}`}</span> },
              { key: "r", label: "结果", render: (a) => <Outcome k={a.outcome === "denied" ? "rejected" : a.outcome} /> },
            ]}
          />
        </Card>
      )}

      {isAdmin && tab === "hint" && (
        <Card title="浏览器网络辅助线索" className="max-w-2xl">
          <Switch
            checked={data.config?.network_diagnostics_enabled ?? false}
            disabled={!data.canConfigWrite}
            onChange={async (value) => {
              if (!data.config) return;
              const saved = await data.mutate({ kind: "config", revision: data.config.revision, network_diagnostics_enabled: value });
              if (saved && mounted.current) toast({ tone: "ok", title: value ? "已允许自愿网络诊断" : "已关闭可选网络诊断" });
            }}
            label="允许用户自愿提供不可信网络线索"
            desc={data.config === null ? "尚未取得诊断策略，不能假定已关闭。" : "可选诊断需要用户明确同意。当前界面不采集浏览器网络地址。"}
          />
          <Callout tone="warn" className="mt-4">
            浏览器自报信息可被伪造，始终标记为“不可信”，不作为真实 IP 或身份依据。当前浏览器线索不可用，且不会留存历史线索；不进行外部 IP 查询。关闭或不可用不影响正常登录。
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
              disabled={!ip.trim() || selfBlock || alreadyBlocked || !data.canBlock}
              onClick={submitBlock}
            >
              阻断
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          {data.notice && <Callout tone={data.notice.needsReview || data.blocked ? "warn" : "info"}>{data.notice.text}</Callout>}
          <Field label="精确 IPv4 / IPv6" error={selfBlock ? "这是当前请求的来源或 TCP 对端，为防止误封已禁止。" : alreadyBlocked ? "该来源已经阻断，请在列表中核对或解除。" : undefined}>
            <Input value={ip} onChange={(e) => setIp(e.target.value)} placeholder="203.0.113.5 或 2001:db8::5" maxLength={64} disabled={data.pending === "source"} />
          </Field>
          <Field label="原因（暂不支持保存）">
            <Input value="" disabled placeholder="来源规则不保存原因" />
          </Field>
          <p className="text-[12px] text-muted">
            当前请求的服务端来源：<Mono>{sourceText(data.source)}</Mono>
          </p>
        </div>
      </Modal>
    </>
  );
}
