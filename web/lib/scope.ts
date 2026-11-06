"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createAccountsClient } from "@sub-pool/contracts/client";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import { useApp } from "@/lib/store";
import type { Participant } from "@/lib/mock";
import { connectionFailure, readManagedPages } from "@/lib/connection-data";

type Catalog = { epoch: number; accounts: (ManagedAccount & { localName: string })[]; revision: string | null; ready: boolean; error: string | null };
const noAccounts: Catalog["accounts"] = [];
const participants: Participant[] = [];

export function useScope() {
  const { isAdmin, auth, refreshSession } = useApp();
  const allowed = auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("accounts"));
  const [catalog, setCatalog] = useState<Catalog>({ epoch: -1, accounts: [], revision: null, ready: false, error: null });
  const request = useRef({ generation: 0, abort: new AbortController(), alive: false });
  const reloadAccounts = useCallback(async () => {
    if (!allowed || !request.current.alive) return;
    request.current.abort.abort(); request.current.abort = new AbortController();
    const generation = ++request.current.generation, signal = request.current.abort.signal;
    const current = () => request.current.alive && request.current.generation === generation;
    setCatalog({ epoch: auth.epoch, accounts: [], revision: null, ready: false, error: null });
    try {
      const client = createAccountsClient(window.location.origin);
      const first = await client.list({ after: "", search: "", limit: 100 }, { signal });
      const accounts = await readManagedPages(after => after ? client.list({ after, search: "", limit: 100 }, { signal }) : Promise.resolve(first), first.connection_revision);
      if (current()) setCatalog({ epoch: auth.epoch, accounts: accounts.map(account => ({ ...account, localName: account.name })), revision: first.connection_revision, ready: true, error: null });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setCatalog({ epoch: auth.epoch, accounts: [], revision: null, ready: false, error: failure.status === 0 ? "暂时无法完整读取账号列表。" : failure.text });
      if (failure.status === 401 || failure.status === 403) void refreshSession();
    }
  }, [allowed, auth.epoch, refreshSession]);
  useEffect(() => {
    request.current.alive = true;
    void reloadAccounts();
    const focus = () => { if (document.visibilityState === "visible") void reloadAccounts(); };
    window.addEventListener("focus", focus);
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); window.removeEventListener("focus", focus); };
  }, [reloadAccounts]);
  const current = allowed && catalog.epoch === auth.epoch;
  return {
    isAdmin, accounts: current ? catalog.accounts : noAccounts, participants,
    connectionRevision: current ? catalog.revision : null,
    accountCatalogAvailable: current && catalog.ready,
    accountCatalogError: current ? catalog.error : allowed ? null : "未授予账号读取权限。",
    reloadAccounts,
    // shortcut: Participant catalogs remain unavailable until BE-007 supplies authorized metadata.
    participantCatalogAvailable: false,
    canAccount: (id: string) => !!auth.access && (auth.access.all_data || auth.access.account_ids.includes(id)),
    canParticipant: (id: string) => !!auth.access && (auth.access.all_data || auth.access.participant_ids.includes(id)),
  };
}
