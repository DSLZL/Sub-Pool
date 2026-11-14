"use client";

import { useEffect, useState } from "react";
import { createParticipantsClient } from "@sub-pool/contracts/client";
import { connectionFailure } from "@/lib/connection-data";
import { readRecordedUsage, RecordedSelectionError, type RecordedUsage } from "@/lib/pools-data";
import { readUsageData, UsageReadError, type UsageWindow } from "@/lib/usage-data";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";

export type RecordedUsageQuery = UsageWindow & { participant_id: string };

export function useRecordedUsage(scope: ReturnType<typeof useScope>, accountID?: string, query?: RecordedUsageQuery) {
  const { auth, isAdmin, refreshSession, timezone, settingsRevision, refreshSettings } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("usage"));
  const authorityRevision = auth.access?.user.revision ?? "";
  const owner = `${auth.epoch}:${authorityRevision}:${query ? settingsRevision + ":" + timezone : ""}`;
  const [refresh, setRefresh] = useState(0);
  const [state, setState] = useState<{ owner: string; accountID?: string; query?: RecordedUsageQuery; refresh: number; accounts: typeof scope.accounts; participants: typeof scope.participantCatalog; data: RecordedUsage | null; ready: boolean; error: string | null }>({ owner: "", accounts: [], participants: [], refresh: -1, data: null, ready: false, error: null });
  useEffect(() => {
    let alive = true;
    const abort = new AbortController();
    const base = { owner, accountID, query, refresh, accounts: scope.accounts, participants: scope.participantCatalog };
    setState({ ...base, data: null, ready: false, error: null });
    if (!allowed || !scope.accountCatalogAvailable || !scope.participantCatalogAvailable || query && (!timezone || !settingsRevision)) return () => { alive = false; abort.abort(); };
    void (async () => {
      try {
        if (accountID !== undefined && !scope.accounts.some(account => account.id === accountID)) throw new RecordedSelectionError("所选账号不在当前授权范围内，请重新选择。");
        if (query?.participant_id && !scope.participantCatalog.some(participant => participant.id === query.participant_id)) throw new RecordedSelectionError("所选参与者不在当前授权范围内，请重新选择。");
        const client = createParticipantsClient(window.location.origin), signal = abort.signal;
        const accounts = accountID === undefined ? scope.accounts : scope.accounts.filter(account => account.id === accountID);
        const captures = (account_id: string, after: string) => client.captures({ account_id, after, limit: 100 }, { signal });
        let data: RecordedUsage;
        if (query && timezone && settingsRevision) {
          data = await readUsageData(accounts, captures,
            (capture_ids, participant_id, after) => client.history({ capture_ids, participant_id, model: "", api_key_id: "", grain: "day", after, limit: 100, start_at: query.start_at, end_at: query.end_at }, { signal }),
            query, authorityRevision, { timezone, revision: settingsRevision }, query.participant_id);
        } else {
          data = await readRecordedUsage(accounts, scope.participantCatalog, captures,
            (capture_ids, participant_id) => client.usage({ capture_ids, participant_id, model: "", api_key_id: "", grain: "day", after: "0", limit: 1 }, { signal }));
        }
        if (alive) setState({ ...base, data, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ ...base, data: null, ready: false, error: error instanceof RecordedSelectionError ? error.message : failure.status === 0 ? "暂时无法确认已记录消费；没有用 0 补齐缺失数据。" : failure.text });
        if (error instanceof UsageReadError || [401, 403, 404].includes(failure.status)) void refreshSession();
        if (error instanceof UsageReadError) void refreshSettings();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [allowed, owner, authorityRevision, accountID, query, refresh, scope.accounts, scope.accountCatalogAvailable, scope.participantCatalog, scope.participantCatalogAvailable, timezone, settingsRevision, refreshSession, refreshSettings]);
  const current = allowed && state.owner === owner && state.accountID === accountID && state.query === query && state.refresh === refresh && state.accounts === scope.accounts && state.participants === scope.participantCatalog;
  return { data: current ? state.data : null, ready: current && state.ready, reload: () => setRefresh(value => value + 1),
    error: !allowed ? "未授予消费读取权限。" : query && (!timezone || !settingsRevision) ? "统计时区尚未确认，请重新读取系统设置。" : scope.accountCatalogError ?? scope.participantCatalogError ?? (current ? state.error : null) };
}
