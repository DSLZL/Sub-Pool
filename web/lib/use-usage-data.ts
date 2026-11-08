"use client";

import { useEffect, useMemo, useState } from "react";
import { createParticipantsClient } from "@sub-pool/contracts/client";
import { connectionFailure } from "@/lib/connection-data";
import { RecordedSelectionError } from "@/lib/pools-data";
import { readUsageData, usageWindow, UsageReadError, type UsageData, type UsageWindow } from "@/lib/usage-data";
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
  const [state, setState] = useState<{ owner: string; accounts: typeof accounts; days: number; account: string; refresh: number; data: UsageData | null; window: UsageWindow | null; ready: boolean; error: string | null }>({ owner: "", accounts: [], days, account, refresh, data: null, window: null, ready: false, error: null });
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") setRefresh(value => value + 1); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, []);
  useEffect(() => {
    let alive = true;
    const abort = new AbortController(), window = timezone && settingsRevision ? usageWindow(days, new Date(), timezone) : null;
    const base = { owner, accounts, days, account, refresh, window };
    setState({ ...base, data: null, ready: false, error: null });
    if (!allowed || !catalogReady || !window || !timezone || !settingsRevision) return () => { alive = false; abort.abort(); };
    void (async () => {
      try {
        if (account !== "all" && !accounts.some(item => item.id === account)) throw new RecordedSelectionError("所选账号不在当前授权范围内，请重新选择。");
        const client = createParticipantsClient(globalThis.window.location.origin), signal = abort.signal;
        const data = await readUsageData(account === "all" ? accounts : accounts.filter(item => item.id === account),
          (account_id, after) => client.captures({ account_id, after, limit: 100 }, { signal }),
          (capture_ids, participant_id, after) => client.history({ capture_ids, participant_id, after, limit: 100, model: "", api_key_id: "", grain: "day", ...window }, { signal }), window, authorityRevision, { timezone, revision: settingsRevision });
        if (alive) setState({ ...base, data, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ ...base, data: null, ready: false, error: error instanceof RecordedSelectionError ? error.message : failure.status === 0 ? "暂时无法完整确认已记录用量；没有用部分明细或 0 补齐。" : failure.text });
        if (error instanceof UsageReadError || [401, 403, 404].includes(failure.status)) void refreshSession();
        if (error instanceof UsageReadError) void refreshSettings();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [allowed, owner, authorityRevision, accounts, catalogReady, days, account, refresh, refreshSession, timezone, settingsRevision, refreshSettings]);
  const current = allowed && state.owner === owner && state.accounts === accounts && state.days === days && state.account === account && state.refresh === refresh;
  return { accounts, catalogReady, timezone, data: current ? state.data : null, window: current ? state.window : null, ready: current && state.ready,
    error: !allowed ? "未授予消费读取权限。" : !timezone || !settingsRevision ? "统计时区尚未确认，请重新读取系统设置。" : catalogAllowed && scope.accountCatalogError || (current ? state.error : null) };
}
