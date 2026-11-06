"use client";

import { useApp } from "@/lib/store";
import type { Account, Participant } from "@/lib/mock";

const accounts: Account[] = [];
const participants: Participant[] = [];

// shortcut: Catalogs stay unavailable until BE-004/007 provide real server results; never fill them from mocks.
export function useScope() {
  const { isAdmin, auth } = useApp();
  return {
    isAdmin, accounts, participants,
    accountCatalogAvailable: false,
    participantCatalogAvailable: false,
    canAccount: (id: string) => !!auth.access && (auth.access.all_data || auth.access.account_ids.includes(id)),
    canParticipant: (id: string) => !!auth.access && (auth.access.all_data || auth.access.participant_ids.includes(id)),
  };
}
