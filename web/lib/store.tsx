"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { usePathname } from "next/navigation";
import { createAuthClient, createConnectionClient, createUsersClient } from "@sub-pool/contracts/client";
import type { Session } from "@sub-pool/contracts/auth";
import { initialAuth, SessionController, type AuthSnapshot } from "@/lib/session";
import { useHelpData } from "@/lib/use-help-data";
import { usageCalendar } from "@sub-pool/contracts/participants";

export type Role = Session["user"]["role"];
export type Toast = { id: number; tone: "ok" | "bad" | "warn" | "info" | "unk"; title: string; body?: string };

type AppState = {
  role: Role;
  isAdmin: boolean;
  userName: string;
  auth: AuthSnapshot;
  signIn: (username: string, password: string) => Promise<boolean>;
  signOut: () => Promise<boolean>;
  changePassword: (current: string, next: string) => Promise<boolean>;
  refreshSession: () => Promise<void>;
  timezone: string | null;
  settingsRevision: string | null;
  refreshSettings: () => Promise<void>;
  theme: "light" | "dark";
  toggleTheme: () => void;
  toasts: Toast[];
  toast: (t: Omit<Toast, "id">) => void;
  dismiss: (id: number) => void;
  help: ReturnType<typeof useHelpData>;
};

const Ctx = createContext<AppState | null>(null);

function safeGet(key: string): string | null {
  try { return window.localStorage.getItem(key); } catch { return null; }
}
function safeSet(key: string, value: string) {
  try { window.localStorage.setItem(key, value); } catch { /* Local preferences may be unavailable. */ }
}

export function AppProvider({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  return pathname === "/demo" ? children : <SessionAppProvider>{children}</SessionAppProvider>;
}

function SessionAppProvider({ children }: { children: React.ReactNode }) {
  const channel = useRef<BroadcastChannel | null>(null);
  const [controller] = useState(() => new SessionController(
    () => createAuthClient(window.location.origin),
    signal => createUsersClient(window.location.origin).access({}, { signal }),
    event => {
      if (channel.current) channel.current.postMessage(event);
      else safeSet("sp.auth.invalidate", `${event}:${Date.now()}:${Math.random()}`);
    },
  ));
  const auth = useSyncExternalStore(controller.subscribe, controller.getSnapshot, () => initialAuth);
  const help = useHelpData(auth, controller);
  const [theme, setTheme] = useState<"light" | "dark">("light");
  const [toastState, setToasts] = useState<{ epoch: number; items: Toast[] }>({ epoch: 0, items: [] });
  const [settingsState, setSettingsState] = useState<{ epoch: number; timezone: string | null; revision: string | null }>({ epoch: -1, timezone: null, revision: null });
  const settingsRequest = useRef({ generation: 0, abort: new AbortController() });
  const refreshSettings = useCallback(async () => {
    const before = controller.getSnapshot();
    if (before.status !== "authenticated") return;
    settingsRequest.current.abort.abort(); settingsRequest.current.abort = new AbortController();
    const generation = ++settingsRequest.current.generation;
    const matches = () => settingsRequest.current.generation === generation
      && controller.getSnapshot().status === "authenticated" && controller.getSnapshot().epoch === before.epoch;
    try {
      const settings = await createConnectionClient(window.location.origin).settings({}, { signal: settingsRequest.current.abort.signal });
      usageCalendar(settings.timezone);
      if (matches()) setSettingsState({ epoch: before.epoch, timezone: settings.timezone, revision: settings.revision });
    } catch { if (matches()) setSettingsState({ epoch: before.epoch, timezone: null, revision: null }); }
  }, [controller]);

  useEffect(() => {
    if (auth.status === "authenticated") void refreshSettings();
    return () => { settingsRequest.current.generation++; settingsRequest.current.abort.abort(); };
  }, [auth.status, auth.epoch, refreshSettings]);

  useEffect(() => {
    const t = safeGet("sp.theme");
    if (t === "light" || t === "dark") setTheme(t);
    else if (window.matchMedia?.("(prefers-color-scheme: dark)").matches) setTheme("dark");
    const receive = (event: unknown) => {
      if (event === "refresh") void controller.revalidateIdentity();
      else if (event === "clear") controller.invalidate("unknown");
    };
    try {
      if (typeof BroadcastChannel !== "undefined") {
        channel.current = new BroadcastChannel("sub-pool-auth");
        channel.current.onmessage = event => receive(event.data);
      }
    } catch { channel.current = null; }
    const storage = (event: StorageEvent) => {
      if (event.key === "sp.auth.invalidate") receive(event.newValue?.split(":")[0]);
    };
    const focus = () => { if (document.visibilityState === "visible") { void controller.refresh(); void refreshSettings(); } };
    const activity = (event: Event) => {
      if (event.isTrusted && document.visibilityState === "visible") void controller.activity();
    };
    window.addEventListener("storage", storage);
    window.addEventListener("focus", focus);
    document.addEventListener("visibilitychange", focus);
    window.addEventListener("pointerdown", activity, { passive: true });
    window.addEventListener("keydown", activity);
    void controller.refresh();
    return () => {
      controller.dispose();
      channel.current?.close(); channel.current = null;
      window.removeEventListener("storage", storage);
      window.removeEventListener("focus", focus);
      document.removeEventListener("visibilitychange", focus);
      window.removeEventListener("pointerdown", activity);
      window.removeEventListener("keydown", activity);
    };
  }, [controller, refreshSettings]);

  useEffect(() => { document.documentElement.dataset.theme = theme; }, [theme]);
  const toggleTheme = useCallback(() => {
    setTheme(t => { const next = t === "light" ? "dark" : "light"; safeSet("sp.theme", next); return next; });
  }, []);
  const dismiss = useCallback((id: number) => setToasts(state => ({ ...state, items: state.items.filter(t => t.id !== id) })), []);
  const toast = useCallback((item: Omit<Toast, "id">) => {
    const id = Date.now() + Math.random();
    setToasts(state => ({ epoch: auth.epoch, items: [...(state.epoch === auth.epoch ? state.items : []), { ...item, id }] }));
    setTimeout(() => dismiss(id), 4200);
  }, [auth.epoch, dismiss]);
  const refreshSession = useCallback(() => controller.revalidateIdentity(), [controller]);

  const value = useMemo<AppState>(() => ({
    role: auth.session?.user.role ?? "user",
    isAdmin: auth.session?.user.role === "admin",
    userName: auth.session?.user.username ?? "",
    auth,
    signIn: (username, password) => controller.login(username, password),
    signOut: () => controller.logout(),
    changePassword: (current, next) => controller.changePassword(current, next),
    refreshSession,
    timezone: settingsState.epoch === auth.epoch ? settingsState.timezone : null, refreshSettings,
    settingsRevision: settingsState.epoch === auth.epoch ? settingsState.revision : null,
    theme, toggleTheme,
    toasts: toastState.epoch === auth.epoch ? toastState.items : [], toast, dismiss,
    help,
  }), [auth, controller, refreshSession, settingsState, refreshSettings, theme, toggleTheme, toastState, toast, dismiss, help]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp() {
  const value = useContext(Ctx);
  if (!value) throw new Error("useApp outside provider");
  return value;
}
