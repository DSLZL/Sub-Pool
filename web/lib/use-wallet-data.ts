"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createWalletApplicationsClient, createWalletsClient } from "@sub-pool/contracts/client";
import type { WalletView } from "@sub-pool/contracts/wallets";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";
import { readWalletData, WalletReadError, walletOperation, walletRow, type WalletData } from "./wallets-data";

const empty: WalletData = { wallets: [], operations: [], automation: null };
export function useWalletData() {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("balances"));
  const [refresh, setRefresh] = useState(0), key = `${auth.epoch}:${allowed}:${isAdmin}:${refresh}`;
  const [state, setState] = useState<{ key: string; data: WalletData; ready: boolean; error: string | null }>({ key: "", data: empty, ready: false, error: null });
  const [detail, setDetail] = useState<{ key: string; participant: WalletView | null; operation: WalletApplication | null; error: string | null } | null>(null);
  const mounted = useRef(false), ticket = useRef(0), detailAbort = useRef(new AbortController());
  const live = useRef({ key, allowed }); live.current = { key, allowed };
  const closeDetail = useCallback(() => { ticket.current++; detailAbort.current.abort(); setDetail(null); }, []);
  const reload = useCallback(() => { closeDetail(); setRefresh(value => value + 1); }, [closeDetail]);
  useEffect(() => {
    mounted.current = true;
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => { mounted.current = false; ticket.current++; detailAbort.current.abort(); window.removeEventListener("focus", focus); };
  }, [reload]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    closeDetail(); setState({ key, data: empty, ready: false, error: null });
    if (allowed) void (async () => {
      try {
        const data = await readWalletData(createWalletsClient(window.location.origin), createWalletApplicationsClient(window.location.origin), isAdmin, abort.signal);
        if (alive) setState({ key, data, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ key, data: empty, ready: false, error: error instanceof WalletReadError ? error.message : failure.status === 0 ? "暂时无法完整读取钱包与请求记录；请点击标签重新读取。" : failure.text });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, isAdmin, closeDetail, refreshSession]);

  async function inspect(kind: "participant" | "operation", id: string) {
    if (!allowed || state.key !== key || !state.ready) return;
    detailAbort.current.abort(); detailAbort.current = new AbortController();
    const currentTicket = ++ticket.current, readKey = key, signal = detailAbort.current.signal;
    const current = () => mounted.current && live.current.key === readKey && live.current.allowed && ticket.current === currentTicket;
    setDetail(null);
    try {
      const participant = kind === "participant" ? await createWalletsClient(window.location.origin).get({ participant_id: id }, { signal }) : null;
      const operation = kind === "operation" ? await createWalletApplicationsClient(window.location.origin).get({ id }, { signal }) : null;
      if (participant && participant.participant_id !== id || operation && operation.id !== id) throw new WalletReadError("详情与请求对象不一致，请重新读取。");
      if (current()) setDetail({ key: readKey, participant, operation, error: null });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setDetail({ key: readKey, participant: null, operation: null, error: error instanceof WalletReadError ? error.message : failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    }
  }
  const visible = allowed && state.key === key, data = visible && state.ready ? state.data : empty;
  const selected = visible && detail?.key === key ? detail : null;
  return {
    isAdmin, allowed, ready: visible && state.ready, error: visible ? state.error ?? selected?.error ?? null : null,
    rows: data.wallets.map(view => walletRow(view, data.operations)),
    operations: data.operations.map(walletOperation), automation: data.automation,
    participant: selected?.participant ? walletRow(selected.participant, data.operations) : null,
    operation: selected?.operation ? walletOperation(selected.operation) : null,
    inspectParticipant: (id: string) => inspect("participant", id),
    inspectOperation: (id: string) => inspect("operation", id),
    closeDetail, reload,
  };
}
