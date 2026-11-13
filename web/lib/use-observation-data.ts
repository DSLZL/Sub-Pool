"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { createObservationsClient } from "@sub-pool/contracts/client";
import { observationReason, type Observation } from "@sub-pool/contracts/observations";
import { connectionFailure } from "@/lib/connection-data";
import { observationDecisionMatches, ObservationReadError, readObservationEvents, readObservationPages, readObservationSelection, type ObservationDecision, type ObservationQuery, type ObservationSelection } from "@/lib/observations-data";
import type { useScope } from "@/lib/scope";
import { useApp } from "@/lib/store";

type Pending = ObservationDecision & { epoch: number; acknowledged: boolean };
type ActionPhase = "ready" | "busy" | "unknown" | "conflict";
const empty: Observation[] = [];
const recoveryText = "决定尚未完成核对；确认按钮只读取已保存状态，不会重复提交。";

export function useObservationData(scope: ReturnType<typeof useScope>, account: string, validity: ObservationQuery["validity"], source: ObservationQuery["source_mode"]) {
  const { auth, isAdmin, refreshSession, toast } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("observations"));
  const catalogAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const estimatesAllowed = isAdmin || !!auth.access?.features.includes("estimates");
  const usageAllowed = isAdmin || !!auth.access?.features.includes("usage");
  // Session epochs change with grants; an identical refresh must preserve the selector catalog.
  const accounts = useMemo(() => catalogAllowed ? scope.accounts : (auth.access?.account_ids ?? []).map(id => ({ id, name: id })), [catalogAllowed, scope.accounts, auth.epoch]);
  const catalogReady = !catalogAllowed || scope.accountCatalogAvailable;
  const query = useMemo<ObservationQuery>(() => ({ account_id: account === "all" ? "" : account, source_id: "", validity, source_mode: source, start_at: "1970-01-01T00:00:00Z", end_at: "9998-12-31T23:59:59Z" }), [account, validity, source]);
  const [refresh, setRefresh] = useState(0);
  const key = useMemo(() => ({ epoch: auth.epoch, query, accounts, catalogReady, refresh, allowed }), [auth.epoch, query, accounts, catalogReady, refresh, allowed]);
  const [list, setList] = useState<{ key: typeof key | null; rows: Observation[]; ready: boolean; error: string | null }>({ key: null, rows: [], ready: false, error: null });
  const [selected, setSelected] = useState<{ key: typeof key; id: string; account: string } | null>(null);
  const selection = allowed && list.key === key && list.ready && selected?.key === key ? selected : null;
  const [detail, setDetail] = useState<{ selection: typeof selected; data: ObservationSelection | null; error: string | null }>({ selection: null, data: null, error: null });
  const data = selection && detail.selection === selection ? detail.data : null;
  const [action, setAction] = useState<{ key: typeof key; selection: NonNullable<typeof selected>; ticket: number; kind: ObservationDecision["kind"] | "anchor"; revision: string; reason: string; phase: ActionPhase; error: string | null } | null>(null);
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
        if (query.account_id && !accounts.some(item => item.id === query.account_id)) throw new ObservationReadError("所选账号不在当前授权范围内，请重新选择。");
        const client = createObservationsClient(window.location.origin);
        const rows = await readObservationPages(query, after => client.list({ ...query, after, limit: 100 }, { signal: abort.signal }));
        if (alive) setList({ key, rows, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const result = connectionFailure(error);
        setList({ key, rows: [], ready: false, error: error instanceof ObservationReadError ? error.message : result.status === 0 ? "暂时无法完整确认观测记录；没有展示部分历史或模拟数据。" : result.text });
        if ([401, 403, 404].includes(result.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, catalogReady, query, accounts, refreshSession]);
  useEffect(() => {
    let alive = true;
    const abort = new AbortController();
    setDetail({ selection, data: null, error: null });
    if (selection) void (async () => {
      try {
        const client = createObservationsClient(window.location.origin), signal = abort.signal;
        const data = await readObservationSelection(selection.id, selection.account, () => client.get({ id: selection.id }, { signal }), () => client.state({ account_id: selection.account }, { signal }), estimatesAllowed ? () => client.estimateLink({ id: selection.id }, { signal }) : null, isAdmin ? () => client.recommendationLink({ id: selection.id }, { signal }) : null, usageAllowed ? () => client.usageEvidence({ id: selection.id }, { signal }) : null);
        if (alive) setDetail({ selection, data, error: null });
      } catch (error) {
        if (!alive) return;
        const result = connectionFailure(error);
        setDetail({ selection, data: null, error: error instanceof ObservationReadError ? error.message : result.status === 0 ? "暂时无法核对观测详情与维护状态；管理操作保持关闭。" : result.text });
        if ([401, 403, 404].includes(result.status)) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [selection, estimatesAllowed, usageAllowed, isAdmin, refreshSession]);

  function openAction(kind: ObservationDecision["kind"] | "anchor") {
    if (!isAdmin || !selection || !data || busy.current) return;
    const unresolved = pendingRef.current;
    if (unresolved && (unresolved.account_id !== selection.account || unresolved.observation_id !== selection.id)) return;
    setAction({ key, selection, ticket: ++serial.current, kind: unresolved?.kind ?? kind, revision: unresolved?.revision ?? data.management.revision,
      reason: unresolved?.reason ?? (kind === "restore" ? "管理员通过观测记录页确认恢复此记录" : kind === "unanchor" ? "管理员通过观测记录页确认撤销人工起点区间" : ""),
      phase: unresolved ? "unknown" : "ready", error: unresolved ? recoveryText : null });
  }
  function closeAction() { if (!busy.current) setAction(null); }
  async function apply() {
    const draft = currentAction, actor = auth.session?.user.id;
    if (!draft || !isAdmin || !actor || busy.current || draft.kind === "anchor" || draft.phase === "conflict") return;
    const recovering = draft.phase === "unknown", unresolved = pendingRef.current;
    const reason = draft.reason.trim();
    if (!recovering && (unresolved || !observationReason.safeParse(reason).success)) return;
    const decision: Pending = recovering && unresolved ? unresolved : { epoch: auth.epoch, account_id: draft.selection.account, observation_id: draft.selection.id, kind: draft.kind, revision: draft.revision, reason, actor_id: actor, acknowledged: false };
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
        if (decision.kind === "unanchor") {
          const saved = await client.revoke({ account_id: decision.account_id, revision: decision.revision, reason: decision.reason }, { signal });
          if (saved.account_id !== decision.account_id || saved.revision !== expected || saved.manual_start !== null) throw new ObservationReadError("撤销回执与提交的版本不一致。");
        } else {
          const saved = await client.review({ account_id: decision.account_id, observation_id: decision.observation_id, revision: decision.revision, reason: decision.reason, excluded: decision.kind === "exclude" }, { signal });
          if (saved.id !== decision.observation_id || saved.account_id !== decision.account_id || saved.management_revision !== expected || saved.excluded !== (decision.kind === "exclude") || saved.review_reason !== decision.reason) throw new ObservationReadError("维护回执与提交的观测或版本不一致。");
        }
        acknowledged = true;
        if (mounted.current && live.current.epoch === decision.epoch) retain({ ...decision, acknowledged: true });
      }
      const result = await readObservationSelection(decision.observation_id, decision.account_id, () => client.get({ id: decision.observation_id }, { signal }), () => client.state({ account_id: decision.account_id }, { signal }), estimatesAllowed ? () => client.estimateLink({ id: decision.observation_id }, { signal }) : null, () => client.recommendationLink({ id: decision.observation_id }, { signal }), () => client.usageEvidence({ id: decision.observation_id }, { signal }));
      if (recovering) {
        const events = await readObservationEvents(decision.account_id, after => client.events({ account_id: decision.account_id, after, limit: 100 }, { signal }));
        if ((events.at(-1)?.revision ?? "1") !== result.management.revision) throw new ObservationReadError("维护历史在核对期间变化；请再次只读核对。");
        const event = events.find(event => event.revision === expected);
        if (!event) throw new ObservationReadError("尚未读到该版本的决定；结果继续待核对，请勿重复提交。");
        if (!observationDecisionMatches(event, decision)) {
          if (current()) { retain(null); update("conflict", "该版本保存了其他决定。本次操作未获确认，请关闭弹窗并重新读取详情。"); }
          return;
        }
      } else if (result.management.revision !== expected || (decision.kind === "unanchor" ? result.management.manual_start !== null : result.detail.observation.excluded !== (decision.kind === "exclude") || result.detail.observation.review_reason !== decision.reason)) {
        throw new ObservationReadError("决定已收到回执，但当前维护状态已变化；请只读核对历史。");
      }
      if (!current()) return;
      retain(null); setAction(null); setSelected(null); setRefresh(value => value + 1);
      toast({ tone: "info", title: recovering ? "已核对保存的维护决定" : { exclude: "已保存排除决定", restore: "已保存恢复决定", unanchor: "已撤销人工起点" }[decision.kind], body: "原始事实保留。已配置账号会记录新的本地估算解释；不代表估算金额或余额可用。" });
    } catch (error) {
      if (!mounted.current || live.current.epoch !== decision.epoch) return;
      const result = failure(error);
      if (!recovering && !acknowledged && result.status !== 0) {
        retain(null);
        update(result.status === 409 ? "conflict" : "ready", result.text);
      } else update("unknown", (acknowledged ? "已收到保存回执，但回读尚未完成。" : "提交结果尚未确认。") + result.text + " " + recoveryText);
    } finally { busy.current = false; }
  }

  const currentList = allowed && list.key === key;
  const unresolved = pending?.epoch === auth.epoch && isAdmin ? pending : null;
  return { accounts, rows: currentList ? list.rows : empty, ready: currentList && list.ready,
    error: !allowed ? "未授予观测读取权限。" : catalogAllowed && scope.accountCatalogError || (currentList ? list.error : null),
    selection, data, detailError: selection && detail.selection === selection ? detail.error : null,
    select: (record: Observation | null) => { setAction(null); setSelected(record && currentList && list.ready ? { key, id: record.id, account: record.account_id } : null); },
    action: currentAction, openAction, closeAction, apply,
    setReason: (reason: string) => setAction(value => value?.phase === "ready" ? { ...value, reason } : value),
    canConfirm: !!currentAction && currentAction.kind !== "anchor" && (currentAction.phase === "unknown" || currentAction.phase === "ready" && observationReason.safeParse(currentAction.reason.trim()).success),
    mutationBlocked: !!unresolved && (unresolved.account_id !== selection?.account || unresolved.observation_id !== selection?.id),
    pendingNotice: unresolved ? `观测 ${unresolved.observation_id} 的${unresolved.acknowledged ? "保存回执已收到，仍需回读" : "决定尚未确认"}；切换“全部”筛选后打开该观测核对，勿重复提交。` : null };
}
