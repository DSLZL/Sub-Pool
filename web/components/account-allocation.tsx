"use client";

import { glossary } from "@/lib/glossary";
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
  return <Card title="份额与已记录归属" desc="当前合同与历史记录分别展示；消费不按当前份额重算，也不代表钱包结算。">
    <div className="space-y-3">
      {!pools.allowed ? <Callout tone="unk">未授予额度池读取权限。</Callout> : !pools.ready ? <Callout tone="unk">{pools.error ?? "正在读取当前额度池…"}</Callout> : contracts.length > 1 ? <Callout tone="warn">当前账号的启用合同不唯一，份额需核对。</Callout> : contract ? <KV items={[
        ["当前额度池", contract.name], ["合同版本", `v${contract.revision}`], ["实际生效", time(contract.effective_at)],
        ["当前未分配份额", contract.unallocated_percent === null ? "当前授权不可见" : `${contract.unallocated_percent}%`],
      ]} /> : <Callout tone="unk">当前账号没有可确认的启用合同。</Callout>}
      {recorded.error ? <Callout tone="unk">{recorded.error}</Callout> : !recorded.ready ? <Callout tone="unk">正在读取当前账号的已记录归属…</Callout> : !view ? <Callout tone="unk">当前账号暂无可确认的消费。请在用量与费用核对账号和日期。</Callout> : <>
        <KV items={[["记录区间", `${time(view.start_at)} — ${time(view.end_at)}`],
          ["最近捕获", time(view.newest_capture_at)], [`当前可见${glossary.recordedConsumption}`, <Val key="cost" v={view.summary.metrics.total_cost} prefix="$" />]]} />
        <Callout tone="warn">本页显示最近一次可用记录中的费用。结算前需核对完整账期和扣费。</Callout>
      </>}
      <Table rows={rows} rowKey={row => row.id} cols={[
        { key: "member", label: "参与者", render: row => row.name },
        { key: "share", label: "当前合同份额", render: row => row.member ? <>{row.member.share_percent}% {!row.member.enabled && <Badge>已停用</Badge>}</> : <Badge tone="unk">未在当前合同</Badge> },
        { key: "cost", label: glossary.recordedConsumption, align: "right", render: row => <Val v={row.cost} prefix="$" /> },
      ]} />
      {view?.unassigned_visible && <Table rows={view.groups.filter(group => group.participant_id === null)} rowKey={row => `${row.account_id}:${row.basis}`} cols={[
        { key: "basis", label: "未归属记录依据", render: row => ({ local_binding_at_log_time: "已记录绑定", mapping_not_recorded: "未记录映射", missing_user_id: `${glossary.sub2api} 未提供用户`, outside_recorded_binding: "不在已记录绑定区间" })[row.basis] },
        { key: "cost", label: glossary.recordedConsumption, align: "right", render: row => <Val v={row.metrics.total_cost} prefix="$" /> },
      ]} />}
    </div>
  </Card>;
}
