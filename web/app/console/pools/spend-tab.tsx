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
      <Card title="消费查询" desc="按参与者、账号和时间范围读取已记录消费。">
        <Grid cols={2}>
          <Field label="参与者"><Select aria-label="消费参与者" value={usageDraft.participantID} onChange={e => setUsageDraft({ ...usageDraft, participantID: e.target.value })} options={[{ value: "", label: "全部可见参与者" }, ...members.map(member => ({ value: member.id, label: member.name }))]} /></Field>
          <Field label="账号"><Select aria-label="消费账号" value={usageDraft.accountID} onChange={e => setUsageDraft({ ...usageDraft, accountID: e.target.value })} options={[{ value: "", label: "全部可见账号" }, ...scope.accounts.map(account => ({ value: account.id, label: account.name }))]} /></Field>
          <DateTimeField label="消费起始时间（含）" value={usageDraft.startAt} onChange={startAt => setUsageDraft({ ...usageDraft, startAt })} />
          <DateTimeField label="消费结束时间（不含）" value={usageDraft.endAt} onChange={endAt => setUsageDraft({ ...usageDraft, endAt })} />
        </Grid>
        <div className="mt-4 flex justify-end gap-2">
          <Button onClick={clearUsage}>清除筛选</Button>
          <Button variant="primary" disabled={!scope.accountCatalogAvailable || !scope.participantCatalogAvailable} onClick={queryUsage}>查询消费</Button>
        </div>
        {usageError && <Callout tone="warn" className="mt-3">{usageError}</Callout>}
        <UsageDayCollection account={scope.accounts.find(item => item.id === (usageQuery?.accountID || usageDraft.accountID))}
          connectionRevision={scope.connectionRevision} range={recorded.window} onChange={recorded.reload} />
      </Card>
      <Callout tone="unk">{recorded.error ?? (view ? `查询 ${time(view.query_start_at ?? view.start_at)} 至 ${time(view.query_end_at ?? view.end_at)}（结束不含）；仅为所选已记录捕获，区间 ${time(view.start_at)} 至 ${time(view.end_at)}；捕获时间 ${time(view.oldest_capture_at)} 至 ${time(view.newest_capture_at)}。${view.history ? `按 ${view.history.selections.length} 个已记录时段汇总。` : ""}${recorded.data?.missingAccounts.length ? `另有 ${recorded.data.missingAccounts.length} 个可见账号没有可确认捕获。` : ""}不代表全量账期，不能用于结算。` : recorded.ready ? "尚无可确认的消费捕获；没有记录不代表消费为零。" : "正在读取已记录消费…")}</Callout>
      <Card title="按参与者与账号的消费" desc="日志标准费用（USD）· 所选已记录捕获 · 钱包是全局的，消费按账号和实际计费路径记录" pad={false}>
        <Table
          rows={matrixMembers}
          rowKey={(m) => m.id}
          cols={[
            { key: "n", label: "参与者", render: (m) => m.name },
            ...matrixAccounts.map((a) => ({
              key: a.id,
              label: a.name,
              align: "right" as const,
              render: (m: Participant) => {
                const group = view?.groups.find(g => g.account_id === a.id && g.participant_id === m.id);
                return group ? <Val v={group.metrics.total_cost} /> : !view || recorded.data?.missingAccounts.includes(a.id) ? <Val v={null} /> : <span className="text-faint">无记录</span>;
              },
            })),
            { key: "t", label: "已记录账号合计", align: "right", render: (m) => <span className="font-medium"><Val v={recorded.data?.byParticipant[m.id]?.summary.metrics.total_cost} /></span> },
          ]}
        />
      </Card>
      {isAdmin && !usageQuery?.window.participant_id && (
        <Card title="未归属消耗与差异" desc="同一捕获区间内的已记录依据；不是钱包扣除额，未知消耗不会自动摊给成员">
          <Grid cols={3}>
            <KV cols={1} items={[["所选记录总量", <Val key="t" v={view?.summary.metrics.total_cost} prefix="$" />]]} />
            <KV cols={1} items={[["已归属", <Val key="a" v={view?.assigned.metrics.total_cost} prefix="$" />]]} />
            <KV cols={1} items={[["未归属", <span key="u" className="font-semibold text-warn"><Val v={view?.unassigned?.metrics.total_cost} prefix="$" /></span>]]} />
          </Grid>
          <ul className="mt-4 divide-y divide-line rounded-md border border-line">
            {(view?.groups.filter(g => g.participant_id === null) ?? []).map((r) => (
              <li key={`${r.account_id}-${r.basis}`} className="flex items-center gap-3 px-3 py-2.5 text-[13px]">
                <Badge tone="ok">已记录</Badge>
                <span className="flex-1">{accountName(r.account_id)} · {r.basis === "mapping_not_recorded" ? "当时未记录归属映射" : r.basis === "missing_user_id" ? "日志未提供用户 ID" : "日志时间不在已记录绑定内"}</span>
                <Val v={r.metrics.total_cost} prefix="$" />
              </li>
            ))}
          </ul>
          <Callout tone="unk" className="mt-3">
            实例外使用只是可能因素，无法从百分比与费用差异确证其发生次数或金额。
          </Callout>
        </Card>
      )}
    </div>
  );
}
