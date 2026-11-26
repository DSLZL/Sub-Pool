"use client";

import { Fragment, useEffect } from "react";
import { useRouter } from "next/navigation";
import { Shell } from "@/components/shell";
import { Button, Empty } from "@/components/ui";
import { useApp } from "@/lib/store";
import { restorePlanID } from "@/lib/backup-return";

export default function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const { auth, refreshSession } = useApp();
  const router = useRouter();
  useEffect(() => {
    if (auth.status === "password-change-required") router.replace("/change-password");
    else if (auth.status === "anonymous") {
      const id = window.location.pathname === "/console/backup" ? restorePlanID(window.location.search) : null;
      router.replace(id ? "/login?restore=" + id : "/login");
    }
  }, [auth.status, router]);
  if (auth.status === "authenticated") return <Fragment key={auth.epoch}><Shell>{children}</Shell></Fragment>;
  if (auth.status === "unavailable") return (
    <Empty title="会话待核对">
      {auth.message}
      <div className="mt-2"><Button onClick={() => { void refreshSession(); }}>恢复会话</Button></div>
    </Empty>
  );
  if (auth.status === "password-change-required") return <Empty title="跳转改密…" />;
  return <Empty title={auth.status === "loading" ? "正在恢复会话…" : "正在前往登录…"} />;
}
