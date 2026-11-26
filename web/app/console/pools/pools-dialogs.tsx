import { glossary, uiCopy } from "@/lib/glossary";
import { Search } from "lucide-react";
import { Badge, Button, Callout, Card, Checkbox, ConfirmModal, Empty, Field, Input, KV, Modal, Mono, Switch, Textarea } from "@/components/ui";
import { shareDraft } from "@/lib/pools-data";
import type { PoolsConsole } from "./use-pools-console";

export function PoolsDialogs({ view }: { view: PoolsConsole["dialogs"] }) {
  const {
    isAdmin, pending, blocked, stale, error, poolForm, editPool, editMembers, members, editAccounts, draft, editMember,
    userQ, upstreamUsers, candidateError, candidatesReady, exitTarget, exitAttempt, deleteTarget, deleteAttempt,
    setPoolForm, setDraft, setEditMember, setUserQ, saveShares, saveMember, exitMember, deleteMember,
    closePool, closeMember, closeExit, closeDelete,
  } = view;
  const participantName = (id: string) => editMembers.find(member => member.id === id)?.name
    ?? members.find(member => member.id === id)?.name
    ?? editPool?.participants.find(member => member.id === id)?.name ?? id;
  const draftState = shareDraft(draft);
  const draftInvalid = !draftState.valid, draftTotal = draftState.valid ? Number(draftState.total) : Infinity;

  return (
    <>
      {/* Edit pool and shares */}
      <Modal
        open={!!poolForm}
        onClose={closePool}
        title={editPool ? `编辑额度池 · ${editPool.name}` : "新建额度池"}
        footer={
          <>
            <Button disabled={pending} onClick={closePool}>取消</Button>
            <Button
              variant="primary"
              disabled={!poolForm?.name.trim() || !poolForm.accountIDs.length || draftInvalid || pending || blocked || stale || !!editPool && (!editPool.source_current || editPool.accounts.some(a => !a.identity_current))}
              onClick={() => void saveShares()}
            >
              {editPool ? "保存为新版本" : "创建额度池"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          {poolForm && <>
            <Field label="池名称"><Input disabled={pending || blocked || stale} value={poolForm.name} onChange={e => setPoolForm({ ...poolForm, name: e.target.value })} /></Field>
            <Card title={uiCopy.account} desc="一账号一池。">
              <div className="space-y-3">
                {editAccounts.map(account => <Checkbox key={account.id} label={`${account.name} · ${account.upstream_id}`} checked={poolForm.accountIDs.includes(account.id)} disabled={pending || blocked || stale}
                  onChange={checked => setPoolForm({ ...poolForm, accountIDs: checked ? [...poolForm.accountIDs, account.id] : poolForm.accountIDs.filter(id => id !== account.id) })} />)}
                {!editAccounts.length && <Callout tone="unk">请先纳管账号。</Callout>}
              </div>
            </Card>
            <Switch label="启用额度池" desc="停用保留账目。" checked={poolForm.enabled} disabled={pending || blocked || stale} onChange={enabled => setPoolForm({ ...poolForm, enabled })} />
          </>}
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
          {draftInvalid && <Callout tone="bad">合计 ≤100%，最多六位小数。</Callout>}
          {poolForm && (
            <Callout tone="warn" title="份额生效">
              按生效时间。
            </Callout>
          )}
          {error && <Callout tone="warn">{error}</Callout>}
        </div>
      </Modal>

      {/* Edit participant */}
      <Modal
        open={!!editMember}
        onClose={closeMember}
        title={editMember?.original ? "编辑参与者" : "新建参与者"}
        wide
        footer={
          <>
            <Button disabled={pending} onClick={closeMember}>取消</Button>
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
              <Field label="联系邮箱">
                <Input disabled={pending || blocked || stale} value={editMember.contact} onChange={(e) => setEditMember({ ...editMember, contact: e.target.value })} />
              </Field>
              <Field label="说明">
                <Textarea disabled={pending || blocked || stale} value={editMember.notes} onChange={(e) => setEditMember({ ...editMember, notes: e.target.value })} />
              </Field>
              <Switch label="启用参与者" desc="停用保留历史。" checked={editMember.enabled} disabled={pending || blocked || stale} onChange={enabled => setEditMember({ ...editMember, enabled })} />
            </div>
            <div>
              <Field label={`绑定 ${glossary.sub2api} 用户`} hint="请选用户。">
                <div className="relative">
                  <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
                  <Input className="pl-8" placeholder="用户 ID 或邮箱" disabled={pending || blocked || stale} maxLength={100} value={userQ} onChange={(e) => setUserQ(e.target.value)} />
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
                          <span className="flex-1 truncate text-muted">{u.email}{u.status === "disabled" ? `（${glossary.sub2api} 已停用）` : ""}</span>
                          {taken && <Badge>已绑定 {owner.name}</Badge>}
                          {selected && <Badge tone="brand">已选</Badge>}
                        </button>
                      </li>
                    );
                  })}
              </ul>
              {!candidatesReady && !candidateError && <Empty title={pending ? "提交中" : "查询用户…"} />}
              {candidatesReady && !upstreamUsers.length && <Empty title="暂无匹配用户" />}
              {candidateError && <Callout tone="warn">{candidateError}</Callout>}
              {editMember.original && (
                <Callout tone="warn" className="mt-3">
                  有历史，不能换绑。
                </Callout>
              )}
            </div>
          </div>
        )}
        {error && <Callout tone="warn">{error}</Callout>}
      </Modal>

      <ConfirmModal
        open={isAdmin && !!exitTarget}
        onClose={closeExit}
        onConfirm={exitMember}
        disabled={pending || stale && !exitAttempt || !isAdmin}
        title={`让 ${exitTarget?.name ?? ""} 退出分配`}
        confirmLabel={exitAttempt ? "核对退出结果" : "确认退出"}
        danger
      >
        <Callout tone="warn">{error ?? "释放份额并停用。保留账目。"}</Callout>
      </ConfirmModal>
      <ConfirmModal
        open={isAdmin && !!deleteTarget}
        onClose={closeDelete}
        onConfirm={deleteMember}
        disabled={pending || stale && !deleteAttempt || !isAdmin}
        title={`删除 ${deleteTarget?.name ?? ""}`}
        confirmLabel={deleteAttempt ? "核对删除结果" : "确认删除"}
        danger
      >
        <Callout tone="warn">{error ?? "删除本地参与者及绑定，保留审计记录。"}</Callout>
      </ConfirmModal>
    </>
  );
}
