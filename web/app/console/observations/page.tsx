"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
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
  Input,
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
import type { Observation } from "@sub-pool/contracts/observations";
import type { ObservationQuery } from "@/lib/observations-data";
import { DateTimeField } from "@/components/date-time-field";
import { displayTime } from "@/lib/connection-data";
import { useObservationData } from "@/lib/use-observation-data";

const STATE = {
  valid: { label: "未排除", tone: "ok" as const },
  excluded: { label: "已排除", tone: "muted" as const },
  anchor: { label: "人工起点", tone: "info" as const },
};
const QUALITY = { label: "未评定", tone: "unk" as const };
const stateOf = (record: Observation) => STATE[record.excluded ? "excluded" : record.manual_start ? "anchor" : "valid"];
const windowOf = (record: Observation, kind: "short" | "seven_day") => record.snapshot.windows.find(window => window.kind === kind);
const rangeReasons = {
  VERIFIED_LOCAL_RANGE: "已核对", PERIOD_EVIDENCE_MISSING: "缺少周期", ENDPOINT_EXCLUDED: "端点已排除",
  SOURCE_CHANGED: "来源已变", CONNECTION_UNAVAILABLE: "连接不可用", CONNECTION_CHANGED: "连接已变", IDENTITY_UNVERIFIED: "身份未核实",
  SOURCE_MODE_CHANGED: "采集方式已变", SOURCE_TIME_UNVERIFIED: "源时间未核实", WINDOW_EXPIRED: "窗口到期", SOURCE_STALE: "观测过期",
};

