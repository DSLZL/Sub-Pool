"use client";

import { useMemo, useState } from "react";
import { Plus, Pencil, Search, UserMinus } from "lucide-react";
import {
  AdminButton,
  Badge,
  Button,
  Callout,
  Card,
  ConfirmModal,
  Field,
  Grid,
  Input,
  KV,
  Modal,
  Mono,
  PageHeader,
  Table,
  Tabs,
  Textarea,
  Val,
} from "@/components/ui";
import { ShareBar, CHART_COLORS } from "@/components/charts";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { accountName, attribution, costRows, participantName, pools as initialPools, shareHistory, upstreamUsers, type Participant, type Pool } from "@/lib/mock";

export default function PoolsPage() {
  const { toast } = useApp();
  const { isAdmin, participants: scoped, canAccount } = useScope();
  const [tab, setTab] = useState("pools");
  const [pools, setPools] = useState<Pool[]>(initialPools);
  const [members, setMembers] = useState<Participant[]>(scoped);
  const [editPool, setEditPool] = useState<Pool | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [editMember, setEditMember] = useState<Partial<Participant> | null>(null);
  const [userQ, setUserQ] = useState("");
  const [exitTarget, setExitTarget] = useState<Participant | null>(null);

  const colorOf = (pid: string) => CHART_COLORS[members.findIndex((m) => m.id === pid) % CHART_COLORS.length];

  const draftTotal = Object.values(draft).reduce((a, b) => a + (parseFloat(b) || 0), 0);
  const draftInvalid = draftTotal > 100 || Object.values(draft).some((v) => v !== "" && (isNaN(parseFloat(v)) || parseFloat(v) < 0));

  const spendMatrix = useMemo(() => {
    const m: Record<string, Record<string, number>> = {};
    costRows.forEach((r) => {
      if (r.participant === "—" || r.standard === null) return;
      m[r.participant] ??= {};
      m[r.participant][r.account] = (m[r.participant][r.account] ?? 0) + r.standard;
    });
    return m;
  }, []);

  return (
    <>
      <PageHeader
        title="参与者与额度池"
        tag="S2A-POOL-001 ~ 009"
        desc="额度池是本系统的本地组织方式，不等于 Sub2API 原生分组。份额是合同约定，保存份额不会直接写入钱包。"
        actions={
          tab === "members" ? (
            <AdminButton variant="primary" icon={<Plus size={14} />} onClick={() => setEditMember({ status: "active" })}>
              新建参与者
            </AdminButton>
          ) : null
        }
      />
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

      {tab === "pools" && (
        <Grid cols={2}>
          {pools.map((p) => {
            const used = p.shares.reduce((a, b) => a + b.pct, 0);
            return (
              <Card
                key={p.id}
                title={p.name}
                desc={`版本 v${p.version} · 更新于 ${p.updatedAt}`}
                actions={
                  <AdminButton
                    size="sm"
                    icon={<Pencil size={13} />}
                    onClick={() => {
                      setEditPool(p);
                      setDraft(Object.fromEntries(members.filter((m) => m.status === "active").map((m) => [m.id, String(p.shares.find((s) => s.participant === m.id)?.pct ?? "")])));
                    }}
                  >
                    编辑份额
                  </AdminButton>
                }
              >
                <div className="mb-3 flex flex-wrap gap-1.5">
                  {p.accounts.map((a) => (
                    <Badge key={a} tone="brand">
                      {accountName(a)}
                    </Badge>
                  ))}
                </div>
                <ShareBar parts={p.shares.map((s) => ({ label: participantName(s.participant), pct: s.pct, color: colorOf(s.participant) }))} />
                {used < 100 && <p className="mt-3 text-[12px] text-muted">未分配的 {100 - used}% 不会自动归给任何成员。</p>}
              </Card>
            );
          })}
        </Grid>
      )}

      {tab === "members" && (
        <Card pad={false}>
          <Table
            rows={members}
            rowKey={(m) => m.id}
            cols={[
              { key: "n", label: "参与者", render: (m) => <div><div className="font-medium">{m.name}</div><div className="text-[12px] text-muted">{m.note || "—"}</div></div> },
              { key: "u", label: "绑定上游用户", render: (m) => <div><Mono>{m.upstreamUserId}</Mono><div className="text-[12px] text-muted">{m.upstreamEmail}</div></div> },
              { key: "c", label: "联系邮箱", render: (m) => m.contact || <span className="text-faint">未填写</span> },
              {
                key: "p",
                label: "所在池",
                render: (m) => (
                  <div className="flex flex-wrap gap-1">
                    {pools
                      .filter((p) => p.shares.some((s) => s.participant === m.id))
                      .map((p) => (
                        <Badge key={p.id}>
                          {p.name} {p.shares.find((s) => s.participant === m.id)!.pct}%
                        </Badge>
                      ))}
                  </div>
                ),
              },
              { key: "s", label: "状态", render: (m) => <Badge tone={m.status === "active" ? "ok" : "muted"} dot>{m.status === "active" ? "活跃" : m.status === "disabled" ? "已停用" : "已退出"}</Badge> },
              {
                key: "a",
                label: "",
                render: (m) =>
                  isAdmin && (
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" onClick={() => setEditMember(m)} aria-label="编辑">
                        <Pencil size={13} />
                      </Button>
                      {m.status === "active" && (
                        <Button size="sm" variant="ghost" onClick={() => setExitTarget(m)} aria-label="退出">
                          <UserMinus size={13} />
                        </Button>
                      )}
                    </div>
                  ),
              },
            ]}
          />
        </Card>
      )}

      {tab === "spend" && (
        <div className="space-y-4">
          <Card title="按参与者与账号的消费" desc="标准费用口径（USD）· 本周期 · 钱包是全局的，消费按账号和实际计费路径记录" pad={false}>
            <Table
              rows={members.filter((m) => m.status === "active")}
              rowKey={(m) => m.id}
              cols={[
                { key: "n", label: "参与者", render: (m) => m.name },
                ...["a1", "a2", "a3", "a4"].filter(canAccount).map((a) => ({
                  key: a,
                  label: accountName(a),
                  align: "right" as const,
                  render: (m: Participant) => (spendMatrix[m.id]?.[a] !== undefined ? <Val v={spendMatrix[m.id][a]} /> : <span className="text-faint">无记录</span>),
                })),
                { key: "t", label: "跨账号合计", align: "right", render: (m) => <span className="font-medium"><Val v={m.cycleSpend} /></span> },
              ]}
            />
          </Card>
          {isAdmin && (
            <Card title="未归属消耗与差异" desc="区分已证实与待证实原因；未知消耗不会自动摊给成员">
              <Grid cols={3}>
                <KV cols={1} items={[["上游可见总量", <Val key="t" v={attribution.totalStandard} prefix="$" />]]} />
                <KV cols={1} items={[["已归属", <Val key="a" v={attribution.attributed} prefix="$" />]]} />
                <KV cols={1} items={[["未归属", <span key="u" className="font-semibold text-warn"><Val v={attribution.unattributed} prefix="$" /></span>]]} />
              </Grid>
              <ul className="mt-4 divide-y divide-line rounded-md border border-line">
                {attribution.reasons.map((r) => (
                  <li key={r.label} className="flex items-center gap-3 px-3 py-2.5 text-[13px]">
                    <Badge tone={r.confirmed ? "ok" : "unk"}>{r.confirmed ? "已证实" : "待证实"}</Badge>
                    <span className="flex-1">{r.label}</span>
                    <Val v={r.amount} prefix="$" />
                  </li>
                ))}
              </ul>
              <Callout tone="unk" className="mt-3">
                实例外使用只是可能因素，无法从百分比与费用差异确证其发生次数或金额。
              </Callout>
            </Card>
          )}
        </div>
      )}

      {tab === "history" && (
        <Card title="份额版本" desc="历史观测保留当时使用的份额依据；这里不是编辑历史合同的入口。" pad={false}>
          <Table
            rows={shareHistory}
            rowKey={(h) => `${h.pool}-${h.version}`}
            cols={[
              { key: "v", label: "版本", render: (h) => <Mono>v{h.version}</Mono> },
              { key: "p", label: "额度池", render: (h) => pools.find((p) => p.id === h.pool)?.name },
              { key: "at", label: "时间", render: (h) => <span className="num">{h.at}</span> },
              { key: "c", label: "变化", render: (h) => h.change },
              { key: "e", label: "生效边界", render: (h) => <span className="text-muted">{h.effective}</span> },
              { key: "b", label: "操作者", render: (h) => h.by },
            ]}
          />
        </Card>
      )}

      {/* Edit shares */}
      <Modal
        open={!!editPool}
        onClose={() => setEditPool(null)}
        title={`编辑份额 · ${editPool?.name ?? ""}`}
        footer={
          <>
            <Button onClick={() => setEditPool(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={draftInvalid}
              onClick={() => {
                if (!editPool) return;
                const shares = Object.entries(draft)
                  .filter(([, v]) => parseFloat(v) > 0)
                  .map(([participant, v]) => ({ participant, pct: parseFloat(v) }));
                setPools((ps) => ps.map((p) => (p.id === editPool.id ? { ...p, shares, version: p.version + 1, updatedAt: "2026-11-04 14:20" } : p)));
                setEditPool(null);
                toast({ tone: "ok", title: `已保存为 v${editPool.version + 1}`, body: "自现在起的新观测使用新份额；既有观测保留原依据。" });
              }}
            >
              保存为新版本
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          {Object.keys(draft).map((pid) => (
            <div key={pid} className="flex items-center gap-3">
              <span className="w-28 text-[13px]">{participantName(pid)}</span>
              <Input type="number" min={0} max={100} step={0.5} value={draft[pid]} onChange={(e) => setDraft({ ...draft, [pid]: e.target.value })} className="w-28" aria-label={`${participantName(pid)} 份额`} />
              <span className="text-muted">%</span>
            </div>
          ))}
          <div className={`flex justify-between border-t border-line pt-3 text-[13px] ${draftTotal > 100 ? "text-bad" : ""}`}>
            <span>合计</span>
            <span className="num font-semibold">
              {draftTotal}% {draftTotal < 100 && <span className="font-normal text-muted">（未分配 {100 - draftTotal}%）</span>}
            </span>
          </div>
          {draftTotal > 100 && <Callout tone="bad">份额合计不能超过 100%。</Callout>}
          {editPool?.id === "p1" && (
            <Callout tone="warn" title="存在未完成业务">
              此池有进行中的临时轮次 R-07。保存新份额前请确认轮次处理方式；修改不会重新分摊既有费用。
            </Callout>
          )}
        </div>
      </Modal>

      {/* Edit participant */}
      <Modal
        open={!!editMember}
        onClose={() => setEditMember(null)}
        title={editMember?.id ? "编辑参与者" : "新建参与者"}
        wide
        footer={
          <>
            <Button onClick={() => setEditMember(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={!editMember?.name || !editMember?.upstreamUserId}
              onClick={() => {
                if (!editMember) return;
                if (editMember.id) setMembers((ms) => ms.map((m) => (m.id === editMember.id ? ({ ...m, ...editMember } as Participant) : m)));
                else
                  setMembers((ms) => [
                    ...ms,
                    {
                      ...(editMember as Participant),
                      id: `m${ms.length + 1}`,
                      balance: null,
                      frozen: null,
                      balanceReadAt: null,
                      recommendation: null,
                      recState: "insufficient",
                      lastApplied: null,
                      cycleSpend: null,
                      contact: editMember.contact ?? "",
                      note: editMember.note ?? "",
                    },
                  ]);
                toast({ tone: "ok", title: "参与者已保存", body: "参与者不会因此获得本系统或上游登录权限。" });
                setEditMember(null);
              }}
            >
              保存
            </Button>
          </>
        }
      >
        {editMember && (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div className="space-y-3">
              <Field label="名称">
                <Input value={editMember.name ?? ""} onChange={(e) => setEditMember({ ...editMember, name: e.target.value })} />
              </Field>
              <Field label="联系邮箱" hint="仅作联系记录，不代表已订阅通知。">
                <Input value={editMember.contact ?? ""} onChange={(e) => setEditMember({ ...editMember, contact: e.target.value })} />
              </Field>
              <Field label="说明">
                <Textarea value={editMember.note ?? ""} onChange={(e) => setEditMember({ ...editMember, note: e.target.value })} />
              </Field>
            </div>
            <div>
              <Field label="绑定 Sub2API 用户" hint="必须明确选择，系统不会按同名自动绑定。">
                <div className="relative">
                  <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
                  <Input className="pl-8" placeholder="搜索用户 ID 或邮箱" value={userQ} onChange={(e) => setUserQ(e.target.value)} />
                </div>
              </Field>
              <ul className="mt-2 max-h-56 divide-y divide-line overflow-y-auto rounded-md border border-line">
                {upstreamUsers
                  .filter((u) => !userQ || u.id.includes(userQ) || u.email.includes(userQ))
                  .map((u) => {
                    const taken = u.bound && u.bound !== editMember.id;
                    const selected = editMember.upstreamUserId === u.id;
                    return (
                      <li key={u.id}>
                        <button
                          disabled={!!taken}
                          onClick={() => setEditMember({ ...editMember, upstreamUserId: u.id, upstreamEmail: u.email })}
                          className={`flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] disabled:cursor-not-allowed disabled:opacity-50 ${selected ? "bg-brand-soft" : "hover:bg-surface-2"}`}
                        >
                          <Mono>{u.id}</Mono>
                          <span className="flex-1 truncate text-muted">{u.email}</span>
                          {taken && <Badge>已绑定 {participantName(u.bound!)}</Badge>}
                          {selected && <Badge tone="brand">已选</Badge>}
                        </button>
                      </li>
                    );
                  })}
              </ul>
              {editMember.id && (editMember.cycleSpend ?? 0) > 0 && (
                <Callout tone="warn" className="mt-3">
                  该参与者已有消费历史，换绑上游用户需先处理历史与未结清权益。
                </Callout>
              )}
            </div>
          </div>
        )}
      </Modal>

      <ConfirmModal
        open={!!exitTarget}
        onClose={() => setExitTarget(null)}
        onConfirm={() => {
          setMembers((ms) => ms.map((m) => (m.id === exitTarget?.id ? { ...m, status: "exited" } : m)));
          toast({ tone: "ok", title: `${exitTarget?.name} 已退出当前分配`, body: "历史用量与分配依据已保留。" });
        }}
        title={`让 ${exitTarget?.name ?? ""} 退出分配`}
        confirmLabel="确认退出"
        danger
      >
        <KV
          cols={1}
          items={[
            ["处理方式", "退出当前池分配，释放的份额成为未分配部分"],
            ["保留内容", "历史用量、份额版本、余额应用记录"],
            ["不会发生", "删除上游用户、清零钱包、重分旧费用"],
          ]}
        />
        {exitTarget?.id === "m2" && <Callout tone="warn">该成员在进行中的临时轮次 R-07 内有待结算权益，退出后相关权益将挂起等待人工处理。</Callout>}
      </ConfirmModal>
    </>
  );
}
