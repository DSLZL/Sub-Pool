"use client";

import { CursorNavigation } from "@/components/cursor-navigation";

import { useEffect, useState } from "react";
import { Zap } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, ConfirmModal, Field, Input, KV, Modal, PageHeader, Select, Table, Tabs, Toolbar, cx } from "@/components/ui";
import { useTemporaryControls } from "@/lib/use-temporary-controls";
import { temporaryMessage, temporaryDetailRows } from "@/lib/temporary-data";
import { buildTemporaryStart, buildTemporaryRights, TemporaryReadError, type TemporaryStart } from "@/lib/temporary-actions";
import { backupRequestID } from "@/lib/backup-data";
import { displayTime } from "@/lib/connection-data";

const checks = [
  { k: "数据新鲜度", note: "尚无本轮采集新鲜度核验依据。" },
  { k: "参与者绑定", note: "尚无本轮绑定与影响范围核验依据。" },
  { k: "官方周期", note: "尚未确认本轮相关账号的周期与截止时间。" },
  { k: "运行模式", note: "尚无本轮运行模式核验依据。" },
  { k: "钱包计费路径", note: "余额应用与安全收回尚未通过核验。" },
  { k: "钱包整体管理范围", note: "尚未确认完整钱包管理范围；不能按局部账号放行。" },
];
type Draft = { owner: string; request: string; hash: string; mode: "carry" | "no_carry"; informed: boolean; understand: boolean; deadline: string; caps: TemporaryStart["caps"]; acknowledged: string[] };
export default function TemporaryPage() {
  const [selection, setSelection] = useState({ owner: "", tab: "round" });
  const [draft, setDraft] = useState<Draft | null>(null), [stopKey, setStopKey] = useState("");
  const [reminderDirty, setReminderDirty] = useState(false);
  const data = useTemporaryControls(draft !== null || stopKey !== "" || reminderDirty);
  useEffect(() => { setDraft(null); setStopKey(""); setReminderDirty(false); }, [data.owner]);
  const [reminder, setReminder] = useState({ key: "", enabled: false, threshold: "" });
  const [edit, setEdit] = useState({ key: "", request: "", after: "", reason: "" });
  const tab = selection.owner === data.owner ? selection.tab : "round", mode = draft?.mode ?? "carry";
  const message = !data.allowed ? "全局轮次仅管理员可查看。请在获授权的钱包建议中查看本人临时影响。"
    : data.status ? temporaryMessage(data.status, data.timezone) : data.error ?? "正在读取临时参与可用性；尚无可确认的轮次记录。";
  const details = data.details, round = details?.round, rows = temporaryDetailRows(details, data.timezone);
  const roundKey = round ? data.owner + ":" + round.id : "", reminderKey = roundKey + ":" + details?.reminders.revision;
  const rights = data.rights, editKey = rights ? data.owner + ":" + rights.value.round_id + ":" + rights.value.account_id + ":" + rights.participant + ":" + rights.value.revision : "";
  useEffect(() => { setReminder({ key: reminderKey, enabled: details?.reminders.enabled ?? false, threshold: details?.reminders.threshold ?? "" }); }, [reminderKey, details?.reminders.enabled, details?.reminders.threshold]);
  useEffect(() => { setEdit({ key: editKey, request: editKey ? backupRequestID() : "", after: rights?.value.adjustments[rights.participant] ?? "0", reason: "" }); }, [editKey, rights]);
  const scope = data.preflight?.state === "ready" && data.preflight.scope_hash === draft?.hash ? data.preflight.scope : null;
  let startInput: TemporaryStart | null = null, startError = "";
  if (draft && scope && data.preflight) {
    try { startInput = buildTemporaryStart(data.preflight, { request_id: draft.request, mode: draft.mode, deadline: draft.deadline, caps: draft.caps, acknowledged_participants: draft.acknowledged, informed: draft.informed }); }
    catch (error) { startError = error instanceof TemporaryReadError ? error.message : "请填写有效上限、UTC截止时间，并确认全部受影响成员。"; }
  }
  let editValid = false;
  if (round && rights && edit.key === editKey) {
    try { buildTemporaryRights(round, rights.value, rights.participant, edit.after, edit.reason, edit.request); editValid = true; } catch {}
  }
  const reminderValid = reminder.key === reminderKey && (!reminder.enabled || /^(?:0|[1-9]\d*)(?:\.\d{1,8})?$/.test(reminder.threshold) && Number(reminder.threshold) > 0 && Number(reminder.threshold) < 100);
  async function inspect() {
    const value = await data.inspect();
    if (value?.state === "ready") setDraft(previous => previous?.owner === data.owner ? { ...previous, request: backupRequestID(), hash: value.scope_hash, deadline: value.scope.latest_deadline,
      caps: value.scope.accounts.flatMap(account => account.members.map(member => ({ account_id: account.account_id, participant_id: member.participant_id, amount: "" }))), acknowledged: [], informed: false, understand: false } : previous);
  }
  const notice = data.notice && <Callout tone={data.notice.unknown ? "warn" : "info"}>{data.notice.text}</Callout>;
  return (
    <>
      <PageHeader title="临时放宽" tag="S2A-TEMP-001 ~ 010" desc="在一个明确轮次内临时放宽余额建议。它只影响本地建议和按授权应用的钱包目标，不会增加上游订阅容量。"
        actions={data.allowed && <Button variant="primary" icon={<Zap size={14} />} disabled={data.pending !== null} onClick={() => {
          if (data.error) { setDraft(null); void data.reload(); }
          else setDraft({ owner: data.owner, request: "", hash: "", mode: "carry", informed: false, understand: false, deadline: "", caps: [], acknowledged: [] });
        }}>{data.error ? "重新读取" : "启用前核验"}</Button>} />
      {notice}
      {data.allowed && <Tabs value={tab} onChange={(id) => { setDraft(null); data.closeRights(); setSelection({ owner: data.owner, tab: id }); if (id === tab) void data.reload(); }}
        tabs={[{ id: "round", label: "当前轮次" }, { id: "settle", label: "后续权益处理" }]} />}
      {data.allowed && <Toolbar>
        {data.history && <Field label="查看轮次"><Select value={data.selectedID} disabled={data.pending !== null} onChange={(event) => { data.closeRights(); void data.select(event.target.value); }}
          options={data.history.length ? data.history.map(row => ({ value: row.id, label: `${row.id} · ${row.mode === "carry" ? "结转" : "不结转"} · ${displayTime(row.started_at, data.timezone)} · ${row.exit ? "已退出" : "进行中"}` })) : [{ value: "", label: "已读取完整历史，当前没有轮次" }]} /></Field>}
        <Button disabled={data.pending !== null} onClick={() => { setDraft(null); data.closeRights(); void data.reload(); }}>只读重新读取</Button>
      </Toolbar>}
      {data.history && <CursorNavigation after={data.historyAfter} next={data.historyNext} onChange={data.changeHistoryPage} disabled={data.pending !== null} />}
      {(!data.allowed || tab === "round") && <Card>
        <p className="text-[13px] text-muted" role="status">{message}</p>
        {round && <div className="space-y-3">
          <KV items={[["所选轮次", round.id], ["模式", round.mode === "carry" ? "结转" : "不结转"], ["开始", displayTime(round.started_at, data.timezone)], ["截止（UTC）", round.deadline],
            ["退出", round.exit ? `${round.exit.reason} · ${displayTime(round.exit.exited_at, data.timezone)}` : "本轮进行中"], ["来源与环境", `${round.scope.source_id} · 隔离模拟 · ${round.scope.simulation.epoch}`]]} />
          <Table rows={round.scope.accounts.flatMap(account => account.members.map(member => ({ account, member })))} rowKey={({ account, member }) => account.account_id + ":" + member.participant_id}
            cols={[{ key: "a", label: "账号 / 原周期", render: ({ account }) => `#${account.upstream_id} · ${displayTime(account.settlement_period.start_at, data.timezone)} — ${displayTime(account.settlement_period.end_at, data.timezone)}` },
              { key: "m", label: "成员", render: ({ member }) => `#${member.user_id}` },
              { key: "cap", label: "冻结上限 / 基线（钱包 USD）", render: ({ account, member }) => `${round.caps.find(cap => cap.account_id === account.account_id && cap.participant_id === member.participant_id)?.amount ?? "未记录"} / ${member.baseline.numerator}/${member.baseline.denominator}` },
              { key: "p", label: "完整周期依据", render: ({ account }) => account.periods.map(period => `${period.kind} · ${period.start_at} — ${period.end_at}`).join("；") }]} />
          <Button variant="danger" disabled={!data.canWrite || round.exit !== null} onClick={() => setStopKey(roundKey)}>提前终止所选轮次</Button>
          <Checkbox checked={reminder.key === reminderKey && reminder.enabled} disabled={!data.canWrite || round.exit !== null} onChange={(enabled) => { setReminderDirty(true); setReminder(previous => ({ ...previous, enabled })); }} label="启用本轮用满提醒（新轮默认关闭）" />
          <Field label="本轮接近用满阈值（%，大于0且小于100）"><Input value={reminder.key === reminderKey ? reminder.threshold : ""} disabled={!data.canWrite || !reminder.enabled || round.exit !== null} onChange={(event) => { setReminderDirty(true); setReminder(previous => ({ ...previous, threshold: event.target.value })); }} /></Field>
          <Button disabled={!data.canWrite || !reminderValid || round.exit !== null} onClick={async () => { if (await data.reminders({ round_id: round.id, revision: details.reminders.revision, enabled: reminder.enabled, threshold: reminder.enabled ? reminder.threshold : "" })) setReminderDirty(false); }}>保存本轮提醒</Button>
          <p className="text-[13px] text-muted">{round.exit ? "本轮已结束；保存过的提醒偏好不表示还会发送。" : "仅保存本轮偏好；实际投递还需当前可用的邮件渠道，不能据此认定已发信。"}</p>
        </div>}
      </Card>}
      {data.allowed && tab === "settle" && <Card pad={false} title="待结算、异常与完成记录" desc={`所选轮次：${round?.id ?? "尚未读取"}。${message}`}>
        <Table rows={rows} rowKey={(row) => row.key} empty={data.history?.length === 0 ? "当前来源完整历史尚无轮次；不能推断其他来源历史。" : "权益记录不可用；尚未读取待结算、异常或已完成记录。"}
          cols={[{ key: "r", label: "轮次", render: (row) => row.round }, { key: "a", label: "账号 / 原周期", render: (row) => row.account }, { key: "m", label: "成员", render: (row) => row.member },
            { key: "v", label: "权益调整", align: "right", render: (row) => row.adjustment }, { key: "s", label: "状态", render: (row) => row.state },
            { key: "why", label: "依据 / 原因", render: (row) => `${row.reason}；本轮消费 ${row.spent} / 原周期消费 ${row.periodSpent} 钱包 USD；尾段 ${row.closedThrough}；下一周期 ${row.nextPeriod}` },
            { key: "x", label: "", render: (row) => <Button size="sm" disabled={!data.canWrite || data.status?.state === "active"} onClick={() => void data.openRights(row.accountID, row.participantID)}>读取当前权益 · {row.member}</Button> }]} />
      </Card>}
      <Modal open={data.allowed && draft?.owner === data.owner} onClose={() => { if (!data.pending) setDraft(null); }} title="开启临时放宽轮次" wide
        footer={<><Button disabled={data.pending !== null} onClick={() => setDraft(null)}>取消</Button><Button variant="primary" disabled={!data.canWrite || !startInput || !draft?.understand} onClick={async () => {
          if (startInput && data.preflight && await data.start(startInput, data.preflight)) setDraft(null);
        }}>开启轮次</Button></>}>
        {notice}
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <div className="space-y-3">
            <div className="text-[13px] font-semibold">启用前核验</div>
            <Button disabled={!data.canWrite} onClick={() => void inspect()}>读取完整范围并核验</Button>
            <ul className="space-y-2">{checks.map((c) => <li key={c.k} className="flex items-start gap-2 text-[12.5px]">
              <Badge tone={scope ? "ok" : "warn"}>{scope ? "已核验" : "未核验"}</Badge><div><div className="font-medium">{c.k}</div><div className="text-muted">{scope ? "已包含在本次服务端完整范围核验中。" : c.note}</div></div>
            </li>)}</ul>
            {data.preflight?.state === "unavailable" && <Callout tone="warn">本次预检不可用：{data.preflight.reason === "TEMPORARY_EVIDENCE_LIMIT" ? "完整范围超过预检容量（10,000 位参与者或账户、20,000 项成员关系或 16 MiB 证据）" : data.preflight.reason}。未获得可开启范围。</Callout>}
            {scope && <><KV cols={1} items={[["来源", scope.source_id], ["核验有效至（UTC）", scope.expires_at], ["最晚截止（UTC）", scope.latest_deadline], ["完整范围", `${scope.accounts.length} 个账号 / ${scope.wallets.length} 位成员`]]} />
              {scope.accounts.map(account => <div key={account.account_id} className="space-y-3">
                <p className="text-[13px] text-muted">账号 #{account.upstream_id} · 剩余 {account.remaining.numerator}/{account.remaining.denominator} 钱包 USD · 保留 {account.reserve.numerator}/{account.reserve.denominator} · 价格版本 {account.price_version} · 钱包倍率 {account.wallet_multiplier}。{account.periods.map(period => `${period.kind}：${period.start_at} — ${period.end_at}`).join("；")}</p>
                {account.members.map(member => <Field key={member.participant_id} label={`账号 #${account.upstream_id} / 成员 #${member.user_id} 上限（钱包 USD）`} hint={`基线 ${member.baseline.numerator}/${member.baseline.denominator}；绑定 #${member.binding_id}`}>
                  <Input inputMode="decimal" disabled={data.pending !== null} value={draft?.caps.find(cap => cap.account_id === account.account_id && cap.participant_id === member.participant_id)?.amount ?? ""} onChange={(event) => setDraft(previous => previous ? { ...previous, caps: previous.caps.map(cap => cap.account_id === account.account_id && cap.participant_id === member.participant_id ? { ...cap, amount: event.target.value } : cap) } : null)} />
                </Field>)}
              </div>)}
              {scope.wallets.map(wallet => <Checkbox key={wallet.participant_id} disabled={data.pending !== null} checked={draft?.acknowledged.includes(wallet.participant_id) ?? false}
                onChange={(checked) => setDraft(previous => previous ? { ...previous, acknowledged: checked ? [...previous.acknowledged.filter(id => id !== wallet.participant_id), wallet.participant_id] : previous.acknowledged.filter(id => id !== wallet.participant_id) } : null)}
                label={`确认成员 #${wallet.user_id} 的完整钱包影响范围（绑定 #${wallet.binding_id}）`} />)}
            </>}
          </div>
          <div className="space-y-4">
            <div className="text-[13px] font-semibold">选择模式</div>
            {([["carry", "结转", "本轮借用的权益在后续周期补偿或扣减。"], ["no_carry", "不结转", "不新增后续补偿；需确认已告知参与者。"]] as const).map(([id, label, desc]) =>
              <button key={id} disabled={!data.status || data.pending !== null} aria-pressed={mode === id} onClick={() => setDraft(previous => previous ? { ...previous, mode: id, informed: false } : null)}
                className={cx("w-full rounded-md border p-3 text-left", mode === id ? "border-brand bg-brand-soft" : "border-line hover:bg-surface-2")}>
                <div className="text-[13px] font-medium">{label}</div><div className="text-[12px] text-muted">{desc}</div>
              </button>)}
            <Field label="本轮截止时间（UTC）" hint="使用完整UTC时间，例如 2026-11-15T12:30:00Z；不能晚于最早已确认周期结束。"><Input disabled={!scope || data.pending !== null} value={draft?.deadline ?? ""} onChange={(event) => setDraft(previous => previous ? { ...previous, deadline: event.target.value } : null)} /></Field>
            <Callout tone="info">{message} 开轮仅保存冻结的本地轮次和临时建议；钱包应用与恢复独立核对。每轮提醒默认关闭，需在开轮后单独保存；本处勾选不会发信。</Callout>
            {startError && <Callout tone="warn">{startError}</Callout>}
            {mode === "no_carry" && <Checkbox disabled={data.pending !== null} checked={draft?.informed ?? false} onChange={(informed) => setDraft(previous => previous ? { ...previous, informed } : null)} label="我已告知所有受影响参与者（此勾选仅记录业务确认，系统不会逐人发邮件）" />}
            <Checkbox disabled={data.pending !== null} checked={draft?.understand ?? false} onChange={(understand) => setDraft(previous => previous ? { ...previous, understand } : null)} label="我了解：结束建议不等于收回上游余额，需单独核对。" />
          </div>
        </div>
      </Modal>
      <ConfirmModal open={data.allowed && !!round && stopKey === roundKey} onClose={() => setStopKey("")} title="提前终止所选轮次" confirmLabel="确认提前终止" danger disabled={!data.canWrite || !!round?.exit} onConfirm={() => data.stop()}>
        <p>轮次 {round?.id}。只取消本轮尚未处理的权益；原有当前权益保留。普通建议恢复与远端余额恢复仍需分开核对。</p>{notice}
      </ConfirmModal>
      <Modal open={data.allowed && !!rights} onClose={() => { if (!data.pending) data.closeRights(); }} title="修订当前结转权益"
        footer={<><Button disabled={data.pending !== null} onClick={() => data.closeRights()}>取消</Button><Button disabled={!data.canWrite || !rights?.value.period} onClick={() => setEdit(previous => ({ ...previous, after: "0" }))}>填写清除值 0</Button>
          <Button variant="primary" disabled={!data.canWrite || !editValid} onClick={async () => { if (await data.changeRights(edit.after, edit.reason, edit.request)) data.closeRights(); }}>保存当前权益</Button></>}>
        {notice}<div className="space-y-3"><KV cols={1} items={[["轮次 / 账号", `${rights?.value.round_id ?? "未知"} / ${rights?.value.account_id ?? "未知"}`], ["当前版本", rights?.value.revision ?? "未知"],
          ["当前原周期（UTC）", rights?.value.period ? `${rights.value.period.start_at} — ${rights.value.period.end_at}` : "未知，禁止修订"],
          ["当前聚合权益（钱包 USD）", rights ? rights.value.adjustments[rights.participant] ?? "0（当前聚合无单独权益项）" : "未知"]]} />
          <Callout tone="info">仅修订本地当前权益，不写远端钱包。0 表示清除该成员当前权益；修订必须通过服务端资金、绑定及当前周期核验。</Callout>
          <Field label="修订后权益（钱包 USD，可为负数）"><Input value={edit.key === editKey ? edit.after : ""} disabled={data.pending !== null || !rights?.value.period} onChange={(event) => setEdit(previous => ({ ...previous, after: event.target.value }))} /></Field>
          <Field label="修订原因"><Input value={edit.key === editKey ? edit.reason : ""} maxLength={500} disabled={data.pending !== null} onChange={(event) => setEdit(previous => ({ ...previous, reason: event.target.value }))} /></Field>
        </div>
      </Modal>
    </>
  );
}
