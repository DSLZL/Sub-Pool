"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { requestFailureText } from "./request-id";
import { createAccountsClient, createParticipantsClient } from "@sub-pool/contracts/client";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { Participant as RecordedParticipant } from "@sub-pool/contracts/participants";
import { useApp } from "@/lib/store";
import { connectionFailure, readManagedPages } from "@/lib/connection-data";
import { readParticipantPages } from "@/lib/pools-data";

type Catalog = { epoch: number; accounts: (ManagedAccount & { localName: string })[]; revision: string | null; ready: boolean; error: string | null };
const noAccounts: Catalog["accounts"] = [];
const noParticipants: RecordedParticipant[] = [];

export function useScope(active = true) {
  const { isAdmin, auth, refreshSession } = useApp();
  const allowed = active && auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("accounts"));
  const membersAllowed = active && auth.status === "authenticated" && (isAdmin || !!auth.access?.features.includes("pools"));
  const [catalog, setCatalog] = useState<Catalog>({ epoch: -1, accounts: [], revision: null, ready: false, error: null });
  const [memberCatalog, setMemberCatalog] = useState<{ epoch: number; items: RecordedParticipant[]; ready: boolean; error: string | null }>({ epoch: -1, items: [], ready: false, error: null });
  const request = useRef({ generation: 0, abort: new AbortController(), alive: false });
  const memberRequest = useRef({ generation: 0, abort: new AbortController(), alive: false });
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
      setCatalog({ epoch: auth.epoch, accounts: [], revision: null, ready: false, error: failure.status === 0 ? requestFailureText(failure.text, "账号读取不齐。") : failure.text });
      if (failure.status === 401 || failure.status === 403) void refreshSession();
    }
  }, [allowed, auth.epoch, refreshSession]);
  const reloadParticipants = useCallback(async () => {
    if (!membersAllowed || !memberRequest.current.alive) return;
    memberRequest.current.abort.abort(); memberRequest.current.abort = new AbortController();
    const generation = ++memberRequest.current.generation, signal = memberRequest.current.abort.signal;
    const current = () => memberRequest.current.alive && memberRequest.current.generation === generation;
    setMemberCatalog({ epoch: auth.epoch, items: [], ready: false, error: null });
    try {
      const client = createParticipantsClient(window.location.origin);
      const items = await readParticipantPages(after => client.list({ after, search: "", limit: 100 }, { signal }));
      if (current()) setMemberCatalog({ epoch: auth.epoch, items, ready: true, error: null });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setMemberCatalog({ epoch: auth.epoch, items: [], ready: false, error: failure.status === 0 ? requestFailureText(failure.text, "参与者读取不全。") : failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    }
  }, [membersAllowed, auth.epoch, refreshSession]);
  useEffect(() => {
    request.current.alive = true;
    void reloadAccounts();
    const focus = () => { if (document.visibilityState === "visible") void reloadAccounts(); };
    window.addEventListener("focus", focus);
    return () => { request.current.alive = false; request.current.generation++; request.current.abort.abort(); window.removeEventListener("focus", focus); };
  }, [reloadAccounts]);
  useEffect(() => {
    memberRequest.current.alive = true;
    void reloadParticipants();
    const focus = () => { if (document.visibilityState === "visible") void reloadParticipants(); };
    window.addEventListener("focus", focus);
    return () => { memberRequest.current.alive = false; memberRequest.current.generation++; memberRequest.current.abort.abort(); window.removeEventListener("focus", focus); };
  }, [reloadParticipants]);
  const current = allowed && catalog.epoch === auth.epoch;
  const currentMembers = membersAllowed && memberCatalog.epoch === auth.epoch;
  return {
    isAdmin, accounts: current ? catalog.accounts : noAccounts,
    connectionRevision: current ? catalog.revision : null,
    accountCatalogAvailable: current && catalog.ready,
    accountCatalogError: current ? catalog.error : allowed ? null : "无账号权限。",
    reloadAccounts,
    participantCatalog: currentMembers ? memberCatalog.items : noParticipants,
    participantCatalogAvailable: currentMembers && memberCatalog.ready,
    participantCatalogError: currentMembers ? memberCatalog.error : membersAllowed ? null : "无参与者权限。",
    reloadParticipants,
    canAccount: (id: string) => !!auth.access && (auth.access.all_data || auth.access.account_ids.includes(id)),
    canParticipant: (id: string) => !!auth.access && (auth.access.all_data || auth.access.participant_ids.includes(id)),
  };
}
