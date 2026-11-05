"use client";

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
  FlaskConical,
  Lock,
} from "lucide-react";
import { useApp } from "@/lib/store";
import { cx, Badge, Empty } from "@/components/ui";

type NavItem = { href: string; label: string; icon: React.ElementType; perm: "all" | "admin" | string };

export const NAV: { group: string; items: NavItem[] }[] = [
  { group: "", items: [{ href: "/", label: "概览", icon: LayoutDashboard, perm: "overview" }] },
  {
    group: "资源与数据",
    items: [
      { href: "/accounts", label: "账号与额度", icon: Server, perm: "accounts" },
      { href: "/usage", label: "用量与费用", icon: BarChart3, perm: "usage" },
      { href: "/observations", label: "观测记录", icon: ScanSearch, perm: "observations" },
      { href: "/estimates", label: "容量估算", icon: Gauge, perm: "estimates" },
    ],
  },
  {
    group: "分配与余额",
    items: [
      { href: "/pools", label: "参与者与额度池", icon: Users, perm: "participants" },
      { href: "/balances", label: "余额建议与应用", icon: Wallet, perm: "balances" },
      { href: "/temporary", label: "临时放宽", icon: Zap, perm: "admin" },
    ],
  },
  {
    group: "上游操作",
    items: [
      { href: "/pricing", label: "分组计价", icon: Tags, perm: "admin" },
      { href: "/dispatch", label: "账号暂停", icon: PauseCircle, perm: "dispatch" },
    ],
  },
  {
    group: "系统",
    items: [
      { href: "/connection", label: "连接与采集", icon: Plug, perm: "admin" },
      { href: "/notifications", label: "通知", icon: Mail, perm: "notifications" },
      { href: "/users", label: "用户与权限", icon: UserCog, perm: "admin" },
      { href: "/security", label: "登录与审计", icon: ShieldCheck, perm: "audit" },
      { href: "/api-keys", label: "API Key", icon: KeyRound, perm: "all" },
      { href: "/maintenance", label: "历史维护", icon: Wrench, perm: "admin" },
      { href: "/backup", label: "备份与恢复", icon: DatabaseBackup, perm: "admin" },
    ],
  },
  { group: "", items: [{ href: "/help", label: "帮助与公告", icon: LifeBuoy, perm: "all" }] },
];

/** Front-end visibility only. Real authorization is decided server-side. */
export function canSee(perm: string, isAdmin: boolean) {
  return perm === "all" || isAdmin;
}

function navPermFor(path: string) {
  for (const g of NAV) for (const i of g.items) if (i.href === path) return i.perm;
  return "all";
}

