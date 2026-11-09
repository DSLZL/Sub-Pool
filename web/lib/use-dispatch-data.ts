"use client";

import { useCallback, useEffect, useState } from "react";
import { createDispatchClient } from "@sub-pool/contracts/client";
import type { DispatchObservation } from "@sub-pool/contracts/dispatch";
import { connectionFailure } from "./connection-data";
import { DispatchReadError, readDispatchAccounts, readDispatchObservation } from "./dispatch-data";
import { useApp } from "./store";
import { useDispatchControl } from "./use-dispatch-control";

type Targets = Awaited<ReturnType<typeof readDispatchAccounts>>;
export function useDispatchData(selectedID: string | null = null) {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("dispatch"));
  const authorityRevision = auth.access?.user.revision ?? "", owner = `${auth.epoch}:${allowed}:${authorityRevision}`;
  const [refresh, setRefresh] = useState(0), key = `${owner}:${refresh}`;
  const control = useDispatchControl(key, allowed);
  const [targets, setTargets] = useState<{ key: string; data: Targets | null; error: string | null }>({ key: "", data: null, error: null });
  const [detail, setDetail] = useState<{ key: string; data: DispatchObservation | null; error: string | null }>({ key: "", data: null, error: null });
  const reload = useCallback(() => setRefresh(value => value + 1), []);
  const failure = useCallback((error: unknown) => {
    const safe = connectionFailure(error);
    if (error instanceof DispatchReadError && error.refreshIdentity || [401, 403, 404].includes(safe.status)) void refreshSession();
    return error instanceof DispatchReadError ? error.message : safe.status === 0 ? "调度读取尚未完成，请重新读取；没有使用演示数据。" : safe.text;
  }, [refreshSession]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [reload]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setTargets({ key, data: null, error: null });
    if (allowed) void (async () => {
      try {
        const client = createDispatchClient(window.location.origin);
        const result = await readDispatchAccounts(after => client.list({ after, search: "", limit: 100 }, { signal: abort.signal }), authorityRevision);
        if (alive) setTargets({ key, data: result, error: null });
      } catch (error) { if (alive) setTargets({ key, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, authorityRevision, failure]);
  const data = allowed && targets.key === key ? targets.data : null;
  const selected = selectedID === null ? null : selectedID === "" ? data?.accounts[0] ?? null : data?.accounts.find(account => account.id === selectedID) ?? null;
  const detailKey = `${key}:${selected?.id}:${selected?.revision}:${selected?.source_id}:${data?.last.connection_revision}`;
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setDetail({ key: detailKey, data: null, error: null });
    if (allowed && data && selected?.source_current) void (async () => {
      const input = { id: selected.id, revision: selected.revision, source_id: selected.source_id, connection_revision: data.last.connection_revision };
      try {
        const result = await readDispatchObservation(() => createDispatchClient(window.location.origin).account(input, { signal: abort.signal }), input, authorityRevision);
        if (alive) setDetail({ key: detailKey, data: result, error: null });
      } catch (error) { if (alive) setDetail({ key: detailKey, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [detailKey, allowed, data, selected, authorityRevision, failure]);
  const visible = allowed && detail.key === detailKey;
  return { allowed, owner, key, control, accounts: data?.accounts ?? [], status: data?.last ?? null, selected, ready: data !== null,
    observation: visible ? detail.data : null,
    error: allowed && targets.key === key ? targets.error ?? (visible ? detail.error : null) : null,
    loading: allowed && (!data && !targets.error || !!selected?.source_current && (!visible || !detail.data && !detail.error)), reload };
}
