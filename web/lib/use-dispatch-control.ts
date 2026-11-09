"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createDispatchClient } from "@sub-pool/contracts/client";
import type { DispatchControl, DispatchEvent, DispatchPause } from "@sub-pool/contracts/dispatch";
import { connectionFailure } from "./connection-data";
import { dispatchReasonText, dispatchStatusText, readDispatchHistory } from "./dispatch-control-data";
import { DispatchReadError } from "./dispatch-data";
import { useApp } from "./store";

type Client = ReturnType<typeof createDispatchClient>;
type Snapshot = { control: DispatchControl; events: DispatchEvent[] };
export function useDispatchControl(dataKey: string, allowed: boolean) {
  const { auth, isAdmin, refreshSession } = useApp();
  const owner = `${auth.epoch}:${auth.access?.user.revision ?? ""}:${allowed}`, current = useRef(owner);
  current.current = owner;
  const [refresh, setRefresh] = useState(0), key = `${owner}:${dataKey}:${refresh}`;
  const [state, setState] = useState<{ key: string; data: Snapshot | null; error: string | null }>({ key: "", data: null, error: null });
  const [feedback, setFeedback] = useState({ owner: "", text: "" });
  const [pending, setPending] = useState(""), busy = useRef(false);
  const reload = useCallback(() => setRefresh(value => value + 1), []);
  const authorityRevision = auth.access?.user.revision ?? "";
  const failure = useCallback((error: unknown) => {
    const safe = connectionFailure(error);
    if (error instanceof DispatchReadError && error.refreshIdentity || [401, 403, 404].includes(safe.status)) void refreshSession();
    return error instanceof DispatchReadError ? error.message : safe.status === 0 ? "暂停记录尚不能确认，请重新读取。" : safe.text;
  }, [refreshSession]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setState({ key, data: null, error: null });
    if (allowed) void (async () => {
      try {
        const client = createDispatchClient(window.location.origin);
        const [control, events] = await Promise.all([
          client.control({}, { signal: abort.signal }),
          readDispatchHistory(after => client.history({ account_id: "", after, limit: 100 }, { signal: abort.signal }), authorityRevision),
        ]);
        if (alive && current.current === owner) setState({ key, data: { control, events }, error: null });
      } catch (error) { if (alive && current.current === owner) setState({ key, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, authorityRevision, owner, failure]);
  const visible = allowed && state.key === key;
  const data = visible ? state.data : null;
  useEffect(() => {
    const latest = new Map(data?.events.map(event => [event.pause.id, event.pause.state]));
    if (![...latest.values()].some(value => ["pausing", "paused", "restoring"].includes(value))) return;
    const timer = window.setInterval(() => { if (document.visibilityState === "visible") reload(); }, 5000);
    return () => window.clearInterval(timer);
  }, [data, reload]);
  const perform = async (action: (client: Client) => Promise<DispatchPause>) => {
    if (busy.current || !allowed || !isAdmin || auth.status !== "authenticated" || current.current !== owner) return null;
    busy.current = true; setPending(owner); setFeedback({ owner, text: "操作已提交，正在确认结果…" });
    try {
      const result = await action(createDispatchClient(window.location.origin));
      if (current.current !== owner) return null;
      setFeedback({ owner, text: dispatchReasonText(result) });
      reload(); return result;
    } catch (error) {
      if (current.current === owner) {
        setFeedback({ owner, text: `${failure(error)} 没有自动重发操作，请以重新读取的记录为准。` }); reload();
      }
      return null;
    } finally {
      busy.current = false;
      if (current.current === owner) setPending("");
    }
  };
  return { control: data?.control ?? null, events: data?.events ?? [], ready: data !== null,
    statusText: (accountID?: string) => visible && state.error ? state.error : data ? dispatchStatusText(data.control, data.events, accountID) : "正在读取当前授权范围的暂停记录…",
    error: visible ? state.error : null, message: feedback.owner === owner ? feedback.text : "",
    pending: pending === owner, reload,
    pause: (input: Parameters<Client["pause"]>[0]) => perform(client => client.pause(input)),
    reschedule: (input: Parameters<Client["reschedule"]>[0]) => perform(client => client.reschedule(input)),
    restore: (input: Parameters<Client["restore"]>[0]) => perform(client => client.restore(input)),
    review: (input: Parameters<Client["review"]>[0]) => perform(client => client.review(input)),
  };
}
