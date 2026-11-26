"use client";

import { uiCopy } from "@/lib/glossary";
import { useEffect, useMemo, useRef, useState } from "react";
import { requestFailureText } from "./request-id";
import { createObservationsClient } from "@sub-pool/contracts/client";
import { observationReason, type Observation } from "@sub-pool/contracts/observations";
import { connectionFailure } from "@/lib/connection-data";
import { observationDecisionMatches, observationRangeEnds, ObservationReadError, readObservationEvents, readObservationPages, readObservationSelection, type ObservationDecision, type ObservationQuery, type ObservationRow, type ObservationSelection } from "@/lib/observations-data";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";

type Pending = ObservationDecision & { epoch: number; acknowledged: boolean };
type ActionPhase = "ready" | "busy" | "unknown" | "conflict";
const empty: ObservationRow[] = [];
const recoveryText = "待核对，请重读。";

export function useObservationData(scope: ReturnType<typeof useScope>, account: string, validity: ObservationQuery["validity"], source: ObservationQuery["source_mode"], sourceID: string, startAt: string, endAt: string) {
  const { auth, isAdmin, refreshSession, toast } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("observations"));
  const catalogAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const estimatesAllowed = isAdmin || !!auth.access?.features.includes("estimates");
  const usageAllowed = isAdmin || !!auth.access?.features.includes("usage");
  // Session epochs change with grants; an identical refresh must preserve the selector catalog.
  const accounts = useMemo(() => catalogAllowed ? scope.accounts : (auth.access?.account_ids ?? []).map(id => ({ id, name: id })), [catalogAllowed, scope.accounts, auth.epoch]);
  const catalogReady = !catalogAllowed || scope.accountCatalogAvailable;
  const query = useMemo<ObservationQuery>(() => ({ account_id: account === "all" ? "" : account, source_id: sourceID.trim(), validity, source_mode: source, start_at: startAt.trim() || "1970-01-01T00:00:00Z", end_at: endAt.trim() || "9998-12-31T23:59:59Z" }), [account, validity, source, sourceID, startAt, endAt]);
  const [refresh, setRefresh] = useState(0);
  const key = useMemo(() => ({ epoch: auth.epoch, query, accounts, catalogReady, refresh, allowed }), [auth.epoch, query, accounts, catalogReady, refresh, allowed]);
  const [list, setList] = useState<{ key: typeof key | null; rows: ObservationRow[]; ready: boolean; error: string | null }>({ key: null, rows: [], ready: false, error: null });
  const [selected, setSelected] = useState<{ key: typeof key; id: string; account: string } | null>(null);
  const selection = allowed && list.key === key && list.ready && selected?.key === key ? selected : null;
  const [detail, setDetail] = useState<{ selection: typeof selected; data: ObservationSelection | null; error: string | null }>({ selection: null, data: null, error: null });
  const data = selection && detail.selection === selection ? detail.data : null;
  const [action, setAction] = useState<{ key: typeof key; selection: NonNullable<typeof selected>; ticket: number; kind: ObservationDecision["kind"]; revision: string; reason: string; endID: string; window: "short" | "seven_day" | ""; phase: ActionPhase; error: string | null } | null>(null);
  const currentAction = selection && action?.key === key && action.selection === selection ? action : null;
  const [pending, setPending] = useState<Pending | null>(null);
  const pendingRef = useRef<Pending | null>(null);
  const serial = useRef(0), busy = useRef(false), mounted = useRef(false), mutation = useRef(new AbortController());
  const live = useRef({ key, selection, action: currentAction, allowed, isAdmin, epoch: auth.epoch });
  live.current = { key, selection, action: currentAction, allowed, isAdmin, epoch: auth.epoch };
  function retain(value: Pending | null) { pendingRef.current = value; setPending(value); }
  function failure(error: unknown) {
    const result = connectionFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return { ...result, text: error instanceof ObservationReadError ? error.message : result.text };
  }
  useEffect(() => {
    mounted.current = true;
    const focus = () => { if (document.visibilityState === "visible") setRefresh(value => value + 1); };
    window.addEventListener("focus", focus);
    return () => { mounted.current = false; mutation.current.abort(); window.removeEventListener("focus", focus); };
  }, []);
  useEffect(() => {
    let alive = true;
    const abort = new AbortController();
    setList({ key, rows: [], ready: false, error: null });
    if (allowed && catalogReady) void (async () => {
      try {
        if (query.account_id && !accounts.some(item => item.id === query.account_id)) throw new ObservationReadError(uiCopy.chooseAccount);
        const client = createObservationsClient(window.location.origin);
        const rows = await readObservationPages(query, after => usageAllowed ? client.listUsage({ ...query, after, limit: 100 }, { signal: abort.signal }) : client.list({ ...query, after, limit: 100 }, { signal: abort.signal }), usageAllowed);
        if (alive) setList({ key, rows, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const result = connectionFailure(error);
        setList({ key, rows: [], ready: false, error: error instanceof ObservationReadError ? error.message : result.status === 0 ? requestFailureText(result.text, "读取失败，重试。") : result.text });
        if ([401, 403, 404].includes(result.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, catalogReady, query, accounts, usageAllowed, refreshSession]);
  useEffect(() => {
    let alive = true;
    const abort = new AbortController();
    setDetail({ selection, data: null, error: null });
    if (selection) void (async () => {
      try {
        const client = createObservationsClient(window.location.origin), signal = abort.signal;
        const data = await readObservationSelection(selection.id, selection.account, () => client.get({ id: selection.id }, { signal }), () => client.state({ account_id: selection.account }, { signal }), estimatesAllowed ? () => client.estimateLink({ id: selection.id }, { signal }) : null, isAdmin ? () => client.recommendationLink({ id: selection.id }, { signal }) : null, usageAllowed ? () => client.usageEvidence({ id: selection.id }, { signal }) : null, () => readObservationEvents(selection.account, after => client.events({ account_id: selection.account, after, limit: 100 }, { signal })));
        if (alive) setDetail({ selection, data, error: null });
      } catch (error) {
        if (!alive) return;
        const result = connectionFailure(error);
        setDetail({ selection, data: null, error: error instanceof ObservationReadError ? error.message : result.status === 0 ? requestFailureText(result.text, "详情待核对。") : result.text });
        if ([401, 403, 404].includes(result.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [selection, estimatesAllowed, usageAllowed, isAdmin, refreshSession]);

  function openAction(kind: ObservationDecision["kind"]) {
    if (!isAdmin || !selection || !data || busy.current) return;
    const unresolved = pendingRef.current;
    if (unresolved && (unresolved.account_id !== selection.account || unresolved.observation_id !== selection.id)) return;
    setAction({ key, selection, ticket: ++serial.current, kind: unresolved?.kind ?? kind, revision: unresolved?.revision ?? data.management.revision,
      reason: unresolved?.reason ?? (kind === "restore" ? "管理员通过观测记录页确认恢复此记录" : kind === "unanchor" ? "管理员通过观测记录页确认撤销人工起点区间" : ""),
      endID: unresolved?.kind === "anchor" ? unresolved.end_observation_id : "", window: unresolved?.kind === "anchor" ? unresolved.window_kind : "",
      phase: unresolved ? "unknown" : "ready", error: unresolved ? recoveryText : null });
  }
  function closeAction() { if (!busy.current) setAction(null); }
  async function apply() {
    const draft = currentAction, actor = auth.session?.user.id;
    if (!draft || !isAdmin || !actor || busy.current || draft.phase === "conflict") return;
    const recovering = draft.phase === "unknown", unresolved = pendingRef.current;
    const reason = draft.reason.trim();
    if (!recovering && (unresolved || !observationReason.safeParse(reason).success)) return;
    let decision: Pending;
    if (recovering && unresolved) decision = unresolved;
    else {
      const base = { epoch: auth.epoch, account_id: draft.selection.account, observation_id: draft.selection.id, revision: draft.revision, reason, actor_id: actor, acknowledged: false };
      if (draft.kind === "anchor") {
        if (!data || !observationRangeEnds(data.detail.observation, list.rows).some(end => end.id === draft.endID) || !data.detail.observation.snapshot.windows.some(window => window.kind === draft.window) || draft.window !== "short" && draft.window !== "seven_day") return;
        decision = { ...base, kind: "anchor", end_observation_id: draft.endID, window_kind: draft.window };
      } else decision = { ...base, kind: draft.kind };
    }
    busy.current = true;
    mutation.current = new AbortController();
    const signal = mutation.current.signal, client = createObservationsClient(window.location.origin);
    const current = () => mounted.current && live.current.key === draft.key && live.current.selection === draft.selection && live.current.action?.ticket === draft.ticket && live.current.allowed && live.current.isAdmin;
    const update = (phase: ActionPhase, error: string | null) => { if (current()) setAction(value => value?.ticket === draft.ticket ? { ...value, phase, error } : value); };
    update("busy", null);
    if (!recovering) retain(decision);
    let acknowledged = decision.acknowledged;
    try {
      const expected = (BigInt(decision.revision) + 1n).toString();
      if (!recovering) {
        if (decision.kind === "anchor") {
          const saved = await client.start({ account_id: decision.account_id, revision: decision.revision, start_observation_id: decision.observation_id, end_observation_id: decision.end_observation_id, window_kind: decision.window_kind, reason: decision.reason }, { signal });
          if (saved.account_id !== decision.account_id || saved.revision !== expected || !saved.manual_start || !observationDecisionMatches(saved.manual_start.event, decision)) throw new ObservationReadError("起点不符。");
        } else if (decision.kind === "unanchor") {
          const saved = await client.revoke({ account_id: decision.account_id, revision: decision.revision, reason: decision.reason }, { signal });
          if (saved.account_id !== decision.account_id || saved.revision !== expected || saved.manual_start !== null) throw new ObservationReadError("撤销待核对。");
        } else {
          const saved = await client.review({ account_id: decision.account_id, observation_id: decision.observation_id, revision: decision.revision, reason: decision.reason, excluded: decision.kind === "exclude" }, { signal });
          if (saved.id !== decision.observation_id || saved.account_id !== decision.account_id || saved.management_revision !== expected || saved.excluded !== (decision.kind === "exclude") || saved.review_reason !== decision.reason) throw new ObservationReadError("回执不符。");
        }
        acknowledged = true;
        if (mounted.current && live.current.epoch === decision.epoch) retain({ ...decision, acknowledged: true });
      }
      const result = await readObservationSelection(decision.observation_id, decision.account_id, () => client.get({ id: decision.observation_id }, { signal }), () => client.state({ account_id: decision.account_id }, { signal }), estimatesAllowed ? () => client.estimateLink({ id: decision.observation_id }, { signal }) : null, () => client.recommendationLink({ id: decision.observation_id }, { signal }), () => client.usageEvidence({ id: decision.observation_id }, { signal }), () => readObservationEvents(decision.account_id, after => client.events({ account_id: decision.account_id, after, limit: 100 }, { signal })));
      if (recovering) {
        const event = result.events?.find(event => event.revision === expected);
        if (!event) throw new ObservationReadError(uiCopy.pendingResult);
        if (!observationDecisionMatches(event, decision)) {
          if (current()) { retain(null); update("conflict", "已有其他决定。"); }
          return;
        }
      } else if (result.management.revision !== expected || (decision.kind === "anchor" ? !result.management.manual_start || !observationDecisionMatches(result.management.manual_start.event, decision) : decision.kind === "unanchor" ? result.management.manual_start !== null : result.detail.observation.excluded !== (decision.kind === "exclude") || result.detail.observation.review_reason !== decision.reason)) {
        throw new ObservationReadError("状态已变。");
      }
      if (!current()) return;
      retain(null); setAction(null); setSelected(null); setRefresh(value => value + 1);
      toast({ tone: "info", title: recovering ? "已核对决定" : { exclude: "已排除", restore: "已恢复", anchor: "起点已保存", unanchor: "已撤销起点" }[decision.kind], body: "更新估算说明。" });
    } catch (error) {
      if (!mounted.current || live.current.epoch !== decision.epoch) return;
      const result = failure(error);
      if (!recovering && !acknowledged && result.status !== 0) {
        retain(null);
        update(result.status === 409 ? "conflict" : "ready", result.text);
      } else update("unknown", (acknowledged ? "回执待核对。" : "提交待核对。") + result.text + " " + recoveryText);
    } finally { busy.current = false; }
  }

  const currentList = allowed && list.key === key;
  const unresolved = pending?.epoch === auth.epoch && isAdmin ? pending : null;
  const endpoints = currentAction && data ? observationRangeEnds(data.detail.observation, list.rows) : [];
  return { accounts, rows: currentList ? list.rows : empty, ready: currentList && list.ready,
    error: !allowed ? "无观测权限。" : catalogAllowed && scope.accountCatalogError || (currentList ? list.error : null),
    selection, data, detailError: selection && detail.selection === selection ? detail.error : null,
    select: (record: Observation | null) => { setAction(null); setSelected(record && currentList && list.ready ? { key, id: record.id, account: record.account_id } : null); },
    action: currentAction, openAction, closeAction, apply,
    setReason: (reason: string) => setAction(value => value?.phase === "ready" ? { ...value, reason } : value),
    endpoints,
    setEnd: (endID: string) => setAction(value => value?.phase === "ready" ? { ...value, endID } : value),
    setWindow: (window: "short" | "seven_day" | "") => setAction(value => value?.phase === "ready" ? { ...value, window } : value),
    canConfirm: !!currentAction && (currentAction.phase === "unknown" || currentAction.phase === "ready" && observationReason.safeParse(currentAction.reason.trim()).success && (currentAction.kind !== "anchor" || endpoints.some(end => end.id === currentAction.endID) && !!data?.detail.observation.snapshot.windows.some(window => window.kind === currentAction.window))),
    mutationBlocked: !!unresolved && (unresolved.account_id !== selection?.account || unresolved.observation_id !== selection?.id),
    pendingNotice: unresolved ? `观测 ${unresolved.observation_id} 的${unresolved.acknowledged ? "待核对回执" : "决定未确认"}。到全部核对。` : null };
}
