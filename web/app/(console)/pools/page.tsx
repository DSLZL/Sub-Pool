"use client";

import { useEffect, useRef, useState } from "react";
import { Plus, Pencil, Search, UserMinus } from "lucide-react";
import {
  AdminButton,
  Badge,
  Button,
  Callout,
  Card,
  ConfirmModal,
  Empty,
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
import { createAccountsClient, createConnectionClient, createParticipantsClient, createPoolsClient } from "@sub-pool/contracts/client";
import { participantCreateInput, participantUpdateInput, type Participant } from "@sub-pool/contracts/participants";
import { poolUpdateInput, type PoolContract } from "@sub-pool/contracts/pools";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { ConnectionConfiguration } from "@sub-pool/contracts/connection";
import { connectionFailure, displayTime, readManagedPages } from "@/lib/connection-data";
import { readCandidatePages, readParticipantPages, shareDraft } from "@/lib/pools-data";
import { usePoolData } from "@/lib/use-pool-data";
import { useRecordedUsage } from "@/lib/use-recorded-usage";

type MemberForm = { original: Participant | null; name: string; contact: string; notes: string; upstreamID: string; enabled: boolean; sourceID: string; connectionRevision: string };
type NativeUser = NonNullable<Participant["binding"]["identity"]>;

export default function PoolsPage() {
  const { toast, refreshSession, timezone } = useApp();
  const scope = useScope();
  const { isAdmin, participantCatalog: members } = scope;
  const poolData = usePoolData(), recorded = useRecordedUsage(scope);
  const { pools, history: shareHistory } = poolData;
  const [tab, setTab] = useState("pools");
  const [editPool, setEditPool] = useState<PoolContract | null>(null);
  const [editMembers, setEditMembers] = useState<Participant[]>([]);
  const [editAccounts, setEditAccounts] = useState<ManagedAccount[]>([]);
  const [editConfig, setEditConfig] = useState<ConnectionConfiguration | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [editMember, setEditMember] = useState<MemberForm | null>(null);
  const [userQ, setUserQ] = useState("");
  const [upstreamUsers, setUpstreamUsers] = useState<NativeUser[]>([]);
  const [candidateError, setCandidateError] = useState<string | null>(null);
  const [candidatesReady, setCandidatesReady] = useState(false);
  const [exitTarget, setExitTarget] = useState<Participant | null>(null);
  const [pending, setPending] = useState(false), [opening, setOpening] = useState(false), [blocked, setBlocked] = useState(false), [stale, setStale] = useState(false);
  const [error, setError] = useState<string | null>(null), [notice, setNotice] = useState<string | null>(null);
  const request = useRef({ alive: false, generation: 0, abort: new AbortController(), submitting: false, opening: false });
  useEffect(() => {
    request.current.alive = true;
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); };
  }, []);
  const begin = () => {
    request.current.abort.abort(); request.current.abort = new AbortController();
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  };
  const current = (generation: number) => request.current.alive && request.current.generation === generation;
  const time = (value: string | null | undefined) => displayTime(value, timezone);
  const colorOf = (pid: string) => CHART_COLORS[Math.max(0, members.findIndex((m) => m.id === pid)) % CHART_COLORS.length];
  const accountName = (id: string) => scope.accounts.find(a => a.id === id)?.name ?? id;
  const participantName = (id: string) => editMembers.find(m => m.id === id)?.name ?? members.find(m => m.id === id)?.name ?? editPool?.participants.find(m => m.id === id)?.name ?? id;
  const draftState = shareDraft(draft);
  const draftInvalid = !draftState.valid, draftTotal = draftState.valid ? Number(draftState.total) : Infinity;
  const view = recorded.data?.view ?? null;
  const problem = notice ?? poolData.error ?? scope.participantCatalogError;
  const reload = async () => { if (!request.current.submitting) await Promise.all([poolData.reload(), scope.reloadAccounts(), scope.reloadParticipants()]); };
  const fail = (cause: unknown, confirmed: string | null = null, dispatched = false) => {
    const failure = connectionFailure(cause), text = confirmed ? `${confirmed}；回读尚未完成，请重新读取核对，勿重复提交。` : !dispatched && failure.status === 0 ? "暂时无法完整读取编辑依据，请重新读取后再操作。" : failure.text;
    setError(text); setNotice(text);
    if (confirmed || dispatched && failure.status === 0) setBlocked(true);
    if ([404, 409].includes(failure.status)) setStale(true);
    if ([401, 403].includes(failure.status)) { setEditMember(null); setEditPool(null); setExitTarget(null); void refreshSession(); }
  };
  const openPool = async (pool: PoolContract) => {
    if (!isAdmin || blocked || request.current.submitting || request.current.opening) return;
    request.current.opening = true; setOpening(true); setError(null); setStale(false);
    const ticket = begin();
    try {
      const [value, config] = await Promise.all([createPoolsClient(window.location.origin).get({ id: pool.pool_id }, { signal: ticket.signal }), createConnectionClient(window.location.origin).get({}, { signal: ticket.signal })]);
      const [accounts, participants] = await Promise.all([
        readManagedPages(after => createAccountsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal: ticket.signal }), config.revision),
        readParticipantPages(after => createParticipantsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal: ticket.signal })),
      ]);
      if (!current(ticket.generation)) return;
      setEditPool(value); setEditConfig(config); setEditAccounts(accounts); setEditMembers(participants);
      const ids = new Set([...participants.filter(m => m.enabled).map(m => m.id), ...value.participants.map(m => m.id)]);
      setDraft(Object.fromEntries([...ids].map(id => [id, value.participants.find(m => m.id === id)?.share_percent ?? ""])));
    } catch (cause) { if (current(ticket.generation)) fail(cause); }
    finally { if (current(ticket.generation)) { request.current.opening = false; setOpening(false); } }
  };
  const openMember = async (member: Participant | null = null) => {
    if (!isAdmin || blocked || request.current.submitting || request.current.opening || !scope.participantCatalogAvailable) return;
    request.current.opening = true; setOpening(true); setError(null); setStale(false); setUserQ("");
    const ticket = begin();
    try {
      const [config, value] = await Promise.all([createConnectionClient(window.location.origin).get({}, { signal: ticket.signal }), member ? createParticipantsClient(window.location.origin).get({ id: member.id }, { signal: ticket.signal }) : Promise.resolve(null)]);
      if (!current(ticket.generation)) return;
      if (!config.source_id || !config.credential_set) { setNotice("请先在连接页保存可用的管理连接。"); return; }
      setEditMember({ original: value, name: value?.name ?? "", contact: value?.contact ?? "", notes: value?.notes ?? "", enabled: value?.enabled ?? true,
        upstreamID: value?.source_current ? value.binding.upstream_user_id : "", sourceID: config.source_id, connectionRevision: config.revision });
    } catch (cause) { if (current(ticket.generation)) fail(cause); }
    finally { if (current(ticket.generation)) { request.current.opening = false; setOpening(false); } }
  };
  const candidateSource = editMember?.sourceID, candidateRevision = editMember?.connectionRevision;
  useEffect(() => {
    const abort = new AbortController(); let alive = true;
    setUpstreamUsers([]); setCandidateError(null); setCandidatesReady(false);
    if (!isAdmin || !candidateSource || !candidateRevision || pending) return () => { alive = false; abort.abort(); };
    if ([...userQ].length > 100 || new TextEncoder().encode(userQ).length > 256) { setCandidateError("搜索内容过长，请缩短后再查询。"); return () => { alive = false; abort.abort(); }; }
    void readCandidatePages(page => createParticipantsClient(window.location.origin).candidates({ source_id: candidateSource, revision: candidateRevision, page, search: userQ }, { signal: abort.signal }), candidateSource, candidateRevision)
      .then(value => { if (alive) { setUpstreamUsers(value.users); setCandidatesReady(value.read.state === "available"); if (value.read.state !== "available") setCandidateError(`当前连接无法确认用户读取能力（${value.read.state} / ${value.read.reason}）。`); } })
      .catch(cause => { if (alive) { const failure = connectionFailure(cause); setCandidateError(failure.status === 0 ? "候选用户未完整读取，请重新查询。" : failure.text); if ([401, 403].includes(failure.status)) { setEditMember(null); void refreshSession(); } } });
    return () => { alive = false; abort.abort(); };
  }, [isAdmin, candidateSource, candidateRevision, userQ, pending, refreshSession]);
  const saveShares = async () => {
    if (!isAdmin || !editPool || !editConfig?.source_id || blocked || stale || request.current.submitting) return;
    const input = poolUpdateInput.safeParse({ id: editPool.pool_id, revision: editPool.revision, name: editPool.name, enabled: editPool.enabled, source_id: editConfig.source_id, connection_revision: editConfig.revision,
      accounts: editPool.accounts.map(a => ({ id: a.id, revision: editAccounts.find(current => current.id === a.id)?.revision })),
      participants: Object.entries(draft).filter(([, value]) => value !== "").map(([id, share_percent]) => ({ id, share_percent, revision: editMembers.find(m => m.id === id)?.revision })) });
    if (!input.success) { setError("请核对份额格式、合计及当前成员。百分比最多六位小数，合计不超过 100%。"); return; }
    request.current.submitting = true; setPending(true); setError(null);
    const ticket = begin(); let confirmed: string | null = null;
    try {
      const value = await createPoolsClient(window.location.origin).update(input.data, { signal: ticket.signal });
      confirmed = `合同 v${value.revision} 已保存`;
      const saved = await createPoolsClient(window.location.origin).contract({ id: value.id }, { signal: ticket.signal });
      if (saved.pool_id !== value.pool_id || saved.revision !== value.revision) throw new Error("Saved contract mismatch");
      if (!current(ticket.generation)) return;
      setEditPool(null); setNotice(null); toast({ tone: "ok", title: confirmed, body: `实际生效 ${saved.effective_at}；既有记录保留原依据，未写入钱包。` });
      void poolData.reload();
    } catch (cause) { if (current(ticket.generation)) fail(cause, confirmed, true); }
    finally { if (current(ticket.generation)) { request.current.submitting = false; setPending(false); } }
  };
  const saveMember = async () => {
    if (!isAdmin || !editMember || blocked || stale || request.current.submitting) return;
    const fields = { source_id: editMember.sourceID, connection_revision: editMember.connectionRevision, upstream_id: editMember.upstreamID, name: editMember.name, contact: editMember.contact, notes: editMember.notes, enabled: editMember.enabled };
    if (!participantCreateInput.safeParse(fields).success) { setError("请核对名称、联系信息、说明及明确选择的上游用户；名称最多 128 字，内容不能含控制字符。"); return; }
    const original = editMember.original;
    request.current.submitting = true; setPending(true); setError(null);
    const ticket = begin(); let confirmed: string | null = null;
    try {
      const client = createParticipantsClient(window.location.origin);
      const result = original ? await client.update(participantUpdateInput.parse({ ...fields, id: original.id, revision: original.revision }), { signal: ticket.signal }) : await client.create(fields, { signal: ticket.signal });
      if (!result.participant) { if (current(ticket.generation)) setError(`当前连接未通过用户读取核对（${result.read?.state} / ${result.read?.reason}），未保存参与者。`); return; }
      confirmed = "参与者已保存";
      const saved = await client.get({ id: result.participant.id }, { signal: ticket.signal });
      if (saved.revision !== result.participant.revision) throw new Error("Saved participant changed");
      if (!current(ticket.generation)) return;
      setEditMember(null); setNotice(null); toast({ tone: "ok", title: confirmed, body: "服务端已读回；参与者不因此获得任何登录权限。" });
      void scope.reloadParticipants(); void poolData.reload();
    } catch (cause) { if (current(ticket.generation)) fail(cause, confirmed, true); }
    finally { if (current(ticket.generation)) { request.current.submitting = false; setPending(false); } }
  };

  return (
    <>
      <PageHeader
        title="参与者与额度池"
        tag="S2A-POOL-001 ~ 009"
        desc="额度池是本系统的本地组织方式，不等于 Sub2API 原生分组。份额是合同约定，保存份额不会直接写入钱包。"
        actions={
          tab === "members" ? (
            <AdminButton variant="primary" icon={<Plus size={14} />} disabled={pending || opening || blocked || !scope.participantCatalogAvailable} onClick={() => void openMember()}>
              新建参与者
            </AdminButton>
          ) : null
        }
      />
      {(problem || opening) && <Callout tone="unk">{problem ?? "正在读取当前编辑依据…"} {problem && <Button disabled={pending || opening} onClick={() => void reload()}>重新读取</Button>}</Callout>}
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
            return (
              <Card
                key={p.pool_id}
                title={p.name}
                desc={`版本 v${p.revision} · 生效于 ${time(p.effective_at)}${p.enabled ? "" : " · 已停用"}${p.source_current ? "" : " · 历史来源"}`}
                actions={
                  <AdminButton
                    size="sm"
                    icon={<Pencil size={13} />}
                    disabled={pending || opening || blocked}
                    onClick={() => void openPool(p)}
                  >
                    编辑份额
                  </AdminButton>
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
                {p.unallocated_percent !== null && p.unallocated_percent !== "0" && <p className="mt-3 text-[12px] text-muted">未分配的 {p.unallocated_percent}% 不会自动归给任何成员。</p>}
              </Card>
            );
          })}
        </Grid>
      )}
      {tab === "pools" && !poolData.ready && <Empty title={poolData.error ? "额度池读取未完成" : "正在读取额度池…"} />}
      {tab === "pools" && poolData.ready && !pools.length && <Empty title="当前授权范围没有额度池">现有界面未提供池创建或账号成员变更入口。</Empty>}

      {tab === "members" && scope.participantCatalogAvailable && (
        <Card pad={false}>
          <Table
            rows={members}
            rowKey={(m) => m.id}
            cols={[
              { key: "n", label: "参与者", render: (m) => <div><div className="font-medium">{m.name}</div><div className="text-[12px] text-muted">{m.notes ?? "—"}</div></div> },
              { key: "u", label: "绑定上游用户", render: (m) => <div><Mono>{m.binding.upstream_user_id}</Mono><div className="text-[12px] text-muted">{m.binding.identity?.email ?? "身份详情未提供"}{m.source_current ? "" : " · 绑定来源已变化"}</div></div> },
              { key: "c", label: "联系邮箱", render: (m) => m.contact || <span className="text-faint">{m.contact === null ? "未提供" : "未填写"}</span> },
              {
                key: "p",
                label: "所在池",
                render: (m) => (
                  <div className="flex flex-wrap gap-1">
                    {pools
                      .filter((p) => p.participants.some((s) => s.id === m.id))
                      .map((p) => (
                        <Badge key={p.pool_id}>
                          {p.name} {p.participants.find((s) => s.id === m.id)!.share_percent}%
                        </Badge>
                      ))}
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
                      {m.enabled && (
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
      {tab === "members" && !scope.participantCatalogAvailable && <Empty title={scope.participantCatalogError ? "参与者读取未完成" : "正在读取参与者…"} />}

      {tab === "spend" && (
        <div className="space-y-4">
          <Callout tone="unk">{recorded.error ?? (view ? `仅为已记录捕获，${view.start_at} 至 ${view.end_at}；捕获时间 ${time(view.oldest_capture_at)} 至 ${time(view.newest_capture_at)}。${recorded.data?.missingAccounts.length ? `另有 ${recorded.data.missingAccounts.length} 个可见账号没有可确认捕获。` : ""}不代表全量账期，不能用于结算。` : recorded.ready ? "尚无可确认的消费捕获；没有记录不代表消费为零。" : "正在读取已记录消费…")}</Callout>
          <Card title="按参与者与账号的消费" desc="日志标准费用（USD）· 同一已记录区间 · 钱包是全局的，消费按账号和实际计费路径记录" pad={false}>
            <Table
            rows={members}
              rowKey={(m) => m.id}
              cols={[
                { key: "n", label: "参与者", render: (m) => m.name },
                ...scope.accounts.map((a) => ({
                  key: a.id,
                  label: a.name,
                  align: "right" as const,
                  render: (m: Participant) => {
                    const group = view?.groups.find(g => g.account_id === a.id && g.participant_id === m.id);
                    return group ? <Val v={group.metrics.total_cost} /> : !view || recorded.data?.missingAccounts.includes(a.id) ? <Val v={null} /> : <span className="text-faint">无记录</span>;
                  },
                })),
                { key: "t", label: "已记录账号合计", align: "right", render: (m) => <span className="font-medium"><Val v={recorded.data?.byParticipant[m.id]?.summary.metrics.total_cost} /></span> },
              ]}
            />
          </Card>
          {isAdmin && (
            <Card title="未归属消耗与差异" desc="同一捕获区间内的已记录依据；不是钱包扣除额，未知消耗不会自动摊给成员">
              <Grid cols={3}>
                <KV cols={1} items={[["所选记录总量", <Val key="t" v={view?.summary.metrics.total_cost} prefix="$" />]]} />
                <KV cols={1} items={[["已归属", <Val key="a" v={view?.assigned.metrics.total_cost} prefix="$" />]]} />
                <KV cols={1} items={[["未归属", <span key="u" className="font-semibold text-warn"><Val v={view?.unassigned?.metrics.total_cost} prefix="$" /></span>]]} />
              </Grid>
              <ul className="mt-4 divide-y divide-line rounded-md border border-line">
                {(view?.groups.filter(g => g.participant_id === null) ?? []).map((r) => (
                  <li key={`${r.account_id}-${r.basis}`} className="flex items-center gap-3 px-3 py-2.5 text-[13px]">
                    <Badge tone="ok">已记录</Badge>
                    <span className="flex-1">{accountName(r.account_id)} · {r.basis === "mapping_not_recorded" ? "当时未记录归属映射" : r.basis === "missing_user_id" ? "日志未提供用户 ID" : "日志时间不在已记录绑定内"}</span>
                    <Val v={r.metrics.total_cost} prefix="$" />
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

      {tab === "history" && poolData.ready && (
        <Card title="份额版本" desc="历史观测保留当时使用的份额依据；这里不是编辑历史合同的入口。" pad={false}>
          <Table
            rows={shareHistory}
            rowKey={(h) => h.id}
            cols={[
              { key: "v", label: "版本", render: (h) => <Mono>v{h.revision}</Mono> },
              { key: "p", label: "额度池", render: (h) => h.name },
              { key: "at", label: "时间", render: (h) => <span className="num">{time(h.effective_at)}</span> },
              { key: "c", label: "当时依据", render: (h) => `${h.accounts.map(a => a.name).join("、")}；${h.participants.map(m => `${m.name} ${m.share_percent}%`).join("；") || "没有分配成员"}${h.unallocated_percent === null ? "；仅显示授权成员" : `；未分配 ${h.unallocated_percent}%`}` },
              { key: "e", label: "生效边界", render: (h) => <span className="text-muted">{h.effective_at} ≤ 日志时间{h.ended_at ? ` < ${h.ended_at}` : "；当前合同"}</span> },
              { key: "b", label: "操作者", render: () => "未提供" },
            ]}
          />
        </Card>
      )}
      {tab === "history" && !poolData.ready && <Empty title={poolData.error ? "历史依据读取未完成" : "正在读取历史依据…"} />}

      {/* Edit shares */}
      <Modal
        open={!!editPool}
        onClose={() => { if (!request.current.submitting) setEditPool(null); }}
        title={`编辑份额 · ${editPool?.name ?? ""}`}
        footer={
          <>
            <Button disabled={pending} onClick={() => setEditPool(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={draftInvalid || pending || blocked || stale || !editPool?.source_current || editPool.accounts.some(a => !a.identity_current)}
              onClick={() => void saveShares()}
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
              <Input type="number" min={0} max={100} step={0.000001} disabled={pending || blocked || stale} value={draft[pid]} onChange={(e) => setDraft({ ...draft, [pid]: e.target.value })} className="w-28" aria-label={`${participantName(pid)} 份额`} />
              <span className="text-muted">%</span>
            </div>
          ))}
          <div className={`flex justify-between border-t border-line pt-3 text-[13px] ${draftTotal > 100 ? "text-bad" : ""}`}>
            <span>合计</span>
            <span className="num font-semibold">
              {draftState.total ?? "格式待核对"}% {draftTotal < 100 && <span className="font-normal text-muted">（未分配 {draftState.unallocated}%）</span>}
            </span>
          </div>
          {draftInvalid && <Callout tone="bad">份额格式需为最多六位小数的非负百分比，合计不能超过 100%。</Callout>}
          {editPool && (
            <Callout tone="warn" title="生效边界与能力范围">
              新合同从服务端事务确定的实际时间生效，按已记录的原生日志时间关联，既有记录不重分费用。临时权益与钱包结算尚不可用；来源或账号身份失效时不能保存。
            </Callout>
          )}
          {error && <Callout tone="warn">{error}</Callout>}
        </div>
      </Modal>

      {/* Edit participant */}
      <Modal
        open={!!editMember}
        onClose={() => { if (!request.current.submitting) setEditMember(null); }}
        title={editMember?.original ? "编辑参与者" : "新建参与者"}
        wide
        footer={
          <>
            <Button disabled={pending} onClick={() => setEditMember(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={!editMember?.name || !editMember?.upstreamID || pending || blocked || stale}
              onClick={() => void saveMember()}
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
                <Input disabled={pending || blocked || stale} value={editMember.name} onChange={(e) => setEditMember({ ...editMember, name: e.target.value })} />
              </Field>
              <Field label="联系邮箱" hint="仅作联系记录，不代表已订阅通知。">
                <Input disabled={pending || blocked || stale} value={editMember.contact} onChange={(e) => setEditMember({ ...editMember, contact: e.target.value })} />
              </Field>
              <Field label="说明">
                <Textarea disabled={pending || blocked || stale} value={editMember.notes} onChange={(e) => setEditMember({ ...editMember, notes: e.target.value })} />
              </Field>
            </div>
            <div>
              <Field label="绑定 Sub2API 用户" hint="必须明确选择，系统不会按同名自动绑定。">
                <div className="relative">
                  <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
                  <Input className="pl-8" placeholder="搜索用户 ID 或邮箱" disabled={pending || blocked || stale} maxLength={100} value={userQ} onChange={(e) => setUserQ(e.target.value)} />
                </div>
              </Field>
              <ul className="mt-2 max-h-56 divide-y divide-line overflow-y-auto rounded-md border border-line">
                {upstreamUsers
                  .map((u) => {
                    const owner = members.find(m => m.binding.source_id === editMember.sourceID && m.binding.upstream_user_id === u.upstream_id);
                    const taken = owner && owner.id !== editMember.original?.id;
                    const selected = editMember.upstreamID === u.upstream_id;
                    return (
                      <li key={u.upstream_id}>
                        <button
                          disabled={!!taken || pending || blocked || stale}
                          onClick={() => setEditMember({ ...editMember, upstreamID: u.upstream_id })}
                          className={`flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] disabled:cursor-not-allowed disabled:opacity-50 ${selected ? "bg-brand-soft" : "hover:bg-surface-2"}`}
                        >
                          <Mono>{u.upstream_id}</Mono>
                          <span className="flex-1 truncate text-muted">{u.email}{u.status === "disabled" ? "（上游已停用）" : ""}</span>
                          {taken && <Badge>已绑定 {owner.name}</Badge>}
                          {selected && <Badge tone="brand">已选</Badge>}
                        </button>
                      </li>
                    );
                  })}
              </ul>
              {!candidatesReady && !candidateError && <Empty title={pending ? "提交中，候选读取已暂停" : "正在读取候选用户…"} />}
              {candidatesReady && !upstreamUsers.length && <Empty title="没有匹配的可见用户" />}
              {candidateError && <Callout tone="warn">{candidateError}</Callout>}
              {editMember.original && (
                <Callout tone="warn" className="mt-3">
                  换绑由服务端核对；已有消费或池合同历史时不可换绑。停用不释放绑定，未结权益需单独核对。
                </Callout>
              )}
            </div>
          </div>
        )}
        {error && <Callout tone="warn">{error}</Callout>}
      </Modal>

      <ConfirmModal
        open={!!exitTarget}
        onClose={() => setExitTarget(null)}
        onConfirm={() => false}
        disabled
        title={`让 ${exitTarget?.name ?? ""} 退出分配`}
        confirmLabel="确认退出"
        danger
      >
        <KV
          cols={1}
          items={[
            ["处理方式", "需同时核对当前池份额释放与未结权益，当前不可执行"],
            ["保留内容", "已记录用量和份额版本不会被此页面删除"],
            ["不会发生", "删除上游用户、清零钱包、重分旧费用"],
          ]}
        />
        <Callout tone="warn">退出尚不可用：当前缺少份额释放与未结权益核对的完整业务能力，普通停用不能代替退出。现有界面亦未提供独立停用入口。</Callout>
      </ConfirmModal>
    </>
  );
}
