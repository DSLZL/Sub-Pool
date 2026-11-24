"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { BookOpen, Megaphone, FlaskConical, Check } from "lucide-react";
import { Badge, Button, Callout, Card, Empty, PageHeader, Tabs, cx } from "@/components/ui";
import { useApp } from "@/lib/store";
import { displayTime } from "@/lib/connection-data";
import { useDemoData } from "@/lib/use-demo-data";

const audience = { all: "全部用户", admin: "管理员", scoped: "当前授权用户" };

function HelpInner() {
  const params = useSearchParams();
  const { isAdmin, help, timezone } = useApp();
  const [tab, setTab] = useState("guide");
  const activeTab = tab === "news" && !isAdmin ? "guide" : tab;
  const visibleTutorials = help.catalog?.guides ?? [];
  const announcements = help.catalog?.announcements ?? [];
  const [tid, setTid] = useState("");
  const t = visibleTutorials.find((x) => x.id === tid) ?? visibleTutorials[0];
  const demo = useDemoData(activeTab === "demo");

  useEffect(() => {
    const q = params.get("tab");
    if (q === "news") setTab(isAdmin ? "news" : "guide");
    else if (q === "demo") setTab("demo");
  }, [params, isAdmin]);

  return (
    <>
      <PageHeader title="帮助与公告" desc="原创使用说明只覆盖当前实际可用的功能。" />
      <Tabs
        value={activeTab}
        onChange={setTab}
        tabs={[
          { id: "guide", label: <span className="inline-flex items-center gap-1.5"><BookOpen size={14} />使用说明</span> },
          ...(isAdmin
            ? [
                {
                  id: "news",
                  label: (
                    <span className="inline-flex items-center gap-1.5">
                      <Megaphone size={14} />
                      产品公告
                      {announcements.some((a) => a.read_at === null) && <span className="h-1.5 w-1.5 rounded-full bg-bad" />}
                    </span>
                  ),
                },
              ]
            : []),
          { id: "demo", label: <span className="inline-flex items-center gap-1.5"><FlaskConical size={14} />演示模式</span> },
        ]}
      />

      {activeTab !== "demo" && help.notice && (
        <Callout tone={help.unknown ? "warn" : "bad"}>{help.notice} <Button size="sm" disabled={help.pending !== null} onClick={() => { void help.load(); }}>{help.unknown ? "核对已读结果" : "重新读取"}</Button></Callout>
      )}

      {activeTab === "guide" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[260px_1fr]">
          <Card pad={false}>
            <ul className="divide-y divide-line">
              {visibleTutorials.map((x) => (
                <li key={x.id}>
                  <button onClick={() => setTid(x.id)} className={cx("w-full px-4 py-3 text-left", t?.id === x.id ? "bg-brand-soft" : "hover:bg-surface-2")}>
                    <div className="text-[13px] font-medium">{x.title}</div>
                    <div className="text-[12px] text-muted">
                      约 {x.minutes} 分钟 · {audience[x.audience]}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <Card title={t?.title ?? "使用说明"} desc={t ? `适用：${audience[t.audience]}` : undefined}>
            {!t && <Empty title={help.pending === "catalog" ? "正在读取使用说明" : "尚未取得使用说明"}>目录来自服务端当前权限，不使用模拟教程补齐。</Empty>}
            <ol className="space-y-4">
              {(t?.steps ?? []).map((b, i) => (
                <li key={i} className="flex gap-3">
                  <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-brand-soft text-[12px] font-semibold text-brand">{i + 1}</span>
                  <p className="pt-0.5 text-[13.5px] leading-relaxed">{b}</p>
                </li>
              ))}
            </ol>
          </Card>
        </div>
      )}

      {activeTab === "news" && isAdmin && (
        <div className="space-y-3">
          {!announcements.length && <Card><Empty title={help.pending === "catalog" ? "正在读取公告" : help.catalog ? "暂无产品公告" : "尚未取得公告"}>本人已读状态以服务端当前记录为准。</Empty></Card>}
          {announcements.map((a) => {
            const read = a.read_at !== null;
            return (
              <Card key={a.id}>
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <div className="flex items-center gap-2">
                      {!read && <Badge tone="bad">未读</Badge>}
                      <h3 className="text-[14px] font-semibold">{a.title}</h3>
                    </div>
                    <div className="mt-0.5 text-[12px] text-muted">{displayTime(a.published_at, timezone)}</div>
                  </div>
                  {!read ? (
                    <Button size="sm" icon={<Check size={12} />} disabled={help.pending !== null} onClick={() => { void help.markRead(a.id); }}>
                      {help.pending === "read" ? "正在提交…" : help.unknown ? "核对已读结果" : "标为已读"}
                    </Button>
                  ) : (
                    <span className="text-[12px] text-faint" title={displayTime(a.read_at, timezone)}>已读</span>
                  )}
                </div>
                <p className="mt-2 text-[13px] leading-relaxed text-ink/85">{a.body}</p>
              </Card>
            );
          })}
        </div>
      )}

      {activeTab === "demo" && (
        <Card title="关于演示模式">
          <div className="space-y-3 text-[13px] leading-relaxed">
            <p>{demo.data ? `${demo.data.title}（合成数据）。这些示例不会写入真实业务，也不会切换当前登录身份。` : "当前为真实系统会话。此标签仅从独立服务读取合成示例，不切换真实身份或写入真实业务。"}</p>
            <ul className="list-disc space-y-1 pl-5 text-muted">
              {demo.data ? demo.data.facts.map(f => <li key={f.label}>{f.label}：{f.value}{f.unit ? ` ${f.unit}` : ""}</li>) : (
                <>
                  <li>{demo.pending ? "正在读取独立合成示例" : "尚未取得独立合成示例"}</li>
                  <li>不携带本系统会话或上游凭据，不调用真实 Sub2API、邮件或生产数据库</li>
                  <li>当前页面没有独立演示身份、场景和角色切换控件</li>
                </>
              )}
            </ul>
            <Callout tone="warn">{demo.error ?? demo.data?.explanation} 模拟结果不能作为真实联调或业务验收的依据。</Callout>
          </div>
        </Card>
      )}
    </>
  );
}

export default function HelpPage() {
  return (
    <Suspense>
      <HelpInner />
    </Suspense>
  );
}
