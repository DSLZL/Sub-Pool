"use client";

import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { requestFailureText } from "./request-id";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import type { TemporaryStatus } from "@sub-pool/contracts/temporary";
import { connectionFailure } from "./connection-data";
import { useApp } from "./store";

export function useTemporaryStatus() {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && isAdmin;
  const [refresh, setRefresh] = useState(0), key = `${auth.epoch}:${allowed}:${refresh}`;
  const [state, setState] = useState<{ key: string; status: TemporaryStatus | null; error: string | null }>({ key: "", status: null, error: null });
  const reload = useCallback(() => setRefresh(value => value + 1), []);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [reload]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setState({ key, status: null, error: null });
    if (allowed) void (async () => {
      try {
        const status = await createTemporaryClient(window.location.origin).status({}, { signal: abort.signal });
        if (alive) setState({ key, status, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ key, status: null, error: failure.status === 0 ? requestFailureText(failure.text, "临时参与状态尚未读取，请重新读取。") : failure.text });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, refreshSession]);
  const visible = allowed && state.key === key;
  const status = visible ? state.status : null;
  return { simulation: status && status.state !== "unavailable" ? status : null, error: visible ? state.error : null, reload };
}

export const TemporaryStatusContext = createContext<ReturnType<typeof useTemporaryStatus> | null>(null);

export function useConsoleTemporaryStatus() {
  const temporary = useContext(TemporaryStatusContext);
  if (!temporary) throw new Error("TemporaryStatusContext is required");
  return temporary;
}
