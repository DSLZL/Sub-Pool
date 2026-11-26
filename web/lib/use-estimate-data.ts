"use client";

import { useEffect, useMemo, useState } from "react";
import { createEstimatesClient } from "@sub-pool/contracts/client";
import { useApp } from "./store";
import type { useScope } from "./scope";
import { useBoundRead } from "./use-bound-read";
import { connectionFailure } from "./connection-data";
import { EstimateReadError, readEstimateArchive, readEstimateRecord, readEstimateSummaries, type EstimateAccount, type EstimateEntry, type EstimateHistoryFilter } from "./estimates-data";

type Filters = EstimateHistoryFilter & { account: string; after: string; accountAfter: string };
const defaults: Filters = { account: "", window_kind: "all", period_end_at: "", after: "0", accountAfter: "" };
const accountPageSize = 25;
const client = () => createEstimatesClient(window.location.origin);
const failure = (error: unknown) => error instanceof EstimateReadError ? error.message : connectionFailure(error).text;

function archivedPage({ account, after, window_kind, period_end_at }: Filters, signal: AbortSignal) {
  return readEstimateArchive(window.location.origin, { account_id: account, after, limit: 100, window_kind, period_end_at }, signal);
}

async function archivedRecord(key: { account: string; id: string }, signal: AbortSignal) {
  return readEstimateRecord(key.account, key.id, () => client().record({ id: key.id }, { signal }));
}

export function useEstimateData(scope: ReturnType<typeof useScope>, history: boolean) {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("estimates"));
  const catalogAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const accounts = useMemo(() => catalogAllowed ? scope.accounts : (auth.access?.account_ids ?? []).map(id => ({ id, name: id })), [catalogAllowed, scope.accounts, auth.access]);
  const catalogReady = !catalogAllowed || scope.accountCatalogAvailable;
  const [selection, setSelection] = useState({ epoch: auth.epoch, filters: defaults });
  const filters = selection.epoch === auth.epoch ? selection.filters : defaults;
  const account = accounts.find(value => value.id === filters.account);
  const accountStart = filters.accountAfter ? accounts.findIndex(value => value.id === filters.accountAfter) + 1 : 0;
  const selected = useMemo(() => filters.account ? account ? [account] : [] : accounts.slice(accountStart, accountStart + accountPageSize),
    [account, accounts, accountStart, filters.account]);
  const nativeKey = useMemo(() => allowed && catalogReady && !history ? { epoch: auth.epoch, selected } : null, [allowed, auth.epoch, catalogReady, history, selected]);
  const archiveKey = useMemo(() => allowed && catalogReady && account && history ? { ...filters, epoch: auth.epoch, account } : null,
    [account, allowed, auth.epoch, catalogReady, filters, history]);
  const historyKey = useMemo(() => archiveKey && { ...archiveKey, account: archiveKey.account.id }, [archiveKey]);
  const archive = useBoundRead(historyKey, archivedPage);
  const [detailState, setDetail] = useState<{ key: typeof historyKey; account: string; id: string } | null>(null);
  const detailKey = detailState?.key === historyKey ? detailState : null;
  const detail = useBoundRead(detailKey, archivedRecord);
  const [native, setNative] = useState<{ key: typeof nativeKey; entries: EstimateEntry[]; error: string | null } | null>(null);
  useEffect(() => {
    if (!nativeKey) return;
    const abort = new AbortController();
    let timer: number | undefined;
    let running = false;
    async function read(accounts: EstimateAccount[]) {
      if (running) return;
      running = true;
      try {
        const entries = await readEstimateSummaries(accounts, client(), abort.signal);
        if (abort.signal.aborted) return;
        setNative({ key: nativeKey, entries, error: null });
        if (entries.some(entry => entry.current.native_weekly?.state === "collecting")) {
          timer = window.setTimeout(() => { void read(accounts); }, 1500);
        }
      } catch (error) {
        if (abort.signal.aborted) return;
        setNative({ key: nativeKey, entries: [], error: failure(error) });
        if ([401, 403, 404].includes(connectionFailure(error).status)) void refreshSession();
      } finally {
        running = false;
      }
    }
    const focus = () => { window.clearTimeout(timer); void read(nativeKey.selected); };
    void read(nativeKey.selected);
    window.addEventListener("focus", focus);
    return () => { abort.abort(); window.clearTimeout(timer); window.removeEventListener("focus", focus); };
  }, [nativeKey, refreshSession]);

  function change(next: Filters) {
    setDetail(null);
    setSelection({ epoch: auth.epoch, filters: next });
  }
  const visible = native?.key === nativeKey ? native : null;
  const accountNext = !filters.account && accountStart + accountPageSize < accounts.length ? selected.at(-1)?.id ?? null : null;
  return {
    accounts, filters, entries: visible?.entries ?? [], history: archive?.value?.records ?? [],
    ready: history ? !account || archive?.value != null : visible !== null && !visible.error,
    error: archive?.error ? failure(archive.error) : visible?.error ?? null,
    after: filters.after, next: archive?.value?.next_after ?? null, accountAfter: filters.accountAfter, accountNext,
    selectAccount: (id: string) => change({ ...filters, account: id, after: "0", accountAfter: "" }),
    changeAccountPage: (accountAfter: string) => change({ ...filters, accountAfter }),
    changeHistoryPage: (after: string) => change({ ...filters, after }),
    selectHistoryWindow: (kind: string) => {
      if (kind === "all" || kind === "short" || kind === "seven_day") change({ ...filters, window_kind: kind, period_end_at: kind === "all" ? "" : filters.period_end_at, after: "0" });
    },
    editPeriod: (period_end_at: string) => change({ ...filters, period_end_at, after: "0" }),
    closeDetail: () => setDetail(null),
    openRecord: (account: string, id: string) => {
      if (historyKey?.account === account && archive?.value?.records.some(record => record.id === id)) setDetail({ key: historyKey, account, id });
    },
    detail: detailKey && { record: detail?.value ?? null, error: detail?.error ? failure(detail.error) : null },
  };
}
