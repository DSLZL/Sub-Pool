"use client";

import { Timestamp } from "@/components/timestamp";
import { useEffect, useMemo, useState } from "react";
import { Send } from "lucide-react";
import type { NotificationConfiguration } from "@sub-pool/contracts/notifications";
import { Badge, Button, Callout, Card, Drawer, Empty, Field, Grid, Input, KV, Mono, Outcome, PageHeader, Segmented, Select, Switch, Table, Tabs } from "@/components/ui";
import { DateTimeField } from "@/components/date-time-field";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { allNotificationFilters, channelDraft, channelSave, financialActivationPending, notificationKinds, notificationKindLabel, notificationReason, notificationRecordLabel, policySave, type ChannelDraft, type NotificationFilter } from "@/lib/notifications-data";
import { useNotificationData } from "@/lib/use-notification-data";
import { useScope } from "@/lib/scope";

const states = [
  { value: "", label: "全部" }, { value: "queued", label: "已排队" }, { value: "dispatching", label: "发送中" },
  { value: "accepted", label: "渠道已接受" }, { value: "skipped", label: "已跳过" }, { value: "failed", label: "失败" }, { value: "unknown", label: "结果未知" },
] as const;
const intervals = [{ value: "1800", label: "30 分钟" }, { value: "3600", label: "1 小时" }, { value: "14400", label: "4 小时" }, { value: "86400", label: "1 天" }];
const tlsOptions = [{ value: "tls", label: "TLS" }, { value: "starttls", label: "强制 STARTTLS" }, { value: "loopback_plain", label: "仅本机开发明文" }] as const;
const subjectTypes = [{ value: "", label: "全部" }, { value: "system", label: "系统" }, { value: "account", label: "账号" }, { value: "participant", label: "参与者" }] as const;
const eventKinds = (["test", "balance_low", "recommendation_changed", "estimate_changed", "collection_failed", "temporary_near", "temporary_exhausted"] as const).map(value => ({ value, label: notificationKindLabel[value] }));
const financialDescriptions = {
  native: {
    balance_low: "可用余额跌破当期已确认发放净额的 20% 时提醒，同一钱包及有效周期最多一次",
    recommendation_changed: "同一钱包、账号范围及周期下，完整周额度建议的目标金额变化时提醒",
    estimate_changed: "同账号、同周及同计价口径下，可信七天容量相对变化达到 20% 时提醒",
  },
  simulated: {
    balance_low: "完整模拟钱包建议高于独立读取余额时提醒",
    recommendation_changed: "同来源、绑定及周期下，完整模拟钱包建议的目标金额变化时提醒",
    estimate_changed: "相同配置、周期与计价下，完整模拟容量相对变化达到 20% 时提醒",
  },
};

