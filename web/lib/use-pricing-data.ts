"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { requestFailureText } from "./request-id";
import { createConnectionClient, createPricingClient } from "@sub-pool/contracts/client";
import type { PricingChange, PricingControl, PricingOperation, PricingDefault } from "@sub-pool/contracts/pricing";
import { connectionFailure } from "./connection-data";
import { checkPricingReceipt, pricingActionAllowed, pricingOperationText, pricingRequestID, PricingReadError, readPricingDefault, readPricingGroup, readPricingGroups } from "./pricing-data";
import { useApp } from "./store";
import { usePricingHistory } from "./use-pricing-history";

type Targets = { sourceID: string; revision: string; result: Awaited<ReturnType<typeof readPricingGroups>> };
type Selection = Awaited<ReturnType<typeof readPricingGroup>>;
export function usePricingData() {
  const { auth, isAdmin, refreshSession, toast } = useApp();
  const { operations, error: historyError, reload: reloadHistory } = usePricingHistory();
  const allowed = auth.status === "authenticated" && isAdmin, owner = `${auth.epoch}:${allowed}`;
  const [refresh, setRefresh] = useState(0), key = `${owner}:${refresh}`;
  const [targets, setTargets] = useState<{ key: string; data: Targets | null; error: string | null }>({ key: "", data: null, error: null });
  const [selection, setSelection] = useState({ owner: "", id: "" });
  const [detail, setDetail] = useState<{ key: string; data: Selection | null; error: string | null }>({ key: "", data: null, error: null });
  const [control, setControl] = useState<{ key: string; data: PricingControl | null; error: string | null }>({ key: "", data: null, error: null });
  const [defaultTarget, setDefaultTarget] = useState({ key: "", model: "", generation: 0 });
  const [defaults, setDefaults] = useState<{ key: string; data: PricingDefault | null; error: string | null }>({ key: "", data: null, error: null });
  const [busy, setBusy] = useState(false), submitting = useRef(false), current = useRef(key);
  const [notice, setNotice] = useState({ owner: "", text: "" });
  useEffect(() => () => { current.current = ""; }, []);
  const reload = useCallback(() => { setRefresh(value => value + 1); reloadHistory(); }, [reloadHistory]);
  const failure = useCallback((error: unknown) => {
    const safe = connectionFailure(error);
    if ([401, 403, 404].includes(safe.status)) void refreshSession();
    return error instanceof PricingReadError ? error.message : safe.status === 0 ? requestFailureText(safe.text, "计价读取尚未完成，请重新读取；没有使用演示数据。") : safe.text;
  }, [refreshSession]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") setRefresh(value => value + 1); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, []);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setTargets({ key, data: null, error: null });
    if (allowed) void (async () => {
      try {
        const connection = await createConnectionClient(window.location.origin).get({}, { signal: abort.signal });
        if (!connection.source_id || !connection.credential_set) throw new PricingReadError("尚未保存上游连接及管理凭据，请先到连接与采集页面配置。");
        const sourceID = connection.source_id, revision = connection.revision, client = createPricingClient(window.location.origin);
        const result = await readPricingGroups(page => client.groups({ source_id: sourceID, revision, page, search: "" }, { signal: abort.signal }), sourceID, revision);
        if (alive) setTargets({ key, data: { sourceID, revision, result }, error: null });
      } catch (error) { if (alive) setTargets({ key, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [key, allowed, failure]);
  const data = allowed && targets.key === key ? targets.data : null;
  const groups = data?.result.groups ?? [];
  const selectionOwner = data ? `${owner}:${data.sourceID}:${data.revision}` : "";
  const groupID = selection.owner === selectionOwner && groups.some(group => group.id === selection.id) ? selection.id : groups[0]?.id ?? "";
  const detailKey = `${key}:${groupID}`;
  const defaultModel = defaultTarget.key === detailKey ? defaultTarget.model : "", defaultKey = `${detailKey}:${defaultModel}:${defaultTarget.generation}`;
  current.current = detailKey;
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setControl({ key: detailKey, data: null, error: null });
    if (allowed && data && groupID) void (async () => {
      try {
        const result = await createPricingClient(window.location.origin).control({ source_id: data.sourceID, revision: data.revision, group_id: groupID }, { signal: abort.signal });
        if (result.source_id !== data.sourceID || result.connection_revision !== data.revision || result.group_id !== groupID) throw new PricingReadError("写入条件与所选分组不符，请重新读取。");
        if (alive) setControl({ key: detailKey, data: result, error: null });
      } catch (error) { if (alive) setControl({ key: detailKey, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [detailKey, allowed, data, groupID, failure]);
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setDetail({ key: detailKey, data: null, error: null });
    if (allowed && data && groupID) void (async () => {
      try {
        const client = createPricingClient(window.location.origin);
        const result = await readPricingGroup(page => client.group({ source_id: data.sourceID, revision: data.revision, group_id: groupID, page }, { signal: abort.signal }), data.sourceID, data.revision, groupID);
        if (alive) setDetail({ key: detailKey, data: result, error: null });
      } catch (error) { if (alive) setDetail({ key: detailKey, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [detailKey, allowed, data, groupID, failure]);
  const visible = allowed && detail.key === detailKey;
  useEffect(() => {
    let alive = true; const abort = new AbortController();
    setDefaults({ key: defaultKey, data: null, error: null });
    if (allowed && data && defaultModel) void (async () => {
      try {
        const result = await readPricingDefault(() => createPricingClient(window.location.origin).default({ source_id: data.sourceID, revision: data.revision, model: defaultModel }, { signal: abort.signal }), data.sourceID, data.revision, defaultModel);
        if (alive) setDefaults({ key: defaultKey, data: result, error: null });
      } catch (error) { if (alive) setDefaults({ key: defaultKey, data: null, error: failure(error) }); }
    })();
    return () => { alive = false; abort.abort(); };
  }, [defaultKey, defaultModel, allowed, data, failure]);
  const visibleControl = allowed && control.key === detailKey ? control.data : null;
  const noticeOwner = `${owner}:${data?.sourceID ?? ""}:${data?.revision ?? ""}`;
  const perform = async (run: () => Promise<PricingOperation>, expected: Parameters<typeof checkPricingReceipt>[1], success: string) => {
    if (!allowed || submitting.current) return false;
    submitting.current = true; setBusy(true); setNotice({ owner: noticeOwner, text: "" });
    const started = detailKey;
    try {
      const receipt = await run(); checkPricingReceipt(receipt, expected);
      if (current.current === started) {
        const text = pricingOperationText(receipt);
        setNotice({ owner: noticeOwner, text });
        toast({ tone: ["applied", "reverted"].includes(receipt.state) ? "ok" : "warn", title: ["applied", "reverted"].includes(receipt.state) ? success : "请核对计价变更记录", body: text });
      }
    } catch (error) {
      if (current.current === started) { failure(error); setNotice({ owner: noticeOwner, text: "本次操作结果尚未确认，请重新读取变更记录；可能已生效，不要重复提交。" }); }
    } finally {
      submitting.current = false;
      if (current.current !== "") setBusy(false);
      if (current.current === started) reload();
    }
    return true;
  };
  const apply = (changes: PricingChange[], fingerprint: string) => {
    if (!data || !visibleControl?.can_apply || visibleControl.fingerprint !== fingerprint || !changes.length) return Promise.resolve(false);
    const input = { source_id: data.sourceID, revision: data.revision, group_id: groupID, request_id: pricingRequestID(), expected_fingerprint: fingerprint, acknowledge_shared_impact: true as const, changes };
    return perform(() => createPricingClient(window.location.origin).apply(input), { ...input, connection_revision: input.revision }, "计价已写入并读回确认");
  };
  const act = (kind: "revert" | "retry" | "review", operation: PricingOperation) => {
    if (!pricingActionAllowed(kind, operation, data) || !operations?.some(item => item.id === operation.id && item.revision === operation.revision && item.source_id === operation.source_id)) return Promise.resolve(false);
    const client = createPricingClient(window.location.origin), input = { id: operation.id, revision: operation.revision, acknowledge_shared_impact: true as const };
    const requestID = kind === "retry" ? pricingRequestID() : operation.request_id;
    return perform(() => kind === "revert" ? client.revert(input) : kind === "retry" ? client.retry({ ...input, request_id: requestID }) : client.review({ id: operation.id, revision: operation.revision, acknowledge_result_unknown: true }), { ...operation, request_id: requestID }, kind === "revert" ? "计价已撤回并读回确认" : "计价重试已读回确认");
  };
  return { allowed, owner, key, detailKey, groups, groupID, targets: data, detail: visible ? detail.data : null, control: visibleControl, operations, busy, apply, act,
    defaultPrice: allowed && defaults.key === defaultKey && defaultModel ? defaults.data : null,
    defaultError: allowed && defaults.key === defaultKey && defaultModel ? defaults.error : null,
    selectDefault: (model: string) => setDefaultTarget(value => ({ key: detailKey, model, generation: value.generation + 1 })),
    notice: allowed && notice.owner === noticeOwner ? notice.text : "",
    historyError,
    error: allowed && targets.key === key ? targets.error ?? (visible ? detail.error : null) ?? (control.key === detailKey ? control.error : null) : null,
    loading: allowed && (targets.key !== key || !targets.data && !targets.error || !!groupID && (!visible || !detail.data && !detail.error)),
    select: (id: string) => setSelection({ owner: selectionOwner, id }), reload };
}
