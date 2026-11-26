import { glossary, uiCopy } from "@/lib/glossary";
import { ShieldCheck } from "lucide-react";
import { Badge, Button, Callout, CapBadge, Card, Mono, Table } from "@/components/ui";
import { grantNames, type ConnectionConsole } from "./use-connection-console";

export function CapabilityTab({ view }: { view: ConnectionConsole["capability"] }) {
  const {
    grantView, pending, time, locked, saved, capabilities, reloadGrants, openGrant, runTest,
  } = view;
  return (
    <div className="space-y-4">
      <Card title="生产写入授权" actions={<Button size="sm" disabled={!!pending} onClick={reloadGrants}>重新读取授权</Button>}>
        {!grantView ? <p className="text-[13px] text-muted">授权读取中。</p> : (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[13px]">紧急禁写</span>
              <Badge tone={grantView.upstream_writes_disabled ? "warn" : "muted"}>{grantView.upstream_writes_disabled ? "已开启" : "未开启"}</Badge>
            </div>
            {grantView.upstream_writes_disabled && <Callout tone="warn">紧急禁写暂停  {glossary.sub2api}   写入。</Callout>}
            {!grantView.source_id && <p className="text-[13px] text-muted">请先保存 {glossary.sub2api} 连接地址。</p>}
            {grantView.grants.map(({ capability, grant }) => {
              const active = !!grant && grant.revoked_at === null;
              const name = grantNames[capability];
              return (
                <div key={capability} className="flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2 text-[13px]">{name}<Badge tone={active ? "ok" : "muted"}>{active ? "已授权" : grant ? "已撤销" : "未授权"}</Badge></div>
                    {grant && <p className="flex flex-wrap gap-x-2 text-[12px] text-muted">
                      <span className="break-all">授权人 {grant.granted_by}</span><span>{time(grant.granted_at)}</span>
                    </p>}
                    {grant?.revoked_at && <p className="flex flex-wrap gap-x-2 text-[12px] text-muted">
                      <span className="break-all">撤销人 {grant.revoked_by}</span><span>{time(grant.revoked_at)}</span>
                    </p>}
                  </div>
                  <Button size="sm" disabled={locked || !grantView.source_id} aria-label={(active ? "撤销" : "开启") + name + "授权"}
                    onClick={() => openGrant(capability)}>
                    {active ? "撤销授权" : "开启授权"}
                  </Button>
                </div>
              );
            })}
          </div>
        )}
      </Card>
      <Card
        title="能力状态"
        desc={`实例版本 ${saved?.diagnostic?.version ?? "未知"} · 运行模式 ${saved?.diagnostic?.run_mode ?? "未知"}`}
        actions={
          <Button size="sm" icon={<ShieldCheck size={13} />} disabled={locked || !saved?.base_url} onClick={() => runTest(true)}>
            重新检查
          </Button>
        }
        pad={false}
      >
        <Table
          rows={capabilities}
          rowKey={(c) => c.id}
          cols={[
            {
              key: "n", label: "能力", render: (c) => (
                <div>
                  <div className="font-medium">{c.name}</div>
                  <details className="text-[12px] text-muted">
                    <summary className="cursor-pointer">详情</summary>
                    <div className="break-all"><Mono>{c.id}</Mono></div>
                    {c.details && <p>{c.details}</p>}
                  </details>
                </div>
              ),
            },
            { key: "s", label: uiCopy.status, render: (c) => <CapBadge c={c.state} /> },
            { key: "note", label: "说明", render: (c) => <span className="text-[12.5px] text-muted">{c.note}</span> },
          ]}
        />
        <div className="border-t border-line p-4">
          <Callout tone="info">写入须授权。</Callout>
        </div>
      </Card>
    </div>
  );
}
