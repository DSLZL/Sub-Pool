"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";

export type Role = "admin" | "user";

export type Toast = { id: number; tone: "ok" | "bad" | "warn" | "info" | "unk"; title: string; body?: string };

type AppState = {
  role: Role;
  setRole: (r: Role) => void;
  isAdmin: boolean;
  userName: string;
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
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}
function safeSet(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [role, setRoleState] = useState<Role>("admin");
  const [theme, setTheme] = useState<"light" | "dark">("light");
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [readAnnouncements, setRead] = useState<string[]>([]);

  useEffect(() => {
    const r = safeGet("sp.role");
    if (r === "admin" || r === "user") setRoleState(r);
    const t = safeGet("sp.theme");
    if (t === "light" || t === "dark") setTheme(t);
    else if (window.matchMedia?.("(prefers-color-scheme: dark)").matches) setTheme("dark");
    const a = safeGet("sp.read");
    if (a) {
      try {
        setRead(JSON.parse(a));
      } catch {
        /* ignore */
      }
    }
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  const setRole = useCallback((r: Role) => {
    setRoleState(r);
    safeSet("sp.role", r);
  }, []);

  const toggleTheme = useCallback(() => {
    setTheme((t) => {
      const n = t === "light" ? "dark" : "light";
      safeSet("sp.theme", n);
      return n;
    });
  }, []);

  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), []);
  const toast = useCallback(
    (t: Omit<Toast, "id">) => {
      const id = Date.now() + Math.random();
      setToasts((ts) => [...ts, { ...t, id }]);
      setTimeout(() => dismiss(id), 4200);
    },
    [dismiss],
  );

  const markRead = useCallback((id: string) => {
    setRead((prev) => {
      if (prev.includes(id)) return prev;
      const n = [...prev, id];
      safeSet("sp.read", JSON.stringify(n));
      return n;
    });
  }, []);

  const value = useMemo<AppState>(
    () => ({
      role,
      setRole,
      isAdmin: role === "admin",
      userName: role === "admin" ? "admin" : "lin.yu",
      theme,
      toggleTheme,
      toasts,
      toast,
      dismiss,
      readAnnouncements,
      markRead,
    }),
    [role, setRole, theme, toggleTheme, toasts, toast, dismiss, readAnnouncements, markRead],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp() {
  const v = useContext(Ctx);
  if (!v) throw new Error("useApp outside provider");
  return v;
}
