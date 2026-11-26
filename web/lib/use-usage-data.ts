"use client";

import { uiCopy } from "@/lib/glossary";
import { useCallback, useEffect, useMemo, useState } from "react";
import { requestFailureText } from "./request-id";
import { createParticipantsClient } from "@sub-pool/contracts/client";
import { connectionFailure } from "@/lib/connection-data";
import { RecordedSelectionError } from "@/lib/pools-data";
import { usageWindow, UsageReadError, type UsageWindow } from "@/lib/usage-data";
import { readRecordedUsagePage, type RecordedUsagePage, type UsageCursor } from "@/lib/recorded-usage-page";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";

export function useUsageData(scope: ReturnType<typeof useScope>, days: 7 | 14, account: string) {
  const { auth, isAdmin, refreshSession, timezone, settingsRevision, refreshSettings } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("usage"));
  const catalogAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const accounts = useMemo(() => catalogAllowed ? scope.accounts : (auth.access?.account_ids ?? []).map(id => ({ id, name: id })), [catalogAllowed, scope.accounts, auth.access?.account_ids]);
  const catalogReady = !catalogAllowed || scope.accountCatalogAvailable;
  const authorityRevision = auth.access?.user.revision ?? "";
  const owner = `${auth.epoch}:${authorityRevision}:${settingsRevision}:${timezone}`;
  const [refresh, setRefresh] = useState(0);
  const reload = useCallback(() => setRefresh(value => value + 1), []);
  const [page, setPage] = useState<(UsageCursor & { owner: string; account: string; days: number; refresh: number; window: UsageWindow; accounts: typeof accounts }) | null>(null);
  const [state, setState] = useState<{ owner: string; accounts: typeof accounts; days: number; account: string; refresh: number; data: RecordedUsagePage | null; window: UsageWindow | null; ready: boolean; error: string | null }>({ owner: "", accounts: [], days, account, refresh, data: null, window: null, ready: false, error: null });
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") setRefresh(value => value + 1); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, []);
  useEffect(() => {
    let alive = true;
    const cursor = page && page.owner === owner && page.account === account && page.days === days && page.refresh === refresh && page.accounts === accounts ? page : null;
    const abort = new AbortController(), window = cursor?.window ?? (timezone && settingsRevision ? usageWindow(days, new Date(), timezone) : null);
    const base = { owner, accounts, days, account, refresh, window };
    setState({ ...base, data: null, ready: false, error: null });
    if (!allowed || !catalogReady || !window || !timezone || !settingsRevision) return () => { alive = false; abort.abort(); };
    void (async () => {
      try {
        if (account !== "all" && !accounts.some(item => item.id === account)) throw new RecordedSelectionError(uiCopy.chooseAccount);
        const client = createParticipantsClient(globalThis.window.location.origin), signal = abort.signal;
        const data = await readRecordedUsagePage(client.recorded, account === "all" ? accounts : accounts.filter(item => item.id === account),
          window, { timezone, revision: settingsRevision, authority: authorityRevision }, "", cursor, signal);
        if (alive) setState({ ...base, data, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ ...base, data: null, ready: false, error: error instanceof RecordedSelectionError ? error.message : failure.status === 0 ? requestFailureText(failure.text, "用量读取不齐。") : failure.text });
        if (error instanceof UsageReadError || [401, 403, 404].includes(failure.status)) void refreshSession();
        if (error instanceof UsageReadError) void refreshSettings();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [allowed, owner, authorityRevision, accounts, catalogReady, days, account, refresh, page, refreshSession, timezone, settingsRevision, refreshSettings]);
  const current = allowed && state.owner === owner && state.accounts === accounts && state.days === days && state.account === account && state.refresh === refresh;
  const nextPage = () => {
    const after = state.data?.view?.next_after;
    if (current && after && state.window && state.data?.basis) setPage({ owner, account, days, refresh, accounts, window: state.window, after, basis: state.data.basis });
  };
  return { accounts, catalogReady, timezone, reload, nextPage, data: current ? state.data : null, window: current ? state.window : null, ready: current && state.ready,
    error: !allowed ? "无消费权限。" : !timezone || !settingsRevision ? "时区待核对。" : catalogAllowed && scope.accountCatalogError || (current ? state.error : null) };
}