export default function NotificationsPage() {
  const { auth, isAdmin, displayTimezone } = useApp();
  const time = (value: string | null) => displayTime(value, displayTimezone, false);
  const [tab, setTab] = useState(isAdmin ? "channel" : "log");
  const [filters, setFilters] = useState({ epoch: auth.epoch, value: allNotificationFilters });
  const draftFilter = filters.epoch === auth.epoch ? filters.value : allNotificationFilters;
  const activeTab = isAdmin ? tab : "log";
  const scope = useScope(activeTab === "log" && (isAdmin || !!auth.access?.features.includes("notifications")));
  const participants = {
    ready: scope.participantCatalogAvailable,
    placeholder: scope.participantCatalogAvailable ? "全部" : scope.participantCatalogError ?? "正在读取参与者目录…",
    options: scope.participantCatalog.map(item => ({ value: item.id, label: item.name })),
  };
  const subject = {
    "": { ready: false, placeholder: "请先选择对象类型", options: [] },
    system: { ready: true, placeholder: "全部", options: [{ value: "test", label: "测试邮件" }] },
    account: {
      ready: scope.accountCatalogAvailable,
      placeholder: scope.accountCatalogAvailable ? "全部" : scope.accountCatalogError ?? "正在读取账号目录…",
      options: scope.accounts.map(item => ({ value: item.id, label: item.name })),
    },
    participant: participants,
  }[draftFilter.subject_type];
  const participantID = participants.ready && !participants.options.some(option => option.value === draftFilter.participant_id) ? "" : draftFilter.participant_id;
  const subjectID = subject.ready && !subject.options.some(option => option.value === draftFilter.subject_id) ? "" : draftFilter.subject_id;
  const filter = useMemo(() => ({ ...draftFilter, participant_id: participantID, subject_id: subjectID }), [draftFilter, participantID, subjectID]);
  const data = useNotificationData(activeTab === "log" ? filter : allNotificationFilters);
  const [draft, setDraft] = useState<ChannelDraft | null>(null);
  const [preferences, setPreferences] = useState<NotificationConfiguration["preferences"] | null>(null), [dedupe, setDedupe] = useState("");
  const [enabled, setEnabled] = useState(false);
  useEffect(() => {
    setDraft(data.configuration ? channelDraft(data.configuration) : null);
    setPreferences(data.configuration?.preferences ?? null); setDedupe(data.configuration ? String(data.configuration.dedupe_seconds) : "");
    setEnabled(data.configuration?.enabled ?? false);
  }, [data.configuration]);
  const changeTab = (value: string) => {
    if (data.pending && data.pending !== "read") return;
    setDraft(value => value ? { ...value, credential: "", clear_credential: false } : null);
    data.closeDetail();
    if (value !== activeTab) { setTab(value); return; }
    void data.reload();
    if (value === "log") { void scope.reloadAccounts(); void scope.reloadParticipants(); }
  };
  const saved = data.configuration;
  const busy = !!data.pending, locked = busy || data.blocked || !!data.unresolvedTest || !data.ready;
  const channelDirty = !!saved && !!draft && JSON.stringify(channelDraft(saved)) !== JSON.stringify(draft);
  const activateFinancial = !!saved && !!preferences && financialActivationPending(saved, preferences, enabled);
  const nativeFinancial = !!saved?.native_source_id;
  const financialActive = nativeFinancial ? saved?.native_financial_enabled : saved?.financial_enabled && saved?.capabilities[0].available;
  const policyDirty = !!saved && !!preferences && (activateFinancial || saved.enabled !== enabled || JSON.stringify(saved.preferences) !== JSON.stringify(preferences) || String(saved.dedupe_seconds) !== dedupe);
  const editFilter = (update: Partial<NotificationFilter>) => { if (!busy) { data.closeDetail(); setFilters({ epoch: auth.epoch, value: { ...filter, ...update } }); } };
  const edit = (update: Partial<ChannelDraft>) => { if (!locked) setDraft(value => value ? { ...value, ...update } : null); };
  const saveChannel = () => {
    if (!saved || !draft) return;
    try {
      const input = channelSave(saved, draft);
      setDraft(value => value ? { ...value, credential: "", clear_credential: false } : null);
      void data.save(input);
    } catch (error) { data.inputError(error); }
  };
  const savePolicy = () => {
    if (!saved || !preferences) return;
    try { void data.save(policySave(saved, preferences, dedupe, enabled)); } catch (error) { data.inputError(error); }
  };
  const tabs = isAdmin ? [{ id: "channel", label: "邮件渠道" }, { id: "rules", label: "通知种类" }, { id: "log", label: "通知记录" }] : [{ id: "log", label: "通知记录" }];
  const log = [...data.rows].reverse(), testResult = data.test;
  const pageReady = activeTab === "log" ? data.historyReady : data.ready;
  const dedupeOptions = intervals.some(item => item.value === dedupe) ? intervals : [...intervals, { value: dedupe, label: `已保存：${dedupe} 秒` }];

  return (
    <>
      <PageHeader title="通知" desc="邮件发送到配置的管理员接收地址。参与者联系邮箱不代表已自动订阅。" />
      <Tabs value={activeTab} onChange={changeTab} tabs={tabs} />
      {data.notice && <Callout tone={data.notice.unknown ? "unk" : "info"}>{data.notice.text}</Callout>}
      {data.error && <Callout tone="unk">{data.error}</Callout>}
      {!pageReady && <Empty title={data.error ? "通知数据不可用" : "正在读取通知…"}>点击当前标签可重新读取。</Empty>}

      {isAdmin && activeTab === "channel" && saved && draft && (
        <Grid cols={2}>
          <Card title="渠道配置" actions={<Segmented value={draft.channel} onChange={(value) => { if (!locked && (value === "smtp" || value === "resend")) edit({ channel: value, credential: "", clear_credential: false }); }} options={[{ id: "smtp", label: "SMTP" }, { id: "resend", label: "Resend" }]} />}>
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
                  <Field label="SMTP 传输安全" hint="已有凭据绑定发送目标；修改主机、端口、TLS 或用户名后，须重新输入凭据或明确清除。">
                    <Select aria-label="SMTP 传输安全" value={draft.tls} disabled={locked} onChange={event => { const option = tlsOptions.find(item => item.value === event.target.value); if (option) edit({ tls: option.value }); }} options={[...tlsOptions]} />
                  </Field>
                  <Field label="用户名">
                    <Input aria-label="SMTP 用户名" value={draft.username} onChange={event => edit({ username: event.target.value })} disabled={locked} autoComplete="off" />
                  </Field>
                  <Field label="密码" hint={saved.secret_set && saved.channel === "smtp" ? "已配置 · 同目标留空保持" : "尚未配置 · 留空不新增"}>
                    <Input aria-label="SMTP 密码" type="password" value={draft.credential} onChange={event => edit({ credential: event.target.value })} placeholder="••••••••" autoComplete="new-password" disabled={locked || draft.clear_credential} />
                  </Field>
                </>
              ) : (
                <Field label="Resend API Key" hint={saved.secret_set && saved.channel === "resend" ? "已配置 · 留空保持不变" : "请输入新渠道的凭据"}>
                  <Input aria-label="Resend API Key" type="password" value={draft.credential} onChange={event => edit({ credential: event.target.value })} placeholder="re_••••••••" autoComplete="new-password" disabled={locked || draft.clear_credential} />
                </Field>
              )}
              <Switch checked={draft.clear_credential} disabled={locked || !saved.secret_set || draft.channel !== saved.channel} onChange={value => edit({ clear_credential: value, credential: "" })} label="清除已保存凭据" desc="点击保存才会清除；普通留空仍保留原凭据，认证渠道随后需要新凭据。" />
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
              最近一次：{testResult ? time(testResult.created_at) : "尚无可确认记录"}。渠道接受不代表已送达或已读。{!saved.secret_storage_available && "凭据存储不可用。"}
            </p>
          </Card>
        </Grid>
      )}

      {isAdmin && activeTab === "rules" && saved && preferences && (
        <Grid cols={2}>
          <Card title="通知种类">
            <div className="space-y-4">
              <Switch checked={enabled} disabled={locked} onChange={setEnabled} label="全局通知" desc="保存后才生效；关闭会抑制后续业务提醒，保留各类偏好和已发记录。测试邮件仍需单独点击发送。" />
              {notificationKinds.map(kind => {
                const available = saved.capabilities.find(item => item.kind === kind.id)?.available;
                const description = kind.id === "collection_failed" ? "对已确认失败的被动采集记录提醒"
                  : available ? financialDescriptions[nativeFinancial ? "native" : "simulated"][kind.id] : "当前业务依据不可用";
                return <Switch key={kind.id} checked={preferences[kind.id]} disabled={locked || !available}
                  onChange={value => setPreferences(current => current ? { ...current, [kind.id]: value } : null)} label={kind.label} desc={description} />;
              })}
              <Switch checked={false} disabled onChange={() => undefined} label="临时轮次接近用满" desc="提醒由各轮次单独配置；此处暂不提供设置" />
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
              全局通知{saved.enabled ? "已启用" : "已关闭"}，采集失败提醒{saved.capabilities[3].effective ? "当前有效" : "当前未生效"}。{nativeFinancial ? "当前原生来源的金融提醒" : "金融提醒"}{financialActive ? "已明确启用，须有完整当前依据" : "尚未明确启用"}。{activateFinancial && (nativeFinancial ? "本次保存将为当前原生来源启用已选金融提醒。" : "本次保存将启用已选模拟金融提醒。")}
            </Callout>
            <Button variant="primary" className="mt-4" disabled={locked || !policyDirty} onClick={savePolicy}>
              {data.pending === "save" ? "保存中…" : activateFinancial ? "保存并启用已选金融提醒" : "保存"}
            </Button>
          </Card>
        </Grid>
      )}

      {activeTab === "log" && (
        <Card pad={false}>
          <div className="p-4 pb-0">
            <div className="grid items-start gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Field label="类型">
                <Select aria-label="通知类型" disabled={busy} value={filter.kind} onChange={event => editFilter({ kind: eventKinds.find(item => item.value === event.target.value)?.value ?? "" })} options={[{ value: "", label: "全部" }, ...eventKinds]} />
              </Field>
              <Field label="结果">
                <Select
                  aria-label="通知结果"
                  value={filter.state}
                  disabled={busy}
                  onChange={event => editFilter({ state: states.find(item => item.value === event.target.value)?.value ?? "" })}
                  options={[...states]}
                />
              </Field>
              <DateTimeField compact label="通知开始时间（含）" value={filter.start_at} disabled={busy} onChange={start_at => editFilter({ start_at })} />
              <DateTimeField compact label="通知结束时间（不含）" value={filter.end_at} disabled={busy} onChange={end_at => editFilter({ end_at })} />
              <Field label="参与者">
                <Select aria-label="通知参与者" disabled={busy || !participants.ready} value={filter.participant_id}
                  onChange={event => editFilter({ participant_id: event.target.value })}
                  options={[{ value: participants.ready ? "" : filter.participant_id, label: participants.placeholder }, ...participants.options]} />
              </Field>
              <Field label="相关对象类型">
                <Select aria-label="通知相关对象类型" value={filter.subject_type} disabled={busy} options={[...subjectTypes]}
                  onChange={event => editFilter({ subject_type: subjectTypes.find(item => item.value === event.target.value)?.value ?? "", subject_id: "" })} />
              </Field>
              <Field label="相关对象">
                <Select aria-label="通知相关对象" disabled={busy || !subject.ready} value={filter.subject_id}
                  onChange={event => editFilter({ subject_id: event.target.value })}
                  options={[{ value: subject.ready ? "" : filter.subject_id, label: subject.placeholder }, ...subject.options]} />
              </Field>
            </div>
          </div>
          <Table
            rows={log}
            rowKey={row => row.id}
            empty="没有你可见的通知记录"
            cols={[
              { key: "t", label: "时间", render: row => <Timestamp short className="num whitespace-nowrap" value={row.created_at} /> },
              { key: "k", label: "类型", render: row => <Badge>{notificationRecordLabel(row)}</Badge> },
              { key: "s", label: "主题", render: row => row.details?.subject ?? `${row.subject_type === "participant" ? "参与者" : "账号"} ${row.subject_id} · ${notificationRecordLabel(row)}` },
              { key: "p", label: "参与者", render: row => row.participant_id ?? "—" },
              ...(isAdmin ? [{ key: "to", label: "收件人", render: (row: (typeof log)[number]) => <Mono>{row.details?.recipient ?? "—"}</Mono> }] : []),
              { key: "o", label: "投递", render: row => <Outcome k={row.state} /> },
              { key: "r", label: "原因", render: row => <><span className="text-[12px] text-muted">{notificationReason(row)}</span><Button size="sm" disabled={busy} onClick={() => { void data.openDetail(row); }}>查看记录 {row.id}</Button></> },
            ]}
          />
        </Card>
      )}
      <Drawer open={!!data.detail} onClose={data.closeDetail} title={`通知记录 #${data.detail?.id ?? ""}`}>
        {data.detail?.pending && <Empty title="正在读取详情…">仅核对所选记录，不会再次发送。</Empty>}
        {data.detail?.error && <Callout tone="unk">{data.detail.error}</Callout>}
        {data.detail?.row && <>
          <KV cols={1} items={[
            ["类型", notificationRecordLabel(data.detail.row)], ["投递状态", <Outcome key="outcome" k={data.detail.row.state} />], ["原因", notificationReason(data.detail.row)],
            ["相关对象", `${data.detail.row.subject_type} · ${data.detail.row.subject_id}`], ["参与者", data.detail.row.participant_id ?? "未关联"],
            ["记录时间", time(data.detail.row.created_at)], ["发送开始", time(data.detail.row.started_at)], ["结束时间", time(data.detail.row.finished_at)],
          ]} />
          {data.detail.row.details && <Card title="创建时保存的邮件内容">
            <KV cols={1} items={[
              ["配置版本", data.detail.row.details.config_revision], ["渠道", data.detail.row.details.channel], ["发件人", data.detail.row.details.from || "未配置"],
              ["收件人", data.detail.row.details.recipient || "未配置"], ["主题", data.detail.row.details.subject], ["正文", <p key="body" className="whitespace-pre-wrap break-words">{data.detail.row.details.body}</p>],
              ["事件原因", data.detail.row.details.event_reason], ["渠道记录", data.detail.row.details.provider_id ?? "未提供"],
            ]} />
          </Card>}
          <Callout tone="info">渠道接受不代表收件箱已送达或已读。此处仅查看已有记录，不会补发。</Callout>
        </>}
      </Drawer>
    </>
  );
}
