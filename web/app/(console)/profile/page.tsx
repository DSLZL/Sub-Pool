"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { Badge, Button, Callout, Card, Field, Grid, Input, KV, Mono, PageHeader } from "@/components/ui";
import { useApp } from "@/lib/store";

export default function ProfilePage() {
  const { userName, isAdmin, toast } = useApp();
  const router = useRouter();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [err, setErr] = useState<string | null>(null);

  const rules = [
    { ok: next.length >= 10, label: "至少 10 位" },
    { ok: /[A-Za-z]/.test(next) && /\d/.test(next), label: "同时包含字母和数字" },
    { ok: next.length > 0 && next !== cur, label: "与当前密码不同" },
  ];
  const valid = cur && rules.every((r) => r.ok) && next === again;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (cur !== "demo") {
      setErr("当前密码不正确。");
      return;
    }
    setErr(null);
    toast({ tone: "ok", title: "密码已修改", body: "其他设备上的会话已失效，请重新登录。" });
    setTimeout(() => router.push("/login?logout=1"), 900);
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
              ["角色", isAdmin ? <Badge key="r" tone="brand">管理员</Badge> : <Badge key="r">普通用户 · 只读</Badge>],
              ["当前会话", "2026-11-04 14:02 登录 · 闲置 8 小时后过期"],
            ]}
          />
          <Callout tone="info" className="mt-4">本系统账户与 Sub2API 账户相互独立，修改这里的密码不会影响 Sub2API。</Callout>
        </Card>
        <Card title="修改密码">
          <form onSubmit={submit} className="space-y-3.5">
            {err && <Callout tone="bad">{err}</Callout>}
            <Field label="当前密码" hint="演示：demo">
              <Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" />
            </Field>
            <Field label="新密码">
              <Input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
            </Field>
            <ul className="space-y-0.5 text-[12px]">
              {rules.map((r) => (
                <li key={r.label} className={r.ok ? "text-ok" : "text-faint"}>
                  {r.ok ? "✓" : "·"} {r.label}
                </li>
              ))}
            </ul>
            <Field label="确认新密码" error={again && again !== next ? "两次输入不一致" : undefined}>
              <Input type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" />
            </Field>
            <Button variant="primary" type="submit" disabled={!valid}>
              修改密码
            </Button>
          </form>
        </Card>
      </Grid>
    </>
  );
}
