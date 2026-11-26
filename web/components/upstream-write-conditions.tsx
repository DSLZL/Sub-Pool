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
        <p>在能力检查开启{pricing ? "计价" : "调度"}授权。</p>
        <p>需管理凭据，关闭紧急禁写。</p>
        {pricing && <p>核对标准模式及分组。</p>}
        {isAdmin && <Link href="/console/connection" className="inline-block text-brand hover:underline">去连接</Link>}
      </div>
    </Card>
  );
}
