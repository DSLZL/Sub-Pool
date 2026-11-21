"use client";

import { useRef, useState } from "react";
import { Button, Callout, Field, Input } from "@/components/ui";
import { useApp } from "@/lib/store";

export function PasswordForm() {
  const { auth, changePassword } = useApp();
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
  );
}
