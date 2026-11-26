"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { useCallback, useEffect, useRef, useState } from "react";
import { requestFailureText } from "./request-id";
import { createWalletApplicationsClient, createWalletsClient } from "@sub-pool/contracts/client";
import type { WalletView } from "@sub-pool/contracts/wallets";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";
import { readWalletData, readWalletHistory, readWalletOperationPage, readWalletRequests, WalletReadError, walletOperation, walletRow, type WalletData, type WalletHistory, type WalletOperationFilter } from "./wallets-data";
import { backupRequestID } from "./backup-data";
import { canAdjustNativeWallet, nativeAdjustmentInput, checkNativeAdjustmentReceipt, canReconcileWallet, checkReconciliationReceipt, walletReadInput, checkWalletRefreshReceipt } from "./wallet-actions";

const empty: WalletData = { wallets: [], catalog: [], nextAfter: null, operations: [] };
type Action = "apply" | "reconcile" | "refresh";
export function useWalletData(operationFilter: WalletOperationFilter | null = null) {
  const { auth, isAdmin, refreshSession, displayTimezone } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("balances"));
  const owner = `${auth.epoch}:${allowed}:${isAdmin}`;
  const [refresh, setRefresh] = useState(0);
  const [walletCursor, setWalletCursor] = useState({ owner: "", after: "" });
  const walletAfter = walletCursor.owner === owner ? walletCursor.after : "";
  const key = `${owner}:${refresh}:${walletAfter}`;
  const operationEnabled = operationFilter !== null;
  const { participant_id: operationParticipant = "", start_at: operationStart = "", end_at: operationEnd = "" } = operationFilter ?? {};
  const operationsOwner = JSON.stringify([owner, operationEnabled, operationParticipant, operationStart, operationEnd]);
  const [operationsCursor, setOperationsCursor] = useState({ owner: "", after: "0" });
  const operationsAfter = operationsCursor.owner === operationsOwner ? operationsCursor.after : "0";
  const operationsKey = `${operationsOwner}:${refresh}:${operationsAfter}`;
  const [operationsState, setOperationsState] = useState<{ key: string; rows: WalletApplication[]; next: string | null; error: string | null }>({ key: "", rows: [], next: null, error: null });
  const [state, setState] = useState<{ key: string; data: WalletData; ready: boolean; error: string | null }>({ key: "", data: empty, ready: false, error: null });
  const [detail, setDetail] = useState<{ key: string; kind: "participant" | "operation" | "apply" | "history"; participant: WalletView | null; operation: WalletApplication | null; error: string | null; history?: WalletHistory } | null>(null);
  const [mutation, setMutation] = useState<{ owner: string; action: Action | null; error: string | null; notice: string | null; participant?: string }>({ owner, action: null, error: null, notice: null });
  const writer = useRef<{ owner: string; action: Action; abort: AbortController } | null>(null);
  const uncertain = useRef<{ owner: string; participant: string; request: string }[]>([]);
  const mounted = useRef(false), ticket = useRef(0), detailAbort = useRef(new AbortController());
  const live = useRef({ key, owner, allowed }); live.current = { key, owner, allowed };
  const closeDetail = useCallback(() => { ticket.current++; detailAbort.current.abort(); setDetail(null); }, []);
  const reload = useCallback(() => { closeDetail(); setRefresh(value => value + 1); }, [closeDetail]);
  useEffect(() => {
    mounted.current = true;
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => { mounted.current = false; ticket.current++; detailAbort.current.abort(); writer.current?.abort.abort(); window.removeEventListener("focus", focus); };
  }, [reload]);
  useEffect(() => {
    writer.current?.abort.abort(); writer.current = null; uncertain.current = [];
    setMutation({ owner, action: null, error: null, notice: null });
  }, [owner]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    closeDetail(); setState({ key, data: empty, ready: false, error: null });
    if (allowed) void (async () => {
      try {
        const data = await readWalletData(createWalletsClient(window.location.origin), createWalletApplicationsClient(window.location.origin), isAdmin, abort.signal, walletAfter);
        if (alive) {
          uncertain.current = uncertain.current.filter(intent => intent.owner !== owner || !data.operations.some(row =>
            row.participant_id === intent.participant && row.details?.input.request_id === intent.request));
          setState({ key, data, ready: true, error: null });
          if (!writer.current) setMutation(previous => previous.owner === owner ? { ...previous,
            error: uncertain.current.some(intent => intent.owner === owner) ? `请核对 ${glossary.adjustmentRecords}。` : null } : previous);
        }
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setState({ key, data: empty, ready: false, error: error instanceof WalletReadError ? error.message : failure.status === 0 ? requestFailureText(failure.text, uiCopy.readFailed) : failure.text });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, owner, allowed, isAdmin, closeDetail, refreshSession, walletAfter]);

  useEffect(() => {
    const abort = new AbortController();
    if (allowed && operationEnabled) void (async () => {
      try {
        const page = await readWalletOperationPage(createWalletApplicationsClient(window.location.origin),
          { participant_id: operationParticipant, start_at: operationStart, end_at: operationEnd }, operationsAfter, isAdmin, abort.signal);
        if (!abort.signal.aborted) setOperationsState({ key: operationsKey, rows: page.requests, next: page.next_after, error: null });
      } catch (error) {
        if (abort.signal.aborted) return;
        const failure = connectionFailure(error);
        setOperationsState({ key: operationsKey, rows: [], next: null, error: error instanceof WalletReadError ? error.message : failure.text });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
    })();
    return () => abort.abort();
  }, [allowed, isAdmin, operationEnabled, operationParticipant, operationStart, operationEnd, operationsAfter, operationsKey, refreshSession]);

  async function inspect(kind: "participant" | "operation" | "apply" | "history", id: string, after?: WalletHistory["after"], range?: WalletHistory["window"]) {
    if (!allowed || state.key !== key || !state.ready) return;
    if ((kind === "apply" || kind === "history") && (!isAdmin || writer.current)) return;
    detailAbort.current.abort(); detailAbort.current = new AbortController();
    const currentTicket = ++ticket.current, readKey = key, signal = detailAbort.current.signal;
    const current = () => mounted.current && live.current.key === readKey && live.current.allowed && ticket.current === currentTicket;
    if (!after) setDetail(null);
    try {
      const participant = kind !== "operation" ? await createWalletsClient(window.location.origin).get({ participant_id: id }, { signal }) : null;
      const operation = kind === "operation" ? await createWalletApplicationsClient(window.location.origin).get({ id }, { signal }) : null;
      if (participant && participant.participant_id !== id || operation && operation.id !== id) throw new WalletReadError("详情不符。");
      const history = kind === "history" ? await readWalletHistory(createWalletsClient(window.location.origin), id, signal, after, range) : undefined;
      if (current()) setDetail({ key: readKey, kind, participant, operation, error: null, history });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setDetail({ key: readKey, kind, participant: null, operation: null, error: error instanceof WalletReadError ? error.message : failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    }
  }
  const visible = allowed && state.key === key, data = visible && state.ready ? state.data : empty;
  const selected = visible && detail?.key === key ? detail : null;
  const action = mutation.owner === owner ? mutation : null;
  const busy = action?.action !== null && action?.action !== undefined;
  const blocked = (id: string) => uncertain.current.some(intent => intent.owner === owner && intent.participant === id);

  async function mutate(kind: Action, nativeDelta?: string) {
    if (!mounted.current || live.current.key !== key || !isAdmin || !visible || !state.ready || writer.current) return false;
    const run = { owner, action: kind, abort: new AbortController() }; writer.current = run;
    const readKey = key, selection = ticket.current, current = () => mounted.current && live.current.owner === owner;
    const ownsView = () => current() && live.current.key === readKey;
    setMutation({ owner, action: kind, error: null, notice: null });
    let sent = false;
    try {
      const client = createWalletApplicationsClient(window.location.origin), signal = run.abort.signal;
      let result: WalletApplication | null = null;
      if (kind === "refresh") {
        const previous = selected?.kind === "participant" ? selected.participant : null;
        if (!previous) throw new WalletReadError("请先打开详情。");
        const wallets = createWalletsClient(window.location.origin);
        const before = await wallets.get({ participant_id: previous.participant_id }, { signal });
        const input = walletReadInput(before);
        if (JSON.stringify(input) !== JSON.stringify(walletReadInput(previous))) throw new WalletReadError("钱包变更，请重开。");
        if (!ownsView() || ticket.current !== selection) return false;
        sent = true;
        const receipt = await wallets.refresh(input, { signal });
        checkWalletRefreshReceipt(input, receipt);
        const view = await wallets.get({ participant_id: input.participant_id }, { signal });
        if (JSON.stringify({ ...walletReadInput(view), latest_id: input.latest_id }) !== JSON.stringify(input)
          || BigInt(view.latest_read?.id ?? "0") < BigInt(receipt.id)) {
          throw new WalletReadError("请查钱包历史。");
        }
        if (ownsView()) {
          setState(previous => previous.key === readKey ? { ...previous, data: { ...previous.data, wallets: previous.data.wallets.map(row => row.participant_id === view.participant_id ? view : row) } } : previous);
          if (ticket.current === selection) setDetail({ key: readKey, kind: "participant", participant: view, operation: null, error: null });
          setMutation({ owner, action: kind, error: null, participant: view.participant_id, notice: "已保存读数。" });
        }
        return ownsView();
      }
      if (kind === "apply") {
        const view = selected?.kind === "apply" ? selected.participant : null;
        if (!view || blocked(view.participant_id)) throw new WalletReadError("核对原请求。");
        const requests = await readWalletRequests(client, view.participant_id, isAdmin, signal);
        if (!ownsView() || ticket.current !== selection) return false;
        const requestID = backupRequestID();
        const input = nativeAdjustmentInput(view, requests, requestID, nativeDelta ?? "");
        uncertain.current.push({ owner, participant: view.participant_id, request: input.request_id }); sent = true;
        result = await client.adjust(input, { signal });
        checkNativeAdjustmentReceipt(input, result);
        if (ownsView()) uncertain.current = uncertain.current.filter(intent => intent.owner !== owner || intent.request !== input.request_id);
      } else if (kind === "reconcile") {
        const previous = selected?.operation;
        if (!previous || !canReconcileWallet(previous)) throw new WalletReadError("暂无待核对项。");
        sent = true; result = await client.reconcile({ id: previous.id }, { signal }); checkReconciliationReceipt(previous, result);
      }
      if (result && ownsView()) {
        const receipt = result;
        setOperationsState(previous => previous.key === operationsKey ? { ...previous, rows: previous.rows.map(row => row.id === receipt.id ? receipt : row) } : previous);
        setState(previous => previous.key === readKey ? { ...previous, data: { ...previous.data, operations:
          [...previous.data.operations.filter(row => row.id !== receipt.id), receipt].sort((left, right) => BigInt(left.id) < BigInt(right.id) ? -1 : 1) } } : previous);
        if (ticket.current === selection) setDetail({ key: readKey, kind: "operation", participant: null, operation: receipt, error: null });
        setMutation({ owner, action: kind, error: null, notice: walletOperation(receipt).note });
      }
      return ownsView();
    } catch (error) {
      if (ownsView()) {
        const failure = connectionFailure(error), text = error instanceof WalletReadError ? error.message : failure.text;
        setMutation({ owner, action: kind, error: sent ? `${text} 待核对。${kind === "refresh" ? "查钱包和历史。" : "查原调整，勿重试。"}` : text,
          notice: null, participant: kind === "refresh" ? selected?.participant?.participant_id : undefined });
        if ([401, 403, 404].includes(failure.status)) void refreshSession();
      }
      return false;
    } finally {
      if (writer.current === run) writer.current = null;
      if (current()) setMutation(previous => previous.owner === owner ? { ...previous, action: null } : previous);
    }
  }
  const operationsPage = allowed && operationEnabled && operationsState.key === operationsKey ? operationsState : null;
  function changeOperationsPage(after: string) {
    closeDetail();
    setOperationsCursor({ owner: operationsOwner, after });
  }
  function changeHistoryPage(kind: keyof WalletHistory["after"], after: string) {
    if (selected?.kind !== "history" || !selected.participant || !selected.history) return;
    void inspect("history", selected.participant.participant_id, { ...selected.history.after, [kind]: after }, selected.history.window);
  }
  function filterHistory(range: WalletHistory["window"]) {
    if (selected?.kind !== "history" || !selected.participant) return;
    void inspect("history", selected.participant.participant_id, { wallet: "0", recommendation: "0" }, range);
  }
  return {
    isAdmin, allowed, ready: visible && state.ready, generation: key, busy,
    error: visible ? state.error ?? selected?.error ?? action?.error ?? null : null,
    actionError: visible ? action?.error ?? null : null, notice: visible ? action?.notice ?? null : null,
    participantError: selected?.participant?.participant_id === action?.participant ? action?.error ?? null : null,
    participantNotice: selected?.participant?.participant_id === action?.participant ? action?.notice ?? null : null,
    rows: data.wallets.map(view => walletRow(view, data.operations, displayTimezone)),
    catalog: data.catalog,
    walletAfter, walletNext: data.nextAfter, changeWalletPage: (after: string) => setWalletCursor({ owner, after }),
    operations: (operationsPage?.rows ?? []).map(walletOperation), operationsReady: operationsPage !== null,
    operationsError: operationsPage?.error ?? null, operationsAfter, operationsNext: operationsPage?.next ?? null, changeOperationsPage,
    changeHistoryPage, filterHistory,
    participant: selected?.kind === "participant" && selected.participant ? walletRow(selected.participant, data.operations, displayTimezone) : null,
    applyTarget: selected?.kind === "apply" && selected.participant ? walletRow(selected.participant, data.operations, displayTimezone) : null,
    operation: selected?.operation ? walletOperation(selected.operation) : null,
    inspectParticipant: (id: string) => inspect("participant", id),
    inspectHistory: (id: string) => inspect("history", id),
    history: selected?.kind === "history" && selected.participant && selected.history ? { participant: walletRow(selected.participant, data.operations, displayTimezone), ...selected.history } : null,
    refreshWallet: () => mutate("refresh"),
    inspectOperation: (id: string) => inspect("operation", id),
    prepareApplication: (id: string) => inspect("apply", id),
    canApply: (view: WalletView) => isAdmin && visible && state.ready && !busy && !blocked(view.participant_id)
      && canAdjustNativeWallet(view, data.operations),
    apply: (nativeDelta?: string) => mutate("apply", nativeDelta), reconcile: () => mutate("reconcile"),
    closeDetail, reload,
  };
}
