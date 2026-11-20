"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, Pencil, Trash2 } from "lucide-react";
import { ORPCError } from "@orpc/client";
import { createUsersClient } from "@sub-pool/contracts/client";
import { authErrorData, newPassword } from "@sub-pool/contracts/auth";
import { readFeature, type ManagedUser, type ReadFeature, type UserSummary } from "@sub-pool/contracts/users";
import { Badge, Button, Callout, Checkbox, ConfirmModal, Field, Input, Modal, Mono, PageHeader, Table, Card, Switch, Section, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { requestFailureText } from "@/lib/request-id";
import { checkUserSave } from "@/lib/users-data";

type Form = Pick<ManagedUser, "id" | "revision" | "username" | "enabled" | "features" | "account_ids" | "participant_ids">;
const labels: Record<ReadFeature, string> = {
  overview: "概览", accounts: "账号与额度", pools: "参与者与额度池", usage: "用量与费用",
  observations: "观测记录", estimates: "容量估算", balances: "余额建议与应用",
  temporary: "临时放宽", pricing: "分组计价", dispatch: "账号暂停", notifications: "通知", security: "登录审计与来源只读",
};
function formOf(user?: ManagedUser): Form {
  return user ? { id: user.id, revision: user.revision, username: user.username, enabled: user.enabled,
    features: user.features, account_ids: user.account_ids, participant_ids: user.participant_ids }
    : { id: "", revision: "", username: "", enabled: true, features: [], account_ids: [], participant_ids: [] };
}
function failure(error: unknown) {
  const messages = {
    BAD_REQUEST: [400, "请核对用户名、密码和授权内容。"],
    UNAUTHORIZED: [401, "会话已失效，请重新登录。"],
    FORBIDDEN: [403, "当前身份没有用户管理权限。"],
    SOURCE_BLOCKED: [403, "当前来源已被阻断，请联系管理员核对。"],
    NOT_FOUND: [404, "该登录身份已不存在，请重新读取列表。"],
    CONFLICT: [409, "用户信息已变化或用户名已占用，请重新读取后核对。"],
    TOO_MANY_REQUESTS: [429, "操作暂时受限，请稍后重新核对。"],
  } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, text: requestFailureText(error, text) };
  }
  return { status: 0, text: requestFailureText(error, "操作结果尚未确认。请关闭弹窗，重新读取当前用户信息；勿重复提交刚才的操作。") };
}

