"use client";

import { useCallback, useEffect, useState } from "react";
import { requestFailureText } from "./request-id";
import { createPricingClient } from "@sub-pool/contracts/client";
import type { PricingOperation } from "@sub-pool/contracts/pricing";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";
import { PricingReadError, readPricingHistory } from "./pricing-data";

export function usePricingHistory() {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && isAdmin;
  const [refresh, setRefresh] = useState(0), key = `${auth.epoch}:${auth.access?.user.revision ?? ""}:${allowed}:${refresh}`;
  const [state, setState] = useState<{ key: string; operations: PricingOperation[] | null; error: string | null }>({ key: "", operations: null, error: null });
  const reload = useCallback(() => setRefresh(value => value + 1), []);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [reload]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setState({ key, operations: null, error: null });
    if (allowed) void (async () => {
      try {
        const client = createPricingClient(window.location.origin);
        const operations = await readPricingHistory(after => client.history({ source_id: "", group_id: "", after, limit: 100 }, { signal: abort.signal }), "");
        if (alive) setState({ key, operations, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ key, operations: null, error: error instanceof PricingReadError ? error.message : failure.status === 0 ? requestFailureText(failure.text, "计价操作记录尚未完整读取，请重新读取。") : failure.text });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, refreshSession]);
  const visible = allowed && state.key === key;
  return { operations: visible ? state.operations : null, error: visible ? state.error : null, reload };
}
