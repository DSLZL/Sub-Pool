"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createAPIKeysClient } from "@sub-pool/contracts/client";
import { apiKey, type APIKey, type APICatalog, type APIKeyIssuance } from "@sub-pool/contracts/api-keys";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";
import { APIKeyReadError, checkAPIKeyCatalog, checkAPIKeyReceipt, readAPIKeyPages, type KeyMutation } from "./api-keys-data";

type Data = { keys: APIKey[]; keysReady: boolean; catalog: APICatalog | null };
type Notice = { text: string; needsReview: boolean };
const empty: Data = { keys: [], keysReady: false, catalog: null };

/** Lost issuance is unrecoverable; keep writes and one-time receipts bound to a live auth epoch. */
export function useAPIKeysData() {
  const { auth, refreshSession } = useApp();
  const access = auth.access, allowed = auth.status === "authenticated" && access !== null;
  const owner = auth.epoch, admin = access?.all_data === true;
  const accessRef = useRef(access); accessRef.current = access;
  const [state, setState] = useState<{ owner: number; data: Data; error: string | null }>({ owner, data: empty, error: null });
  const [pending, setPending] = useState<"read" | KeyMutation["kind"] | null>(null);
  const [blocked, setBlocked] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);
  const request = useRef({ alive: true, generation: 0, busy: false, abort: new AbortController() });
  const live = useRef({ owner, allowed, admin }); live.current = { owner, allowed, admin };
  const current = useCallback((generation: number, actor: number) => request.current.alive
    && request.current.generation === generation && live.current.owner === actor && live.current.allowed, []);
  const begin = useCallback(() => {
    request.current.abort.abort(); request.current.abort = new AbortController(); request.current.busy = true;
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const failure = useCallback((error: unknown) => {
    const result = connectionFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return { ...result, text: error instanceof APIKeyReadError ? error.message : result.text };
  }, [refreshSession]);

  const load = useCallback(async () => {
    const expected = accessRef.current;
    if (!allowed || !expected || request.current.busy) return;
    const ticket = begin(); setPending("read"); setState({ owner, data: empty, error: null });
    try {
      const client = createAPIKeysClient(window.location.origin);
      const [rows, catalog] = await Promise.allSettled([
        readAPIKeyPages(client, ticket.signal),
        client.catalog({}, { signal: ticket.signal }).then(value => checkAPIKeyCatalog(value, expected)),
      ]);
      if (!current(ticket.generation, owner)) return;
      const errors = [rows, catalog].flatMap(result => result.status === "rejected" ? [failure(result.reason).text] : []);
      if (catalog.status === "rejected" && catalog.reason instanceof APIKeyReadError) void refreshSession();
      setState({ owner, data: { keys: rows.status === "fulfilled" ? rows.value : [], keysReady: rows.status === "fulfilled",
        catalog: catalog.status === "fulfilled" ? catalog.value : null }, error: errors.length ? errors.join(" ") : null });
      if (rows.status === "fulfilled" && catalog.status === "fulfilled") setBlocked(false);
    } catch (error) {
      if (current(ticket.generation, owner)) setState({ owner, data: empty, error: failure(error).text });
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }, [allowed, owner, begin, current, failure, refreshSession]);
  useEffect(() => {
    request.current.alive = true; void load();
    return () => { request.current.alive = false; request.current.generation++; request.current.busy = false; request.current.abort.abort(); };
  }, [load]);

  const visible = allowed && state.owner === owner, data = visible ? state.data : empty;
  const writable = visible && data.keysReady && data.catalog !== null && !blocked && pending === null;
  async function mutate(input: KeyMutation): Promise<APIKey | APIKeyIssuance | null> {
    if (!writable || request.current.busy || !live.current.allowed || live.current.owner !== owner
      || (input.kind === "create" ? input.mode === "management" : input.key.mode === "management" && input.kind === "rotate") && !live.current.admin) return null;
    if (input.kind === "create" && !apiKey.shape.name.safeParse(input.name).success) {
      setNotice({ text: "名称须为 1 至 80 个字符、最多 256 个 UTF-8 字节，不含控制字符或首尾空白。", needsReview: false }); return null;
    }
    if (input.kind !== "create" && !data.keys.some(key => key.id === input.key.id && key.revision === input.key.revision && key.revoked_at === null)) return null;
    const ticket = begin(); setPending(input.kind); setNotice(null);
    try {
      const client = createAPIKeysClient(window.location.origin);
      const value = input.kind === "create" ? await client.create({ name: input.name, mode: input.mode }, { signal: ticket.signal })
        : input.kind === "rotate" ? await client.rotate({ id: input.key.id, revision: input.key.revision }, { signal: ticket.signal })
        : await client.revoke({ id: input.key.id, revision: input.key.revision }, { signal: ticket.signal });
      if (!current(ticket.generation, owner)) return null;
      checkAPIKeyReceipt(input, value);
      const key = "key" in value ? value.key : value;
      setState(previous => ({ ...previous, data: { ...previous.data, keys: input.kind === "create"
        ? [key, ...previous.data.keys] : previous.data.keys.map(row => row.id === key.id ? key : row) } }));
      setNotice({ text: input.kind === "revoke" ? "Key 已撤销，后续请求会被拒绝。" : "完整 Key 只在本次回执中显示；关闭后无法找回。", needsReview: false });
      return value;
    } catch (error) {
      if (!current(ticket.generation, owner)) return null;
      const result = failure(error), unknown = result.status === 0;
      setNotice({ text: unknown ? "本次操作结果尚未确认。点击当前标签只回读元数据，不会重新提交；完整 Key 无法找回，请核对列表并主动撤销或轮换选中的记录。" : result.text, needsReview: unknown });
      setBlocked(unknown || [401, 403, 404, 409].includes(result.status)); return null;
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }
  function discardSecret() {
    setBlocked(true); setNotice({ text: "服务端已确认新 Key，但本次展示已关闭，完整 Key 已丢弃。请点击当前标签回读后主动撤销或轮换对应记录。", needsReview: true });
  }
  return { ...data, pending, writable, blocked, notice: visible ? notice : null, error: visible ? state.error : null, load, mutate, discardSecret, clearNotice: () => { if (!blocked) setNotice(null); } };
}
