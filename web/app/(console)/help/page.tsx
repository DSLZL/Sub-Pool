"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { BookOpen, Megaphone, FlaskConical, Check } from "lucide-react";
import { Badge, Button, Callout, Card, PageHeader, Tabs, cx } from "@/components/ui";
import { useApp } from "@/lib/store";
import { announcements, tutorials } from "@/lib/mock";

function HelpInner() {
  const params = useSearchParams();
  const { isAdmin, readAnnouncements, markRead } = useApp();
  const [tab, setTab] = useState("guide");
  const visibleTutorials = tutorials.filter((t) => isAdmin || t.roles === "全部用户");
  const [tid, setTid] = useState(visibleTutorials[0].id);
  const t = visibleTutorials.find((x) => x.id === tid) ?? visibleTutorials[0];

  useEffect(() => {
    const q = params.get("tab");
    if (q === "news" && isAdmin) setTab("news");
  }, [params, isAdmin]);

  return (
    <>
      <PageHeader title="帮助与公告" tag="S2A-OPS-006 ~ 008" desc="原创使用说明只覆盖当前实际可用的功能。" />
      <Tabs
        value={tab}
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
                      {announcements.some((a) => !readAnnouncements.includes(a.id)) && <span className="h-1.5 w-1.5 rounded-full bg-bad" />}
                    </span>
                  ),
                },
              ]
            : []),
          { id: "demo", label: <span className="inline-flex items-center gap-1.5"><FlaskConical size={14} />演示模式</span> },
        ]}
      />

      {tab === "guide" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[260px_1fr]">
          <Card pad={false}>
            <ul className="divide-y divide-line">
              {visibleTutorials.map((x) => (
                <li key={x.id}>
                  <button onClick={() => setTid(x.id)} className={cx("w-full px-4 py-3 text-left", tid === x.id ? "bg-brand-soft" : "hover:bg-surface-2")}>
                    <div className="text-[13px] font-medium">{x.title}</div>
                    <div className="text-[12px] text-muted">
                      约 {x.minutes} 分钟 · {x.roles}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <Card title={t.title} desc={`适用：${t.roles}`}>
            <ol className="space-y-4">
              {t.body.map((b, i) => (
                <li key={i} className="flex gap-3">
                  <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-brand-soft text-[12px] font-semibold text-brand">{i + 1}</span>
                  <p className="pt-0.5 text-[13.5px] leading-relaxed">{b}</p>
                </li>
              ))}
            </ol>
          </Card>
        </div>
      )}

      {tab === "news" && (
        <div className="space-y-3">
          {announcements.map((a) => {
            const read = readAnnouncements.includes(a.id);
            return (
              <Card key={a.id}>
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <div className="flex items-center gap-2">
                      {!read && <Badge tone="bad">未读</Badge>}
                      <h3 className="text-[14px] font-semibold">{a.title}</h3>
                    </div>
                    <div className="mt-0.5 text-[12px] text-muted">{a.date}</div>
                  </div>
                  {!read ? (
                    <Button size="sm" icon={<Check size={12} />} onClick={() => markRead(a.id)}>
                      标为已读
                    </Button>
                  ) : (
                    <span className="text-[12px] text-faint">已读</span>
                  )}
                </div>
                <p className="mt-2 text-[13px] leading-relaxed text-ink/85">{a.body}</p>
              </Card>
            );
          })}
        </div>
      )}

      {tab === "demo" && (
        <Card title="关于演示模式">
          <div className="space-y-3 text-[13px] leading-relaxed">
            <p>当前界面运行在演示模式下，展示的账号、参与者、余额与操作结果全部是合成数据。</p>
            <ul className="list-disc space-y-1 pl-5 text-muted">
              <li>不连接真实的 Sub2API、邮件服务或生产数据库</li>
              <li>所有“写入”“发送”“恢复”操作只在页面内模拟，刷新后复原</li>
              <li>可通过右上角切换“管理员 / 普通用户”，查看不同权限下的界面差异</li>
            </ul>
            <Callout tone="warn">模拟结果不能作为真实联调或业务验收的依据。</Callout>
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
