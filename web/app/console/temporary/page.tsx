"use client";

import { uiCopy } from "@/lib/glossary";
import { useMemo, useState } from "react";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import { CursorNavigation } from "@/components/cursor-navigation";
import { Timestamp } from "@/components/timestamp";
import { Badge, Button, Callout, Card, Field, KV, PageHeader, Select, Table, Tabs, Toolbar } from "@/components/ui";
import { temporaryDetailRows } from "@/lib/temporary-data";
import { checkTemporaryDetails, readTemporaryHistory, TemporaryReadError, type TemporaryHistoryRow } from "@/lib/temporary-actions";
import { connectionFailure, displayTime } from "@/lib/connection-data";
import { useApp } from "@/lib/store";
import { useBoundRead } from "@/lib/use-bound-read";

const client = () => createTemporaryClient(window.location.origin);
const failure = (error: unknown) => error instanceof TemporaryReadError ? error.message : connectionFailure(error).text;
const history = (key: { boundary: TemporaryHistoryRow | null }, signal: AbortSignal) => readTemporaryHistory(client(), signal, key.boundary);

async function readRound(key: { row: TemporaryHistoryRow }, signal: AbortSignal) {
  const result = await client().round({ round_id: key.row.id }, { signal });
  checkTemporaryDetails(result, key.row);
  return result;
}

async function readRights(key: { round: string; account: string }, signal: AbortSignal) {
  const result = await client().rights({ round_id: key.round, account_id: key.account }, { signal });
  if (result.round_id !== key.round || result.account_id !== key.account) throw new TemporaryReadError("权益不符。");
  return result;
}

