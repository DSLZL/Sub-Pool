"use client";

import { useState } from "react";
import { Plus, Unlock } from "lucide-react";
import { AdminButton, Badge, Button, Callout, Card, Field, Grid, Input, Modal, Mono, Outcome, PageHeader, Stat, Switch, Table, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { adminAudit, ipBlocks as initialBlocks, loginAudit } from "@/lib/mock";

const MY_IP = "10.20.3.14";

export default function SecurityPage() {
  const { toast, isAdmin } = useApp();
  const [tab, setTab] = useState("login");
  const [blocks, setBlocks] = useState(initialBlocks);
  const [addOpen, setAddOpen] = useState(false);
  const [ip, setIp] = useState("");
  const [reason, setReason] = useState("");
  const [hints, setHints] = useState(true);

  const selfBlock = ip.trim() === MY_IP;
  const ok = loginAudit.filter((l) => l.ok).length;

  return (
    <>
      <PageHeader title="登录与审计" tag="S2A-SEC-001 ~ 004" desc="登录来源以服务端观察为准，受代理信任配置影响。审计记录不包含密码或其他秘密。" />
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "login", label: "登录审计" },
          { id: "block", label: "来源阻断" },
          ...(isAdmin ? [{ id: "admin", label: "管理操作留痕" }, { id: "hint", label: "浏览器线索" }] : []),
        ]}
      />

      {tab === "login" && (
        <div className="space-y-4">
          <Grid cols={3}>
            <Stat label="今日登录成功" value={ok} />
            <Stat label="今日登录失败" value={loginAudit.length - ok} tone="bad" />
            <Stat label="当前阻断来源" value={blocks.length} />
          </Grid>
          <Card pad={false}>
            <Table
              rows={loginAudit}
              rowKey={(l) => l.at + l.user + l.reason}
              cols={[
                { key: "t", label: "时间", render: (l) => <span className="num">{l.at}</span> },
                { key: "u", label: "用户名", render: (l) => <Mono>{l.user}</Mono> },
                { key: "r", label: "结果", render: (l) => (l.ok ? <Badge tone="ok" dot>成功</Badge> : <Badge tone="bad" dot>失败</Badge>) },
                { key: "ip", label: "服务端来源", render: (l) => <Mono>{l.ip}</Mono> },
                { key: "h", label: "浏览器自报线索", render: (l) => (l.hint ? <span className="text-[12px] text-warn">{l.hint}</span> : <span className="text-faint">—</span>) },
                { key: "why", label: "原因", render: (l) => <span className="text-muted">{l.reason || "—"}</span> },
              ]}
            />
          </Card>
        </div>
      )}

      {tab === "block" && (
        <Card
          title="来源 IP 阻断"
          desc="不能替代网络防火墙"
          actions={
            <AdminButton size="sm" variant="primary" icon={<Plus size={13} />} onClick={() => setAddOpen(true)}>
              添加阻断
            </AdminButton>
          }
          pad={false}
        >
          <Table
            rows={blocks}
            rowKey={(b) => b.ip}
            cols={[
              { key: "ip", label: "来源", render: (b) => <Mono>{b.ip}</Mono> },
              { key: "t", label: "阻断时间", render: (b) => <span className="num">{b.at}</span> },
              { key: "by", label: "来源", render: (b) => b.by },
              { key: "u", label: "到期", render: (b) => b.until },
              { key: "r", label: "原因", render: (b) => <span className="text-muted">{b.reason}</span> },
              {
                key: "x",
                label: "",
                render: (b) => (
                  <AdminButton
                    size="sm"
                    variant="ghost"
                    icon={<Unlock size={12} />}
                    onClick={() => {
                      setBlocks((bs) => bs.filter((x) => x.ip !== b.ip));
                      toast({ tone: "ok", title: `已解除 ${b.ip}` });
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

      {tab === "admin" && (
        <Card pad={false} title="敏感管理操作" desc="权限变更、上游写入、临时业务、历史维护与恢复">
          <Table
            rows={adminAudit}
            rowKey={(a) => a.at + a.action}
            cols={[
              { key: "t", label: "时间", render: (a) => <span className="num">{a.at}</span> },
              { key: "a", label: "操作者", render: (a) => a.actor },
              { key: "ac", label: "动作", render: (a) => a.action },
              { key: "tg", label: "对象", render: (a) => <span className="text-muted">{a.target}</span> },
              { key: "r", label: "结果", render: (a) => <Outcome k={a.result} /> },
            ]}
          />
        </Card>
      )}

      {tab === "hint" && (
        <Card title="浏览器网络辅助线索" className="max-w-2xl">
          <Switch
            checked={hints}
            onChange={(v) => {
              setHints(v);
              toast({ tone: "info", title: v ? "已开启浏览器线索" : "已关闭浏览器线索" });
            }}
            label="在登录时收集浏览器自报的网络线索"
            desc="用于辅助排查登录问题。登录页会告知用户。"
          />
          <Callout tone="warn" className="mt-4">
            浏览器自报信息可被伪造，始终标记为“不可信”，不作为真实 IP 或身份依据。关闭后不影响正常登录。
          </Callout>
        </Card>
      )}

      <Modal
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="添加来源阻断"
        footer={
          <>
            <Button onClick={() => setAddOpen(false)}>取消</Button>
            <Button
              variant="primary"
              disabled={!ip.trim() || selfBlock}
              onClick={() => {
                setBlocks((bs) => [{ ip: ip.trim(), at: "2026-11-04 14:22", by: "admin", until: "永久", reason: reason || "—" }, ...bs]);
                setAddOpen(false);
                setIp("");
                setReason("");
                toast({ tone: "ok", title: "阻断已生效" });
              }}
            >
              阻断
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field label="IP 或网段" error={selfBlock ? "这是你当前会话的来源，为防止误封已禁止。" : undefined}>
            <Input value={ip} onChange={(e) => setIp(e.target.value)} placeholder="203.0.113.0/24" />
          </Field>
          <Field label="原因">
            <Input value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <p className="text-[12px] text-muted">
            你当前的服务端来源：<Mono>{MY_IP}</Mono>
          </p>
        </div>
      </Modal>
    </>
  );
}
