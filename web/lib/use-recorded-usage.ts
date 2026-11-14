"use client";

import { useEffect, useState } from "react";
import { createParticipantsClient } from "@sub-pool/contracts/client";
import { connectionFailure } from "@/lib/connection-data";
import { readRecordedUsage, RecordedSelectionError, type RecordedUsage } from "@/lib/pools-data";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";

export function useRecordedUsage(scope: ReturnType<typeof useScope>, accountID?: string) {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("usage"));
  const [state, setState] = useState<{ epoch: number; accountID?: string; accounts: typeof scope.accounts; participants: typeof scope.participantCatalog; data: RecordedUsage | null; ready: boolean; error: string | null }>({ epoch: -1, accounts: [], participants: [], data: null, ready: false, error: null });
  useEffect(() => {
    let alive = true;
    const abort = new AbortController();
    const base = { epoch: auth.epoch, accountID, accounts: scope.accounts, participants: scope.participantCatalog };
    setState({ ...base, data: null, ready: false, error: null });
    if (!allowed || !scope.accountCatalogAvailable || !scope.participantCatalogAvailable) return () => { alive = false; abort.abort(); };
    void (async () => {
      try {
        const client = createParticipantsClient(window.location.origin), signal = abort.signal;
        const data = await readRecordedUsage(accountID === undefined ? scope.accounts : scope.accounts.filter(account => account.id === accountID), scope.participantCatalog,
          (account_id, after) => client.captures({ account_id, after, limit: 100 }, { signal }),
          (capture_ids, participant_id) => client.usage({ capture_ids, participant_id, model: "", api_key_id: "", grain: "day", after: "0", limit: 1 }, { signal }));
        if (alive) setState({ ...base, data, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ ...base, data: null, ready: false, error: error instanceof RecordedSelectionError ? error.message : failure.status === 0 ? "暂时无法确认已记录消费；没有用 0 补齐缺失数据。" : failure.text });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [allowed, auth.epoch, accountID, scope.accounts, scope.accountCatalogAvailable, scope.participantCatalog, scope.participantCatalogAvailable, refreshSession]);
  const current = allowed && state.epoch === auth.epoch && state.accountID === accountID && state.accounts === scope.accounts && state.participants === scope.participantCatalog;
  return { data: current ? state.data : null, ready: current && state.ready,
    error: !allowed ? "未授予消费读取权限。" : scope.accountCatalogError ?? scope.participantCatalogError ?? (current ? state.error : null) };
}
