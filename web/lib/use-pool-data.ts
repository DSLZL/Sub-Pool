"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { requestFailureText } from "./request-id";
import { createPoolsClient } from "@sub-pool/contracts/client";
import type { PoolContract } from "@sub-pool/contracts/pools";
import { useApp } from "@/lib/store";
import { connectionFailure } from "@/lib/connection-data";
import { readPoolPages, readPoolHistoryPages, readPoolContracts } from "@/lib/pools-data";

type PoolData = { epoch: number; pools: PoolContract[]; history: PoolContract[]; ready: boolean; error: string | null };
const empty: PoolData = { epoch: -1, pools: [], history: [], ready: false, error: null };

export function usePoolData(withHistory = true) {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("pools"));
  const [data, setData] = useState<PoolData>(empty);
  const request = useRef({ alive: false, generation: 0, abort: new AbortController() });
  const reload = useCallback(async () => {
    if (!allowed || !request.current.alive) return;
    request.current.abort.abort(); request.current.abort = new AbortController();
    const generation = ++request.current.generation, signal = request.current.abort.signal;
    const current = () => request.current.alive && request.current.generation === generation;
    setData({ ...empty, epoch: auth.epoch });
    try {
      const client = createPoolsClient(window.location.origin);
      const [summaries, history] = await Promise.all([
        readPoolPages(after => client.list({ after, search: "", limit: 100 }, { signal })),
        withHistory ? readPoolHistoryPages(after => client.history({ id: "", after, limit: 100 }, { signal })) : Promise.resolve([]),
      ]);
      const [pools, contracts] = await Promise.all([
        readPoolContracts(summaries, item => client.get({ id: item.pool_id }, { signal })),
        readPoolContracts(history, item => client.contract({ id: item.id }, { signal })),
      ]);
      if (current()) setData({ epoch: auth.epoch, pools, history: contracts, ready: true, error: null });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setData({ ...empty, epoch: auth.epoch, error: failure.status === 0 ? requestFailureText(failure.text, "暂时无法完整读取额度池与历史依据。") : failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    }
  }, [allowed, auth.epoch, refreshSession, withHistory]);
  useEffect(() => {
    request.current.alive = true; void reload();
    const focus = () => { if (document.visibilityState === "visible") void reload(); };
    window.addEventListener("focus", focus);
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); window.removeEventListener("focus", focus); };
  }, [reload]);
  return { ...(allowed && data.epoch === auth.epoch ? data : empty), allowed, reload };
}
