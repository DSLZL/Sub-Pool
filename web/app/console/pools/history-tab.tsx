import { CursorNavigation } from "@/components/cursor-navigation";
import { Timestamp } from "@/components/timestamp";
import { Card, Empty, Mono, Table } from "@/components/ui";
import type { PoolsConsole } from "./use-pools-console";

export function HistoryTab({ view: poolData }: { view: PoolsConsole["history"] }) {
  return (
    <>
      {poolData.ready ? (
        <Card title="份额版本" desc="历史观测保留当时使用的份额依据；这里不是编辑历史合同的入口。" pad={false}>
          <Table
            rows={poolData.history}
            rowKey={(h) => h.id}
            cols={[
              { key: "v", label: "版本", render: (h) => <Mono>v{h.revision}</Mono> },
              { key: "p", label: "额度池", render: (h) => h.name },
              { key: "at", label: "时间", render: (h) => <Timestamp short className="num" value={h.effective_at} /> },
              { key: "c", label: "当时依据", render: (h) => `${h.accounts.map(a => a.name).join("、")}；${h.participants.map(m => `${m.name} ${m.share_percent}%`).join("；") || "没有分配成员"}${h.unallocated_percent === null ? "；仅显示授权成员" : `；未分配 ${h.unallocated_percent}%`}` },
              { key: "e", label: "生效边界", render: (h) => <span className="text-muted"><Timestamp short value={h.effective_at} /> ≤ 日志时间{h.ended_at ? <> &lt; <Timestamp short value={h.ended_at} /></> : "；当前合同"}</span> },
              { key: "b", label: "操作者", render: () => "未提供" },
            ]}
          />
        </Card>
      ) : <Empty title={poolData.error ? "历史依据读取未完成" : "正在读取历史依据…"} />}
      <CursorNavigation after={poolData.historyAfter} next={poolData.historyNext} first="0" onChange={poolData.changeHistoryPage} />
    </>
  );
}
