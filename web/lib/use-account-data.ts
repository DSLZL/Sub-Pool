"use client";

import { useCallback, useEffect, useState } from "react";
import { createConnectionClient, createMonitoringClient } from "@sub-pool/contracts/client";
import type { ConnectionConfiguration } from "@sub-pool/contracts/connection";
import type { MonitoringConfig, MonitoringStatus, CollectionRun } from "@sub-pool/contracts/monitoring";
import { bindObservation, readRunHistory, type AccountRow } from "@/lib/accounts-data";
import { connectionFailure } from "@/lib/connection-data";
import { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";

type AdminData = { connection: ConnectionConfiguration | null; config: MonitoringConfig | null; status: MonitoringStatus | null; runs: CollectionRun[]; health: boolean | null; error: string | null; checkedAt: string | null };
const noAdminData: AdminData = { connection: null, config: null, status: null, runs: [], health: null, error: null, checkedAt: null };

export function useAccountData() {
  const scope = useScope();
  const { auth, refreshSession } = useApp();
  const [version, setVersion] = useState(0);
  const [observed, setObserved] = useState<{ accounts: typeof scope.accounts; rows: AccountRow[]; ready: boolean; error: string | null; readAt: string | null }>({ accounts: [], rows: [], ready: false, error: null, readAt: null });
  const [admin, setAdmin] = useState<AdminData>(noAdminData);
  const reload = useCallback(async () => { setVersion(value => value + 1); await scope.reloadAccounts(); }, [scope.reloadAccounts]);

  useEffect(() => {
    const abort = new AbortController();
    let alive = true;
    setObserved({ accounts: scope.accounts, rows: [], ready: false, error: null, readAt: null });
    if (!scope.accountCatalogAvailable) return () => { alive = false; abort.abort(); };
    void (async () => {
      const client = createMonitoringClient(window.location.origin), rows: AccountRow[] = [];
      for (let offset = 0; offset < scope.accounts.length; offset += 4) {
        const accounts = scope.accounts.slice(offset, offset + 4);
        const results = await Promise.allSettled(accounts.map(async account => bindObservation(account, await client.account({ id: account.id }, { signal: abort.signal }))));
        if (!alive) return;
        for (let index = 0; index < results.length; index++) {
          const account = accounts[index], result = results[index];
          if (result.status === "fulfilled") rows.push({ account, observation: result.value, error: null });
          else {
            const failure = connectionFailure(result.reason);
            if ([401, 403, 404].includes(failure.status)) {
              setObserved({ accounts: scope.accounts, rows: [], ready: false, error: failure.text, readAt: null });
              void refreshSession(); return;
            }
            rows.push({ account, observation: null, error: failure.status === 0 ? "暂时无法确认观测读取；没有用 0 补齐数据。" : failure.text });
          }
        }
      }
      if (alive) setObserved({ accounts: scope.accounts, rows, ready: true, error: null, readAt: new Date().toISOString() });
    })();
    return () => { alive = false; abort.abort(); };
  }, [scope.accounts, scope.accountCatalogAvailable, version, refreshSession]);

  useEffect(() => {
    const abort = new AbortController();
    let alive = true;
    setAdmin(noAdminData);
    if (!scope.isAdmin) return () => { alive = false; abort.abort(); };
    const client = createMonitoringClient(window.location.origin);
    void (async () => {
      const [health, business] = await Promise.allSettled([
        client.health({}, { signal: abort.signal }),
        Promise.all([client.config({}, { signal: abort.signal }), client.status({}, { signal: abort.signal }), readRunHistory(after => client.runs({ after, limit: 100 }, { signal: abort.signal })), createConnectionClient(window.location.origin).get({}, { signal: abort.signal })]),
      ]);
      if (!alive) return;
      const checkedAt = new Date().toISOString();
      if (business.status === "fulfilled") {
        const [config, status, runs, connection] = business.value;
        setAdmin({ connection, config, status, runs, health: health.status === "fulfilled", error: null, checkedAt });
      } else {
        const failure = connectionFailure(business.reason);
        setAdmin({ ...noAdminData, health: health.status === "fulfilled", checkedAt, error: failure.status === 0 ? "暂时无法完整读取采集配置与执行记录。" : failure.text });
        if (failure.status === 401 || failure.status === 403) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [scope.isAdmin, auth.epoch, version, refreshSession]);

  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") setVersion(value => value + 1); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, []);

  const current = scope.accountCatalogAvailable && observed.accounts === scope.accounts;
  return { ...scope, rows: current ? observed.rows : [], observationsReady: current && observed.ready, observationError: current ? observed.error : null, readAt: current ? observed.readAt : null, admin, reload };
}
