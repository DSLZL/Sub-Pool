"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { createAuthClient, createUsersClient } from "@sub-pool/contracts/client";
import type { Session } from "@sub-pool/contracts/auth";
import { initialAuth, SessionController, type AuthSnapshot } from "@/lib/session";

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
  theme: "light" | "dark";
  toggleTheme: () => void;
  toasts: Toast[];
  toast: (t: Omit<Toast, "id">) => void;
  dismiss: (id: number) => void;
  readAnnouncements: string[];
  markRead: (id: string) => void;
};

const Ctx = createContext<AppState | null>(null);

function safeGet(key: string): string | null {
  try { return window.localStorage.getItem(key); } catch { return null; }
}
function safeSet(key: string, value: string) {
  try { window.localStorage.setItem(key, value); } catch { /* Local preferences may be unavailable. */ }
}

export function AppProvider({ children }: { children: React.ReactNode }) {
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
  const [theme, setTheme] = useState<"light" | "dark">("light");
  const [toastState, setToasts] = useState<{ epoch: number; items: Toast[] }>({ epoch: 0, items: [] });
  const [readState, setRead] = useState<{ epoch: number; items: string[] }>({ epoch: 0, items: [] });

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
    const focus = () => { if (document.visibilityState === "visible") void controller.refresh(); };
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
  }, [controller]);

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
  const markRead = useCallback((id: string) => {
    setRead(state => {
      const items = state.epoch === auth.epoch ? state.items : [];
      return { epoch: auth.epoch, items: items.includes(id) ? items : [...items, id] };
    });
  }, [auth.epoch]);
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
    theme, toggleTheme,
    toasts: toastState.epoch === auth.epoch ? toastState.items : [], toast, dismiss,
    readAnnouncements: readState.epoch === auth.epoch ? readState.items : [], markRead,
  }), [auth, controller, refreshSession, theme, toggleTheme, toastState, toast, dismiss, readState, markRead]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp() {
  const value = useContext(Ctx);
  if (!value) throw new Error("useApp outside provider");
  return value;
}
