"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { requestFailureText } from "./request-id";
import { createPoolsClient } from "@sub-pool/contracts/client";
import type { PoolContract } from "@sub-pool/contracts/pools";
import { useApp } from "@/lib/store";
import { connectionFailure } from "@/lib/connection-data";
import { readPoolPages, readPoolHistoryPage, readPoolContracts, readPoolMemberPages } from "@/lib/pools-data";

type PoolData = { epoch: number; pools: PoolContract[]; history: PoolContract[]; historyNext: string | null; ready: boolean; error: string | null };
const empty: PoolData = { epoch: -1, pools: [], history: [], historyNext: null, ready: false, error: null };

export function usePoolData(withHistory = true) {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("pools"));
  const scopeRevision = auth.access?.user.revision;
  const [data, setData] = useState<PoolData>(empty);
  const [historyCursor, setHistoryCursor] = useState({ epoch: -1, after: "0" });
  const historyAfter = historyCursor.epoch === auth.epoch ? historyCursor.after : "0";
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
        withHistory ? readPoolHistoryPage(after => client.history({ id: "", after, limit: 25 }, { signal }), historyAfter) : Promise.resolve(null),
      ]);
      const [pools, contracts] = await Promise.all([
        readPoolContracts(summaries, item => readPoolMemberPages(client, item, scopeRevision, signal)),
        readPoolContracts(history?.contracts ?? [], item => readPoolMemberPages(client, item, scopeRevision, signal)),
      ]);
      if (current()) setData({ epoch: auth.epoch, pools, history: contracts, historyNext: history?.next_after ?? null, ready: true, error: null });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setData({ ...empty, epoch: auth.epoch, error: failure.status === 0 ? requestFailureText(failure.text, "池记录不全。") : failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    }
  }, [allowed, auth.epoch, historyAfter, refreshSession, scopeRevision, withHistory]);
  useEffect(() => {
    request.current.alive = true; void reload();
    const focus = () => { if (document.visibilityState === "visible") void reload(); };
    window.addEventListener("focus", focus);
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); window.removeEventListener("focus", focus); };
  }, [reload]);
  return { ...(allowed && data.epoch === auth.epoch ? data : empty), allowed, reload, historyAfter,
    changeHistoryPage: (after: string) => setHistoryCursor({ epoch: auth.epoch, after }) };
}
