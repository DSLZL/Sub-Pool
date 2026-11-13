"use client";

import { useEffect, useState } from "react";
import { createEstimatesClient, createMonitoringClient, createObservationsClient } from "@sub-pool/contracts/client";
import type { AccountObservation } from "@sub-pool/contracts/monitoring";
import type { DailyObservationHistory } from "@sub-pool/contracts/observations";
import { bindObservation } from "@/lib/accounts-data";
import { connectionFailure } from "@/lib/connection-data";
import { estimateCard, readEstimateData } from "@/lib/estimates-data";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import { UsageReadError, type UsageWindow } from "./usage-data";
import { readDailyObservationHistory } from "./usage-account-data";

type Result = { observation: AccountObservation | null; model: ReturnType<typeof estimateCard> | null; daily: DailyObservationHistory | null; observationError: string | null; modelError: string | null; dailyError: string | null };
const empty: Result = { observation: null, model: null, daily: null, observationError: null, modelError: null, dailyError: null };

export function useUsageAccount(scope: ReturnType<typeof useScope>, account: string, active: boolean, range: UsageWindow | null) {
  const { auth, isAdmin, refreshSession, timezone, settingsRevision, refreshSettings } = useApp();
  const usageAllowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("usage"));
  const accountsAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const estimatesAllowed = isAdmin || !!auth.access?.features.includes("estimates");
  const observationsAllowed = isAdmin || !!auth.access?.features.includes("observations");
  const managed = scope.accounts.find(item => item.id === account);
  const selected = account !== "all" && (accountsAllowed ? !!managed && scope.accountCatalogAvailable : !!auth.access?.account_ids.includes(account));
  const [refresh, setRefresh] = useState(0);
  const key = `${auth.epoch}:${auth.access?.user.revision}:${account}:${active}:${usageAllowed}:${accountsAllowed}:${estimatesAllowed}:${observationsAllowed}:${scope.accountCatalogAvailable}:${scope.connectionRevision}:${managed?.revision}:${managed?.source_id}:${managed?.source_mode}:${settingsRevision}:${timezone}:${range?.start_at}:${range?.end_at}:${refresh}`;
  const [state, setState] = useState<Result & { key: string; ready: boolean }>({ ...empty, key: "", ready: false });
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") setRefresh(value => value + 1); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, []);
  useEffect(() => {
    let alive = true;
    const abort = new AbortController(), signal = abort.signal;
    setState({ ...empty, key, ready: false });
    if (!active || !usageAllowed || !selected) return () => { alive = false; abort.abort(); };
    void (async () => {
      const origin = globalThis.window.location.origin;
      const results = await Promise.allSettled([
        accountsAllowed && managed ? createMonitoringClient(origin).account({ id: account }, { signal }).then(value => bindObservation(managed, value)) : Promise.resolve(null),
        estimatesAllowed ? readEstimateData([{ id: account, name: managed?.name ?? account }], createEstimatesClient(origin), false, signal).then(value => value.entries[0] ? estimateCard(value.entries[0]) : null) : Promise.resolve(null),
        observationsAllowed && range && settingsRevision && timezone ? readDailyObservationHistory({ account_id: account, settings_revision: settingsRevision, ...range }, { authority: auth.access?.user.revision ?? "", timezone, scope: isAdmin ? "account" : "authorized_participants" },
          input => createObservationsClient(origin).dailyHistory(input, { signal })) : Promise.resolve(null),
      ]);
      if (!alive) return;
      if (results.some(result => result.status === "rejected" && (result.reason instanceof UsageReadError || [401, 403, 404].includes(connectionFailure(result.reason).status)))) {
        setState({ ...empty, key, ready: true, observationError: "当前授权需要重新核对。", modelError: "当前授权需要重新核对。", dailyError: "当前授权或统计设置需要重新核对。" });
        void refreshSession();
        void refreshSettings();
        return;
      }
      const [observation, model, daily] = results;
      if (daily.status === "rejected" && connectionFailure(daily.reason).status === 409) void refreshSettings();
      setState({ key, ready: true,
        observation: observation.status === "fulfilled" ? observation.value : null,
        model: model.status === "fulfilled" ? model.value : null,
        daily: daily.status === "fulfilled" ? daily.value : null,
        observationError: !accountsAllowed ? "未授予账号观测读取权限。" : observation.status === "rejected" ? "暂时无法确认当前账号观测。" : null,
        modelError: !estimatesAllowed ? "未授予模型估计读取权限。" : model.status === "rejected" ? "暂时无法确认当前模型状态。" : null,
        dailyError: !observationsAllowed ? "每日快照还需观测读取权限。" : !range || !settingsRevision || !timezone ? "每日快照的统计时区与区间尚未确认。" : daily.status === "rejected" ? "每日快照未完整读取，请重新核对区间、权限与统计设置。" : null,
      });
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, active, usageAllowed, selected, accountsAllowed, estimatesAllowed, observationsAllowed, managed, account, isAdmin, refreshSession, refreshSettings, settingsRevision, timezone, range, auth.access?.user.revision]);
  const current = active && usageAllowed && selected && state.key === key;
  return { ...(current ? state : empty), ready: current && state.ready, selected };
}
