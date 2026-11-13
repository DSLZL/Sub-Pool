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
import type { Observation } from "@sub-pool/contracts/observations";
import type { ObservationQuery } from "@/lib/observations-data";
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
  VERIFIED_LOCAL_RANGE: "已核对本地范围", PERIOD_EVIDENCE_MISSING: "缺少原生周期证据", ENDPOINT_EXCLUDED: "端点已排除",
  SOURCE_CHANGED: "来源已变化", CONNECTION_UNAVAILABLE: "连接不可用", CONNECTION_CHANGED: "连接版本已变化", IDENTITY_UNVERIFIED: "身份尚未核实",
  SOURCE_MODE_CHANGED: "采集方式已变化", SOURCE_TIME_UNVERIFIED: "源时间未核实", WINDOW_EXPIRED: "原生窗口已到期", SOURCE_STALE: "结束证据已过期",
};

export default function ObservationsPage() {
  const { isAdmin, timezone } = useApp();
  const scope = useScope();
  const [fAcc, setFAcc] = useState("all");
  const [fState, setFState] = useState<ObservationQuery["validity"]>("all");
  const [fSrc, setFSrc] = useState<ObservationQuery["source_mode"]>("all");
  const recorded = useObservationData(scope, fAcc, fState, fSrc);
  const { rows: visible, action, data } = recorded;
  const sel = data?.detail.observation ?? null, pool = data?.detail.basis ?? null;
  const manual = data?.management.manual_start;
  const usage = data?.usage_evidence?.windows.find(window => window.kind === "short")?.capture;
  const accountName = (id: string) => recorded.accounts.find(account => account.id === id)?.name ?? id;
  const periodText = (record: Observation, kind: "short" | "seven_day") => {
    const window = windowOf(record, kind);
    return `${window?.used_percent == null ? "未知" : window.used_percent + "%"} · 时长 ${window?.duration_seconds ?? "未知"} 秒 · 重置 ${window?.reset_at ?? "未知"}`;
  };

  return (
    <>
      <PageHeader title="观测记录" tag="S2A-OBS-001 ~ 005" desc="查询本地保留的被动额度观测，按接收时间记录；费用依据在详情按当前授权展示，覆盖完整性未确认。已配置账号会记录本地估算解释，排除或恢复不改动原始值和上游账单。" />
      <Card pad={false}>
        <div className="p-4 pb-0">
          <Toolbar>
            <Field label="账号" className="w-40">
              <Select value={fAcc} onChange={(e) => setFAcc(e.target.value)} options={[{ value: "all", label: "全部" }, ...recorded.accounts.map(account => ({ value: account.id, label: account.name }))]} />
            </Field>
            <Field label="有效状态" className="w-32">
              <Select value={fState} onChange={(e) => { const value = e.target.value; if (value === "all" || value === "included" || value === "excluded" || value === "manual_start") setFState(value); }} options={[{ value: "all", label: "全部" }, { value: "included", label: "未排除" }, { value: "excluded", label: "已排除" }, { value: "manual_start", label: "人工起点（已设）" }]} />
            </Field>
            <Field label="来源" className="w-32">
              <Select value={fSrc} onChange={(e) => { const value = e.target.value; if (value === "all" || value === "active" || value === "passive") setFSrc(value); }} options={[{ value: "all", label: "全部" }, { value: "active", label: "主动查询" }, { value: "passive", label: "被动快照" }]} />
            </Field>
            <span className="ml-auto pb-1.5 text-[12px] text-muted">{recorded.ready ? `共 ${visible.length} 条` : recorded.error ? "读取未完成" : "读取中"}</span>
          </Toolbar>
          {(recorded.error || !recorded.ready || recorded.pendingNotice) && <Callout tone="info">{recorded.error ?? recorded.pendingNotice ?? "正在完整读取当前范围内的本地观测。"}</Callout>}
        </div>
        <Table
          rows={visible}
          rowKey={(o) => o.id}
          onRow={recorded.select}
          cols={[
            { key: "id", label: "编号", render: (o) => <Mono>{o.id}</Mono> },
            { key: "t", label: "接收时间", render: (o) => <span className="num" title={o.snapshot.received_at}>{displayTime(o.snapshot.received_at, timezone)}</span> },
            { key: "a", label: "账号", render: (o) => accountName(o.account_id) },
            { key: "s", label: "短周期", align: "right", render: (o) => (windowOf(o, "short")?.used_percent == null ? <Val v={null} /> : <span className="num">{windowOf(o, "short")?.used_percent}%</span>) },
            { key: "w", label: "7 天（源分类）", align: "right", render: (o) => (windowOf(o, "seven_day")?.used_percent == null ? <Val v={null} /> : <span className="num">{windowOf(o, "seven_day")?.used_percent}%</span>) },
            { key: "c", label: "费用依据（详情）", align: "right", render: () => <Val v={null} prefix="$" /> },
            { key: "src", label: "来源", render: () => "被动" },
            { key: "q", label: "质量", render: () => <Badge tone={QUALITY.tone}>{QUALITY.label}</Badge> },
            { key: "st", label: "状态", render: (o) => <Badge tone={stateOf(o).tone} dot>{stateOf(o).label}</Badge> },
          ]}
        />
      </Card>

      <Drawer open={!!recorded.selection} onClose={() => recorded.select(null)} title={recorded.selection ? `观测 ${recorded.selection.id}` : ""}>
        {recorded.selection && !sel && <Callout tone="info">{recorded.detailError ?? "正在核对观测详情、历史依据与当前维护版本。"}</Callout>}
        {sel && (
          <div className="space-y-4">
            <KV
              items={[
                ["账号", accountName(sel.account_id)],
                ["接收时间", `${displayTime(sel.snapshot.received_at, timezone)} · ${sel.snapshot.received_at}`],
                ["短周期已用", periodText(sel, "short")],
                ["7 天源分类已用", periodText(sel, "seven_day")],
                ["短周期可见费用（USD）", <span key="c">标准 <Val v={usage?.summary.metrics.total_cost} prefix="$" /> · 实际 <Val v={usage?.summary.metrics.actual_cost} prefix="$" /> · 账号统计 <Val v={usage?.summary.metrics.account_stats_cost} prefix="$" /></span>],
                ["来源", `被动快照 · 原生时间 ${sel.snapshot.source_at ?? "未知"}${usage ? ` · 用量采集 #${usage.id}，${displayTime(usage.captured_at, timezone)}` : ""}`],
                ["数据质量", <Badge key="q" tone={QUALITY.tone}>{usage ? `${usage.state === "exhausted" ? "已读至末页" : usage.state === "partial" ? "部分读取" : "读取失败"} · ${data?.usage_evidence?.scope === "authorized_participants" ? "仅授权成员 · " : ""}覆盖及扣款未确认` : data?.usage_evidence ? "未记录匹配用量" : "未授予用量读取权限"}</Badge>],
                ["状态", <Badge key="s" tone={stateOf(sel).tone}>{stateOf(sel).label}</Badge>],
              ]}
            />
            {sel.excluded && <Callout tone="info" title="排除原因">{sel.review_reason}</Callout>}
            <Card title="当时的份额依据">
              {pool?.contract_id ? (
                <ul className="space-y-1.5 text-[13px]">
                  {pool.members
                    .map((s) => (
                      <li key={s.participant_id} className="flex justify-between">
                        <span>{s.name}</span>
                        <span className="num">{s.share_percent}%</span>
                      </li>
                    ))}
                  {!isAdmin && <li className="text-[12px] text-faint">仅显示当前授权范围内的历史成员</li>}
                </ul>
              ) : (
                <span className="text-muted">未记录可确认的历史份额依据</span>
              )}
              <p className="mt-2 text-[12px] text-muted">{pool?.contract_id ? `本地历史合约 #${pool.contract_id}（原生观测时间生效的版本）` : "不以当前份额倒填历史"}</p>
            </Card>
            <Card title="关联结果">
              <ul className="space-y-1 text-[13px]">
                <li>估算关联 · {data?.estimate_link ? data.estimate_link.estimate ? `最近引用的历史解释 #${data.estimate_link.estimate.id} · ${displayTime(data.estimate_link.estimate.generated_at, timezone)}` : "未记录引用" : "未授予估算读取权限"}；{manual ? `人工区间 ${manual.start.id} → ${manual.end.id} · ${rangeReasons[manual.reason]} · ${manual.period ? `${manual.period.duration_seconds} 秒，${manual.period.start_at} 至 ${manual.period.end_at}` : "周期证据不可用"}` : "未设置人工区间"}</li>
                <li>余额建议 · {data?.recommendation_link ? data.recommendation_link.recommendation ? `最近引用的历史建议 #${data.recommendation_link.recommendation.id} · 参与者 ${data.recommendation_link.recommendation.participant_id} · 估算 #${data.recommendation_link.recommendation.estimate_id} · ${displayTime(data.recommendation_link.recommendation.generated_at, timezone)}` : "未记录引用" : "仅管理员可读取历史建议引用"}；历史引用不代表金额可用，人工区间不代表官方周期重置；同期可见用量不证明额度变化的原因或实际钱包扣款。</li>
              </ul>
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
        title={action ? { exclude: "排除观测", restore: "恢复观测", anchor: "设为人工测算起点", unanchor: "撤销人工起点" }[action.kind] : ""}
        footer={
          <>
            <Button disabled={action?.phase === "busy"} onClick={recorded.closeAction}>取消</Button>
            <AdminButton variant="primary" disabled={!recorded.canConfirm} onClick={() => { void recorded.apply(); }}>
              {action?.phase === "busy" ? "正在核对" : action?.phase === "unknown" ? "核对已保存状态" : "确认"}
            </AdminButton>
          </>
        }
      >
        <div className="space-y-3">
          <p className="text-[13px]">
            观测 <Mono>{action?.selection.id}</Mono> · {action && accountName(action.selection.account)} · {sel && displayTime(sel.snapshot.received_at, timezone)}
          </p>
          {(action?.kind === "exclude" || action?.kind === "anchor") && (
            <Field label="原因（必填）">
              <Textarea value={action?.reason ?? ""} disabled={action?.kind === "anchor" || action?.phase !== "ready"} title="原因不超过 512 个 UTF-8 字节，不能包含控制字符" onChange={(e) => recorded.setReason(e.target.value)} />
            </Field>
          )}
          <Callout tone="info">
            {action?.error ?? (action?.kind === "restore" || action?.kind === "unanchor" ? `确认缘由：${action.reason}。` : "仅追加本地排除决定并保留原因。")}
            原始事实保留；已配置账号会追加本地估算解释，金额与完整性仍需独立证据。不修改上游账单，不写钱包，不发送通知。
          </Callout>
          {action?.kind === "anchor" && <Callout tone="warn">原界面缺少第二个真实观测端点与窗口选择，当前不能创建起点区间。人工起点不是官方重置，不会抹去同一官方周期内的真实消费。</Callout>}
        </div>
      </Modal>
    </>
  );
}
