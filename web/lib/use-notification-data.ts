"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createNotificationsClient } from "@sub-pool/contracts/client";
import type { Notification, NotificationConfiguration } from "@sub-pool/contracts/notifications";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";
import { checkNotificationSave, checkTestReceipt, NotificationReadError, readNotificationPages, recoverNotificationTest, type NotificationFilter, type NotificationSave, type TestIntent } from "./notifications-data";

type Data = { configuration: NotificationConfiguration | null; rows: Notification[]; historyReady: boolean };
type Notice = { text: string; unknown: boolean };
const empty: Data = { configuration: null, rows: [], historyReady: false };

export function useNotificationData(filter: NotificationFilter) {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("notifications"));
  const owner = `${auth.epoch}:${allowed}:${isAdmin}`, actor = auth.session?.user.id;
  const readKey = `${owner}:${filter.kind}:${filter.state}`;
  const [state, setState] = useState<{ owner: string; key: string; data: Data; ready: boolean; error: string | null }>({ owner: "", key: "", data: empty, ready: false, error: null });
  const [notice, setNotice] = useState<Notice | null>(null), [pending, setPending] = useState<string | null>(null), [blocked, setBlocked] = useState(false);
  const [test, setTest] = useState<{ owner: string; row: Notification } | null>(null);
  const intent = useRef<{ owner: string; value: TestIntent } | null>(null);
  const request = useRef({ alive: false, generation: 0, busy: false, abort: new AbortController() });
  const live = useRef({ owner, allowed, isAdmin }); live.current = { owner, allowed, isAdmin };
  const current = useCallback((generation: number, readOwner: string) => request.current.alive && request.current.generation === generation && live.current.owner === readOwner && live.current.allowed, []);
  const begin = useCallback(() => {
    request.current.abort.abort(); request.current.abort = new AbortController();
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const failure = useCallback((error: unknown) => {
    const result = connectionFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return { ...result, text: error instanceof NotificationReadError ? error.message : result.text };
  }, [refreshSession]);
  const load = useCallback(async () => {
    if (request.current.busy || !allowed) return;
    const ticket = begin(); setPending("read"); setTest(null); setState({ owner, key: readKey, data: empty, ready: false, error: null });
    try {
      const client = createNotificationsClient(window.location.origin);
      const [configResult, rowsResult] = await Promise.allSettled([
        isAdmin ? client.configuration({}, { signal: ticket.signal }) : Promise.resolve(null),
        readNotificationPages(client, isAdmin, { kind: filter.kind, state: filter.state }, ticket.signal),
      ]);
      if (!current(ticket.generation, owner)) return;
      const configuration = configResult.status === "fulfilled" ? configResult.value : null;
      const rows = rowsResult.status === "fulfilled" ? rowsResult.value : [];
      const errors = [configResult, rowsResult].flatMap(result => {
        if (result.status === "fulfilled") return [];
        const problem = failure(result.reason);
        return [problem.status === 0 && !(result.reason instanceof NotificationReadError) ? "通知读取未完成；点击当前标签可重新读取。记录失败时不展示部分结果。" : problem.text];
      });
      setState({ owner, key: readKey, data: { configuration, rows, historyReady: rowsResult.status === "fulfilled" }, ready: isAdmin ? configuration !== null : rowsResult.status === "fulfilled", error: errors.join(" ") || null }); setBlocked(false);
      // A refreshed page resumes an already queued test by reading its receipt, never by sending again.
      const queued = rows.findLast(row => row.kind === "test" && row.details?.actor_id === actor && ["queued", "dispatching"].includes(row.state));
      if (queued?.details && (!intent.current || intent.current.owner !== owner)) {
        intent.current = { owner, value: { id: queued.id, request_id: queued.details.event_key.slice(5), revision: queued.details.config_revision, actor_id: queued.details.actor_id ?? "" } };
        setTest({ owner, row: queued });
      }
    } catch (error) {
      if (!current(ticket.generation, owner)) return;
      const result = failure(error);
      setState({ owner, key: readKey, data: empty, ready: false, error: result.status === 0 && !(error instanceof NotificationReadError) ? "通知尚未完整读取；请点击当前标签重新读取，没有展示部分或演示记录。" : result.text });
    } finally { if (current(ticket.generation, owner)) setPending(null); }
  }, [allowed, owner, readKey, isAdmin, actor, filter.kind, filter.state, begin, current, failure]);
  useEffect(() => {
    request.current.alive = true;
    void load();
    const focus = () => { if (document.visibilityState === "visible") void load(); };
    window.addEventListener("focus", focus);
    return () => { request.current.alive = false; request.current.generation++; request.current.busy = false; request.current.abort.abort(); window.removeEventListener("focus", focus); };
  }, [load]);

  const visible = allowed && state.owner === owner && state.key === readKey, data = visible ? state.data : empty;
  async function save(input: NotificationSave) {
    if (!visible || !state.ready || !isAdmin || blocked || request.current.busy || intent.current?.owner === owner) return;
    request.current.busy = true;
    const ticket = begin(); setPending("save"); setNotice(null);
    try {
      const value = await createNotificationsClient(window.location.origin).save(input, { signal: ticket.signal });
      checkNotificationSave(value, input);
      if (!current(ticket.generation, owner) || !live.current.isAdmin) return;
      setState(previous => ({ ...previous, data: { ...previous.data, configuration: value } }));
      setNotice({ text: "配置已保存。保存不检验渠道可达性，测试发送需要单独操作。", unknown: false });
    } catch (error) {
      if (!current(ticket.generation, owner)) return;
      const result = failure(error), unknown = result.status === 0;
      setNotice({ text: unknown ? "保存结果尚未确认。点击当前标签只读取配置；读取不能证明刚才的凭据保存结果，请核对后修改再保存，勿重复提交。" : result.text, unknown });
      setBlocked(unknown || [404, 409].includes(result.status));
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }

  async function runTest() {
    if (!visible || !state.ready || !data.historyReady || !isAdmin || !actor || request.current.busy || blocked || !data.configuration) return;
    const recovering = intent.current?.owner === owner ? intent.current.value : null;
    const key = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16)))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
    let decision: TestIntent = recovering ?? { request_id: key, revision: data.configuration.revision, actor_id: actor };
    intent.current = { owner, value: decision }; request.current.busy = true;
    const ticket = begin(); setPending("test"); setNotice(null); setTest(null);
    try {
      const client = createNotificationsClient(window.location.origin);
      let row = recovering ? await recoverNotificationTest(client, decision, ticket.signal)
        : checkTestReceipt(await client.test({ request_id: decision.request_id, revision: decision.revision }, { signal: ticket.signal }), decision);
      if (!current(ticket.generation, owner)) return;
      if (!row) {
        setNotice({ text: "尚未查到本次测试记录，结果仍未确认。测试按钮只会继续读取核对，不会再次发送。", unknown: true });
        return;
      }
      decision = { ...decision, id: row.id }; intent.current = { owner, value: decision }; setTest({ owner, row });
      for (let index = 0; index < 40 && ["queued", "dispatching"].includes(row.state); index++) {
        await new Promise(resolve => setTimeout(resolve, 250));
        if (!current(ticket.generation, owner)) return;
        row = checkTestReceipt(await client.get({ id: row.id }, { signal: ticket.signal }), decision);
        if (current(ticket.generation, owner)) setTest({ owner, row });
      }
      if (!current(ticket.generation, owner)) return;
      if (!["queued", "dispatching"].includes(row.state)) intent.current = null;
      const receipt = row;
      setState(previous => ({ ...previous, data: { ...previous.data, rows: [...previous.data.rows.filter(item => item.id !== receipt.id),
        ...((!filter.kind || receipt.kind === filter.kind) && (!filter.state || receipt.state === filter.state) ? [receipt] : [])].sort((a, b) => BigInt(a.id) < BigInt(b.id) ? -1 : 1) } }));
    } catch (error) {
      if (!current(ticket.generation, owner)) return;
      const result = failure(error);
      if (!recovering && !decision.id && result.status !== 0) intent.current = null;
      const unknown = result.status === 0 || !!recovering || !!decision.id;
      setNotice({ text: unknown ? "测试结果尚未确认。测试按钮只读取原记录核对，不会自动重发。" + result.text : result.text, unknown });
      setBlocked([404, 409].includes(result.status));
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }
  return {
    ...data, isAdmin, allowed, ready: visible && state.ready, pending: visible ? pending : null, blocked,
    error: visible ? state.error : null, notice: visible ? notice : null,
    test: visible && test?.owner === owner ? test.row : intent.current?.owner === owner ? null : data.rows.findLast(row => row.kind === "test") ?? null,
    unresolvedTest: intent.current?.owner === owner, reload: load, save, runTest,
    inputError: () => setNotice({ text: "请核对邮箱、端口、凭据和设置内容；本次尚未提交。", unknown: false }),
  };
}
