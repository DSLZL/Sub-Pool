"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Suspense, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { Eye, EyeOff } from "lucide-react";
import { Button, Callout, Field, Input } from "@/components/ui";
import { useApp } from "@/lib/store";
import { restorePlanID } from "@/lib/backup-return";

function LoginInner() {
  const router = useRouter();
  const { signIn, auth, theme } = useApp();
  const [user, setUser] = useState("");
  const [pwd, setPwd] = useState("");
  const [show, setShow] = useState(false);
  const [lockLeft, setLockLeft] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const pending = useRef(false);
  const busy = auth.busy;
  const reason = auth.reason;

  useEffect(() => {
    const update = () => setLockLeft(auth.retryAt ? Math.max(0, Math.ceil((Date.parse(auth.retryAt) - Date.now()) / 1000)) : 0);
    update();
    const timer = setInterval(update, 1000);
    return () => clearInterval(timer);
  }, [auth.retryAt]);
  useEffect(() => {
    if (auth.status === "password-change-required") router.replace("/change-password");
    else if (auth.status === "authenticated") {
      const id = restorePlanID(window.location.search);
      router.replace(id ? "/console/backup?restore=" + id : "/console");
    }
  }, [auth.status, router]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (lockLeft > 0 || busy || pending.current) return;
    if (!user || !pwd) {
      setError("填写账号密码。");
      return;
    }
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$/.test(user.trim()) || new TextEncoder().encode(pwd).length > 128) {
      setError("核对账号密码。");
      setPwd("");
      return;
    }
    pending.current = true;
    setError(null);
    setPwd("");
    try { await signIn(user, pwd); } finally { pending.current = false; }
  };

  return (
    <div className="flex min-h-screen items-center justify-center px-4 py-10" data-theme={theme}>
      <div className="w-full max-w-[380px]">
        <div className="mb-6 flex items-center gap-2.5">
          <svg width="30" height="30" viewBox="0 0 24 24" aria-hidden>
            <rect x="2" y="2" width="20" height="20" rx="6" fill="var(--brand)" />
            <path d="M6.5 14.5c2.2 1.6 4.4 1.6 6.6 0s3.6-1.6 4.4-.6" stroke="var(--brand-ink)" strokeWidth="1.8" fill="none" strokeLinecap="round" />
            <path d="M6.5 10c2.2 1.6 4.4 1.6 6.6 0s3.6-1.6 4.4-.6" stroke="var(--brand-ink)" strokeWidth="1.8" fill="none" strokeLinecap="round" opacity=".55" />
          </svg>
          <div>
            <div className="text-[18px] font-semibold tracking-tight">Sub-Pool</div>
            <div className="text-[12px] text-muted">{glossary.sub2api} 额度池管理</div>
          </div>
        </div>

        <div className="rounded-xl border border-line bg-surface p-6 shadow-[var(--shadow)]">
          <h1 className="text-[16px] font-semibold">登录</h1>
          <p className="mt-1 text-[12.5px] text-muted">请使用 Sub-Pool 账户。</p>

          <div className="mt-4 space-y-3">
            {reason === "logout" && <Callout tone="ok">已退出。</Callout>}
            {reason === "expired" && <Callout tone="warn">{uiCopy.signIn}</Callout>}
            {reason === "password" && <Callout tone="ok">{uiCopy.signIn}</Callout>}
            {lockLeft > 0 && (
              <Callout tone="bad" title="登录暂时受限">
                请在 <span className="num font-semibold">{lockLeft}</span> 秒后重试。
              </Callout>
            )}
            {(error || auth.message) && lockLeft === 0 && <Callout tone={reason === "unknown" ? "warn" : "bad"}>{error || auth.message}</Callout>}
          </div>

          <form onSubmit={submit} className="mt-4 space-y-3.5">
            <Field label="用户名">
              <Input value={user} onChange={(e) => setUser(e.target.value)} autoComplete="username" disabled={busy || lockLeft > 0} maxLength={256} />
            </Field>
            <Field label="密码">
              <div className="relative">
                <Input
                  type={show ? "text" : "password"}
                  value={pwd}
                  onChange={(e) => setPwd(e.target.value)}
                  autoComplete="current-password"
                  disabled={busy || lockLeft > 0}
                  className="pr-9"
                />
                <button
                  type="button"
                  onClick={() => setShow((s) => !s)}
                  className="absolute top-1/2 right-2 -translate-y-1/2 text-faint hover:text-ink"
                  aria-label={show ? "隐藏密码" : "显示密码"}
                >
                  {show ? <EyeOff size={15} /> : <Eye size={15} />}
                </button>
              </div>
            </Field>
            <Button variant="primary" className="w-full" disabled={busy || lockLeft > 0} type="submit">
              {busy ? "正在验证…" : "登录"}
            </Button>
          </form>
        </div>

      </div>
    </div>
  );
}

export default function LoginPage() {
  return (
    <Suspense>
      <LoginInner />
    </Suspense>
  );
}