export default function ObservationsPage() {
  const { auth, isAdmin, displayTimezone } = useApp();
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);
  const scope = useScope();
  const [fAcc, setFAcc] = useState("all");
  const [fState, setFState] = useState<ObservationQuery["validity"]>("all");
  const [fSrc, setFSrc] = useState<ObservationQuery["source_mode"]>("all");
  const [fRange, setFRange] = useState({ epoch: auth.epoch, source: "", start: "", end: "" });
  const range = fRange.epoch === auth.epoch ? fRange : { epoch: auth.epoch, source: "", start: "", end: "" };
  const editRange = (field: "source" | "start" | "end", value: string) => setFRange({ ...range, [field]: value });
  const recorded = useObservationData(scope, fAcc, fState, fSrc, range.source, range.start, range.end);
  const { rows: visible, action, data } = recorded;
  const sel = data?.detail.observation ?? null, pool = data?.detail.basis ?? null;
  const manual = data?.management.manual_start;
  const usage = data?.usage_evidence?.windows.find(window => window.kind === "short")?.capture;
  const accountName = (id: string) => recorded.accounts.find(account => account.id === id)?.name ?? id;
  const periodText = (record: Observation, kind: "short" | "seven_day") => {
    const window = windowOf(record, kind);
    return `${window?.used_percent == null ? "未知" : window.used_percent + "%"} · 时长 ${window?.duration_seconds ?? "未知"} 秒 · 重置 ${window?.reset_at ? time(window.reset_at) : "未知"}`;
  };

  return (
    <>
      <PageHeader title={uiCopy.observations} helpTopic="usage" />
      <Card pad={false}>
        <div className="p-4 pb-0">
          <Toolbar>
            <Field label={uiCopy.account} className="w-40">
              <Select value={fAcc} onChange={(e) => setFAcc(e.target.value)} options={[{ value: "all", label: "全部" }, ...recorded.accounts.map(account => ({ value: account.id, label: account.name }))]} />
            </Field>
            <Field label="有效状态" className="w-32">
              <Select value={fState} onChange={(e) => { const value = e.target.value; if (value === "all" || value === "included" || value === "excluded" || value === "manual_start") setFState(value); }} options={[{ value: "all", label: "全部" }, { value: "included", label: "未排除" }, { value: "excluded", label: "已排除" }, { value: "manual_start", label: "人工起点（已设）" }]} />
            </Field>
            <Field label="来源" className="w-32">
              <Select value={fSrc} onChange={(e) => { const value = e.target.value; if (value === "all" || value === "active" || value === "passive") setFSrc(value); }} options={[{ value: "all", label: "全部" }, { value: "active", label: "主动查询" }, { value: "passive", label: "被动快照" }]} />
            </Field>
            <DateTimeField label="接收起点（含）" className="w-52 max-w-full" hint="留空不限起点" value={range.start} onChange={value => editRange("start", value)} />
            <DateTimeField label="接收终点（不含）" className="w-52 max-w-full" hint="留空不限终点" value={range.end} onChange={value => editRange("end", value)} />
            <Field label="来源编号" className="w-40">
              <Input value={range.source} maxLength={22} placeholder="留空查全部" onChange={e => editRange("source", e.target.value)} />
            </Field>
            <span className="ml-auto pb-1.5 text-[12px] text-muted">{recorded.ready ? `共 ${visible.length} 条` : recorded.error ? "读取不全" : "读取中"}</span>
          </Toolbar>
          {(recorded.error || !recorded.ready || recorded.pendingNotice) && <Callout tone="info">{recorded.error ?? recorded.pendingNotice ?? "读取观测…"}</Callout>}
        </div>
        <Table
          rows={visible}
          rowKey={(o) => o.id}
          onRow={recorded.select}
          cols={[
            { key: "id", label: "编号", render: (o) => <Mono>{o.id}</Mono> },
            { key: "t", label: "接收时间", render: (o) => <Timestamp short className="num" value={o.snapshot.received_at} /> },
            { key: "a", label: uiCopy.account, render: (o) => accountName(o.account_id) },
            { key: "s", label: "短周期", align: "right", render: (o) => (windowOf(o, "short")?.used_percent == null ? <Val v={null} /> : <span className="num">{windowOf(o, "short")?.used_percent}%</span>) },
            { key: "w", label: "7 天（源分类）", align: "right", render: (o) => (windowOf(o, "seven_day")?.used_percent == null ? <Val v={null} /> : <span className="num">{windowOf(o, "seven_day")?.used_percent}%</span>) },
            { key: "c", label: "短周期标准费", align: "right", render: (o) => <Val v={o.usage_evidence?.windows.find(window => window.kind === "short")?.capture?.summary.metrics.total_cost} prefix="$" /> },
            { key: "src", label: "来源", render: () => "被动" },
            { key: "q", label: "质量", render: () => <Badge tone={QUALITY.tone}>{QUALITY.label}</Badge> },
            { key: "st", label: uiCopy.status, render: (o) => <Badge tone={stateOf(o).tone} dot>{stateOf(o).label}</Badge> },
          ]}
        />
      </Card>

      <Drawer open={!!recorded.selection} onClose={() => recorded.select(null)} title={recorded.selection ? `观测 ${recorded.selection.id}` : ""}>
        {recorded.selection && !sel && <Callout tone="info">{recorded.detailError ?? uiCopy.loading}</Callout>}
        {sel && (
          <div className="space-y-4">
            <KV
              items={[
                [uiCopy.account, accountName(sel.account_id)],
                ["接收时间", <Timestamp key="received" value={sel.snapshot.received_at} />],
                ["短周期已用", periodText(sel, "short")],
                ["七天已用", periodText(sel, "seven_day")],
                ["短周费用", <span key="c">标准 <Val v={usage?.summary.metrics.total_cost} prefix="$" /> · 实际 <Val v={usage?.summary.metrics.actual_cost} prefix="$" /> · 账号统计 <Val v={usage?.summary.metrics.account_stats_cost} prefix="$" /></span>],
                ["快照时间", <Timestamp key="observed" value={sel.snapshot.source_at} />],
                ["用量", <Badge key="q" tone={QUALITY.tone}>{usage ? (usage.state === "exhausted" ? "已读取" : usage.state === "partial" ? "采集不全" : uiCopy.readFailed) : data?.usage_evidence ? "暂无用量" : uiCopy.noAccess}</Badge>],
                [uiCopy.status, <Badge key="s" tone={stateOf(sel).tone}>{stateOf(sel).label}</Badge>],
              ]}
            />
            {sel.excluded && <Callout tone="info" title="排除原因">{sel.review_reason}</Callout>}
            <Card title="历史份额">
              {pool?.contract_id ? (
                <ul className="space-y-1.5 text-[13px]">
                  {pool.members
                    .map((s) => (
                      <li key={s.participant_id} className="flex justify-between">
                        <span>{s.name}</span>
                        <span className="num">{s.share_percent}%</span>
                      </li>
                    ))}
                </ul>
              ) : (
                <span className="text-muted">暂无历史份额</span>
              )}
            </Card>
            <Card title="关联结果">
              <ul className="space-y-1 text-[13px]">
                <li>估算 · {data?.estimate_link ? data.estimate_link.estimate ? `#${data.estimate_link.estimate.id} · ${time(data.estimate_link.estimate.generated_at)}` : "暂无" : uiCopy.noAccess}</li>
                <li>余额建议 · {data?.recommendation_link ? data.recommendation_link.recommendation ? `#${data.recommendation_link.recommendation.id} · ${time(data.recommendation_link.recommendation.generated_at)}` : "暂无" : uiCopy.noAccess}</li>
                {manual && <li>人工区间 {manual.start.id} → {manual.end.id} · {rangeReasons[manual.reason]}</li>}
              </ul>
            </Card>
            <Card title="维护历史">
              <Table rows={data?.events ?? []} rowKey={event => event.id} cols={[
                { key: "revision", label: "版本", render: event => <Mono>{event.revision}</Mono> },
                { key: "action", label: "操作", render: event => ({ exclude: "排除", restore: "恢复", start: "设定人工区间", revoke: "撤销人工区间" })[event.action] },
                { key: "observation", label: "观测或区间", render: event => event.action === "start" ? `${event.start_observation_id} → ${event.end_observation_id} · ${event.window_kind === "short" ? "短周期" : "7 天源分类"}` : event.observation_id ?? "当前人工区间" },
                { key: "time", label: "记录时间", render: event => <Timestamp short value={event.occurred_at} /> },
                { key: "actor", label: "操作者", render: event => event.actor_id ?? "无账号权限" },
                { key: "reason", label: "原因", render: event => event.reason },
              ]} />
            </Card>
            {isAdmin && (
              <div className="flex flex-wrap gap-2 border-t border-line pt-4">
                {!sel.excluded && !sel.manual_start && (
                  <>
                    <Button icon={<Ban size={14} />} disabled={recorded.mutationBlocked} onClick={() => recorded.openAction("exclude")}>
                      排除此观测
                    </Button>
                    <Button icon={<Flag size={14} />} disabled={recorded.mutationBlocked} onClick={() => recorded.openAction("anchor")}>
                      设为测算起点
                    </Button>
                  </>
                )}
                {sel.excluded && (
                  <Button icon={<RotateCcw size={14} />} disabled={recorded.mutationBlocked} onClick={() => recorded.openAction("restore")}>
                    恢复为未排除
                  </Button>
                )}
                {sel.manual_start && (
                  <Button icon={<RotateCcw size={14} />} disabled={recorded.mutationBlocked} onClick={() => recorded.openAction("unanchor")}>
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
        onClose={recorded.closeAction}
        title={action ? { exclude: "排除观测", restore: "恢复观测", anchor: "设人工起点", unanchor: "撤销人工起点" }[action.kind] : ""}
        footer={
          <>
            <Button disabled={action?.phase === "busy"} onClick={recorded.closeAction}>取消</Button>
            <AdminButton variant="primary" disabled={!recorded.canConfirm} onClick={() => { void recorded.apply(); }}>
              {action?.phase === "busy" ? "正在核对" : action?.phase === "unknown" ? "核对状态" : "确认"}
            </AdminButton>
          </>
        }
      >
        <div className="space-y-3">
          <p className="text-[13px]">
            观测 <Mono>{action?.selection.id}</Mono> · {action && accountName(action.selection.account)} · {sel && time(sel.snapshot.received_at)}
          </p>
          {(action?.kind === "exclude" || action?.kind === "anchor") && (
            <Field label="原因（必填）">
              <Textarea value={action?.reason ?? ""} disabled={action?.phase !== "ready"} title="最多 512 字节" onChange={(e) => recorded.setReason(e.target.value)} />
            </Field>
          )}
          {action?.kind === "anchor" && <>
            <Field label="结束观测">
              <Select value={action.endID} disabled={action.phase !== "ready"} onChange={e => recorded.setEnd(e.target.value)} options={[{ value: "", label: "选择结束观测" }, ...recorded.endpoints.map(end => ({ value: end.id, label: `#${end.id} · 原生时间 ${time(end.snapshot.source_at)}` }))]} />
            </Field>
            <Field label="配额窗口">
              <Select value={action.window} disabled={action.phase !== "ready"} onChange={e => { const value = e.target.value; if (value === "" || value === "short" || value === "seven_day") recorded.setWindow(value); }} options={[{ value: "", label: "选择窗口" }, ...(sel?.snapshot.windows ?? []).map(window => ({ value: window.kind, label: window.kind === "short" ? "短周期" : "7 天源分类" }))]} />
            </Field>
          </>}
          <Callout tone="info">
            {action?.error ?? (action?.kind === "restore" || action?.kind === "unanchor" ? `确认缘由：${action.reason}。` : action?.kind === "anchor" ? "核对官方周期。" : "保存排除决定。")}
            更新估算说明。 {glossary.sub2api}   保留旧账。
          </Callout>
          {action?.kind === "anchor" && <Callout tone="warn">选择结束观测。</Callout>}
        </div>
      </Modal>
    </>
  );
}
