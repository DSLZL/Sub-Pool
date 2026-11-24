import { glossary } from "@/lib/glossary";
import { Pencil, Trash2, UserMinus } from "lucide-react";
import { Badge, Button, Card, Empty, Mono, Table } from "@/components/ui";
import type { PoolsConsole } from "./use-pools-console";

export function ParticipantsTab({ view }: { view: PoolsConsole["participantList"] }) {
  const { scope, pools, isAdmin, members, pending, opening, blocked, exitAttempt, deleteAttempt, openMember, openExit, openDelete } = view;
  if (!scope.participantCatalogAvailable) {
    return <Empty title={scope.participantCatalogError ? "参与者读取未完成" : "正在读取参与者…"} />;
  }

  return (
    <Card pad={false}>
      <Table
        rows={members}
        rowKey={(m) => m.id}
        cols={[
          { key: "n", label: "参与者", render: (m) => <div><div className="font-medium">{m.name}</div><div className="text-[12px] text-muted">{m.notes ?? "—"}</div></div> },
          { key: "u", label: `绑定 ${glossary.sub2api} 用户`, render: (m) => <div><Mono>{m.binding.upstream_user_id}</Mono><div className="text-[12px] text-muted">{m.binding.identity?.email ?? "身份详情未提供"}{m.source_current ? "" : " · 绑定来源已变化"}</div></div> },
          { key: "c", label: "联系邮箱", render: (m) => m.contact || <span className="text-faint">{m.contact === null ? "未提供" : "未填写"}</span> },
          {
            key: "p",
            label: "所在池",
            render: (m) => (
              <div className="flex flex-wrap gap-1">
                {pools.map(pool => {
                  const share = pool.participants.find(member => member.id === m.id);
                  return share && (
                    <Badge key={pool.pool_id}>
                      {pool.name} {share.share_percent}%
                    </Badge>
                  );
                })}
              </div>
            ),
          },
          { key: "s", label: "状态", render: (m) => <Badge tone={m.enabled ? "ok" : "muted"} dot>{m.enabled ? "活跃" : "已停用"}</Badge> },
          {
            key: "a",
            label: "",
            render: (m) =>
              isAdmin && (
                <div className="flex justify-end gap-1">
                  <Button size="sm" variant="ghost" disabled={pending || opening || blocked} onClick={() => void openMember(m)} aria-label="编辑">
                    <Pencil size={13} />
                  </Button>
                  {(m.enabled || pools.some(p => p.participants.some(member => member.id === m.id))) && (
                    <Button size="sm" variant="ghost" disabled={pending || opening || blocked && !exitAttempt} onClick={() => void openExit(m)} aria-label="退出">
                      <UserMinus size={13} />
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" disabled={pending || opening || blocked && !deleteAttempt} onClick={() => void openDelete(m)} aria-label="删除参与者">
                    <Trash2 size={13} />
                  </Button>
                </div>
              ),
          },
        ]}
      />
    </Card>
  );
}