export default function TemporaryPage() {
  const { auth, isAdmin, displayTimezone } = useApp();
  const allowed = auth.status === "authenticated" && isAdmin;
  const [position, setPosition] = useState<{ epoch: number; boundary: TemporaryHistoryRow | null; generation: number }>();
  const active = position?.epoch === auth.epoch ? position : null;
  const pageKey = useMemo(() => allowed ? { epoch: auth.epoch, boundary: active?.boundary ?? null, generation: active?.generation ?? 0 } : null,
    [active, allowed, auth.epoch]);
  const page = useBoundRead(pageKey, history);
  const [selection, setSelection] = useState({ key: pageKey, id: "" });
  const selected = page?.value?.rounds.find(row => selection.key === pageKey && row.id === selection.id) ?? page?.value?.rounds[0];
  const detailKey = useMemo(() => pageKey && selected ? { pageKey, row: selected } : null, [pageKey, selected]);
  const detail = useBoundRead(detailKey, readRound);
  const [tab, setTab] = useState("round");
  const [rightsSelection, setRights] = useState<{ key: typeof detailKey; account: string } | null>(null);
  const rightsKey = useMemo(() => tab === "settle" && detailKey && rightsSelection?.key === detailKey
    ? { round: detailKey.row.id, account: rightsSelection.account } : null, [detailKey, rightsSelection, tab]);
  const rights = useBoundRead(rightsKey, readRights);
  const round = detail?.value?.round;
  const rows = temporaryDetailRows(detail?.value ?? null);
  const error = page?.error ?? detail?.error ?? rights?.error;
  function move(boundary: TemporaryHistoryRow | null) {
    setPosition({ epoch: auth.epoch, boundary, generation: (active?.generation ?? 0) + 1 });
    setRights(null);
  }

  return <>
    <PageHeader title={uiCopy.temporaryHistory} helpTopic="temporary" />
    {!allowed ? <Callout tone="warn">仅管理员可读。</Callout> :
      error ? <Callout tone="warn">{failure(error)}</Callout> :
      !page ? <Callout tone="info">读取轮次…</Callout> :
      !selected ? <Callout tone="info">暂无轮次。</Callout> : null}
    {allowed && <>
      <Toolbar>
        {selected && <Field label="查看轮次">
          <Select value={selected.id} onChange={event => { setSelection({ key: pageKey, id: event.target.value }); setRights(null); }}
            options={(page?.value?.rounds ?? []).map(row => ({ value: row.id, label: `${row.id} · ${displayTime(row.started_at, displayTimezone, false)} · ${row.exit ? "已退出" : "未记录退出"}` }))} />
        </Field>}
        <Button onClick={() => move(active?.boundary ?? null)}>{uiCopy.reload}</Button>
      </Toolbar>
      <CursorNavigation after={active?.boundary?.id ?? ""} next={page?.value?.next_after ?? null} onChange={after => {
        if (!after) move(null);
        else {
          const boundary = page?.value?.rounds.find(row => row.id === after);
          if (boundary) move(boundary);
        }
      }} />
      {selected && <Tabs value={tab} onChange={setTab} tabs={[{ id: "round", label: "轮次记录" }, { id: "settle", label: "权益记录" }]} />}
      {selected && !detail && <Callout tone="info">读取轮次…</Callout>}
      {round && tab === "round" && <Card>
        <div className="space-y-3">
          <KV items={[["所选轮次", round.id], ["模式", round.mode === "carry" ? "结转" : "不结转"], ["开始", <Timestamp key="started" value={round.started_at} />], ["截止", <Timestamp key="deadline" value={round.deadline} />],
            ["退出", round.exit ? <>{round.exit.reason} · <Timestamp value={round.exit.exited_at} /></> : "未记录退出"], ["来源与环境", `${round.scope.source_id} · 隔离模拟 · ${round.scope.simulation.epoch}`]]} />
          <Table rows={round.scope.accounts.flatMap(account => account.members.map(member => ({ account, member })))} rowKey={({ account, member }) => account.account_id + ":" + member.participant_id}
            cols={[{ key: "a", label: "账号 / 原周期", render: ({ account }) => <>#{account.upstream_id} · <Timestamp short value={account.settlement_period.start_at} /> — <Timestamp short value={account.settlement_period.end_at} /></> },
              { key: "m", label: "成员", render: ({ member }) => `#${member.user_id}` },
              { key: "cap", label: "上限 / 基线（钱包 USD）", render: ({ account, member }) => `${round.caps.find(cap => cap.account_id === account.account_id && cap.participant_id === member.participant_id)?.amount ?? "未记录"} / ${member.baseline.numerator}/${member.baseline.denominator}` },
              { key: "p", label: "周期", render: ({ account }) => account.periods.map((period, index) => <span key={period.kind}>{index ? " · " : ""}{period.kind} · <Timestamp short value={period.start_at} /> — <Timestamp short value={period.end_at} /></span>) }]} />
          {detail?.value?.reminders.enabled && <Badge tone="muted">曾启用提醒</Badge>}
        </div>
      </Card>}
      {round && tab === "settle" && <Card title="结算权益" pad={false}>
        <Table rows={rows} rowKey={row => row.key} empty="暂无权益。" cols={[
          { key: "a", label: uiCopy.account, render: row => row.account },
          { key: "m", label: "成员", render: row => row.member },
          { key: "v", label: "权益调整", align: "right", render: row => row.adjustment },
          { key: "s", label: uiCopy.status, render: row => row.state },
          { key: "why", label: "依据", render: row => <>{row.reason} · 原周期 <Timestamp short value={row.period.end_at} /></> },
          { key: "record", label: "记录", render: row => <Button size="sm" onClick={() => setRights({ key: detailKey, account: row.accountID })}>查看记录</Button> },
        ]} />
        {rights?.value && <KV cols={1} items={[
          ["周期结束", <Timestamp key="period" value={rights.value.period?.end_at} />],
          ["已记录权益", Object.entries(rights.value.adjustments).map(([id, value]) => `${id} · ${value}`).join("、") || "无"],
        ]} />}
      </Card>}
    </>}
  </>;
}
