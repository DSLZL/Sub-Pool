"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { createEstimatesClient } from "@sub-pool/contracts/client";
import type { EstimateRecord } from "@sub-pool/contracts/estimates";
import { useApp } from "@/lib/store";
import type { useScope } from "@/lib/scope";
import { connectionFailure } from "./connection-data";
import { EstimateReadError, estimateConfigurationMatches, estimateSave, readEstimateData, reconcileEstimateConfiguration, savedWindowScope, type EstimateData, type EstimateEntry, type EstimateSave, type WindowScope } from "./estimates-data";

type Form = { epoch: number; ticket: number; base: EstimateEntry; scope: WindowScope; phase: "ready" | "busy" | "conflict" | "unknown"; error: string | null };
type Pending = { epoch: number; input: EstimateSave; acknowledgement: EstimateRecord | null };
const empty: EstimateData = { entries: [], history: [] };

export function useEstimateData(scope: ReturnType<typeof useScope>, history: boolean) {
  const { auth, isAdmin, refreshSession, toast } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("estimates"));
  const catalogAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const accounts = useMemo(() => catalogAllowed ? scope.accounts : (auth.access?.account_ids ?? []).map(id => ({ id, name: id })), [catalogAllowed, scope.accounts, auth.epoch]);
  const catalogReady = !catalogAllowed || scope.accountCatalogAvailable;
  const [refresh, setRefresh] = useState(0);
  const key = useMemo(() => ({ epoch: auth.epoch, accounts, catalogReady, history, refresh, allowed }), [auth.epoch, accounts, catalogReady, history, refresh, allowed]);
  const [state, setState] = useState<{ key: typeof key | null; data: EstimateData; ready: boolean; error: string | null }>({ key: null, data: empty, ready: false, error: null });
  const [form, setForm] = useState<Form | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const pendingRef = useRef<Pending | null>(null), busy = useRef(false), serial = useRef(0), mounted = useRef(false), mutation = useRef(new AbortController());
  const visible = allowed && state.key === key;
  const data = visible && state.ready ? state.data : empty;
  const activeForm = allowed && isAdmin && form?.epoch === auth.epoch ? form : null;
  const live = useRef({ epoch: auth.epoch, form: activeForm, allowed, isAdmin });
  live.current = { epoch: auth.epoch, form: activeForm, allowed, isAdmin };
  function retain(value: Pending | null) { pendingRef.current = value; setPending(value); }
  function failure(error: unknown) {
    const result = connectionFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return { ...result, text: error instanceof EstimateReadError ? error.message : result.text };
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
    setState({ key, data: empty, ready: false, error: null });
    if (allowed && catalogReady) void (async () => {
      try {
        const data = await readEstimateData(accounts, createEstimatesClient(window.location.origin), history, abort.signal);
        if (alive) setState({ key, data, ready: true, error: null });
      } catch (error) {
        if (!alive) return;
        const result = failure(error);
        setState({ key, data: empty, ready: false, error: result.status === 0 && !(error instanceof EstimateReadError) ? "暂时无法完整读取估算；没有展示部分结果或模拟数据。" : result.text });
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, catalogReady, accounts, history, refreshSession]);

  function openConfiguration() {
    if (!isAdmin || !allowed || !visible || !state.ready || busy.current) return;
    const unresolved = pendingRef.current?.epoch === auth.epoch ? pendingRef.current : null;
    const base = data.entries.find(value => value.account.id === unresolved?.input.account_id) ?? data.entries[0];
    if (!base) return;
    setForm({ epoch: auth.epoch, ticket: ++serial.current, base, scope: unresolved ? unresolved.input.windows.length === 2 ? "both" : unresolved.input.windows[0] ?? "short" : savedWindowScope(base.current), phase: unresolved ? "unknown" : "ready", error: unresolved ? "保存结果尚未完成核对；确认按钮只读取记录，不会重复提交。" : null });
  }
  function selectTarget(value: string) {
    if (!activeForm || activeForm.phase !== "ready" || busy.current || pendingRef.current) return;
    const [account, selected] = value.split(":");
    if (selected !== "both" && selected !== "short" && selected !== "seven_day") return;
    const base = data.entries.find(value => value.account.id === account);
    if (base) setForm({ ...activeForm, ticket: ++serial.current, base, scope: selected, error: null });
  }
  function closeConfiguration() { if (!busy.current) { setForm(null); setRefresh(value => value + 1); } }
  async function save() {
    const draft = activeForm;
    if (!draft || busy.current || draft.phase === "conflict") return;
    const recovering = draft.phase === "unknown";
    if (!recovering && (!visible || !state.ready || pendingRef.current || !draft.base.current.configuration.assumptions || draft.base.current.current_state === "condition_changed")) return;
    let decision: Pending;
    try { decision = recovering && pendingRef.current ? pendingRef.current : { epoch: auth.epoch, input: estimateSave(draft.base, draft.scope), acknowledgement: null }; }
    catch (error) { setForm(value => value ? { ...value, error: failure(error).text } : value); return; }
    busy.current = true;
    mutation.current = new AbortController();
    const signal = mutation.current.signal, client = createEstimatesClient(window.location.origin);
    const current = () => mounted.current && live.current.epoch === draft.epoch && live.current.form?.ticket === draft.ticket && live.current.allowed && live.current.isAdmin;
    const update = (phase: Form["phase"], error: string | null) => { if (current()) setForm(value => value?.ticket === draft.ticket ? { ...value, phase, error } : value); };
    update("busy", null);
    if (!recovering) retain(decision);
    try {
      if (!recovering) {
        const acknowledgement = await client.configure(decision.input, { signal });
        if (!estimateConfigurationMatches(acknowledgement, decision.input)) throw new EstimateReadError("保存回执与提交的配置版本不一致，结果继续待核对。");
        decision = { ...decision, acknowledgement };
        if (mounted.current && live.current.epoch === decision.epoch) retain(decision);
      }
      const result = await reconcileEstimateConfiguration(decision.input, decision.acknowledgement, client, signal);
      if (!current()) return;
      retain(null); setForm(null); setRefresh(value => value + 1);
      toast({ tone: "info", title: recovering ? "已核对保存的配置" : "配置已保存并回读", body: result.changed ? "相符的原保存记录仍在；当前配置已有更新，请读取最新状态。" : "保留原有明确比例与前提，仅保存所选窗口。金额和资金资格仍不可用。" });
    } catch (error) {
      if (!mounted.current || live.current.epoch !== decision.epoch) return;
      const result = failure(error);
      if (!recovering && !decision.acknowledgement && result.status !== 0) {
        retain(null); update(result.status === 409 ? "conflict" : "ready", result.text);
      } else update("unknown", (decision.acknowledgement ? "保存回执已收到，但回读未完成。" : "提交结果尚未确认。") + result.text + " 保存按钮只会只读核对，不会重复提交。");
    } finally { busy.current = false; }
  }
  const unresolved = pending?.epoch === auth.epoch && allowed && isAdmin ? pending : null;
  return { ...data, ready: visible && state.ready,
    error: !allowed ? "未授予估算读取权限。" : catalogAllowed && scope.accountCatalogError || (visible ? state.error : null),
    form: activeForm, openConfiguration, closeConfiguration, selectTarget, save,
    canOpen: allowed && isAdmin && visible && state.ready && data.entries.length > 0,
    canSave: !!activeForm && (activeForm.phase === "unknown" || activeForm.phase === "ready" && visible && state.ready && !!activeForm.base.current.configuration.assumptions && activeForm.base.current.current_state !== "condition_changed"),
    pendingNotice: unresolved ? "估算配置结果待核对；打开“估算假设”后只读确认，勿重复保存。" : null };
}
