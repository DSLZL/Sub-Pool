"use client";

import { useCallback, useEffect, useState } from "react";
import { createConnectionClient, createPricingClient } from "@sub-pool/contracts/client";
import { connectionFailure } from "./connection-data";
import { PricingReadError, readPricingGroup, readPricingGroups } from "./pricing-data";
import { useApp } from "./store";

type Targets = { sourceID: string; revision: string; result: Awaited<ReturnType<typeof readPricingGroups>> };
type Selection = Awaited<ReturnType<typeof readPricingGroup>>;
export function usePricingData() {
  const { auth, isAdmin, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && isAdmin, owner = `${auth.epoch}:${allowed}`;
  const [refresh, setRefresh] = useState(0), key = `${owner}:${refresh}`;
  const [targets, setTargets] = useState<{ key: string; data: Targets | null; error: string | null }>({ key: "", data: null, error: null });
  const [selection, setSelection] = useState({ owner: "", id: "" });
  const [detail, setDetail] = useState<{ key: string; data: Selection | null; error: string | null }>({ key: "", data: null, error: null });
  const reload = useCallback(() => setRefresh(value => value + 1), []);
  const failure = useCallback((error: unknown) => {
    const safe = connectionFailure(error);
    if ([401, 403, 404].includes(safe.status)) void refreshSession();
    return error instanceof PricingReadError ? error.message : safe.status === 0 ? "计价读取尚未完成，请重新读取；没有使用演示数据。" : safe.text;
  }, [refreshSession]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") reload(); };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [reload]);
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
  return { allowed, owner, key, groups, groupID, targets: data, detail: visible ? detail.data : null,
    error: allowed && targets.key === key ? targets.error ?? (visible ? detail.error : null) : null,
    loading: allowed && (targets.key !== key || !targets.data && !targets.error || !!groupID && (!visible || !detail.data && !detail.error)),
    select: (id: string) => setSelection({ owner: selectionOwner, id }), reload };
}
