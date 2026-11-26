"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, Pencil, Trash2 } from "lucide-react";
import { ORPCError } from "@orpc/client";
import { createUsersClient } from "@sub-pool/contracts/client";
import { authErrorData, currentPassword, newPassword } from "@sub-pool/contracts/auth";
import { readFeature, type ManagedUser, type ReadFeature, type UserSummary } from "@sub-pool/contracts/users";
import { Badge, Button, Callout, Checkbox, ConfirmModal, Field, Input, Modal, Mono, PageHeader, Select, Table, Card, Switch, Section, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { requestFailureText } from "@/lib/request-id";
import { checkUserSave } from "@/lib/users-data";

type Role = UserSummary["role"];
type Form = Pick<ManagedUser, "id" | "revision" | "username" | "role" | "enabled" | "features" | "account_ids" | "participant_ids">;
const roles = [{ value: "user", label: "普通用户" }, { value: "admin", label: "管理员" }];
const labels: Record<ReadFeature, string> = {
  overview: "概览", accounts: uiCopy.quotas, pools: uiCopy.pools, usage: uiCopy.usage,
  observations: uiCopy.observations, estimates: uiCopy.estimates, balances: glossary.balances,
  temporary: "临时放宽", pricing: uiCopy.pricing, dispatch: uiCopy.dispatch, notifications: "通知", security: "登录审计",
};
function formOf(user?: ManagedUser, role: Role = "user"): Form {
  return user ? { id: user.id, revision: user.revision, username: user.username, role: user.role, enabled: user.enabled,
    features: user.features, account_ids: user.account_ids, participant_ids: user.participant_ids }
    : { id: "", revision: "", username: "", role, enabled: true, features: [], account_ids: [], participant_ids: [] };
}
function failure(error: unknown) {
  const messages = {
    BAD_REQUEST: [400, "核对输入。"],
    UNAUTHORIZED: [401, "请重新登录。"],
    FORBIDDEN: [403, "无管理权限。"],
    CURRENT_PASSWORD_INVALID: [403, "核对本人密码。"],
    SOURCE_BLOCKED: [403, "访问被封禁。"],
    NOT_FOUND: [404, "用户不存在。"],
    CONFLICT: [409, "用户已变。"],
    LAST_ADMIN_REQUIRED: [409, "至少保留一名管理员。"],
    TOO_MANY_REQUESTS: [429, "请稍后重试。"],
  } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, code: error.code, text: requestFailureText(error, text) };
  }
  return { status: 0, text: requestFailureText(error, "待核对，请重读。") };
}

