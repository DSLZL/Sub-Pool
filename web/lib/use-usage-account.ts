"use client";

import { useEffect, useState } from "react";
import { createEstimatesClient, createMonitoringClient, createObservationsClient, createParticipantsClient } from "@sub-pool/contracts/client";
import type { AccountObservation } from "@sub-pool/contracts/monitoring";
import type { DailyObservationHistory } from "@sub-pool/contracts/observations";
import type { UsageHistory } from "@sub-pool/contracts/participants";
import { bindObservation } from "@/lib/accounts-data";
import { connectionFailure } from "@/lib/connection-data";
import { readEstimateSummaries } from "@/lib/estimates-data";
import type { NativeWeeklyCurrent } from "@sub-pool/contracts/estimates";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";
import { UsageReadError, type UsageWindow } from "./usage-data";
import { readCurrentKeyUsage, readDailyObservationHistory } from "./usage-account-data";
import { verifyRecordedUsageWindow } from "./recorded-usage-page";

type Result = { observation: AccountObservation | null; model: NativeWeeklyCurrent | null; daily: DailyObservationHistory | null; keys: UsageHistory | null; monthly: UsageHistory | null; observationError: string | null; modelError: string | null; dailyError: string | null; keyError: string | null; monthlyError: string | null };
const empty: Result = { observation: null, model: null, daily: null, keys: null, monthly: null, observationError: null, modelError: null, dailyError: null, keyError: null, monthlyError: null };

export function useUsageAccount(scope: ReturnType<typeof useScope>, account: string, tab: string, range: UsageWindow | null, selectionView: UsageHistory | null) {
  const { auth, isAdmin, refreshSession, timezone, settingsRevision, refreshSettings } = useApp();
  const usageAllowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("usage"));
  const accountsAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const estimatesAllowed = isAdmin || !!auth.access?.features.includes("estimates");
  const observationsAllowed = isAdmin || !!auth.access?.features.includes("observations");
  const active = tab === "detail" || tab === "history";
  const managed = scope.accounts.find(item => item.id === account);
  const selected = account !== "all" && (accountsAllowed ? !!managed && scope.accountCatalogAvailable : !!auth.access?.account_ids.includes(account));
  const [refresh, setRefresh] = useState(0);
  const selections = JSON.stringify(selectionView?.history?.selections ?? []);
  const key = `${auth.epoch}:${auth.access?.user.revision}:${account}:${tab}:${usageAllowed}:${accountsAllowed}:${estimatesAllowed}:${observationsAllowed}:${scope.accountCatalogAvailable}:${scope.connectionRevision}:${managed?.revision}:${managed?.source_id}:${managed?.source_mode}:${settingsRevision}:${timezone}:${range?.start_at}:${range?.end_at}:${selections}:${refresh}`;
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
      const expected = { account, authority: auth.access?.user.revision ?? "", settingsRevision: settingsRevision ?? "", timezone: timezone ?? "", scope: isAdmin ? "account" as const : "authorized_participants" as const };
      const client = createParticipantsClient(origin);
      const readHistory = (input: Parameters<typeof client.history>[0]) => client.history(input, { signal });
      const dailyRead = observationsAllowed && range && settingsRevision && timezone ? readDailyObservationHistory({ account_id: account, settings_revision: settingsRevision, ...range }, expected,
        input => createObservationsClient(origin).dailyHistory(input, { signal })) : Promise.resolve(null);
      const monthlyRead = tab === "history" && range && settingsRevision && timezone && selections !== "[]" ?
        client.recorded({ account_ids: [account], participant_id: "", model: "", api_key_id: "", grain: "month", after: "0", limit: 0, basis: "", ...range }, { signal })
          .then(({ view }) => {
            verifyRecordedUsageWindow(view, range, { timezone, revision: settingsRevision, authority: expected.authority });
            if (!view || JSON.stringify(view.history?.selections ?? []) !== selections || view.unassigned_visible !== isAdmin) {
              throw new UsageReadError("月度数据已变。");
            }
            return view;
          }) : Promise.resolve(null);
      const results = await Promise.allSettled([
        tab === "history" && accountsAllowed && managed ? createMonitoringClient(origin).account({ id: account }, { signal }).then(value => bindObservation(managed, value)) : Promise.resolve(null),
        tab === "history" && estimatesAllowed ? readEstimateSummaries([{ id: account, name: managed?.name ?? account }], createEstimatesClient(origin), signal).then(value => value[0]?.current.native_weekly ?? null) : Promise.resolve(null),
        dailyRead,
        tab === "detail" ? dailyRead.then(value => readCurrentKeyUsage(value, expected, readHistory)) : Promise.resolve(null),
        monthlyRead,
      ]);
      if (!alive) return;
      if (results.some(result => result.status === "rejected" && (result.reason instanceof UsageReadError || [401, 403, 404].includes(connectionFailure(result.reason).status)))) {
        setState({ ...empty, key, ready: true, observationError: "权限待核对。", modelError: "权限待核对。", dailyError: "查权限及时间。", keyError: "Key 范围待核对。", monthlyError: "查权限及时间。" });
        void refreshSession();
        void refreshSettings();
        return;
      }
      const [observation, model, daily, keys, monthly] = results;
      if (results.slice(2).some(result => result.status === "rejected" && connectionFailure(result.reason).status === 409)) void refreshSettings();
      setState({ key, ready: true,
        observation: observation.status === "fulfilled" ? observation.value : null,
        model: model.status === "fulfilled" ? model.value : null,
        daily: daily.status === "fulfilled" ? daily.value : null,
        keys: keys.status === "fulfilled" ? keys.value : null,
        monthly: monthly.status === "fulfilled" ? monthly.value : null,
        observationError: !accountsAllowed ? "无观测权限。" : observation.status === "rejected" ? "观测待核对。" : null,
        modelError: !estimatesAllowed ? "无模型权限。" : model.status === "rejected" ? "模型待核对。" : null,
        dailyError: !observationsAllowed ? "须观测权限。" : !range || !settingsRevision || !timezone ? "快照待核对。" : daily.status === "rejected" ? "快照读取不齐。" : null,
        keyError: !observationsAllowed ? "须观测权限。" : keys.status === "rejected" ? "Key 费用未知。" : null,
        monthlyError: !range || !settingsRevision || !timezone ? "月度待核对。" : selections === "[]" ? "月度费用未知。" : monthly.status === "rejected" ? "月度读取不齐。" : null,
      });
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, tab, active, usageAllowed, selected, accountsAllowed, estimatesAllowed, observationsAllowed, managed, account, isAdmin, refreshSession, refreshSettings, settingsRevision, timezone, range, selections, auth.access?.user.revision]);
  useEffect(() => {
    if (state.key !== key || !state.keys) return;
    const reset = state.daily?.current?.observation.snapshot.windows.find(window => window.kind === "seven_day")?.reset_at;
    if (!reset) return;
    const timer = window.setTimeout(() => setRefresh(value => value + 1), Math.min(2147483647, Math.max(1, Date.parse(reset) - Date.now() + 1)));
    return () => window.clearTimeout(timer);
  }, [state.key, state.keys, state.daily, key]);
  const current = active && usageAllowed && selected && state.key === key;
  return { ...(current ? state : empty), ready: current && state.ready, selected };
}
