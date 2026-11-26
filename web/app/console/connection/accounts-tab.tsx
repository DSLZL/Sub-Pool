import { glossary, uiCopy } from "@/lib/glossary";
import { Plus, Search } from "lucide-react";
import { Badge, Button, Card, Input, Mono, Switch, Table } from "@/components/ui";
import { checkText } from "@/lib/connection-data";
import type { ConnectionConsole } from "./use-connection-console";

export function AccountsTab({ view }: { view: ConnectionConsole["accounts"] }) {
  const {
    accs, pending, ready, locked, editName, updateAccount,
    candidates, discovered, q, setQ, openAdoption,
  } = view;
  return (
    <div className="space-y-4">
      <Card title="已纳管账号" pad={false}>
        <Table
          rows={accs}
          empty={pending === "read" ? uiCopy.loading : !ready ? uiCopy.readFailed : "暂无账号。"}
          rowKey={(a) => a.id}
          cols={[
            { key: "n", label: "本地名称", render: (a) => <span className="font-medium" title={a.source_current ? a.identity_state : "旧账号"} role="button" tabIndex={locked ? -1 : 0} aria-label={`编辑${a.name}的本地名称`} aria-disabled={locked} onClick={() => editName(a)} onKeyDown={event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); editName(a); } }}>{a.name}</span> },
            { key: "u", label: `${glossary.sub2api} ID`, render: (a) => <Mono>{a.upstream_id}</Mono> },
            {
              key: "m",
              label: "本地监控",
              render: (a) => (
                <Switch
                  checked={a.monitoring}
                  ariaLabel={`${a.name}的本地监控`}
                  disabled={locked}
                  onChange={(v) => updateAccount(a, v, a.source_mode)}
                />
              ),
            },
          ]}
        />
      </Card>
      <Card title="纳管账号" desc={pending === "discover" ? `正在完整读取 ${glossary.sub2api} 分页…` : candidates && candidates.read.state !== "available" ? checkText(candidates.read) : ""} pad={false}>
        <div className="p-4 pb-0">
          <div className="relative max-w-xs">
            <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
            <Input className="pl-8" placeholder="按名称或 ID 筛选" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        </div>
        <div className="pt-3">
          <Table
            rows={discovered.filter((d) => !q || d.name.includes(q) || d.upstream_id.includes(q))}
            empty={!candidates ? "先保存连接。" : candidates.read.state !== "available" ? uiCopy.readFailed : "暂无匹配账号。"}
            rowKey={(d) => d.upstream_id}
            cols={[
              { key: "n", label: `${glossary.sub2api} 账号`, render: (d) => <div><div>{d.name}</div><Mono>{d.upstream_id}</Mono></div> },
              { key: "t", label: "类型", render: (d) => d.type },
              { key: "s", label: `${glossary.sub2api} 状态`, render: (d) => <Badge tone={d.status === "active" ? "ok" : d.status === "error" ? "bad" : "muted"}>{d.status}</Badge> },
              {
                key: "a",
                label: "",
                render: (d) =>
                  d.managed ? (
                    <Badge tone="brand">已纳管</Badge>
                  ) : d.unavailableReason ? (
                    <span className="text-[12px] text-warn">{d.unavailableReason}</span>
                  ) : (
                    <Button
                      size="sm"
                      disabled={locked}
                      icon={<Plus size={13} />}
                      onClick={() => openAdoption(d)}
                    >
                      纳管
                    </Button>
                  ),
              },
            ]}
          />
        </div>
      </Card>
    </div>
  );
}
