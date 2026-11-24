"use client";

import { glossary } from "@/lib/glossary";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import {
  LayoutDashboard,
  Server,
  BarChart3,
  ScanSearch,
  Gauge,
  Users,
  Wallet,
  Zap,
  Tags,
  PauseCircle,
  Plug,
  Mail,
  ShieldCheck,
  UserCog,
  KeyRound,
  Wrench,
  DatabaseBackup,
  LifeBuoy,
  Menu,
  X,
  Moon,
  Sun,
  Bell,
  LogOut,
  UserRound,
  Lock,
} from "lucide-react";
import { useApp } from "@/lib/store";
import { canAccess } from "@/lib/access";
import { TemporaryStatusContext, useTemporaryStatus } from "@/lib/use-temporary-status";
import { UpstreamWriteReloadContext, useUpstreamWriteAvailability } from "@/lib/use-upstream-write-availability";
import { cx, Badge, Empty } from "@/components/ui";

type NavItem = { href: string; label: string; icon: React.ElementType; perm: "all" | "admin" | string };

export const NAV: { group: string; items: NavItem[] }[] = [
  { group: "", items: [{ href: "/console", label: "概览", icon: LayoutDashboard, perm: "overview" }] },
  {
    group: "资源与数据",
    items: [
      { href: "/console/accounts", label: "账号与额度", icon: Server, perm: "accounts" },
      { href: "/console/usage", label: "用量与费用", icon: BarChart3, perm: "usage" },
      { href: "/console/observations", label: "观测记录", icon: ScanSearch, perm: "observations" },
      { href: "/console/estimates", label: "容量估算", icon: Gauge, perm: "estimates" },
    ],
  },
  {
    group: "分配与余额",
    items: [
      { href: "/console/pools", label: "参与者与额度池", icon: Users, perm: "pools" },
      { href: "/console/balances", label: glossary.balances, icon: Wallet, perm: "balances" },
      { href: "/console/temporary", label: "临时放宽", icon: Zap, perm: "temporary" },
    ],
  },
  {
    group: `${glossary.sub2api} 操作`,
    items: [
      { href: "/console/pricing", label: "分组计价", icon: Tags, perm: "pricing" },
      { href: "/console/dispatch", label: "账号暂停", icon: PauseCircle, perm: "dispatch" },
    ],
  },
  {
    group: "系统",
    items: [
      { href: "/console/connection", label: "连接与采集", icon: Plug, perm: "admin" },
      { href: "/console/notifications", label: "通知", icon: Mail, perm: "notifications" },
      { href: "/console/users", label: "用户与权限", icon: UserCog, perm: "admin" },
      { href: "/console/security", label: "登录与审计", icon: ShieldCheck, perm: "security" },
      { href: "/console/api-keys", label: "API Key", icon: KeyRound, perm: "all" },
      { href: "/console/maintenance", label: "历史维护", icon: Wrench, perm: "admin" },
      { href: "/console/backup", label: "备份与恢复", icon: DatabaseBackup, perm: "admin" },
    ],
  },
  { group: "", items: [{ href: "/console/help", label: "帮助与公告", icon: LifeBuoy, perm: "all" }] },
];

function navPermFor(path: string) {
  for (const g of NAV) for (const i of g.items) if (i.href === path) return i.perm;
  return path === "/console/profile" ? "all" : "admin";
}

