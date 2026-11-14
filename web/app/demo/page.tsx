"use client";

import { useEffect, useState } from "react";
import type { DemoScenario } from "@sub-pool/contracts/demo";
import { Badge, Callout, Card, PageHeader, Tabs } from "@/components/ui";
import { useDemoData } from "@/lib/use-demo-data";

export default function DemoPage() {
  const [scenario, setScenario] = useState<DemoScenario["scenario"]>("overview");
  const demo = useDemoData(true, scenario);
  useEffect(() => {
    let saved: string | null = null;
    try { saved = localStorage.getItem("sp.theme"); } catch { /* Theme preferences may be unavailable. */ }
    document.documentElement.dataset.theme = saved === "dark" || saved === "light" ? saved
      : window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }, []);

  return (
    <main className="mx-auto max-w-5xl space-y-4 p-4 sm:p-6">
      <PageHeader title="Sub-Pool 合成演示" desc="无需登录，仅使用合成数据。切换场景体验业务边界，不保存操作结果。" tag="SYNTHETIC DEMO" />
      <Callout tone="warn" title="独立合成工作区">
        演示不连接真实 Sub2API、邮件或业务数据库，也不能产生真实权限。演示结果不代表真实业务已通过验收。
      </Callout>
      <Tabs tabs={[{ id: "overview", label: "被动观察" }, { id: "wallet", label: "零目标保护" }, { id: "restore", label: "恢复门禁" }]} value={scenario} onChange={id => {
        if (id === "overview" || id === "wallet" || id === "restore") setScenario(id);
      }} />
      <div role="tabpanel" aria-label="合成场景" aria-live="polite" aria-busy={demo.pending}>
        {demo.pending && <Callout title="正在加载合成场景…" />}
        {demo.error && <Callout tone="bad" title="合成场景暂不可用">请重新选择场景或稍后再试。没有改用真实数据或其他示例结果。</Callout>}
        {demo.data && <Card title={demo.data.title} actions={<Badge tone={demo.data.outcome === "read_only" ? "info" : "warn"}>{demo.data.outcome === "read_only" ? "只读示例" : "操作已阻止"}</Badge>}>
          <div className="space-y-4 text-[13px] leading-relaxed text-muted">
            <ul className="space-y-2">
              {demo.data.facts.map(fact => <li key={fact.label}>{fact.label}：<span className="font-mono text-ink">{fact.value}</span> {fact.unit}</li>)}
            </ul>
            <Callout title="合成结果说明">{demo.data.explanation}</Callout>
            <ul className="space-y-2">{demo.data.limitations.map(limit => <li key={limit}>{limit}</li>)}</ul>
          </div>
        </Card>}
      </div>
    </main>
  );
}
