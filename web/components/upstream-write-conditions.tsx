"use client";

import Link from "next/link";
import { Card } from "@/components/ui";
import { useApp } from "@/lib/store";

export function UpstreamWriteConditions({ capability, status }: {
  capability: "pricing" | "scheduling";
  status?: React.ReactNode;
}) {
  const { isAdmin } = useApp();
  const pricing = capability === "pricing";
  return (
    <Card title="开放条件" className="mb-4">
      <div className="space-y-1 text-[13px] text-muted">
        {status && <p>{status}</p>}
        <p>由管理员在「连接与采集 → 能力检查 → 生产写入授权」开启{pricing ? "计价" : "整账号调度"}授权。</p>
        <p>写入需要有效的 Sub2API 连接，且紧急禁写已关闭。</p>
        <p>{pricing ? "Sub2API 需处于标准模式，所选分组的当前配置须通过核对。" : "目标账号须属于当前连接并通过调度核对。暂停作用于整个账号。"}</p>
        {isAdmin && <Link href="/console/connection" className="inline-block text-brand hover:underline">前往连接与采集</Link>}
      </div>
    </Card>
  );
}
