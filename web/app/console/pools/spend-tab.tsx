import { glossary, uiCopy } from "@/lib/glossary";
import type { Participant } from "@sub-pool/contracts/participants";
import { DateTimeField } from "@/components/date-time-field";
import { UsageDayCollection } from "@/components/usage-day-collection";
import { Badge, Button, Callout, Card, Field, Grid, KV, Select, Table, Val } from "@/components/ui";
import type { PoolsConsole } from "./use-pools-console";

export function SpendTab({ view: spend }: { view: PoolsConsole["spend"] }) {
  const { scope, isAdmin, members, recorded, usageQuery, usageDraft, usageError, setUsageDraft, queryUsage, clearUsage, time } = spend;
  const view = recorded.data?.view ?? null;
  const matrixAccounts = usageQuery?.accountID ? scope.accounts.filter(account => account.id === usageQuery.accountID) : scope.accounts;
  const matrixMembers = usageQuery?.window.participant_id ? members.filter(member => member.id === usageQuery.window.participant_id) : members;
  const accountName = (id: string) => scope.accounts.find(account => account.id === id)?.name ?? id;

  return (
    <div className="space-y-4">
      <Card title="消费查询" desc={`按筛选读取${glossary.recordedConsumption}。`}>
        <Grid cols={2}>
          <Field label={uiCopy.participant}><Select aria-label="消费参与者" value={usageDraft.participantID} onChange={e => setUsageDraft({ ...usageDraft, participantID: e.target.value })} options={[{ value: "", label: "全部成员" }, ...members.map(member => ({ value: member.id, label: member.name }))]} /></Field>
          <Field label={uiCopy.account}><Select aria-label="消费账号" value={usageDraft.accountID} onChange={e => setUsageDraft({ ...usageDraft, accountID: e.target.value })} options={[{ value: "", label: "全部账号" }, ...scope.accounts.map(account => ({ value: account.id, label: account.name }))]} /></Field>
          <DateTimeField label="消费开始（含）" value={usageDraft.startAt} onChange={startAt => setUsageDraft({ ...usageDraft, startAt })} />
          <DateTimeField label="消费结束（不含）" value={usageDraft.endAt} onChange={endAt => setUsageDraft({ ...usageDraft, endAt })} />
        </Grid>
        <div className="mt-4 flex justify-end gap-2">
          <Button onClick={clearUsage}>清除筛选</Button>
          <Button variant="primary" disabled={!scope.accountCatalogAvailable || !scope.participantCatalogAvailable} onClick={queryUsage}>查询消费</Button>
        </div>
        {usageError && <Callout tone="warn" className="mt-3">{usageError}</Callout>}
        <UsageDayCollection account={scope.accounts.find(item => item.id === (usageQuery?.accountID || usageDraft.accountID))}
          connectionRevision={scope.connectionRevision} range={recorded.window} onChange={recorded.reload} />
      </Card>
      <Callout tone="unk">
        {recorded.error ? <>{recorded.error} <button type="button" className="underline" onClick={recorded.reload}>{uiCopy.reload}</button></> : view ? <>
          显示时段{glossary.recordedConsumption}。可补采。
          <details className="text-[12px] text-muted">
            <summary className="cursor-pointer">详情</summary>
            <p>{time(view.start_at)} → {time(view.end_at)}（结束不含）</p>
            {!!recorded.data?.missingAccounts.length && <p>{recorded.data.missingAccounts.length} 个账号待采集。</p>}
          </details>
        </> : recorded.ready ? (isAdmin ? "选择账号与日期。" : "联系管理员补采。") : `${glossary.recordedConsumption}读取中。`}
      </Callout>
      <Card title="成员消费" desc={`标准费用（USD）·${glossary.recordedConsumption}`} pad={false}>
        <Table
          rows={matrixMembers}
          rowKey={(m) => m.id}
          cols={[
            { key: "n", label: uiCopy.participant, render: (m) => m.name },
            ...matrixAccounts.map((a) => ({
              key: a.id,
              label: a.name,
              align: "right" as const,
              render: (m: Participant) => {
                const group = view?.groups.find(g => g.account_id === a.id && g.participant_id === m.id);
                return group ? <Val v={group.metrics.total_cost} /> : !view || recorded.data?.missingAccounts.includes(a.id) ? <Val v={null} /> : <span className="text-faint">无记录</span>;
              },
            })),
            { key: "t", label: "账号合计", align: "right", render: (m) => <span className="font-medium"><Val v={recorded.data?.byParticipant[m.id]?.summary.metrics.total_cost} /></span> },
          ]}
        />
      </Card>
      {isAdmin && !usageQuery?.window.participant_id && (
        <Card title="未归属费用">
          <Grid cols={3}>
            <KV cols={1} items={[["所选记录总量", <Val key="t" v={view?.summary.metrics.total_cost} prefix="$" />]]} />
            <KV cols={1} items={[["已归属", <Val key="a" v={view?.assigned.metrics.total_cost} prefix="$" />]]} />
            <KV cols={1} items={[["未归属", <span key="u" className="font-semibold text-warn"><Val v={view?.unassigned?.metrics.total_cost} prefix="$" /></span>]]} />
          </Grid>
          <ul className="mt-4 divide-y divide-line rounded-md border border-line">
            {(view?.groups.filter(g => g.participant_id === null) ?? []).map((r) => (
              <li key={`${r.account_id}-${r.basis}`} className="flex items-center gap-3 px-3 py-2.5 text-[13px]">
                <Badge tone="ok">已记录</Badge>
                <span className="flex-1">{accountName(r.account_id)} · {r.basis === "mapping_not_recorded" ? "缺少映射" : r.basis === "missing_user_id" ? "缺少用户 ID" : "绑定时段外"}</span>
                <Val v={r.metrics.total_cost} prefix="$" />
              </li>
            ))}
          </ul>
          <Callout tone="unk" className="mt-3">
          </Callout>
        </Card>
      )}
    </div>
  );
}
