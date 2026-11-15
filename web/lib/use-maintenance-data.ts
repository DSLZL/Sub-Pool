"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createAccountsClient, createMaintenanceClient } from "@sub-pool/contracts/client";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { MaintenanceInput, MaintenancePlan } from "@sub-pool/contracts/maintenance";
import { useApp } from "./store";
import { checkMaintenancePreflight, checkMaintenanceReceipt, maintenanceCanApply, maintenanceFailure, maintenanceResultText, MaintenanceReadError, readMaintenanceAccounts, readMaintenancePlans } from "./maintenance-data";

type Data = { owner: number; accounts: ManagedAccount[] | null; plans: MaintenancePlan[] | null; error: string | null };
type Selection = { id: string; plan: MaintenancePlan | null };
type Uncertain = { kind: "preflight" } | { kind: "apply"; plan: MaintenancePlan };
type Notice = { text: string; unknown: boolean };

export function useMaintenanceData() {
  const { auth, refreshSession } = useApp();
  const owner = auth.epoch, actor = auth.access?.user.id, allowed = auth.status === "authenticated" && auth.access?.all_data === true;
  const [state, setState] = useState<Data>({ owner, accounts: null, plans: null, error: null });
  const [selection, setSelection] = useState<Selection | null>(null);
  const [pending, setPending] = useState<"read" | "preflight" | "apply" | null>(null);
  const [blocked, setBlocked] = useState(false), [notice, setNotice] = useState<Notice | null>(null);
  const request = useRef({ alive: true, generation: 0, busy: false, abort: new AbortController(), selection: null as Selection | null, uncertain: null as Uncertain | null });
  const live = useRef({ owner, allowed }); live.current = { owner, allowed };
  const current = useCallback((generation: number, epoch: number) => request.current.alive && request.current.generation === generation && live.current.owner === epoch && live.current.allowed, []);
  const begin = useCallback(() => {
    request.current.abort.abort(); request.current.abort = new AbortController(); request.current.busy = true;
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const failure = useCallback((error: unknown) => {
    const result = maintenanceFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return result;
  }, [refreshSession]);
  const showPlan = useCallback((plan: MaintenancePlan) => {
    setState(previous => previous.owner === owner ? { ...previous, plans: previous.plans === null ? null : [...previous.plans.filter(row => row.id !== plan.id), plan].sort((a, b) => BigInt(a.id) > BigInt(b.id) ? -1 : 1) } : previous);
    if (request.current.selection?.id === plan.id) {
      request.current.selection = { id: plan.id, plan }; setSelection(request.current.selection);
    }
  }, [owner]);
  const load = useCallback(async () => {
    if (!allowed || !current(request.current.generation, owner) || request.current.busy) return;
    const ticket = begin(); setPending("read"); setState(previous => ({ owner, accounts: previous.owner === owner ? previous.accounts : null, plans: previous.owner === owner ? previous.plans : null, error: null }));
    const wanted = request.current.selection;
    if (wanted) { request.current.selection = { id: wanted.id, plan: null }; setSelection(request.current.selection); }
    try {
      const client = createMaintenanceClient(window.location.origin), options = { signal: ticket.signal };
      const accounts = await readMaintenanceAccounts(createAccountsClient(window.location.origin), ticket.signal);
      const plans = await readMaintenancePlans(client, accounts, ticket.signal);
      let selected: MaintenancePlan | null = null;
      const uncertain = request.current.uncertain;
      if (wanted) {
        selected = await client.plan({ id: wanted.id }, options);
        if (selected.id !== wanted.id || !accounts.some(account => account.id === selected?.scope.account_id)) throw new MaintenanceReadError("维护计划不属于当前读取范围。");
        if (wanted.plan) checkMaintenanceReceipt(wanted.plan, selected);
      }
      let reconciled: MaintenancePlan | null = null;
      if (uncertain?.kind === "apply") {
        reconciled = selected?.id === uncertain.plan.id ? selected : await client.plan({ id: uncertain.plan.id }, options);
        checkMaintenanceReceipt(uncertain.plan, reconciled);
      }
      if (!current(ticket.generation, owner)) return;
      for (const plan of [selected, reconciled]) if (plan) {
        const index = plans.findIndex(row => row.id === plan.id);
        if (index < 0) plans.push(plan); else plans[index] = plan;
      }
      plans.sort((a, b) => BigInt(a.id) > BigInt(b.id) ? -1 : 1);
      setState({ owner, accounts, plans, error: null });
      if (selected && request.current.selection?.id === selected.id) { request.current.selection = { id: selected.id, plan: selected }; setSelection(request.current.selection); }
      if (uncertain?.kind === "apply" && reconciled && ["ready", "blocked"].includes(reconciled.state)) {
        setBlocked(true); setNotice({ text: "已读取此计划，但尚未观察到受理或终态；上次确认结果仍未知。请继续只读核对，不会自动重发。", unknown: true });
      } else {
        request.current.uncertain = null; setBlocked(false);
        if (uncertain) setNotice({ text: uncertain.kind === "preflight" ? "已读取已保存计划。上次预检回执仍未确认，请先审阅列表；后续预检需要重新手动发起。" : "已按已知计划 ID 核对当前后台状态；没有重新提交应用。", unknown: uncertain.kind === "preflight" });
        else setNotice(previous => previous?.unknown ? previous : null);
      }
    } catch (error) {
      if (current(ticket.generation, owner)) { setState({ owner, accounts: null, plans: null, error: failure(error).text }); setBlocked(true); }
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }, [allowed, owner, begin, current, failure]);
  useEffect(() => {
    request.current.alive = true; request.current.selection = null; request.current.uncertain = null;
    setSelection(null); setNotice(null); setBlocked(false); void load();
    return () => { request.current.alive = false; request.current.generation++; request.current.busy = false; request.current.abort.abort(); };
  }, [load]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") void load(); };
    window.addEventListener("focus", focus); document.addEventListener("visibilitychange", focus);
    return () => { window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", focus); };
  }, [load]);
  const visible = allowed && state.owner === owner, accounts = visible ? state.accounts : null, plans = visible ? state.plans : null;
  const selected = visible ? selection?.plan ?? null : null;
  useEffect(() => {
    if (pending || !visible) return;
    const working = plans?.some(plan => plan.state === "queued" || plan.state === "running");
    const expiry = selected?.state === "ready" && selected.validity === "valid" ? Date.parse(selected.expires_at) - Date.now() + 1 : null;
    if (!working && expiry === null) return;
    const timer = window.setTimeout(() => void load(), working ? 2000 : Math.max(0, expiry ?? 0));
    return () => window.clearTimeout(timer);
  }, [plans, selected, pending, visible, load]);
  async function open(id: string) {
    if (!allowed || !current(request.current.generation, owner) || request.current.busy) return;
    const ticket = begin(), expected = plans?.find(plan => plan.id === id); setPending("read");
    request.current.selection = { id, plan: null }; setSelection(request.current.selection);
    try {
      const value = await createMaintenanceClient(window.location.origin).plan({ id }, { signal: ticket.signal });
      if (value.id !== id || !accounts?.some(account => account.id === value.scope.account_id)) throw new MaintenanceReadError("维护计划与当前选择不一致。");
      if (expected) checkMaintenanceReceipt(expected, value);
      if (current(ticket.generation, owner)) showPlan(value);
    } catch (error) {
      if (current(ticket.generation, owner)) { setNotice({ text: failure(error).text, unknown: false }); setBlocked(true); }
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }
  function close() { request.current.selection = null; setSelection(null); }
  async function preflight(input: MaintenanceInput) {
    if (!actor || !visible || !current(request.current.generation, owner) || blocked || accounts === null || plans === null || request.current.busy || !accounts.some(account => account.id === input.account_id)) return null;
    const ticket = begin(); setPending("preflight"); setNotice(null);
    try {
      const value = await createMaintenanceClient(window.location.origin).preflight(input, { signal: ticket.signal });
      if (!current(ticket.generation, owner)) return null;
      checkMaintenancePreflight(input, value, actor); showPlan(value); return value;
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const result = failure(error), unknown = result.status === 0;
        if (unknown) request.current.uncertain = { kind: "preflight" };
        setBlocked(unknown || [401, 403, 404, 409].includes(result.status)); setNotice({ text: unknown ? "本次预检回执未确认。请只读刷新已保存计划后审阅，不会自动重新预检。" : result.text, unknown });
      }
      return null;
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }
  async function apply(plan: MaintenancePlan, confirmation: boolean) {
    const active = request.current.selection?.plan;
    if (!confirmation || !visible || !current(request.current.generation, owner) || blocked || accounts === null || plans === null || request.current.busy || !active || active.id !== plan.id
      || active.basis.fingerprint !== plan.basis.fingerprint || !maintenanceCanApply(active)) return;
    const ticket = begin(); setPending("apply"); setNotice(null);
    try {
      const client = createMaintenanceClient(window.location.origin), options = { signal: ticket.signal };
      const receipt = await client.apply({ id: plan.id, basis: plan.basis.fingerprint, confirmation: true }, options);
      if (!current(ticket.generation, owner)) return;
      checkMaintenanceReceipt(plan, receipt);
      if (!["queued", "running", "applied"].includes(receipt.state)) throw new MaintenanceReadError("应用回执未确认受理状态。");
      showPlan(receipt);
      const value = await client.plan({ id: plan.id }, options);
      if (!current(ticket.generation, owner)) return;
      checkMaintenanceReceipt(plan, value);
      if (["ready", "blocked"].includes(value.state)) throw new MaintenanceReadError("受理回执与后台当前状态尚未一致。");
      showPlan(value);
      setNotice({ text: maintenanceResultText(value), unknown: false });
    } catch (error) {
      if (current(ticket.generation, owner)) {
        const result = failure(error), unknown = result.status === 0;
        if (unknown) request.current.uncertain = { kind: "apply", plan };
        setBlocked(unknown || [401, 403, 404, 409].includes(result.status)); setNotice({ text: unknown ? "本次确认结果尚未确认。请按已知计划 ID 只读核对；不会自动重新提交。" : result.text, unknown });
      }
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }
  return { accounts, plans, selected, selectionID: visible ? selection?.id ?? null : null, allowed, pending, blocked,
    ready: accounts !== null && plans !== null, notice: visible ? notice : null, error: visible ? state.error : null, load, open, close, preflight, apply };
}
