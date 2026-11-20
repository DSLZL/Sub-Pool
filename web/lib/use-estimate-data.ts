"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { createAccountsClient, createEstimatesClient } from "@sub-pool/contracts/client";
import type { ManagedAccountView } from "@sub-pool/contracts/accounts";
import type { EstimateRecord } from "@sub-pool/contracts/estimates";
import { useApp } from "@/lib/store";
import type { useScope } from "@/lib/scope";
import { connectionFailure } from "./connection-data";
import { EstimateReadError, estimateConfigurationMatches, estimateHistoryFilter, estimateRecomputeInput, estimateRecomputeMatches, estimateSave, readEstimateData, readEstimateRecord, reconcileEstimateConfiguration, reconcileEstimateRecompute, savedWindowScope, type EstimateData, type EstimateDraft, type EstimateEntry, type EstimateHistoryFilter, type EstimateRecompute, type EstimateSave, type WindowScope } from "./estimates-data";

type Form = { epoch: number; ticket: number; base: EstimateEntry; scope: WindowScope; draft: EstimateDraft; reference: ManagedAccountView | null; phase: "loading" | "ready" | "busy" | "conflict" | "unknown"; error: string | null };
type Pending = { epoch: number; base: EstimateEntry; input: EstimateSave; acknowledgement: EstimateRecord | null };
type Filters = EstimateHistoryFilter & { account: string };
type Detail = { epoch: number; key: object; ticket: number; account: string; id: string; record: EstimateRecord | null; error: string | null };
type RecomputeForm = { epoch: number; ticket: number; base: EstimateEntry; input: EstimateRecompute; acknowledgement: EstimateRecord | null; phase: "ready" | "busy" | "conflict" | "unknown"; error: string | null };
const empty: EstimateData = { entries: [], history: [] };
const allFilters: Filters = { account: "", window_kind: "all", period_end_at: "" };

