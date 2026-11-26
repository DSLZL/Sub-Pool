"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Badge, Callout, Card, KV, Table, Val } from "@/components/ui";
import { displayTime } from "@/lib/connection-data";
import { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import type { usePoolData } from "@/lib/use-pool-data";
import { useRecordedUsage } from "@/lib/use-recorded-usage";

export function AccountAllocation({ accountID, pools }: { accountID: string; pools: ReturnType<typeof usePoolData> }) {
  const scope = useScope(), recorded = useRecordedUsage(scope, accountID);
  const { displayTimezone } = useApp();
  const time = (value: string | null) => displayTime(value, displayTimezone, false);
  const contracts = pools.pools.filter(pool => pool.enabled && pool.source_current && pool.accounts.some(account => account.id === accountID));
  const contract = contracts.length === 1 ? contracts[0] : null;
  const view = recorded.data?.view ?? null;
  const ids = new Set([...contract?.participants.map(member => member.id) ?? [], ...view?.groups.flatMap(group => group.participant_id ? [group.participant_id] : []) ?? []]);
  const rows = [...ids].map(id => {
    const member = contract?.participants.find(member => member.id === id);
    return { id, name: scope.participantCatalog.find(member => member.id === id)?.name ?? member?.name ?? id,
      member, cost: recorded.data?.byParticipant[id]?.summary.metrics.total_cost ?? null };
  });
  return <Card title="份额与归属">
    <div className="space-y-3">
      {!pools.allowed ? <Callout tone="unk">无池读取权限。</Callout> : !pools.ready ? <Callout tone="unk">{pools.error ?? "读取额度池…"}</Callout> : contracts.length > 1 ? <Callout tone="warn">合同冲突，请核对。</Callout> : contract ? <KV items={[
        [uiCopy.pool, contract.name], ["生效", time(contract.effective_at)],
        ["未分配份额", contract.unallocated_percent === null ? "无权查看" : `${contract.unallocated_percent}%`],
      ]} /> : <Callout tone="unk">暂无生效合同。</Callout>}
      {recorded.error ? <Callout tone="unk">{recorded.error}</Callout> : !recorded.ready ? <Callout tone="unk">{uiCopy.loading}</Callout> : !view ? <Callout tone="unk">暂无消费。</Callout> :
        <KV items={[["期间", `${time(view.start_at)} — ${time(view.end_at)}`],
          [glossary.recordedConsumption, <Val key="cost" v={view.summary.metrics.total_cost} prefix="$" />]]} />}
      <Table rows={rows} rowKey={row => row.id} cols={[
        { key: "member", label: uiCopy.participant, render: row => row.name },
        { key: "share", label: "当前合同份额", render: row => row.member ? <>{row.member.share_percent}% {!row.member.enabled && <Badge>已停用</Badge>}</> : <Badge tone="unk">未在当前合同</Badge> },
        { key: "cost", label: glossary.recordedConsumption, align: "right", render: row => <Val v={row.cost} prefix="$" /> },
      ]} />
      {view?.unassigned_visible && <Table rows={view.groups.filter(group => group.participant_id === null)} rowKey={row => `${row.account_id}:${row.basis}`} cols={[
        { key: "basis", label: "未归属原因", render: row => ({ local_binding_at_log_time: "已记录绑定", mapping_not_recorded: "未记录映射", missing_user_id: `${glossary.sub2api} 未提供用户`, outside_recorded_binding: "绑定时段外" })[row.basis] },
        { key: "cost", label: glossary.recordedConsumption, align: "right", render: row => <Val v={row.metrics.total_cost} prefix="$" /> },
      ]} />}
    </div>
  </Card>;
}
