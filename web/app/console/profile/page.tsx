"use client";

import { useRef, useState } from "react";
import { Badge, Button, Callout, Card, Field, Grid, Input, KV, Mono, PageHeader } from "@/components/ui";
import { useApp } from "@/lib/store";

export default function ProfilePage() {
  const { userName, isAdmin, auth, changePassword } = useApp();
  const pending = useRef(false);
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");

  const rules = [
    { ok: [...next].length >= 10, label: "至少 10 位" },
    { ok: /[A-Za-z]/.test(next) && /\d/.test(next), label: "同时包含字母和数字" },
    { ok: next.length > 0 && next !== cur, label: "与当前密码不同" },
  ];
  const tooLong = new TextEncoder().encode(next).length > 128;
  const valid = cur && rules.every((r) => r.ok) && next === again && !tooLong;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!valid || pending.current || auth.busy) return;
    pending.current = true;
    setCur(""); setNext(""); setAgain("");
    try { await changePassword(cur, next); } finally { pending.current = false; }
  };

  return (
    <>
      <PageHeader title="个人设置" tag="S2A-AUTH-003 · S2A-AUTH-004" />
      <Grid cols={2}>
        <Card title="账户信息">
          <KV
            cols={1}
            items={[
              ["用户名", <Mono key="u">{userName}</Mono>],
              ["角色", isAdmin ? <Badge key="r" tone="brand">管理员</Badge> : <Badge key="r">普通用户</Badge>],
              ["当前会话", auth.session ? `${new Date(auth.session.created_at).toLocaleString("zh-CN")} 登录 · ${new Date(auth.session.idle_expires_at).toLocaleString("zh-CN")} 闲置到期 · ${new Date(auth.session.absolute_expires_at).toLocaleString("zh-CN")} 最迟到期` : "未知"],
            ]}
          />
          <Callout tone="info" className="mt-4">本系统账户与 Sub2API 账户相互独立，修改这里的密码不会影响 Sub2API。</Callout>
        </Card>
        <Card title="修改密码">
          <form onSubmit={submit} className="space-y-3.5">
            {auth.message && <Callout tone={auth.reason === "unknown" ? "warn" : "bad"}>{auth.message}</Callout>}
            <Field label="当前密码" hint="请输入本系统的当前密码">
              <Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" disabled={auth.busy} />
            </Field>
            <Field label="新密码" error={tooLong ? "密码太长，请缩短后重试。" : undefined}>
              <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" disabled={auth.busy} />
            </Field>
            <ul className="space-y-0.5 text-[12px]">
              {rules.map((r) => (
                <li key={r.label} className={r.ok ? "text-ok" : "text-faint"}>
                  {r.ok ? "✓" : "·"} {r.label}
                </li>
              ))}
            </ul>
            <Field label="确认新密码" error={again && again !== next ? "两次输入不一致" : undefined}>
              <Input type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" disabled={auth.busy} />
            </Field>
            <Button variant="primary" type="submit" disabled={!valid || auth.busy}>
              {auth.busy ? "正在提交…" : "修改密码"}
            </Button>
          </form>
        </Card>
      </Grid>
    </>
  );
}
