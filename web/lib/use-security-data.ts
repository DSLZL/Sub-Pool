"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createSecurityClient } from "@sub-pool/contracts/client";
import { sourceRule, type ManagementAuditPage, type RequestSource, type SecurityConfig, type SourceRule } from "@sub-pool/contracts/security";
import { useApp } from "./store";
import { canAccess } from "./access";
import { checkConfigReceipt, checkSourceReceipt, readSecurityLogins, readSecurityManagement, readSecuritySources, securityFailure, securityRange, type SecurityChange, type SecurityRange } from "./security-data";

type Data = { range: SecurityRange | null; login: Awaited<ReturnType<typeof readSecurityLogins>> | null; rules: SourceRule[] | null;
  source: RequestSource | null; management: ManagementAuditPage["events"] | null; config: SecurityConfig | null };
type Notice = { text: string; needsReview: boolean };
const empty: Data = { range: null, login: null, rules: null, source: null, management: null, config: null };

export function useSecurityData() {
  const { auth, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && canAccess("security", auth.access), admin = auth.access?.all_data === true;
  const owner = auth.epoch, actor = auth.access?.user.id;
  const [state, setState] = useState<{ owner: number; data: Data; error: string | null }>({ owner, data: empty, error: null });
  const [pending, setPending] = useState<"read" | SecurityChange["kind"] | null>(null);
  const [blocked, setBlocked] = useState(false), [notice, setNotice] = useState<Notice | null>(null);
  const request = useRef({ alive: true, generation: 0, busy: false, abort: new AbortController() });
  const live = useRef({ owner, allowed, admin }); live.current = { owner, allowed, admin };
  const current = useCallback((generation: number, epoch: number) => request.current.alive && request.current.generation === generation
    && live.current.owner === epoch && live.current.allowed, []);
  const begin = useCallback(() => {
    request.current.abort.abort(); request.current.abort = new AbortController(); request.current.busy = true;
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const failure = useCallback((error: unknown) => {
    const result = securityFailure(error);
    if ([401, 403, 404].includes(result.status)) void refreshSession();
    return result;
  }, [refreshSession]);
  const load = useCallback(async () => {
    if (!allowed || request.current.busy) return;
    const ticket = begin(), range = securityRange(); setPending("read"); setState({ owner, data: empty, error: null });
    try {
      const client = createSecurityClient(window.location.origin), options = { signal: ticket.signal };
      const [login, rules, source, management, config] = await Promise.allSettled([
        readSecurityLogins(client, range, ticket.signal), readSecuritySources(client, ticket.signal), client.source({}, options),
        admin ? readSecurityManagement(client, range, ticket.signal) : Promise.resolve(null),
        admin ? client.config({}, options) : Promise.resolve(null),
      ]);
      if (!current(ticket.generation, owner)) return;
      const labels = ["登录审计", "来源规则", "当前来源", "管理审计", "诊断策略"];
      const errors = [login, rules, source, management, config].flatMap((result, index) => result.status === "rejected" ? [`${labels[index]}：${failure(result.reason).text}`] : []);
      setState({ owner, data: { range, login: login.status === "fulfilled" ? login.value : null, rules: rules.status === "fulfilled" ? rules.value : null,
        source: source.status === "fulfilled" ? source.value : null, management: management.status === "fulfilled" ? management.value : null,
        config: config.status === "fulfilled" ? config.value : null }, error: errors.length ? errors.join(" ") : null });
      if (rules.status === "fulfilled" && source.status === "fulfilled" && config.status === "fulfilled") setBlocked(false);
    } catch (error) {
      if (current(ticket.generation, owner)) setState({ owner, data: empty, error: failure(error).text });
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }, [allowed, admin, owner, begin, current, failure]);
  useEffect(() => {
    request.current.alive = true; void load();
    return () => { request.current.alive = false; request.current.generation++; request.current.busy = false; request.current.abort.abort(); };
  }, [load]);
  const visible = allowed && state.owner === owner, data = visible ? state.data : empty;
  const writable = visible && admin && !blocked && pending === null;
  const canSourceWrite = writable && data.rules !== null, canConfigWrite = writable && data.config !== null;
  const canBlock = canSourceWrite && data.source?.ip != null && data.source.peer_ip !== null;
  async function mutate(change: SecurityChange) {
    if (!actor || request.current.busy || !live.current.admin || !live.current.allowed || live.current.owner !== owner
      || (change.kind === "source" ? !canSourceWrite || change.blocked && !canBlock : !canConfigWrite)) return false;
    if (change.kind === "source") {
      if (!sourceRule.shape.ip.safeParse(change.ip).success) { setNotice({ text: "请输入精确 IPv4 或 IPv6，不支持网段；IPv6 请使用规范写法。", needsReview: false }); return false; }
      const saved = data.rules?.find(rule => rule.ip === change.ip);
      if ((saved?.revision ?? "0") !== change.revision || (saved?.blocked ?? false) === change.blocked) return false;
    } else if (data.config?.revision !== change.revision || data.config.network_diagnostics_enabled === change.network_diagnostics_enabled) return false;
    const ticket = begin(); setPending(change.kind); setNotice(null);
    try {
      const client = createSecurityClient(window.location.origin);
      if (change.kind === "source") {
        const { ip, revision, blocked } = change, rule = await client.updateSource({ ip, revision, blocked }, { signal: ticket.signal });
        if (!current(ticket.generation, owner)) return false;
        checkSourceReceipt(change, rule, actor);
        setState(previous => ({ ...previous, data: { ...previous.data, rules: [...(previous.data.rules ?? []).filter(row => row.ip !== rule.ip), rule] } }));
      } else {
        const { revision, network_diagnostics_enabled } = change, config = await client.updateConfig({ revision, network_diagnostics_enabled }, { signal: ticket.signal });
        if (!current(ticket.generation, owner)) return false;
        checkConfigReceipt(change, config);
        setState(previous => ({ ...previous, data: { ...previous.data, config } }));
      }
      setNotice({ text: change.kind === "source" ? "来源规则已保存。后续新请求按服务端规则检查；点击标签可只读刷新核对。" : "诊断策略已保存，不影响正常登录。", needsReview: false });
      return true;
    } catch (error) {
      if (!current(ticket.generation, owner)) return false;
      const result = failure(error), unknown = result.status === 0;
      setNotice({ text: unknown ? "本次操作结果尚未确认。点击当前标签只读刷新来源规则或诊断策略后核对；不会自动重新提交。" : result.text, needsReview: unknown });
      setBlocked(unknown || [401, 403, 404, 409].includes(result.status)); return false;
    } finally { if (current(ticket.generation, owner)) { request.current.busy = false; setPending(null); } }
  }
  return { ...data, pending, canSourceWrite, canConfigWrite, canBlock, blocked, notice: visible ? notice : null,
    error: visible ? state.error : null, load, mutate, clearNotice: () => { if (!blocked) setNotice(null); } };
}
