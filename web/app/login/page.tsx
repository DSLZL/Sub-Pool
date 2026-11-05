"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Eye, EyeOff, FlaskConical } from "lucide-react";
import { Button, Callout, Field, Input, Badge } from "@/components/ui";
import { useApp } from "@/lib/store";

function LoginInner() {
  const router = useRouter();
  const params = useSearchParams();
  const { setRole, theme } = useApp();
  const [user, setUser] = useState("");
  const [pwd, setPwd] = useState("");
  const [show, setShow] = useState(false);
  const [fails, setFails] = useState(0);
  const [lockLeft, setLockLeft] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const reason = params.get("logout") ? "logout" : params.get("expired") ? "expired" : null;

  useEffect(() => {
    if (lockLeft <= 0) return;
    const t = setTimeout(() => setLockLeft((s) => s - 1), 1000);
    return () => clearTimeout(t);
  }, [lockLeft]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (lockLeft > 0) return;
    if (!user || !pwd) {
      setError("请输入用户名和密码。");
      return;
    }
    setBusy(true);
    setError(null);
    setTimeout(() => {
      setBusy(false);
      // 演示：admin / demo 或 lin.yu / demo 可登录
      if ((user === "admin" || user === "lin.yu") && pwd === "demo") {
        setRole(user === "admin" ? "admin" : "user");
        router.push("/");
        return;
      }
      const n = fails + 1;
      setFails(n);
      if (n >= 4) {
        setLockLeft(60);
        setFails(0);
        setError("连续失败次数过多，已暂时限制登录尝试。");
      } else {
        // 不区分“用户不存在”与“密码错误”，避免泄露账户信息
        setError(`用户名或密码不正确。再失败 ${4 - n} 次将被暂时限制。`);
      }
    }, 500);
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
            <div className="text-[12px] text-muted">Sub2API 额度池与余额管理</div>
          </div>
        </div>

        <div className="rounded-xl border border-line bg-surface p-6 shadow-[var(--shadow)]">
          <h1 className="text-[16px] font-semibold">登录</h1>
          <p className="mt-1 text-[12.5px] text-muted">使用本系统账户登录。本系统账户与 Sub2API 账户相互独立。</p>

          <div className="mt-4 space-y-3">
            {reason === "logout" && <Callout tone="ok">你已退出登录，当前会话已失效。</Callout>}
            {reason === "expired" && <Callout tone="warn">会话已过期，请重新登录。</Callout>}
            {lockLeft > 0 && (
              <Callout tone="bad" title="登录暂时受限">
                请在 <span className="num font-semibold">{lockLeft}</span> 秒后重试。
              </Callout>
            )}
            {error && lockLeft === 0 && <Callout tone="bad">{error}</Callout>}
          </div>

          <form onSubmit={submit} className="mt-4 space-y-3.5">
            <Field label="用户名">
              <Input value={user} onChange={(e) => setUser(e.target.value)} autoComplete="username" disabled={lockLeft > 0} />
            </Field>
            <Field label="密码">
              <div className="relative">
                <Input
                  type={show ? "text" : "password"}
                  value={pwd}
                  onChange={(e) => setPwd(e.target.value)}
                  autoComplete="current-password"
                  disabled={lockLeft > 0}
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

        <div className="mt-4 rounded-lg border border-dashed border-line px-4 py-3 text-[12px] text-muted">
          <div className="mb-1 flex items-center gap-1.5">
            <Badge tone="unk">
              <FlaskConical size={11} />
              演示
            </Badge>
          </div>
          管理员：<span className="font-mono">admin / demo</span>
          <br />
          普通用户：<span className="font-mono">lin.yu / demo</span>
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
