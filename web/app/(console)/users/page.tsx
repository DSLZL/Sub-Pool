"use client";

import { useState } from "react";
import { Plus, Pencil, Trash2 } from "lucide-react";
import { Badge, Button, Callout, Checkbox, ConfirmModal, Field, Input, Modal, Mono, PageHeader, Table, Card, Switch, Section, Tabs } from "@/components/ui";
import { useApp } from "@/lib/store";
import { accounts, FEATURES, participants, sysUsers } from "@/lib/mock";

type SysUser = (typeof sysUsers)[number];

export default function UsersPage() {
  const { toast } = useApp();
  const [users, setUsers] = useState<SysUser[]>(sysUsers);
  const [edit, setEdit] = useState<SysUser | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [pwd, setPwd] = useState("");
  const [del, setDel] = useState<SysUser | null>(null);
  const [tab, setTab] = useState("basic");

  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  const open = (u: SysUser | null) => {
    setIsNew(!u);
    setPwd("");
    setTab("basic");
    setEdit(u ?? { id: `su${users.length + 1}`, username: "", role: "user", status: "active", features: ["overview"], accounts: [], participants: [], lastLogin: "—" });
  };

  return (
    <>
      <PageHeader
        title="用户与权限"
        tag="S2A-AUTH-005 ~ 009"
        desc="管理本系统普通用户的功能访问与数据范围。普通用户只读；授予访问权不等于授予管理写权。权限最终由服务端裁决。"
        actions={
          <Button variant="primary" icon={<Plus size={14} />} onClick={() => open(null)}>
            新建用户
          </Button>
        }
      />
      <Card pad={false}>
        <Table
          rows={users}
          rowKey={(u) => u.id}
          cols={[
            { key: "u", label: "用户名", render: (u) => <Mono>{u.username}</Mono> },
            { key: "r", label: "角色", render: (u) => (u.role === "admin" ? <Badge tone="brand">管理员</Badge> : <Badge>普通用户 · 只读</Badge>) },
            { key: "s", label: "状态", render: (u) => <Badge tone={u.status === "active" ? "ok" : "muted"} dot>{u.status === "active" ? "启用" : "已停用"}</Badge> },
            { key: "f", label: "功能权限", render: (u) => (u.role === "admin" ? "全部" : `${u.features.length} 项`) },
            {
              key: "d",
              label: "数据范围",
              render: (u) => (u.role === "admin" ? "全部" : <span className="text-[12px]">账号 {u.accounts.length} · 参与者 {u.participants.length}</span>),
            },
            { key: "l", label: "最近登录", render: (u) => <span className="num text-muted">{u.lastLogin}</span> },
            {
              key: "x",
              label: "",
              render: (u) =>
                u.role !== "admin" && (
                  <div className="flex justify-end gap-1">
                    <Button size="sm" variant="ghost" onClick={() => open(u)} aria-label="编辑">
                      <Pencil size={13} />
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setDel(u)} aria-label="删除">
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
        onClose={() => setEdit(null)}
        title={isNew ? "新建普通用户" : `编辑 ${edit?.username}`}
        wide
        footer={
          <>
            <Button onClick={() => setEdit(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={!edit?.username || (isNew && pwd.length < 10)}
              onClick={() => {
                if (!edit) return;
                setUsers((us) => (isNew ? [...us, edit] : us.map((u) => (u.id === edit.id ? edit : u))));
                setEdit(null);
                toast({ tone: "ok", title: "用户已保存", body: "权限变更立即生效，包括该用户的个人 API Key。" });
              }}
            >
              保存
            </Button>
          </>
        }
      >
        {edit && (
          <>
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
                  <Input value={edit.username} onChange={(e) => setEdit({ ...edit, username: e.target.value })} disabled={!isNew} />
                </Field>
                <Field label={isNew ? "初始密码" : "重置密码"} hint="至少 10 位。不会回显，也不会影响 Sub2API 密码。" error={isNew && pwd && pwd.length < 10 ? "密码长度不足" : undefined}>
                  <Input type="password" value={pwd} onChange={(e) => setPwd(e.target.value)} placeholder={isNew ? "" : "留空则不修改"} autoComplete="new-password" />
                </Field>
                <Switch
                  checked={edit.status === "active"}
                  onChange={(v) => setEdit({ ...edit, status: v ? "active" : "disabled" })}
                  label="启用账户"
                  desc="停用后该用户的会话与个人 API Key 立即失效。"
                />
                <Callout tone="info">此处只能创建普通用户，不能创建管理员。</Callout>
              </div>
            )}
            {tab === "feat" && (
              <Section title="允许访问的功能" desc="仅为只读访问；管理写操作始终需要管理员身份。">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  {FEATURES.map((f) => (
                    <Checkbox key={f.id} checked={edit.features.includes(f.id)} onChange={() => setEdit({ ...edit, features: toggle(edit.features, f.id) })} label={f.label} />
                  ))}
                </div>
              </Section>
            )}
            {tab === "scope" && (
              <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
                <Section title="可见账号" desc="明细、搜索、统计、通知与 API 均受此限制">
                  <div className="space-y-2">
                    {accounts.map((a) => (
                      <Checkbox key={a.id} checked={edit.accounts.includes(a.id)} onChange={() => setEdit({ ...edit, accounts: toggle(edit.accounts, a.id) })} label={a.localName} />
                    ))}
                  </div>
                </Section>
                <Section title="可见参与者" desc="与账号范围分别核验">
                  <div className="space-y-2">
                    {participants.map((p) => (
                      <Checkbox key={p.id} checked={edit.participants.includes(p.id)} onChange={() => setEdit({ ...edit, participants: toggle(edit.participants, p.id) })} label={p.name} />
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
        onConfirm={() => {
          setUsers((us) => us.filter((u) => u.id !== del?.id));
          toast({ tone: "ok", title: `已删除登录身份 ${del?.username}` });
        }}
        title={`删除登录身份 ${del?.username ?? ""}`}
        phrase={del?.username}
        confirmLabel="删除"
        danger
      >
        <p>只删除本系统的登录身份及其个人 API Key。</p>
        <p className="text-muted">不会删除 Sub2API 上游用户、参与者历史或审计记录；这不是参与者退池操作。</p>
      </ConfirmModal>
    </>
  );
}