export function useEstimateData(scope: ReturnType<typeof useScope>, history: boolean) {
  const { auth, isAdmin, refreshSession, toast } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("estimates"));
  const catalogAllowed = isAdmin || !!auth.access?.features.includes("accounts");
  const accounts = useMemo(() => catalogAllowed ? scope.accounts : (auth.access?.account_ids ?? []).map(id => ({ id, name: id })), [catalogAllowed, scope.accounts, auth.epoch]);
  const catalogReady = !catalogAllowed || scope.accountCatalogAvailable;
  const [selection, setSelection] = useState({ epoch: auth.epoch, filters: allFilters });
  const filters = selection.epoch === auth.epoch ? selection.filters : allFilters;
  const selectedAccounts = useMemo(() => filters.account === "" ? accounts : accounts.filter(account => account.id === filters.account), [accounts, filters.account]);
  const [refresh, setRefresh] = useState(0);
  const key = useMemo(() => ({ epoch: auth.epoch, accounts, catalogReady, history, refresh, allowed, filters }), [auth.epoch, accounts, catalogReady, history, refresh, allowed, filters]);
  const [state, setState] = useState<{ key: typeof key | null; data: EstimateData; ready: boolean; error: string | null }>({ key: null, data: empty, ready: false, error: null });
  const [form, setForm] = useState<Form | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [recomputeForm, setRecomputeForm] = useState<RecomputeForm | null>(null);
  const recomputePending = useRef<RecomputeForm | null>(null), detailSerial = useRef(0), detailRead = useRef(new AbortController());
  const pendingRef = useRef<Pending | null>(null), busy = useRef(false), serial = useRef(0), mounted = useRef(false), mutation = useRef(new AbortController());
  const visible = allowed && state.key === key;
  const data = visible && state.ready ? state.data : empty;
  const simulationTargets = data.entries.filter(entry => entry.current.simulation_configurable === true);
  const activeForm = allowed && isAdmin && form?.epoch === auth.epoch ? form : null;
  const activeRecompute = allowed && isAdmin && recomputeForm?.epoch === auth.epoch ? recomputeForm : null;
  const live = useRef({ epoch: auth.epoch, form: activeForm, recompute: activeRecompute, key, allowed, isAdmin });
  live.current = { epoch: auth.epoch, form: activeForm, recompute: activeRecompute, key, allowed, isAdmin };
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
    document.addEventListener("visibilitychange", focus);
    return () => {
      mounted.current = false; mutation.current.abort(); detailRead.current.abort();
      window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", focus);
    };
  }, []);
  useEffect(() => {
    serial.current++; detailSerial.current++; mutation.current.abort(); detailRead.current.abort();
    retain(null); recomputePending.current = null; setForm(null); setDetail(null); setRecomputeForm(null);
  }, [auth.epoch]);
  useEffect(() => {
    let alive = true;
    let timer: number | undefined;
    let pollWeekly = false;
    const abort = new AbortController();
    setState({ key, data: empty, ready: false, error: null });
    async function read() {
      try {
        if (filters.account !== "" && selectedAccounts.length !== 1) throw new EstimateReadError("所选账号已不在当前授权目录，未读取其他账号。");
        const filter = history ? estimateHistoryFilter(filters.window_kind, filters.period_end_at) : undefined;
        const data = await readEstimateData(selectedAccounts, createEstimatesClient(window.location.origin), history, abort.signal, filter);
        if (alive) {
          setState({ key, data, ready: true, error: null });
          pollWeekly = !history && data.entries.some(entry => entry.current.native_weekly);
        }
      } catch (error) {
        if (!alive) return;
        const result = failure(error);
        pollWeekly = pollWeekly && (result.status === 0 || result.status >= 500);
        setState({ key, data: empty, ready: false, error: result.status === 0 && !(error instanceof EstimateReadError) ? "暂时无法完整读取估算；没有展示部分结果或模拟数据。" : result.text });
      } finally {
        if (alive && pollWeekly) timer = window.setTimeout(() => {
          if (document.visibilityState === "visible") void read();
        }, 15000);
      }
    }
    if (allowed && catalogReady) void read();
    return () => { alive = false; window.clearTimeout(timer); abort.abort(); };
  }, [key, allowed, catalogReady, selectedAccounts, history, refreshSession]);

  function changeFilters(next: Filters) {
    if (busy.current) return;
    serial.current++; detailSerial.current++; mutation.current.abort(); detailRead.current.abort();
    setForm(null); setDetail(null); setRecomputeForm(null); setSelection({ epoch: auth.epoch, filters: next });
  }
  function selectAccount(account: string) {
    if (account === "" || accounts.some(value => value.id === account)) changeFilters({ ...filters, account });
  }
  function selectHistoryWindow(value: string) {
    if (value === "all" || value === "short" || value === "seven_day") changeFilters({ ...filters, window_kind: value, period_end_at: value === "all" ? "" : filters.period_end_at });
  }
  function editPeriod(period_end_at: string) { changeFilters({ ...filters, period_end_at }); }
  function closeDetail() { detailSerial.current++; detailRead.current.abort(); setDetail(null); }
  async function openRecord(account: string, id: string) {
    if (!allowed || !visible || !state.ready || !data.entries.some(value => value.account.id === account)) return;
    const epoch = auth.epoch, ticket = ++detailSerial.current;
    detailRead.current.abort(); detailRead.current = new AbortController();
    setDetail({ epoch, key, ticket, account, id, record: null, error: null });
    const current = () => mounted.current && live.current.epoch === epoch && live.current.key === key && live.current.allowed && detailSerial.current === ticket;
    try {
      const record = await readEstimateRecord(account, id, () => createEstimatesClient(window.location.origin).record({ id }, { signal: detailRead.current.signal }));
      if (current()) setDetail({ epoch, key, ticket, account, id, record, error: null });
    } catch (error) {
      if (current()) setDetail({ epoch, key, ticket, account, id, record: null, error: failure(error).text });
    }
  }

  async function readTarget(base: EstimateEntry, scope: WindowScope) {
    const epoch = auth.epoch, ticket = ++serial.current, a = base.current.configuration.assumptions;
    mutation.current.abort(); mutation.current = new AbortController();
    setForm({ epoch, ticket, base, scope, draft: { keep_ratio: a?.keep_ratio ?? "", premise: a?.premise ?? "" }, reference: null, phase: "loading", error: null });
    const current = () => mounted.current && serial.current === ticket && live.current.epoch === epoch && live.current.isAdmin && live.current.allowed;
    try {
      const reference = await createAccountsClient(window.location.origin).get({ id: base.account.id }, { signal: mutation.current.signal });
      if (current()) setForm(value => value?.ticket === ticket ? { ...value, reference, phase: "ready" } : value);
    } catch (error) {
      if (current()) setForm(value => value?.ticket === ticket ? { ...value, phase: "conflict", error: failure(error).text + " 请关闭后重新打开读取，未提交配置。" } : value);
    }
  }
  function openConfiguration() {
    if (!isAdmin || !allowed || !visible || !state.ready || busy.current || recomputePending.current?.epoch === auth.epoch) return;
    const unresolved = pendingRef.current?.epoch === auth.epoch ? pendingRef.current : null;
    const base = unresolved?.base ?? simulationTargets[0];
    if (!base) return;
    if (unresolved) setForm({ epoch: auth.epoch, ticket: ++serial.current, base, scope: unresolved.input.windows.length === 2 ? "both" : unresolved.input.windows[0] ?? "short", draft: { keep_ratio: unresolved.input.keep_ratio, premise: unresolved.input.premise }, reference: null, phase: "unknown", error: "保存结果尚未完成核对；确认按钮只读取记录，不会重复提交。" });
    else void readTarget(base, savedWindowScope(base.current));
  }
  function selectTarget(value: string) {
    if (!activeForm || activeForm.phase !== "ready" || busy.current || pendingRef.current?.epoch === auth.epoch) return;
    const [account, selected] = value.split(":");
    if (selected !== "both" && selected !== "short" && selected !== "seven_day") return;
    const base = simulationTargets.find(value => value.account.id === account);
    if (base?.account.id === activeForm.base.account.id) setForm({ ...activeForm, ticket: ++serial.current, scope: selected, error: null });
    else if (base) void readTarget(base, selected);
  }
  function editDraft(field: keyof EstimateDraft, value: string) {
    if (activeForm?.phase === "ready" && !busy.current && pendingRef.current?.epoch !== auth.epoch) setForm({ ...activeForm, draft: { ...activeForm.draft, [field]: value }, error: null });
  }
  function closeConfiguration() { if (!busy.current) { serial.current++; mutation.current.abort(); setForm(null); setRefresh(value => value + 1); } }
  async function save() {
    const draft = activeForm;
    if (!draft || busy.current || draft.phase !== "ready" && draft.phase !== "unknown") return;
    const recovering = draft.phase === "unknown";
    if (!recovering && (!visible || !state.ready || pendingRef.current?.epoch === auth.epoch || recomputePending.current?.epoch === auth.epoch || !draft.reference)) return;
    let decision: Pending;
    try {
      if (recovering) { if (!pendingRef.current) return; decision = pendingRef.current; }
      else { if (!draft.reference) return; decision = { epoch: auth.epoch, base: draft.base, input: estimateSave(draft.base, draft.scope, draft.draft, draft.reference), acknowledgement: null }; }
    }
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
      toast({ tone: "info", title: recovering ? "已核对保存的配置" : "配置已保存并回读", body: result.changed ? "相符的原保存记录仍在；当前配置已有更新，请读取最新状态。" : "已保存明确声明的比例、前提和窗口。金额和资金资格仍不可用。" });
    } catch (error) {
      if (!mounted.current || live.current.epoch !== decision.epoch) return;
      const result = failure(error);
      if (!recovering && !decision.acknowledgement && result.status !== 0) {
        retain(null); update(result.status === 409 ? "conflict" : "ready", result.text);
      } else update("unknown", (decision.acknowledgement ? "保存回执已收到，但回读未完成。" : "提交结果尚未确认。") + result.text + " 保存按钮只会只读核对，不会重复提交。");
    } finally { busy.current = false; }
  }
  function openRecompute() {
    if (!isAdmin || !allowed || !visible || !state.ready || busy.current || pendingRef.current?.epoch === auth.epoch) return;
    if (recomputePending.current?.epoch === auth.epoch) { setRecomputeForm({ ...recomputePending.current, phase: "unknown", error: "原重算请求结果尚未完成核对；确认按钮只读取持久记录，不会重复提交。" }); return; }
    const base = data.entries.length === 1 ? simulationTargets[0] : undefined;
    if (!base) return;
    try { setRecomputeForm({ epoch: auth.epoch, ticket: ++serial.current, base, input: estimateRecomputeInput(base), acknowledgement: null, phase: "ready", error: null }); }
    catch (error) { toast({ tone: "warn", title: failure(error).text }); }
  }
  function closeRecompute() { if (!busy.current) { setRecomputeForm(null); setRefresh(value => value + 1); } }
  async function recompute() {
    const draft = activeRecompute;
    if (!draft || busy.current || draft.phase !== "ready" && draft.phase !== "unknown") return;
    const recovering = draft.phase === "unknown";
    if (!recovering && (!visible || !state.ready || recomputePending.current?.epoch === auth.epoch || pendingRef.current?.epoch === auth.epoch)) return;
    const selected = recovering ? recomputePending.current : draft;
    if (!selected || selected.epoch !== auth.epoch) return;
    let decision: RecomputeForm = selected;
    busy.current = true; mutation.current = new AbortController();
    const client = createEstimatesClient(window.location.origin), signal = mutation.current.signal;
    const current = () => mounted.current && live.current.epoch === draft.epoch && live.current.recompute?.ticket === draft.ticket && live.current.allowed && live.current.isAdmin;
    const update = (phase: RecomputeForm["phase"], error: string | null) => { if (current()) setRecomputeForm({ ...decision, phase, error }); };
    if (!recovering) recomputePending.current = decision;
    update("busy", null);
    try {
      if (!recovering) {
        const acknowledgement = await client.recompute(decision.input, { signal });
        if (!estimateRecomputeMatches(acknowledgement, decision.input)) throw new EstimateReadError("重算回执与提交版本不一致，结果继续待核对。");
        decision = { ...decision, acknowledgement };
        if (mounted.current && live.current.epoch === decision.epoch) recomputePending.current = decision;
      }
      const result = await reconcileEstimateRecompute(decision.input, decision.acknowledgement, client, signal);
      if (!current()) return;
      recomputePending.current = null; setRecomputeForm(null); closeDetail(); setRefresh(value => value + 1);
      toast({ tone: "info", title: "本地重算记录已回读", body: `记录 ${result.persisted.id}；` + (result.changed ? "当前输入另有更新，请读取最新状态。" : "金额和资金资格仍由实际证据决定。") });
    } catch (error) {
      if (!mounted.current || live.current.epoch !== decision.epoch) return;
      const result = failure(error);
      if (!recovering && !decision.acknowledgement && result.status !== 0) { recomputePending.current = null; update(result.status === 409 ? "conflict" : "ready", result.text); }
      else update("unknown", (decision.acknowledgement ? "重算回执已收到，但持久记录尚未核对。" : "重算请求的结果尚未确认。") + result.text + " 确认按钮只读取结果，不会重复提交。");
    } finally { busy.current = false; }
  }
  const unresolved = pending?.epoch === auth.epoch && allowed && isAdmin ? pending : null;
  let validation: string | null = null;
  if (activeForm?.phase === "ready" && activeForm.reference) {
    try { estimateSave(activeForm.base, activeForm.scope, activeForm.draft, activeForm.reference); }
    catch (error) { validation = error instanceof Error ? error.message : "当前配置输入无效。"; }
  }
  return { ...data, ready: visible && state.ready,
    error: !allowed ? "未授予估算读取权限。" : catalogAllowed && scope.accountCatalogError || (visible ? state.error : null),
    form: activeForm ? { ...activeForm, error: activeForm.error ?? validation } : null, openConfiguration, closeConfiguration, selectTarget, editDraft, save,
    accounts, filters, selectAccount, selectHistoryWindow, editPeriod, simulationTargets,
    detail: allowed && detail?.epoch === auth.epoch && detail.key === key ? detail : null, openRecord, closeDetail,
    recomputeForm: activeRecompute, openRecompute, closeRecompute, recompute,
    canRecompute: allowed && isAdmin && visible && state.ready && !busy.current && !unresolved && (recomputePending.current?.epoch === auth.epoch || data.entries.length === 1 && !!simulationTargets[0]?.current.configuration.assumptions),
    recomputeUnresolved: recomputePending.current?.epoch === auth.epoch,
    canOpen: allowed && isAdmin && visible && state.ready && (simulationTargets.length > 0 || unresolved !== null) && recomputePending.current?.epoch !== auth.epoch,
    canSave: !!activeForm && (activeForm.phase === "unknown" || activeForm.phase === "ready" && visible && state.ready && !!activeForm.reference && validation === null),
    pendingNotice: unresolved ? "估算配置结果待核对；打开“估算假设”后只读确认，勿重复保存。" : recomputePending.current?.epoch === auth.epoch ? "本地重算结果待核对；仅可读取持久记录，勿重复提交。" : null };
}
