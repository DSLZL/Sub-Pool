"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import type { TemporaryStatus } from "@sub-pool/contracts/temporary";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";
import { readTemporaryHistory, temporarySame, checkTemporaryDetails, buildTemporaryStart, checkTemporaryStart, checkTemporaryStop, checkTemporaryRights, buildTemporaryRights, checkTemporaryRightsReceipt, checkTemporaryReminders, TemporaryReadError,
  type TemporaryDetails, type TemporaryHistoryRow, type TemporaryPreflight, type TemporaryRights, type TemporaryStart, type TemporaryReminderInput } from "./temporary-actions";

type Read = { owner: string; status: TemporaryStatus | null; history: TemporaryHistoryRow[] | null; details: TemporaryDetails | null; error: string | null };
type Kind = "start" | "stop" | "rights" | "reminders";
export function useTemporaryControls(editing: boolean) {
  const { auth, isAdmin, refreshSession, timezone } = useApp();
  const allowed = auth.status === "authenticated" && isAdmin, owner = `${auth.epoch}:${allowed}`, actor = auth.access?.user.id;
  const [state, setState] = useState<Read>({ owner, status: null, history: null, details: null, error: null });
  const [preflight, setPreflight] = useState<{ owner: string; value: TemporaryPreflight } | null>(null);
  const [rights, setRights] = useState<{ owner: string; value: TemporaryRights; participant: string } | null>(null);
  const [pending, setPending] = useState<string | null>(null), [notice, setNotice] = useState<{ owner: string; text: string; unknown: boolean } | null>(null);
  const [blocked, setBlocked] = useState(false);
  const live = useRef({ owner, allowed, editing, rightsOpen: rights !== null }); live.current = { owner, allowed, editing, rightsOpen: rights !== null };
  const mounted = useRef(false), selected = useRef(""), rightsTicket = useRef(0), reader = useRef({ ticket: 0, abort: new AbortController() });
  const writer = useRef<{ owner: string; kind: Kind; abort: AbortController } | null>(null), busy = useRef<string | null>(null), uncertain = useRef(false);
  const current = useCallback(() => mounted.current && live.current.allowed && live.current.owner === owner, [owner]);
  const failure = useCallback((error: unknown) => {
    const result = connectionFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return { ...result, text: error instanceof TemporaryReadError ? error.message : result.text };
  }, [refreshSession]);
  const beginRead = useCallback(() => {
    reader.current.abort.abort(); reader.current = { ticket: reader.current.ticket + 1, abort: new AbortController() };
    return reader.current;
  }, []);
  const load = useCallback(async (wanted = selected.current) => {
    if (!allowed || !current() || writer.current) return false;
    const ticket = beginRead(), valid = () => current() && reader.current.ticket === ticket.ticket;
    busy.current = "read"; setPending("read"); setPreflight(null); setRights(null);
    setState(previous => previous.owner === owner ? { ...previous, details: null, error: null } : { owner, status: null, history: null, details: null, error: null });
    try {
      const client = createTemporaryClient(window.location.origin), options = { signal: ticket.abort.signal };
      const status = await client.status({}, options);
      let history: TemporaryHistoryRow[] | null = null, details: TemporaryDetails | null = null;
      if (status.state !== "unavailable") {
        history = await readTemporaryHistory(client, ticket.abort.signal);
        if (status.round && !history.some(row => row.id === status.round?.id)) throw new TemporaryReadError("最近轮次与完整历史不一致。");
        const id = wanted || status.round?.id || history[0]?.id || "";
        if (id) {
          const summary = history.find(row => row.id === id);
          if (!summary) throw new TemporaryReadError("所选轮次不在本次完整历史中。");
          details = await client.round({ round_id: id }, options); checkTemporaryDetails(details, summary);
          if (status.round?.id === id) checkTemporaryDetails(details, status.round);
        }
      }
      if (!valid()) return false;
      selected.current = details?.round.id ?? "";
      setState({ owner, status, history, details, error: null }); setBlocked(false);
      if (uncertain.current) setNotice({ owner, unknown: true, text: "已重新读取当前持久化状态；此前写入回执仍未确认，未自动重放。请核对轮次或重新读取当前权益，再作新的明确操作。" });
      return true;
    } catch (error) {
      if (valid()) { setState({ owner, status: null, history: null, details: null, error: failure(error).text }); setBlocked(true); }
      return false;
    } finally {
      if (valid()) { busy.current = null; setPending(null); }
    }
  }, [allowed, owner, current, beginRead, failure]);
  useEffect(() => {
    mounted.current = true; selected.current = ""; busy.current = null; uncertain.current = false;
    setNotice(null); setPreflight(null); setRights(null); setBlocked(false); void load("");
    return () => { mounted.current = false; reader.current.ticket++; reader.current.abort.abort(); writer.current?.abort.abort(); writer.current = null; busy.current = null; };
  }, [load]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible" && !busy.current && !live.current.editing && !live.current.rightsOpen) void load(); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [load]);
  const visible = allowed && state.owner === owner, status = visible ? state.status : null, details = visible ? state.details : null;
  const canWrite = visible && status !== null && status.state !== "unavailable" && !blocked && pending === null;
  useEffect(() => {
    if (!visible || status?.state !== "active" || !status.round || pending !== null) return;
    const timer = window.setTimeout(() => void load(), Math.min(2147483647, Math.max(0, Date.parse(status.round.deadline) - Date.now() + 1)));
    return () => window.clearTimeout(timer);
  }, [visible, status, pending, load]);
  async function select(id: string) {
    if (!current() || writer.current) return;
    selected.current = id; setRights(null); setPreflight(null); setNotice(previous => previous?.unknown ? previous : null);
    await load(id);
  }
  async function inspect() {
    if (!canWrite || busy.current || !current()) return null;
    const ticket = beginRead(), valid = () => current() && ticket.ticket === reader.current.ticket;
    busy.current = "preflight"; setPending("preflight"); setPreflight(null);
    try {
      const value = await createTemporaryClient(window.location.origin).preflight({}, { signal: ticket.abort.signal });
      if (!valid()) return null;
      setPreflight({ owner, value }); return value;
    } catch (error) {
      if (valid()) { setNotice({ owner, text: failure(error).text, unknown: false }); setBlocked(true); }
      return null;
    } finally { if (valid()) { busy.current = null; setPending(null); } }
  }
  async function openRights(accountID: string, participant: string) {
    if (!canWrite || !details || status?.state === "active" || busy.current || !current()) return;
    const round = details.round, serial = ++rightsTicket.current, ticket = beginRead(), valid = () => current() && serial === rightsTicket.current && ticket.ticket === reader.current.ticket && selected.current === round.id;
    busy.current = "rights-read"; setPending("rights-read"); setRights(null);
    try {
      const value = await createTemporaryClient(window.location.origin).rights({ round_id: round.id, account_id: accountID }, { signal: ticket.abort.signal });
      checkTemporaryRights(value, round, accountID);
      if (!round.scope.accounts.find(account => account.account_id === accountID)?.members.some(member => member.participant_id === participant)) throw new TemporaryReadError("成员不在所选冻结范围。");
      if (valid()) setRights({ owner, value, participant });
    } catch (error) { if (valid()) { setNotice({ owner, text: failure(error).text, unknown: false }); setBlocked(true); } }
    finally { if (current() && ticket.ticket === reader.current.ticket) { busy.current = null; setPending(null); } }
  }
  async function mutate(kind: Kind, action: (client: ReturnType<typeof createTemporaryClient>, signal: AbortSignal, sent: () => void) => Promise<void>) {
    if (!canWrite || busy.current || writer.current || !current()) return false;
    const write = { owner, kind, abort: new AbortController() }; writer.current = write; busy.current = kind;
    reader.current.ticket++; reader.current.abort.abort(); setPending(kind); setNotice(null); setPreflight(null);
    let dispatched = false, confirmed = false;
    try {
      await action(createTemporaryClient(window.location.origin), write.abort.signal, () => { dispatched = true; });
      if (!current() || writer.current !== write) return false;
      confirmed = true; uncertain.current = false;
      setNotice({ owner, text: "本次操作回执已核对；正在只读读取当前轮次和权益处理状态。", unknown: false });
    } catch (error) {
      if (current() && writer.current === write) {
        const result = failure(error), unknown = dispatched && (result.status === 0 || error instanceof TemporaryReadError);
        uncertain.current = uncertain.current || unknown;
        setBlocked(unknown || error instanceof TemporaryReadError || [401, 403, 404, 409].includes(result.status));
        setNotice({ owner, unknown, text: unknown ? "本次操作已派发，回执未确认，结果未知。请只读重新读取持久化记录，不会自动重放。" : result.text });
      }
    } finally {
      if (writer.current === write) { writer.current = null; busy.current = null; if (current()) setPending(null); }
    }
    if (confirmed && current()) await load();
    return confirmed;
  }
  async function start(input: TemporaryStart, expected: TemporaryPreflight) {
    if (!actor || expected.state !== "ready" || !preflight || preflight.owner !== owner || !temporarySame(expected, preflight.value)) return false;
    return mutate("start", async (client, signal, sent) => {
      const fresh = await client.preflight({}, { signal });
      if (!temporarySame(fresh, expected)) throw new TemporaryReadError("启用依据已变化，请重新核验全部范围。");
      buildTemporaryStart(fresh, input);
      sent(); const value = await client.start(input, { signal }); checkTemporaryStart(input, expected.scope, actor, value);
      if (current()) selected.current = value.id;
    });
  }
  async function stop() {
    if (!details || details.round.exit) return false;
    const before = details.round;
    return mutate("stop", async (client, signal, sent) => {
      const fresh = await client.round({ round_id: before.id }, { signal }); checkTemporaryDetails(fresh, before);
      sent(); const value = await client.stop({ round_id: before.id }, { signal }); checkTemporaryStop(before, value);
      if (value.exit?.reason === "terminated" && value.exit.actor_id !== actor) throw new TemporaryReadError("终止回执的操作人不一致。");
    });
  }
  async function reminders(input: TemporaryReminderInput) {
    if (!details || input.round_id !== details.round.id || input.revision !== details.reminders.revision) return false;
    const before = details;
    return mutate("reminders", async (client, signal, sent) => {
      const fresh = await client.round({ round_id: before.round.id }, { signal }); checkTemporaryDetails(fresh, before.round);
      if (!temporarySame(fresh.reminders, before.reminders)) throw new TemporaryReadError("本轮提醒版本已变化，请重新读取。");
      sent(); const value = await client.reminders(input, { signal }); checkTemporaryReminders(input, value);
    });
  }
  async function changeRights(after: string, reason: string, requestID: string) {
    if (!details || !rights || status?.state === "active" || rights.owner !== owner || rights.value.round_id !== details.round.id) return false;
    const before = rights.value, round = details.round, participant = rights.participant;
    return mutate("rights", async (client, signal, sent) => {
      const fresh = await client.rights({ round_id: round.id, account_id: before.account_id }, { signal }); checkTemporaryRights(fresh, round, before.account_id);
      if (!temporarySame(fresh, before)) throw new TemporaryReadError("当前权益版本、周期或金额已变化，请重新读取。");
      const input = buildTemporaryRights(round, fresh, participant, after, reason, requestID);
      sent(); const value = await client.rightsChange(input, { signal }); checkTemporaryRightsReceipt(input, fresh, value);
    });
  }
  return { allowed, owner, timezone, status, selectedID: visible ? selected.current : "", history: visible ? state.history : null, details, error: visible ? state.error : null, pending, canWrite,
    notice: notice?.owner === owner && allowed ? notice : null, preflight: preflight?.owner === owner && allowed ? preflight.value : null,
    rights: rights?.owner === owner && allowed && rights.value.round_id === details?.round.id ? rights : null,
    reload: () => load(), select, inspect, start, stop, reminders, openRights, changeRights, closeRights: () => { rightsTicket.current++; setRights(null); } };
}
