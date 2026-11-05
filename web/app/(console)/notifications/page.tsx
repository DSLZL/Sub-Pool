"use client";

import { useState } from "react";
import { Send } from "lucide-react";
import { Badge, Button, Callout, Card, Field, Grid, Input, Mono, Outcome, PageHeader, Segmented, Select, Switch, Table, Tabs, Toolbar } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { notifyChannel, notifyKinds, notifyLog, participantName } from "@/lib/mock";

const KIND_LABEL = Object.fromEntries(notifyKinds.map((k) => [k.id, k.label]));

export default function NotificationsPage() {
  const { toast } = useApp();
  const { isAdmin, canParticipant } = useScope();
  const [tab, setTab] = useState(isAdmin ? "channel" : "log");
  const [kind, setKind] = useState<"smtp" | "resend">(notifyChannel.kind);
  const [kinds, setKinds] = useState(notifyKinds);
  const [dedupe, setDedupe] = useState("60");
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<null | { outcome: string; detail: string }>(null);
  const [fKind, setFKind] = useState("all");
  const [fOutcome, setFOutcome] = useState("all");

  const log = notifyLog.filter(
    (n) => (isAdmin || (n.participant !== null && canParticipant(n.participant))) && (fKind === "all" || n.kind === fKind) && (fOutcome === "all" || n.outcome === fOutcome),
  );

  const tabs = isAdmin
    ? [
        { id: "channel", label: "邮件渠道" },
        { id: "rules", label: "通知种类" },
        { id: "log", label: "通知记录" },
      ]
    : [{ id: "log", label: "通知记录" }];

  return (
    <>
      <PageHeader title="通知" tag="S2A-NOTIFY-001 ~ 007" desc="邮件发送到配置的管理员接收地址。参与者联系邮箱不代表已自动订阅。" />
      <Tabs value={tab} onChange={setTab} tabs={tabs} />

      {tab === "channel" && (
        <Grid cols={2}>
          <Card title="渠道配置" actions={<Segmented value={kind} onChange={(v) => setKind(v as "smtp" | "resend")} options={[{ id: "smtp", label: "SMTP" }, { id: "resend", label: "Resend" }]} />}>
            <div className="space-y-3">
              {kind === "smtp" ? (
                <>
                  <div className="grid grid-cols-3 gap-3">
                    <Field label="主机" className="col-span-2">
                      <Input defaultValue={notifyChannel.host} />
                    </Field>
                    <Field label="端口">
                      <Input defaultValue={notifyChannel.port} type="number" />
                    </Field>
                  </div>
                  <Field label="用户名">
                    <Input defaultValue={notifyChannel.user} />
                  </Field>
                  <Field label="密码" hint={notifyChannel.secretSet ? "已配置 · 留空保持不变" : undefined}>
                    <Input type="password" placeholder="••••••••" autoComplete="off" />
                  </Field>
                </>
              ) : (
                <Field label="Resend API Key" hint="已配置 · 留空保持不变">
                  <Input type="password" placeholder="re_••••••••" autoComplete="off" />
                </Field>
              )}
              <Field label="发件人">
                <Input defaultValue={notifyChannel.from} />
              </Field>
              <Field label="管理员接收地址">
                <Input defaultValue={notifyChannel.to} />
              </Field>
              <Button variant="primary" onClick={() => toast({ tone: "ok", title: "渠道配置已保存" })}>
                保存
              </Button>
            </div>
          </Card>
          <Card title="发送测试邮件" desc="主动检验当前渠道能否投递">
            <Button
              icon={<Send size={14} />}
              disabled={testing}
              onClick={() => {
                setTesting(true);
                setTestResult(null);
                setTimeout(() => {
                  setTesting(false);
                  setTestResult({ outcome: "success", detail: `服务端已接受投递至 ${notifyChannel.to}（250 OK）` });
                }, 1000);
              }}
            >
              {testing ? "发送中…" : "发送测试邮件"}
            </Button>
            {testResult && (
              <div className="mt-4 flex items-start gap-2">
                <Outcome k={testResult.outcome} />
                <span className="text-[13px] text-muted">{testResult.detail}</span>
              </div>
            )}
            <p className="mt-4 text-[12px] text-muted">
              最近一次：{notifyChannel.lastTest.at} · 成功。服务端接受投递不代表收件人已读。
            </p>
          </Card>
        </Grid>
      )}

      {tab === "rules" && (
        <Grid cols={2}>
          <Card title="通知种类">
            <div className="space-y-4">
              {kinds.map((k) => (
                <Switch key={k.id} checked={k.on} onChange={(v) => setKinds((ks) => ks.map((x) => (x.id === k.id ? { ...x, on: v } : x)))} label={k.label} desc={k.desc} />
              ))}
            </div>
          </Card>
          <Card title="重复提醒控制">
            <Field label="同一问题再次提醒的最短间隔">
              <Select
                value={dedupe}
                onChange={(e) => setDedupe(e.target.value)}
                options={[
                  { value: "30", label: "30 分钟" },
                  { value: "60", label: "1 小时" },
                  { value: "240", label: "4 小时" },
                  { value: "1440", label: "1 天" },
                ]}
              />
            </Field>
            <Callout tone="info" className="mt-4">
              被抑制的通知会以“已跳过”出现在记录中。关闭某种通知不会撤回已发出的邮件。
            </Callout>
            <Button variant="primary" className="mt-4" onClick={() => toast({ tone: "ok", title: "通知策略已保存" })}>
              保存
            </Button>
          </Card>
        </Grid>
      )}

      {tab === "log" && (
        <Card pad={false}>
          <div className="p-4 pb-0">
            <Toolbar>
              <Field label="类型" className="w-52">
                <Select value={fKind} onChange={(e) => setFKind(e.target.value)} options={[{ value: "all", label: "全部" }, ...notifyKinds.map((k) => ({ value: k.id, label: k.label }))]} />
              </Field>
              <Field label="结果" className="w-32">
                <Select
                  value={fOutcome}
                  onChange={(e) => setFOutcome(e.target.value)}
                  options={[
                    { value: "all", label: "全部" },
                    { value: "success", label: "已发送" },
                    { value: "skipped", label: "已跳过" },
                    { value: "failed", label: "失败" },
                  ]}
                />
              </Field>
            </Toolbar>
          </div>
          <Table
            rows={log}
            rowKey={(n) => n.id}
            empty="没有你可见的通知记录"
            cols={[
              { key: "t", label: "时间", render: (n) => <span className="num whitespace-nowrap">{n.at}</span> },
              { key: "k", label: "类型", render: (n) => <Badge>{KIND_LABEL[n.kind]}</Badge> },
              { key: "s", label: "主题", render: (n) => n.subject },
              { key: "p", label: "参与者", render: (n) => (n.participant ? participantName(n.participant) : "—") },
              ...(isAdmin ? [{ key: "to", label: "收件人", render: (n: (typeof notifyLog)[number]) => <Mono>{n.to}</Mono> }] : []),
              { key: "o", label: "投递", render: (n) => <Outcome k={n.outcome} /> },
              { key: "r", label: "原因", render: (n) => <span className="text-[12px] text-muted">{n.reason || "—"}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