export function Shell({ children }: { children: React.ReactNode }) {
  const { isAdmin, theme, toggleTheme, userName, auth, signOut, toasts, dismiss, displayTimezone, help } = useApp();
  const temporary = useTemporaryStatus();
  const writeAvailability = useUpstreamWriteAvailability();
  const pathname = usePathname();
  const [open, setOpen] = useState(false);
  const [menu, setMenu] = useState(false);
  useEffect(() => setOpen(false), [pathname]);

  const allowed = canAccess(navPermFor(pathname), auth.access);
  const roleLabel = isAdmin ? "管理员" : "普通用户";
  const timezoneLabel = displayTimezone === undefined ? "时区读取中…" : "统计时区：" + (displayTimezone ?? "UTC（设置未取得）");
  const unread = isAdmin && help.catalog ? help.catalog.announcements.filter(a => a.read_at === null).length : null;

  const nav = (location: "desktop" | "mobile") => (
    <nav className="flex flex-col gap-4 px-3 py-4" aria-label="主导航">
      {NAV.map((g, gi) => {
        const items = g.items.filter((i) => canAccess(i.perm, auth.access) && (i.perm !== "temporary" || temporary.simulation));
        if (!items.length) return null;
        return (
          <div key={gi}>
            {g.group && <div className="mb-1 px-2 text-[11px] font-medium tracking-wide text-faint">{g.group}</div>}
            <ul className="space-y-0.5">
              {items.map((i) => {
                const active = i.href === "/console" ? pathname === "/console" : pathname.startsWith(i.href);
                const writeStatus = i.perm === "pricing" ? writeAvailability.pricing : i.perm === "dispatch" ? writeAvailability.scheduling : null;
                const writeLabel = writeStatus === "closed" ? "未开放" : writeStatus === "unknown" ? "待核对" : null;
                const writeLabelID = writeLabel ? `${location}-write-${i.perm}` : undefined;
                return (
                  <li key={i.href}>
                    <Link
                      href={i.href}
                      aria-label={writeLabel ? i.label : undefined}
                      aria-describedby={writeLabelID}
                      className={cx(
                        "flex items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
                        active ? "bg-brand-soft font-medium text-brand" : "text-muted hover:bg-surface-2 hover:text-ink",
                      )}
                    >
                      <i.icon size={16} strokeWidth={1.8} />
                      {i.label}
                      {writeLabel && <span id={writeLabelID} className="ml-auto shrink-0"><Badge tone="muted">{writeLabel}</Badge></span>}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        );
      })}
    </nav>
  );

  return (
    <div className="min-h-screen">
      {/* Sidebar desktop */}
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-56 flex-col border-r border-line bg-surface lg:flex">
        <Brand />
        <div className="flex-1 overflow-y-auto">{nav("desktop")}</div>
        <div className="border-t border-line px-4 py-3 text-[11px] leading-relaxed text-faint">
          目标平台：{glossary.sub2api}（单实例）
        </div>
      </aside>

      {/* Sidebar mobile */}
      {open && (
        <div className="fixed inset-0 z-40 bg-black/40 lg:hidden" onClick={() => setOpen(false)}>
          <aside className="absolute inset-y-0 left-0 flex w-64 flex-col bg-surface" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between pr-2">
              <Brand />
              <button onClick={() => setOpen(false)} className="rounded p-1.5 text-muted hover:bg-surface-2" aria-label="关闭菜单">
                <X size={18} />
              </button>
            </div>
            <div className="flex-1 overflow-y-auto">{nav("mobile")}</div>
          </aside>
        </div>
      )}

      <div className="lg:pl-56">
        <header className="sticky top-0 z-20 flex h-13 items-center gap-2 border-b border-line bg-surface/90 px-4 backdrop-blur">
          <button onClick={() => setOpen(true)} className="rounded p-1.5 text-muted hover:bg-surface-2 lg:hidden" aria-label="打开菜单">
            <Menu size={18} />
          </button>
          <span className="min-w-0 truncate text-[11px] text-faint" title={timezoneLabel}>{timezoneLabel}</span>
          <div className="ml-auto flex items-center gap-1.5">
            <div className="hidden sm:block"><Badge>{roleLabel}</Badge></div>
            {isAdmin && (
              <Link href="/console/help?tab=news" className="relative rounded-md p-2 text-muted hover:bg-surface-2" aria-label={unread === null ? "公告，未读数尚未确认" : `公告，${unread} 条未读`}>
                <Bell size={17} />
                {unread !== null && unread > 0 && <span className="absolute top-1 right-1 h-2 w-2 rounded-full bg-bad" />}
              </Link>
            )}
            <button onClick={toggleTheme} className="rounded-md p-2 text-muted hover:bg-surface-2" aria-label="切换明暗主题">
              {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
            </button>
            <div className="relative">
              <button onClick={() => setMenu((m) => !m)} className="flex items-center gap-2 rounded-md px-2 py-1 hover:bg-surface-2">
                <span className="flex h-7 w-7 items-center justify-center rounded-full bg-brand-soft text-[12px] font-semibold text-brand">
                  {userName.slice(0, 1).toUpperCase()}
                </span>
                <span className="hidden text-[13px] sm:block">{userName}</span>
              </button>
              {menu && (
                <div className="absolute right-0 mt-1 w-52 rounded-lg border border-line bg-surface p-1 shadow-lg" onMouseLeave={() => setMenu(false)}>
                  <div className="px-2.5 py-2 text-[12px] text-muted">
                    <Badge className="mr-1.5 sm:hidden">{roleLabel}</Badge>
                    {isAdmin ? "本系统账户" : "按授权范围访问"}
                  </div>
                  <Link href="/console/profile" className="flex items-center gap-2 rounded px-2.5 py-1.5 text-[13px] hover:bg-surface-2" onClick={() => setMenu(false)}>
                    <UserRound size={14} /> 个人设置与密码
                  </Link>
                  <button
                    disabled={auth.busy}
                    onClick={() => { void signOut(); }}
                    className="flex w-full items-center gap-2 rounded px-2.5 py-1.5 text-left text-[13px] text-bad hover:bg-surface-2"
                  >
                    <LogOut size={14} /> 退出登录
                  </button>
                </div>
              )}
            </div>
          </div>
        </header>

        <main className="mx-auto max-w-[1280px] px-4 py-6 sm:px-6">
          {allowed ? (
            <UpstreamWriteReloadContext.Provider value={writeAvailability.reload}>
              <TemporaryStatusContext.Provider value={temporary}>{children}</TemporaryStatusContext.Provider>
            </UpstreamWriteReloadContext.Provider>
          ) : (
            <div className="rounded-lg border border-line bg-surface">
              <Empty icon={<Lock size={28} />} title="当前身份无权访问此功能">
                该功能需要授权。请联系管理员开通。
              </Empty>
            </div>
          )}
        </main>
      </div>

      {/* Toasts */}
      <div className="fixed right-4 bottom-4 z-[60] flex w-[min(360px,calc(100vw-32px))] flex-col gap-2" aria-live="polite">
        {toasts.map((t) => (
          <div
            key={t.id}
            className={cx(
              "rounded-lg border bg-surface px-3.5 py-2.5 shadow-lg",
              { ok: "border-ok/40", bad: "border-bad/40", warn: "border-warn/40", info: "border-info/40", unk: "border-unk/40" }[t.tone],
            )}
          >
            <div className="flex items-start gap-2">
              <span className={cx("mt-1.5 h-2 w-2 shrink-0 rounded-full", { ok: "bg-ok", bad: "bg-bad", warn: "bg-warn", info: "bg-info", unk: "bg-unk" }[t.tone])} />
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-medium">{t.title}</div>
                {t.body && <div className="mt-0.5 text-[12px] text-muted">{t.body}</div>}
              </div>
              <button onClick={() => dismiss(t.id)} className="text-faint hover:text-ink" aria-label="关闭">
                <X size={14} />
              </button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function Brand() {
  return (
    <Link href="/console" className="flex h-13 items-center gap-2 px-4">
      <svg width="24" height="24" viewBox="0 0 24 24" aria-hidden>
        <rect x="2" y="2" width="20" height="20" rx="6" fill="var(--brand)" />
        <path d="M6.5 14.5c2.2 1.6 4.4 1.6 6.6 0s3.6-1.6 4.4-.6" stroke="var(--brand-ink)" strokeWidth="1.8" fill="none" strokeLinecap="round" />
        <path d="M6.5 10c2.2 1.6 4.4 1.6 6.6 0s3.6-1.6 4.4-.6" stroke="var(--brand-ink)" strokeWidth="1.8" fill="none" strokeLinecap="round" opacity=".55" />
      </svg>
      <span className="text-[15px] font-semibold tracking-tight">Sub-Pool</span>
    </Link>
  );
}
