"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { PasswordForm } from "@/components/password-form";
import { Button, Card, Empty } from "@/components/ui";
import { useApp } from "@/lib/store";

export default function ChangePasswordPage() {
  const router = useRouter();
  const { auth, refreshSession, signOut, theme } = useApp();
  useEffect(() => {
    if (auth.status === "anonymous") router.replace("/login");
    if (auth.status === "authenticated") router.replace("/console");
  }, [auth.status, router]);

  return (
    <main className="flex min-h-screen items-center justify-center px-4 py-10" data-theme={theme}>
      <div className="w-full max-w-[380px]">
        {auth.status === "password-change-required" ? (
          <Card title="修改初始密码" desc="改密后请用新密码重新登录。"
            actions={<Button disabled={auth.busy} onClick={() => { void signOut(); }}>退出登录</Button>}>
            <PasswordForm key={auth.epoch} />
          </Card>
        ) : auth.status === "unavailable" ? (
          <Empty title="暂时无法确认会话">
            {auth.message}
            <div className="mt-2"><Button onClick={() => { void refreshSession(); }}>恢复会话</Button></div>
          </Empty>
        ) : (
          <Empty title="正在确认会话…" />
        )}
      </div>
    </main>
  );
}