export default function UsersPage() {
  const { toast, refreshSession } = useApp();
  const { accounts, participantCatalog: participants, accountCatalogAvailable, participantCatalogAvailable } = useScope();
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [ready, setReady] = useState(false);
  const [loading, setLoading] = useState(true);
  const [opening, setOpening] = useState(false);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [notice, setNotice] = useState<{ text: string; unknown: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [edit, setEdit] = useState<Form | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [pwd, setPwd] = useState("");
  const [del, setDel] = useState<UserSummary | null>(null);
  const [tab, setTab] = useState("basic");
  const request = useRef({ alive: false, generation: 0, abort: new AbortController(), submitting: false });
  const begin = useCallback(() => {
    request.current.abort.abort();
    request.current.abort = new AbortController();
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const current = useCallback((generation: number) => request.current.alive && request.current.generation === generation, []);
  const client = () => createUsersClient(window.location.origin);

  const load = useCallback(async () => {
    if (request.current.submitting) return;
    const ticket = begin();
    setLoading(true); setReady(false); setOpening(false); setEdit(null); setDel(null); setPwd(""); setError(null);
    try {
      const users: UserSummary[] = [], cursors = new Set<string>();
      let after = "";
      do {
        const page = await createUsersClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal: ticket.signal });
        if (!current(ticket.generation)) return;
        users.push(...page.users);
        if (page.next && cursors.has(page.next)) throw new Error("Repeated user cursor");
        if (page.next) cursors.add(page.next);
        after = page.next ?? "";
      } while (after);
      setUsers(users); setReady(true); setBlocked(false);
      setNotice(previous => previous?.unknown ? previous : null);
    } catch (error) {
      if (!current(ticket.generation)) return;
      const result = failure(error);
      setUsers([]);
      setNotice(previous => previous?.unknown ? previous : { text: result.status === 0 ? requestFailureText(result.text, "暂时无法读取用户列表，请重新读取。") : result.text, unknown: false });
      if (result.status === 401 || result.status === 403) void refreshSession();
    } finally { if (current(ticket.generation)) setLoading(false); }
  }, [begin, current, refreshSession]);

  useEffect(() => {
    request.current.alive = true;
    void load();
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); };
  }, [load]);

  const open = async (user: UserSummary | null) => {
    if (request.current.submitting || loading || opening || blocked) return;
    const ticket = begin();
    setPwd(""); setError(null); setTab("basic"); setIsNew(!user);
    if (!user) { setEdit(formOf()); return; }
    setOpening(true); setEdit(null);
    try {
      const value = await client().get({ id: user.id }, { signal: ticket.signal });
      if (current(ticket.generation)) setEdit(formOf(value));
    } catch (error) {
      if (!current(ticket.generation)) return;
      const result = failure(error);
      setNotice({ text: result.status === 0 ? requestFailureText(result.text, "暂时无法读取该用户，请重新读取。") : result.text, unknown: false });
      if (result.status === 401 || result.status === 403) void refreshSession();
    } finally { if (current(ticket.generation)) setOpening(false); }
  };

  const save = async () => {
    if (!edit || request.current.submitting || blocked) return;
    const username = edit.username.trim().toLowerCase();
    if (edit.username.length > 256 || !/^[a-z0-9][a-z0-9._-]{2,63}$/.test(username)
      || (isNew || pwd !== "") && !newPassword.safeParse(pwd).success) {
      setError("用户名需为 3–64 位英文字母、数字、点、下划线或连字符；密码至少 10 个字符，含英文字母和数字，内容过长时请缩短。");
      return;
    }
    const submitted = { ...edit, username }, password = pwd;
    request.current.submitting = true; setPending(true); setPwd(""); setError(null);
    const ticket = begin();
    try {
      const common = { username, enabled: submitted.enabled, features: submitted.features,
        account_ids: submitted.account_ids, participant_ids: submitted.participant_ids };
      const receipt = isNew
        ? await client().create({ ...common, password }, { signal: ticket.signal })
        : await client().update({ ...common, id: submitted.id, revision: submitted.revision, new_password: password }, { signal: ticket.signal });
      checkUserSave(receipt, common, isNew ? undefined : submitted);
      if (!current(ticket.generation)) return;
      setEdit(null); toast({ tone: "ok", title: "用户已保存", body: "服务端已保存当前身份和授权设置。" });
      request.current.submitting = false; setPending(false);
      await load();
    } catch (error) {
      if (!current(ticket.generation)) return;
      const result = failure(error); setError(result.text);
      if (result.status === 0) { setBlocked(true); setNotice({ text: result.text, unknown: true }); }
      else if (result.status === 404 || result.status === 409) setNotice({ text: result.text, unknown: false });
      if (result.status === 401 || result.status === 403) void refreshSession();
    } finally {
      if (current(ticket.generation)) { request.current.submitting = false; setPending(false); }
    }
  };

  const remove = async () => {
    if (!del || request.current.submitting || blocked) return false;
    const selected = del, ticket = begin();
    request.current.submitting = true; setPending(true); setError(null);
    try {
      await client().delete({ id: selected.id, revision: selected.revision, confirmation: selected.username }, { signal: ticket.signal });
      if (!current(ticket.generation)) return false;
      setDel(null); toast({ tone: "ok", title: `已删除登录身份 ${selected.username}` });
      request.current.submitting = false; setPending(false);
      await load();
      return true;
    } catch (error) {
      if (!current(ticket.generation)) return false;
      const result = failure(error); setError(result.text);
      if (result.status === 0) { setBlocked(true); setNotice({ text: result.text, unknown: true }); }
      else if (result.status === 404 || result.status === 409) setNotice({ text: result.text, unknown: false });
      if (result.status === 401 || result.status === 403) void refreshSession();
      return false;
    } finally {
      if (current(ticket.generation)) { request.current.submitting = false; setPending(false); }
    }
  };
  const toggle = <T extends string,>(list: T[], value: T) => list.includes(value) ? list.filter(item => item !== value) : [...list, value];
  const locked = pending || loading || opening || blocked;
  const invalidPassword = (isNew || !!pwd) && !newPassword.safeParse(pwd).success;

  return (
    <>
      <PageHeader
        title="用户与权限"
        tag="S2A-AUTH-005 ~ 009"
        desc="管理本系统普通用户的功能访问与数据范围。普通用户只读；授予访问权不等于授予管理写权。权限最终由服务端裁决。"
        actions={
          <>
            {(notice || !ready) && <Button disabled={pending || loading} onClick={() => void load()}>重新读取</Button>}
            <Button variant="primary" icon={<Plus size={14} />} disabled={locked || !ready} onClick={() => void open(null)}>
              新建用户
            </Button>
          </>
        }
      />
      {notice && <Callout tone={notice.unknown ? "warn" : "bad"}>{notice.text}</Callout>}
      <Card pad={false}>
        <Table
          rows={loading ? [] : users}
          empty={loading ? "正在读取用户…" : ready ? "暂无普通用户" : "用户列表暂不可用"}
          rowKey={(u) => u.id}
          cols={[
            { key: "u", label: "用户名", render: (u) => <Mono>{u.username}</Mono> },
            { key: "r", label: "角色", render: () => <Badge>普通用户 · 只读</Badge> },
            { key: "s", label: "状态", render: (u) => <Badge tone={u.enabled ? "ok" : "muted"} dot>{u.enabled ? "启用" : "已停用"}</Badge> },
            { key: "f", label: "功能权限", render: (u) => `${u.feature_count} 项` },
            {
              key: "d",
              label: "数据范围",
              render: (u) => <span className="text-[12px]">账号 {u.account_count} · 参与者 {u.participant_count}</span>,
            },
            { key: "l", label: "最近登录", render: (u) => <span className="num text-muted" title={u.last_login_at ?? undefined}>{u.last_login_at ? new Date(u.last_login_at).toLocaleString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }) : "尚无记录"}</span> },
            {
              key: "x",
              label: "",
              render: (u) => (
                <div className="flex justify-end gap-1">
                  <Button size="sm" variant="ghost" disabled={locked} onClick={() => void open(u)} aria-label="编辑">
                    <Pencil size={13} />
                  </Button>
                  <Button size="sm" variant="ghost" disabled={locked} onClick={() => { setError(null); setDel(u); }} aria-label="删除">
                    <Trash2 size={13} />
                  </Button>
                </div>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        open={!!edit}
        onClose={() => { if (!request.current.submitting) { setEdit(null); setPwd(""); } }}
        title={isNew ? "新建普通用户" : `编辑 ${edit?.username}`}
        wide
        footer={
          <>
            <Button disabled={pending} onClick={() => { if (!request.current.submitting) { setEdit(null); setPwd(""); } }}>取消</Button>
            <Button
              variant="primary"
              disabled={!edit?.username || invalidPassword || pending || blocked}
              onClick={() => void save()}
            >
              保存
            </Button>
          </>
        }
      >
        {edit && (
          <>
            {error && <Callout tone={blocked ? "warn" : "bad"}>{error}</Callout>}
            <Tabs
              value={tab}
              onChange={setTab}
              tabs={[
                { id: "basic", label: "基本信息" },
                { id: "feat", label: "功能权限" },
                { id: "scope", label: "数据范围" },
              ]}
            />
            {tab === "basic" && (
              <div className="space-y-4">
                <Field label="用户名">
                  <Input value={edit.username} onChange={(e) => setEdit({ ...edit, username: e.target.value })} disabled={pending || blocked} />
                </Field>
                <Field label={isNew ? "初始密码" : "重置密码"} hint="至少 10 个字符，含英文字母和数字。不会回显，也不会影响 Sub2API 密码。" error={pwd && invalidPassword ? "密码不符合要求或内容过长" : undefined}>
                  <Input type="password" value={pwd} onChange={(e) => setPwd(e.target.value)} disabled={pending || blocked} placeholder={isNew ? "" : "留空则不修改"} autoComplete="new-password" />
                </Field>
                <Switch
                  checked={edit.enabled}
                  onChange={(enabled) => setEdit({ ...edit, enabled })}
                  disabled={pending || blocked}
                  label="启用账户"
                  desc="停用后该用户无法继续使用本系统。"
                />
                <Callout tone="info">此处只能创建普通用户，不能创建管理员。</Callout>
              </div>
            )}
            {tab === "feat" && (
              <Section title="允许访问的功能" desc="仅为只读访问；管理写操作始终需要管理员身份。">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  {readFeature.options.map((feature) => (
                    <Checkbox key={feature} checked={edit.features.includes(feature)} disabled={pending || blocked} onChange={() => setEdit({ ...edit, features: toggle(edit.features, feature) })} label={labels[feature]} />
                  ))}
                </div>
              </Section>
            )}
            {tab === "scope" && (
              <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
                <Section title="可见账号" desc="明细、搜索、统计、通知与 API 均受此限制">
                  <div className="space-y-2">
                    {!accountCatalogAvailable && <Callout tone="unk">账号列表暂不可用，已保存的范围将保留。</Callout>}
                    {accounts.map((a) => (
                      <Checkbox key={a.id} checked={edit.account_ids.includes(a.id)} disabled={pending || blocked} onChange={() => setEdit({ ...edit, account_ids: toggle(edit.account_ids, a.id) })} label={a.localName} />
                    ))}
                  </div>
                </Section>
                <Section title="可见参与者" desc="与账号范围分别核验">
                  <div className="space-y-2">
                    {!participantCatalogAvailable && <Callout tone="unk">参与者列表暂不可用，已保存的范围将保留。</Callout>}
                    {participants.map((p) => (
                      <Checkbox key={p.id} checked={edit.participant_ids.includes(p.id)} disabled={pending || blocked} onChange={() => setEdit({ ...edit, participant_ids: toggle(edit.participant_ids, p.id) })} label={p.name} />
                    ))}
                  </div>
                </Section>
                <Callout tone="info" className="md:col-span-2">
                  跨范围的汇总数据只按可见部分计算展示，不会泄露隐藏账号或成员的信息。
                </Callout>
              </div>
            )}
          </>
        )}
      </Modal>

      <ConfirmModal
        open={!!del}
        onClose={() => setDel(null)}
        onConfirm={remove}
        title={`删除登录身份 ${del?.username ?? ""}`}
        phrase={del?.username}
        confirmLabel="删除"
        danger
        disabled={blocked || pending}
      >
        {error && <Callout tone={blocked ? "warn" : "bad"}>{error}</Callout>}
        <p>只删除本系统的登录身份。</p>
        <p className="text-muted">不会删除 Sub2API 上游用户、参与者历史或审计记录；这不是参与者退池操作。</p>
      </ConfirmModal>
    </>
  );
}
