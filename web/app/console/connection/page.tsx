"use client";

import { glossary } from "@/lib/glossary";
import { Button, Callout, Empty, PageHeader, Tabs } from "@/components/ui";
import { AccountsTab } from "./accounts-tab";
import { CapabilityTab } from "./capability-tab";
import { ConnectionDialogs } from "./connection-dialogs";
import { ConnectionTab } from "./connection-tab";
import { CollectionTab, SettingsTab } from "./settings-tabs";
import { useConnectionConsole } from "./use-connection-console";

export default function ConnectionPage() {
  const view = useConnectionConsole();
  if (!view.isAdmin) return <Empty title="当前身份无权管理连接与采集" />;

  const { tab, pending, notice, canReload, reload, changeTab } = view;
  return (
    <>
      <PageHeader
        title="连接与采集"
        desc={`配置 ${glossary.sub2api} 连接。`} helpTopic="connection"
        actions={canReload && <Button disabled={!!pending} onClick={reload}>{pending === "read" ? "读取中…" : "重新读取"}</Button>}
      />
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      <Tabs value={tab} onChange={changeTab} tabs={[
        { id: "conn", label: `${glossary.sub2api} 连接` },
        { id: "cap", label: "能力检查" },
        { id: "acc", label: "账号纳管" },
        { id: "job", label: "采集设置" },
        { id: "sys", label: "系统设置" },
      ]} />
      {tab === "conn" && <ConnectionTab view={view.connection} />}
      {tab === "cap" && <CapabilityTab view={view.capability} />}
      {tab === "acc" && <AccountsTab view={view.accounts} />}
      {tab === "job" && <CollectionTab view={view.collection} />}
      {tab === "sys" && <SettingsTab view={view.settings} />}
      <ConnectionDialogs view={view.dialogs} />
    </>
  );
}
