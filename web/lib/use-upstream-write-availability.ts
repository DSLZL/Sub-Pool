"use client";

import { createConnectionClient } from "@sub-pool/contracts/client";
import type { ConnectionConfiguration, ConnectionWriteGrants } from "@sub-pool/contracts/connection";
import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { connectionFailure, sourceWriteAvailability } from "./connection-data";
import { useApp } from "./store";

export const UpstreamWriteReloadContext = createContext<(() => void) | null>(null);

export function useUpstreamWriteReload() {
  const reload = useContext(UpstreamWriteReloadContext);
  if (!reload) throw new Error("UpstreamWriteReloadContext is required");
  return reload;
}

export function useUpstreamWriteAvailability() {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && isAdmin;
  const [refresh, setRefresh] = useState(0);
  const key = `${auth.epoch}:${auth.access?.user.revision ?? ""}:${refresh}`;
  const [snapshot, setSnapshot] = useState<{ key: string; configuration: ConnectionConfiguration; grants: ConnectionWriteGrants } | null>(null);
  const reload = useCallback(() => setRefresh(value => value + 1), []);

  useEffect(() => {
    if (!allowed) return;
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [allowed, reload]);

  useEffect(() => {
    if (!allowed) return;
    const abort = new AbortController();
    const client = createConnectionClient(window.location.origin);
    const options = { signal: abort.signal };
    void Promise.all([client.get({}, options), client.writeGrants({}, options)]).then(([configuration, grants]) => {
      if (!abort.signal.aborted) setSnapshot({ key, configuration, grants });
    }).catch(error => {
      if (abort.signal.aborted) return;
      const { status } = connectionFailure(error);
      if (status === 401 || status === 403) void refreshSession();
    });
    return () => abort.abort();
  }, [allowed, key, refreshSession]);

  const current = allowed && snapshot?.key === key ? snapshot : null;
  const status = (capability: "pricing" | "scheduling") => {
    if (!allowed) return "closed";
    return sourceWriteAvailability(current?.configuration ?? null, current?.grants ?? null, capability);
  };
  return { pricing: status("pricing"), scheduling: status("scheduling"), reload };
}
