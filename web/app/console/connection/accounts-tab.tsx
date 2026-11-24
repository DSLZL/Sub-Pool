import { Plus, Search } from "lucide-react";
import { Badge, Button, Card, Input, Mono, Select, Switch, Table } from "@/components/ui";
import { checkText } from "@/lib/connection-data";
import type { ConnectionConsole } from "./use-connection-console";

export function AccountsTab({ view }: { view: ConnectionConsole["accounts"] }) {
  const {
    accs, pending, ready, locked, editName, status, updateAccount,
    candidates, discovered, q, setQ, openAdoption,
  } = view;
  return (
    <div className="space-y-4">
      <Card title="已纳管账号" pad={false}>
        <Table
          rows={accs}
          empty={pending === "read" ? "正在读取已纳管账号…" : !ready ? "账号读取未完成，请重新读取。" : "暂无已纳管账号。"}
          rowKey={(a) => a.id}
          cols={[
            { key: "n", label: "本地名称", render: (a) => <span className="font-medium" title={a.source_current ? a.identity_state : "原目标账号，历史保留"} role="button" tabIndex={locked ? -1 : 0} aria-label={`编辑${a.name}的本地名称`} aria-disabled={locked} onClick={() => editName(a)} onKeyDown={event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); editName(a); } }}>{a.name}</span> },
            { key: "u", label: "上游 ID", render: (a) => <Mono>{a.upstream_id}</Mono> },
            {
              key: "src",
              label: "额度来源",
              render: (a) => (
                <Select
                  className="w-32"
                  value={a.source_mode}
                  disabled={locked}
                  aria-label={`${a.name}的额度来源`}
                  onChange={(e) => { const value = e.target.value; if (value === "active" || value === "passive") updateAccount(a, a.monitoring, value); }}
                  options={[
                    { value: "active", label: status?.active_available ? "模拟查询" : "主动查询（暂不可用）" },
                    { value: "passive", label: "被动快照" },
                  ]}
                />
              ),
            },
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
      <Card title="发现可纳管账号" desc={pending === "discover" ? "正在完整读取上游分页…" : candidates && candidates.read.state !== "available" ? checkText(candidates.read) : "读取已保存目标的账号列表；读取不到不等于已删除。"} pad={false}>
        <div className="p-4 pb-0">
          <div className="relative max-w-xs">
            <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
            <Input className="pl-8" placeholder="按名称或 ID 筛选" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        </div>
        <div className="pt-3">
          <Table
            rows={discovered.filter((d) => !q || d.name.includes(q) || d.upstream_id.includes(q))}
            empty={!candidates ? "尚无完整发现结果；请先保存连接，再重新读取。" : candidates.read.state !== "available" ? "此项读取不可用，未显示部分账号。" : "本轮读取没有匹配账号。"}
            rowKey={(d) => d.upstream_id}
            cols={[
              { key: "n", label: "上游账号", render: (d) => <div><div>{d.name}</div><Mono>{d.upstream_id}</Mono></div> },
              { key: "t", label: "类型", render: (d) => d.type },
              { key: "s", label: "上游状态", render: (d) => <Badge tone={d.status === "active" ? "ok" : d.status === "error" ? "bad" : "muted"}>{d.status}</Badge> },
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