export function Shell({ children }: { children: React.ReactNode }) {
  const { isAdmin, role, theme, toggleTheme, userName, auth, signOut, toasts, dismiss } = useApp();
  const pathname = usePathname();
  const [open, setOpen] = useState(false);
  const [menu, setMenu] = useState(false);
  useEffect(() => setOpen(false), [pathname]);

  const allowed = canSee(navPermFor(pathname), isAdmin);

  const nav = (
    <nav className="flex flex-col gap-4 px-3 py-4" aria-label="主导航">
      {NAV.map((g, gi) => {
        const items = g.items.filter((i) => canSee(i.perm, isAdmin));
        if (!items.length) return null;
        return (
          <div key={gi}>
            {g.group && <div className="mb-1 px-2 text-[11px] font-medium tracking-wide text-faint">{g.group}</div>}
            <ul className="space-y-0.5">
              {items.map((i) => {
                const active = i.href === "/" ? pathname === "/" : pathname.startsWith(i.href);
                return (
                  <li key={i.href}>
                    <Link
                      href={i.href}
                      className={cx(
                        "flex items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] transition-colors",
                        active ? "bg-brand-soft font-medium text-brand" : "text-muted hover:bg-surface-2 hover:text-ink",
                      )}
                    >
                      <i.icon size={16} strokeWidth={1.8} />
                      {i.label}
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
        <div className="flex-1 overflow-y-auto">{nav}</div>
        <div className="border-t border-line px-4 py-3 text-[11px] leading-relaxed text-faint">
          目标平台：Sub2API（单实例）
          <br />
          统计时区：Asia/Shanghai
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
            <div className="flex-1 overflow-y-auto">{nav}</div>
          </aside>
        </div>
      )}

      <div className="lg:pl-56">
        <header className="sticky top-0 z-20 flex h-13 items-center gap-2 border-b border-line bg-surface/90 px-4 backdrop-blur">
          <button onClick={() => setOpen(true)} className="rounded p-1.5 text-muted hover:bg-surface-2 lg:hidden" aria-label="打开菜单">
            <Menu size={18} />
          </button>
          <Badge tone="unk" className="hidden sm:inline-flex">
            <FlaskConical size={11} />
            本系统会话
          </Badge>
          <div className="ml-auto flex items-center gap-1.5">
            <div className="hidden items-center gap-1 rounded-md border border-line bg-surface-2 p-0.5 sm:flex" title="当前账户身份">
              {(["admin", "user"] as const).map((r) => (
                <button
                  key={r}
                  disabled
                  aria-pressed={role === r}
                  className={cx("rounded px-2 py-0.5 text-[12px] font-medium", role === r ? "bg-surface text-ink shadow-sm" : "text-muted")}
                >
                  {r === "admin" ? "管理员" : "普通用户"}
                </button>
              ))}
            </div>
            {isAdmin && (
              <Link href="/help?tab=news" className="relative rounded-md p-2 text-muted hover:bg-surface-2" aria-label="公告，数据暂不可用">
                <Bell size={17} />
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
                    {isAdmin ? "管理员 · 本系统账户" : "普通用户 · 按授权范围访问"}
                  </div>
                  <div className="flex gap-1 px-2.5 pb-2 sm:hidden">
                    {(["admin", "user"] as const).map((r) => (
                      <button key={r} disabled aria-pressed={role === r} className={cx("rounded border px-2 py-0.5 text-[12px]", role === r ? "border-brand text-brand" : "border-line text-muted")}>
                        {r === "admin" ? "管理员" : "普通用户"}
                      </button>
                    ))}
                  </div>
                  <Link href="/profile" className="flex items-center gap-2 rounded px-2.5 py-1.5 text-[13px] hover:bg-surface-2" onClick={() => setMenu(false)}>
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
            pathname === "/profile" ? children : (
              <div className="rounded-lg border border-line bg-surface">
                <Empty title="此功能暂不可用">当前没有可用的数据服务，请稍后再试。</Empty>
              </div>
            )
          ) : (
            <div className="rounded-lg border border-line bg-surface">
              <Empty icon={<Lock size={28} />} title="当前身份无权访问此功能">
                该功能未授予给你的账户。如需访问，请联系管理员分配功能权限。页面不可见并不代替服务端鉴权，直接请求接口同样会被拒绝。
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
    <Link href="/" className="flex h-13 items-center gap-2 px-4">
      <svg width="24" height="24" viewBox="0 0 24 24" aria-hidden>
        <rect x="2" y="2" width="20" height="20" rx="6" fill="var(--brand)" />
        <path d="M6.5 14.5c2.2 1.6 4.4 1.6 6.6 0s3.6-1.6 4.4-.6" stroke="var(--brand-ink)" strokeWidth="1.8" fill="none" strokeLinecap="round" />
        <path d="M6.5 10c2.2 1.6 4.4 1.6 6.6 0s3.6-1.6 4.4-.6" stroke="var(--brand-ink)" strokeWidth="1.8" fill="none" strokeLinecap="round" opacity=".55" />
      </svg>
      <span className="text-[15px] font-semibold tracking-tight">Sub-Pool</span>
    </Link>
  );
}