export default function UsersPage() {
  const { auth, toast, refreshSession } = useApp();
  const { accounts, participantCatalog: participants, accountCatalogAvailable, participantCatalogAvailable } = useScope();
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [listRole, setListRole] = useState<Role>("user");
  const [ready, setReady] = useState(false);
  const [loading, setLoading] = useState(true);
  const [opening, setOpening] = useState(false);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [notice, setNotice] = useState<{ text: string; unknown: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [edit, setEdit] = useState<Form | null>(null);
  const [original, setOriginal] = useState<ManagedUser | null>(null);
  const [pwd, setPwd] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [del, setDel] = useState<UserSummary | null>(null);
  const [tab, setTab] = useState("basic");
  const isNew = original === null;
  const needsConfirmation = edit !== null && (original ? original.role !== edit.role : edit.role === "admin");
  const grantsEditable = edit?.role === "user" && (!original || original.role === "user");
  const passwordEditable = isNew || grantsEditable;
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
    setLoading(true); setReady(false); setOpening(false); setEdit(null); setOriginal(null); setDel(null); setPwd(""); setConfirmation(""); setError(null);
    try {
      const users: UserSummary[] = [], cursors = new Set<string>();
      let after = "";
      do {
        const page = await createUsersClient(window.location.origin).list({ after, search: "", limit: 100, role: listRole }, { signal: ticket.signal });
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
      setNotice(previous => previous?.unknown ? previous : { text: result.status === 0 ? requestFailureText(result.text, uiCopy.readFailed) : result.text, unknown: false });
      if (result.status === 401 || result.status === 403) void refreshSession();
    } finally { if (current(ticket.generation)) setLoading(false); }
  }, [begin, current, listRole, refreshSession]);

  useEffect(() => {
    request.current.alive = true;
    void load();
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); };
  }, [load]);

  const open = async (user: UserSummary | null) => {
    if (request.current.submitting || loading || opening || blocked) return;
    const ticket = begin();
    setPwd(""); setConfirmation(""); setError(null); setTab("basic"); setOriginal(null);
    if (!user) { setEdit(formOf(undefined, listRole)); return; }
    setOpening(true); setEdit(null);
    try {
      const value = await client().get({ id: user.id, role: user.role }, { signal: ticket.signal });
      if (value.id !== user.id || value.role !== user.role) throw new Error("User details do not match the selected identity");
      if (current(ticket.generation)) {
        setOriginal(value);
        setEdit(formOf(value));
      }
    } catch (error) {
      if (!current(ticket.generation)) return;
      const result = failure(error);
      setNotice({ text: result.status === 0 ? requestFailureText(result.text, uiCopy.readFailed) : result.text, unknown: false });
      if (result.status === 401 || result.status === 403) void refreshSession();
    } finally { if (current(ticket.generation)) setOpening(false); }
  };

  const closeEdit = () => {
    if (request.current.submitting) return;
    setEdit(null); setOriginal(null); setPwd(""); setConfirmation("");
  };

  const save = async () => {
    if (!edit || request.current.submitting || blocked) return;
    const username = edit.username.trim().toLowerCase();
    const password = passwordEditable ? pwd : "";
    if (edit.username.length > 256 || !/^[a-z0-9][a-z0-9._-]{2,63}$/.test(username)
      || (isNew || password !== "") && !newPassword.safeParse(password).success) {
      setError("核对账号密码。");
      return;
    }
    if (needsConfirmation && !currentPassword.safeParse(confirmation).success) {
      setError("输入本人密码。");
      return;
    }
    const submitted = { ...edit, username };
    const grants = grantsEditable ? submitted : original;
    const passwordConfirmation = needsConfirmation ? { current_password: confirmation } : {};
    request.current.submitting = true; setPending(true); setPwd(""); setConfirmation(""); setError(null);
    const ticket = begin();
    try {
      const common = { username, role: submitted.role, enabled: submitted.enabled, features: grants?.features ?? [],
        account_ids: grants?.account_ids ?? [], participant_ids: grants?.participant_ids ?? [] };
      const receipt = isNew
        ? await client().create({ ...common, password, ...passwordConfirmation }, { signal: ticket.signal })
        : await client().update({ ...common, id: submitted.id, revision: submitted.revision, new_password: password, ...passwordConfirmation }, { signal: ticket.signal });
      checkUserSave(receipt, common, isNew ? undefined : submitted);
      if (!current(ticket.generation)) return;
      setEdit(null); toast({ tone: "ok", title: "用户已保存", body: "已保存。" });
      request.current.submitting = false; setPending(false);
      if (auth.session?.user.id === submitted.id) await refreshSession();
      if (current(ticket.generation)) await load();
    } catch (error) {
      if (!current(ticket.generation)) return;
      const result = failure(error); setError(result.text);
      if (result.status === 0) { setBlocked(true); setNotice({ text: result.text, unknown: true }); }
      else if (result.status === 404 || result.status === 409) setNotice({ text: result.text, unknown: false });
      if (result.status === 401 || result.status === 403 && result.code !== "CURRENT_PASSWORD_INVALID") void refreshSession();
    } finally {
      if (current(ticket.generation)) { request.current.submitting = false; setPending(false); }
    }
  };

  const remove = async () => {
    if (!del || request.current.submitting || blocked) return false;
    const selected = del, ticket = begin();
    request.current.submitting = true; setPending(true); setError(null);
    try {
      await client().delete({ id: selected.id, revision: selected.revision, confirmation: selected.username, role: selected.role }, { signal: ticket.signal });
      if (!current(ticket.generation)) return false;
      setDel(null); toast({ tone: "ok", title: ` 已删除 ${selected.username}` });
      request.current.submitting = false; setPending(false);
      if (auth.session?.user.id === selected.id) await refreshSession();
      if (current(ticket.generation)) await load();
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
  const invalidPassword = passwordEditable && (isNew || !!pwd) && !newPassword.safeParse(pwd).success;

  return (
    <>
      <PageHeader
        title={uiCopy.users}
        helpTopic="authority"
        actions={
          <>
            <Select aria-label="按角色查看" className="w-auto" value={listRole} options={roles} disabled={locked || !!edit || !!del}
              onChange={(event) => {
                const role = event.target.value;
                if ((role === "user" || role === "admin") && role !== listRole) {
                  setLoading(true);
                  setListRole(role);
                }
              }} />
            {(notice || !ready) && <Button disabled={pending || loading} onClick={() => void load()}>{uiCopy.reload}</Button>}
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
          empty={loading ? "正在读取用户…" : ready ? listRole === "admin" ? "暂无管理员" : "暂无普通用户" : "用户不可用"}
          rowKey={(u) => u.id}
          cols={[
            { key: "u", label: "用户名", render: (u) => <Mono>{u.username}</Mono> },
            { key: "r", label: "角色", render: (u) => <Badge>{u.role === "admin" ? "管理员" : "普通用户 · 只读"}</Badge> },
            { key: "s", label: uiCopy.status, render: (u) => <Badge tone={u.enabled ? "ok" : "muted"} dot>{u.enabled ? "启用" : "已停用"}</Badge> },
            { key: "f", label: "功能权限", render: (u) => u.role === "admin" ? "管理员权限" : `${u.feature_count} 项` },
            {
              key: "d",
              label: "数据范围",
              render: (u) => u.role === "admin" ? "全量范围" : <span className="text-[12px]">{uiCopy.account} {u.account_count} · 参与者 {u.participant_count}</span>,
            },
            { key: "l", label: "最近登录", render: (u) => <Timestamp short className="num text-muted" value={u.last_login_at} empty={uiCopy.noRecords} /> },
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
        onClose={closeEdit}
        title={isNew ? edit?.role === "admin" ? "新建管理员" : "新建普通用户" : `编辑 ${edit?.username}`}
        wide
        footer={
          <>
            <Button disabled={pending} onClick={closeEdit}>取消</Button>
            <Button
              variant="primary"
              disabled={!edit?.username || invalidPassword || needsConfirmation && !currentPassword.safeParse(confirmation).success || pending || blocked}
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
            {grantsEditable && <Tabs
              value={tab}
              onChange={setTab}
              tabs={[
                { id: "basic", label: "基本信息" },
                { id: "feat", label: "功能权限" },
                { id: "scope", label: "数据范围" },
              ]}
            />}
            {(tab === "basic" || !grantsEditable) && (
              <div className="space-y-4">
                <Field label="用户名">
                  <Input value={edit.username} onChange={(e) => setEdit({ ...edit, username: e.target.value })} disabled={pending || blocked} />
                </Field>
                <Field label="登录角色">
                  <Select options={roles} value={edit.role} disabled={pending || blocked} onChange={(event) => {
                    const role = event.target.value;
                    if (role !== "user" && role !== "admin") return;
                    setEdit({ ...edit, role });
                    setConfirmation("");
                    if (!isNew) setPwd("");
                  }} />
                </Field>
                {passwordEditable && <Field label={isNew ? "初始密码" : "重置密码"} hint="至少 10 位且含字母与数字。" error={pwd && invalidPassword ? "密码不符要求" : undefined}>
                  <Input type="password" value={pwd} onChange={(e) => setPwd(e.target.value)} disabled={pending || blocked} placeholder={isNew ? "" : "留空则不修改"} autoComplete="new-password" />
                </Field>}
                {needsConfirmation && <Field label="本人当前密码" hint={isNew ? undefined : "角色变更将使会话及 Key 失效。"}>
                  <Input type="password" value={confirmation} onChange={(e) => setConfirmation(e.target.value)} disabled={pending || blocked} autoComplete="current-password" />
                </Field>}
                <Switch
                  checked={edit.enabled}
                  onChange={(enabled) => setEdit({ ...edit, enabled })}
                  disabled={pending || blocked}
                  label="启用账户"
                  desc="停用禁止登录。"
                />
              </div>
            )}
            {grantsEditable && tab === "feat" && (
              <Section title="可用功能">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  {readFeature.options.map((feature) => (
                    <Checkbox key={feature} checked={edit.features.includes(feature)} disabled={pending || blocked} onChange={() => setEdit({ ...edit, features: toggle(edit.features, feature) })} label={labels[feature]} />
                  ))}
                </div>
              </Section>
            )}
            {grantsEditable && tab === "scope" && (
              <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
                <Section title="可见账号" desc="适用于查询与 API">
                  <div className="space-y-2">
                    {!accountCatalogAvailable && <Callout tone="unk">账号读取失败。</Callout>}
                    {accounts.map((a) => (
                      <Checkbox key={a.id} checked={edit.account_ids.includes(a.id)} disabled={pending || blocked} onChange={() => setEdit({ ...edit, account_ids: toggle(edit.account_ids, a.id) })} label={a.localName} />
                    ))}
                  </div>
                </Section>
                <Section title="可见参与者">
                  <div className="space-y-2">
                    {!participantCatalogAvailable && <Callout tone="unk">{uiCopy.readFailed}</Callout>}
                    {participants.map((p) => (
                      <Checkbox key={p.id} checked={edit.participant_ids.includes(p.id)} disabled={pending || blocked} onChange={() => setEdit({ ...edit, participant_ids: toggle(edit.participant_ids, p.id) })} label={p.name} />
                    ))}
                  </div>
                </Section>
                <Callout tone="info" className="md:col-span-2">
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
        <p>删除本地登录身份，保留历史。</p>
      </ConfirmModal>
    </>
  );
}
