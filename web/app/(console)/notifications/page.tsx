"use client";

import { useEffect, useState } from "react";
import { Send } from "lucide-react";
import type { NotificationConfiguration } from "@sub-pool/contracts/notifications";
import { Badge, Button, Callout, Card, Empty, Field, Grid, Input, Mono, Outcome, PageHeader, Segmented, Select, Switch, Table, Tabs, Toolbar } from "@/components/ui";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { channelDraft, channelSave, notificationKinds, notificationKindLabel, notificationReason, policySave, type ChannelDraft, type NotificationFilter } from "@/lib/notifications-data";
import { useNotificationData } from "@/lib/use-notification-data";

const states = [
  { value: "", label: "全部" }, { value: "queued", label: "已排队" }, { value: "dispatching", label: "发送中" },
  { value: "accepted", label: "渠道已接受" }, { value: "skipped", label: "已跳过" }, { value: "failed", label: "失败" }, { value: "unknown", label: "结果未知" },
] as const;
const intervals = [{ value: "1800", label: "30 分钟" }, { value: "3600", label: "1 小时" }, { value: "14400", label: "4 小时" }, { value: "86400", label: "1 天" }];

export default function NotificationsPage() {
  const { isAdmin, timezone } = useApp();
  const [tab, setTab] = useState(isAdmin ? "channel" : "log");
  const [fKind, setFKind] = useState<NotificationFilter["kind"]>(""), [fOutcome, setFOutcome] = useState<NotificationFilter["state"]>("");
  const activeTab = isAdmin ? tab : "log";
  const data = useNotificationData(activeTab === "log" ? { kind: fKind, state: fOutcome } : { kind: "", state: "" });
  const [draft, setDraft] = useState<ChannelDraft | null>(null);
  const [preferences, setPreferences] = useState<NotificationConfiguration["preferences"] | null>(null), [dedupe, setDedupe] = useState("");
  useEffect(() => {
    setDraft(data.configuration ? channelDraft(data.configuration) : null);
    setPreferences(data.configuration?.preferences ?? null); setDedupe(data.configuration ? String(data.configuration.dedupe_seconds) : "");
  }, [data.configuration]);
  const changeTab = (value: string) => {
    if (data.pending && data.pending !== "read") return;
    setDraft(value => value ? { ...value, credential: "" } : null);
    if (value === activeTab) void data.reload(); else setTab(value);
  };
  const saved = data.configuration;
  const busy = !!data.pending, locked = busy || data.blocked || !!data.unresolvedTest || !data.ready;
  const channelDirty = !!saved && !!draft && JSON.stringify(channelDraft(saved)) !== JSON.stringify(draft);
  const policyDirty = !!saved && !!preferences && (JSON.stringify(saved.preferences) !== JSON.stringify(preferences) || String(saved.dedupe_seconds) !== dedupe);
  const edit = (update: Partial<ChannelDraft>) => { if (!locked) setDraft(value => value ? { ...value, ...update } : null); };
  const saveChannel = () => {
    if (!saved || !draft) return;
    try {
      const input = channelSave(saved, draft);
      setDraft(value => value ? { ...value, credential: "" } : null);
      void data.save(input);
    } catch { data.inputError(); }
  };
  const savePolicy = () => {
    if (!saved || !preferences) return;
    try { void data.save(policySave(saved, preferences, dedupe)); } catch { data.inputError(); }
  };
  const tabs = isAdmin ? [{ id: "channel", label: "邮件渠道" }, { id: "rules", label: "通知种类" }, { id: "log", label: "通知记录" }] : [{ id: "log", label: "通知记录" }];
  const log = [...data.rows].reverse(), testResult = data.test;
  const pageReady = activeTab === "log" ? data.historyReady : data.ready;
  const dedupeOptions = intervals.some(item => item.value === dedupe) ? intervals : [...intervals, { value: dedupe, label: `已保存：${dedupe} 秒` }];

  return (
    <>
      <PageHeader title="通知" tag="S2A-NOTIFY-001 ~ 007" desc="邮件发送到配置的管理员接收地址。参与者联系邮箱不代表已自动订阅。" />
      <Tabs value={activeTab} onChange={changeTab} tabs={tabs} />
      {data.notice && <Callout tone={data.notice.unknown ? "unk" : "info"}>{data.notice.text}</Callout>}
      {data.error && <Callout tone="unk">{data.error}</Callout>}
      {!pageReady && <Empty title={data.error ? "通知数据不可用" : "正在读取通知…"}>点击当前标签可重新读取。</Empty>}

      {isAdmin && activeTab === "channel" && saved && draft && (
        <Grid cols={2}>
          <Card title="渠道配置" actions={<Segmented value={draft.channel} onChange={(value) => { if (!locked && (value === "smtp" || value === "resend")) edit({ channel: value, credential: "" }); }} options={[{ id: "smtp", label: "SMTP" }, { id: "resend", label: "Resend" }]} />}>
            <div className="space-y-3">
              {draft.channel === "smtp" ? (
                <>
                  <div className="grid grid-cols-3 gap-3">
                    <Field label="主机" className="col-span-2">
                      <Input aria-label="SMTP 主机" value={draft.host} onChange={event => edit({ host: event.target.value })} disabled={locked} />
                    </Field>
                    <Field label="端口">
                      <Input aria-label="SMTP 端口" value={draft.port} onChange={event => edit({ port: event.target.value })} type="number" min={1} max={65535} step={1} disabled={locked} />
                    </Field>
                  </div>
                  <Field label="用户名">
                    <Input aria-label="SMTP 用户名" value={draft.username} onChange={event => edit({ username: event.target.value })} disabled={locked} autoComplete="off" />
                  </Field>
                  <Field label="密码" hint={saved.secret_set && saved.channel === "smtp" ? "已配置 · 同目标留空保持" : "尚未配置 · 留空不新增"}>
                    <Input aria-label="SMTP 密码" type="password" value={draft.credential} onChange={event => edit({ credential: event.target.value })} placeholder="••••••••" autoComplete="new-password" disabled={locked} />
                  </Field>
                </>
              ) : (
                <Field label="Resend API Key" hint={saved.secret_set && saved.channel === "resend" ? "已配置 · 留空保持不变" : "请输入新渠道的凭据"}>
                  <Input aria-label="Resend API Key" type="password" value={draft.credential} onChange={event => edit({ credential: event.target.value })} placeholder="re_••••••••" autoComplete="new-password" disabled={locked} />
                </Field>
              )}
              <Field label="发件人">
                <Input aria-label="发件人" value={draft.from} onChange={event => edit({ from: event.target.value })} disabled={locked} />
              </Field>
              <Field label="管理员接收地址">
                <Input aria-label="管理员接收地址" value={draft.recipient} onChange={event => edit({ recipient: event.target.value })} disabled={locked} />
              </Field>
              <Button variant="primary" disabled={locked || !channelDirty} onClick={saveChannel}>
                {data.pending === "save" ? "保存中…" : "保存"}
              </Button>
            </div>
          </Card>
          <Card title="发送测试邮件" desc={`使用已保存配置 · ${saved.channel === "smtp" ? { tls: "TLS", starttls: "强制 STARTTLS", loopback_plain: "仅本机开发明文" }[saved.smtp.tls] : "Resend HTTPS"}`}>
            <Button
              icon={<Send size={14} />}
              disabled={busy || data.blocked || channelDirty || !data.ready || !data.historyReady}
              onClick={() => { void data.runTest(); }}
            >
              {data.pending === "test" ? "读取投递结果…" : data.unresolvedTest ? "读取原测试结果" : "发送测试邮件"}
            </Button>
            {(testResult || data.unresolvedTest) && (
              <div className="mt-4 flex items-start gap-2" role="status">
                <Outcome k={testResult?.state ?? "unknown"} />
                <span className="text-[13px] text-muted">{testResult ? `记录 #${testResult.id} · ${notificationReason(testResult)}` : "尚未取得本次记录，仅可读取核对，不会补发。"}</span>
              </div>
            )}
            <p className="mt-4 text-[12px] text-muted">
              最近一次：{testResult ? displayTime(testResult.created_at, timezone) : "尚无可确认记录"}。渠道接受不代表已送达或已读。{!saved.secret_storage_available && "凭据存储不可用。"}
            </p>
          </Card>
        </Grid>
      )}

      {isAdmin && activeTab === "rules" && saved && preferences && (
        <Grid cols={2}>
          <Card title="通知种类">
            <div className="space-y-4">
              {notificationKinds.map(kind => (
                <Switch key={kind.id} checked={preferences[kind.id]} disabled={locked || !saved.capabilities.find(item => item.kind === kind.id)?.available} onChange={value => setPreferences(current => current ? { ...current, [kind.id]: value } : null)} label={kind.label} desc={kind.id === "collection_failed" ? "仅对已确认失败的被动采集记录提醒；有效状态由保存后的策略决定" : "当前业务依据不可用；保留偏好但不会触发提醒"} />
              ))}
              <Switch checked={false} disabled onChange={() => undefined} label="临时轮次接近用满" desc="当前尚未提供该类通知能力" />
            </div>
          </Card>
          <Card title="重复提醒控制">
            <Field label="同一问题再次提醒的最短间隔">
              <Select
                aria-label="重复提醒间隔"
                value={dedupe}
                disabled={locked}
                onChange={event => setDedupe(event.target.value)}
                options={dedupeOptions}
              />
            </Field>
            <Callout tone="info" className="mt-4">
              全局通知{saved.enabled ? "已启用" : "已关闭"}，采集失败提醒{saved.capabilities[3].effective ? "当前有效" : "当前未生效"}。本页没有全局启用入口。被抑制记录为“已跳过”；关闭不会撤回已发邮件。
            </Callout>
            <Button variant="primary" className="mt-4" disabled={locked || !policyDirty} onClick={savePolicy}>
              {data.pending === "save" ? "保存中…" : "保存"}
            </Button>
          </Card>
        </Grid>
      )}

      {activeTab === "log" && data.historyReady && (
        <Card pad={false}>
          <div className="p-4 pb-0">
            <Toolbar>
              <Field label="类型" className="w-52">
                <Select aria-label="通知类型" disabled={busy} value={fKind} onChange={event => setFKind(event.target.value === "test" ? "test" : event.target.value === "collection_failed" ? "collection_failed" : "")} options={[{ value: "", label: "全部" }, { value: "test", label: "测试邮件" }, { value: "collection_failed", label: "采集失败" }]} />
              </Field>
              <Field label="结果" className="w-32">
                <Select
                  aria-label="通知结果"
                  value={fOutcome}
                  disabled={busy}
                  onChange={event => setFOutcome(states.find(item => item.value === event.target.value)?.value ?? "")}
                  options={[...states]}
                />
              </Field>
            </Toolbar>
          </div>
          <Table
            rows={log}
            rowKey={row => row.id}
            empty="没有你可见的通知记录"
            cols={[
              { key: "t", label: "时间", render: row => <span className="num whitespace-nowrap">{displayTime(row.created_at, timezone)}</span> },
              { key: "k", label: "类型", render: row => <Badge>{notificationKindLabel[row.kind]}</Badge> },
              { key: "s", label: "主题", render: row => row.details?.subject ?? `账号 ${row.subject_id} 采集通知` },
              { key: "p", label: "参与者", render: row => row.participant_id ?? "—" },
              ...(isAdmin ? [{ key: "to", label: "收件人", render: (row: (typeof log)[number]) => <Mono>{row.details?.recipient ?? "—"}</Mono> }] : []),
              { key: "o", label: "投递", render: row => <Outcome k={row.state} /> },
              { key: "r", label: "原因", render: row => <span className="text-[12px] text-muted">{notificationReason(row)}</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
