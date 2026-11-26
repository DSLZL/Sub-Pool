import { uiCopy } from "@/lib/glossary";
import { Pencil } from "lucide-react";
import { CHART_COLORS, ShareBar } from "@/components/charts";
import { AdminButton, Badge, Button, Card, Empty, Grid } from "@/components/ui";
import type { PoolsConsole } from "./use-pools-console";

export function PoolsTab({ view }: { view: PoolsConsole["poolList"] }) {
  const { poolData, isAdmin, members, pending, opening, blocked, time, openPool, openWeeklyPool } = view;
  const { pools } = poolData;
  const colorOf = (id: string) => CHART_COLORS[Math.max(0, members.findIndex(member => member.id === id)) % CHART_COLORS.length];

  return (
    <>
      <Grid cols={2}>
        {pools.map((p) => {
          return (
            <Card
              key={p.pool_id}
              title={p.name}
              desc={`版本 v${p.revision} · 生效于 ${time(p.effective_at)}${p.enabled ? "" : " · 已停用"}${p.source_current ? "" : " · 历史来源"}`}
              actions={
                <>
                  {isAdmin && <Button size="sm" onClick={() => openWeeklyPool(p)}>周额度发放</Button>}
                  <AdminButton
                    size="sm"
                    icon={<Pencil size={13} />}
                    disabled={pending || opening || blocked}
                    onClick={() => void openPool(p)}
                  >
                    编辑额度池
                  </AdminButton>
                </>
              }
            >
              <div className="mb-3 flex flex-wrap gap-1.5">
                {p.accounts.map((a) => (
                  <Badge key={a.id} tone="brand">
                    {a.name}
                  </Badge>
                ))}
              </div>
              <ShareBar parts={p.participants.map((s) => ({ id: s.id, label: s.name, pct: s.share_percent, color: colorOf(s.id) }))} unallocated={p.unallocated_percent} />
              {p.unallocated_percent !== null && p.unallocated_percent !== "0" && <p className="mt-3 text-[12px] text-muted">未分配的 {p.unallocated_percent} % 未分配。</p>}
            </Card>
          );
        })}
      </Grid>
      {!poolData.ready && <Empty title={poolData.error ? uiCopy.readFailed : uiCopy.loading} />}
      {poolData.ready && !pools.length && <Empty title="暂无额度池"></Empty>}
    </>
  );
}
