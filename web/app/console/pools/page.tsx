"use client";

import { Plus } from "lucide-react";
import { AdminButton, Button, Callout, PageHeader, Tabs } from "@/components/ui";
import { HistoryTab } from "./history-tab";
import { ParticipantsTab } from "./participants-tab";
import { PoolsDialogs } from "./pools-dialogs";
import { PoolsTab } from "./pools-tab";
import { SpendTab } from "./spend-tab";
import { usePoolsConsole } from "./use-pools-console";
import { WeeklyWalletDialog } from "./weekly-wallet";

export default function PoolsPage() {
  const view = usePoolsConsole();
  const { tab, setTab, opening, pending, blocked, problem, reload, openPool, openMember, weeklyPool } = view;

  return (
    <>
      <PageHeader
        title="参与者与额度池"
        desc="查看参与者与份额。" helpTopic="shares"
        actions={
          tab === "members" ? (
            <AdminButton variant="primary" icon={<Plus size={14} />} disabled={pending || opening || blocked || !view.canCreateMember} onClick={() => void openMember()}>
              新建参与者
            </AdminButton>
          ) : tab === "pools" ? (
            <AdminButton variant="primary" icon={<Plus size={14} />} disabled={pending || opening || blocked || !view.canCreatePool} onClick={() => void openPool()}>
              新建额度池
            </AdminButton>
          ) : null
        }
      />
      {(problem || opening) && <Callout tone="unk">
        {problem ?? "正在读取当前编辑依据…"}
        {problem && <div className="mt-2"><Button disabled={pending || opening} onClick={() => void reload()}>重新读取</Button></div>}
      </Callout>}
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "pools", label: "额度池与份额" },
          { id: "members", label: "参与者" },
          { id: "spend", label: "消费归属" },
          { id: "history", label: "分配依据历史" },
        ]}
      />

      {tab === "pools" && <PoolsTab view={view.poolList} />}
      {weeklyPool && <WeeklyWalletDialog key={weeklyPool.id} mode="settings" pool={weeklyPool} time={view.time} close={view.closeWeeklyPool} />}
      {tab === "members" && <ParticipantsTab view={view.participantList} />}
      {tab === "spend" && <SpendTab view={view.spend} />}
      {tab === "history" && <HistoryTab view={view.history} />}
      <PoolsDialogs view={view.dialogs} />
    </>
  );
}
