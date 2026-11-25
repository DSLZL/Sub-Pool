"use client";

import { useState } from "react";

type Snapshot = { pending: string | null; blocked: boolean };
type GuardedRequest<T> = {
  name: string;
  action: (signal: AbortSignal) => Promise<T>;
  apply: (value: T) => void;
  onFailure: (error: unknown) => void;
  recoveryRead?: boolean;
};

export function createMutationGuard(publish: (snapshot: Snapshot) => void) {
  let active = false;
  let generation = 0;
  let controller: AbortController | null = null;
  let pending: string | null = null;
  let blocked = false;
  const publishState = () => publish({ pending, blocked });

  return {
    activate() {
      active = true;
      return () => {
        active = false;
        generation++;
        pending = null;
        controller?.abort();
        controller = null;
      };
    },
    isActive: () => active,
    isBusy: () => pending !== null,
    setBlocked(next: boolean) {
      if (blocked === next) return;
      blocked = next;
      publishState();
    },
    async run<T>({ name, action, apply, onFailure, recoveryRead = false }: GuardedRequest<T>) {
      if (!active || pending !== null || (blocked && !recoveryRead)) return false;
      const ticket = ++generation;
      const current = () => active && generation === ticket;
      const previous = controller;
      controller = new AbortController();
      const { signal } = controller;
      pending = name;
      previous?.abort();
      publishState();

      try {
        const value = await action(signal);
        if (!current()) return false;
        apply(value);
        return true;
      } catch (error) {
        if (current()) onFailure(error);
        return false;
      } finally {
        if (current()) {
          pending = null;
          publishState();
        }
      }
    },
  };
}

export function useGuardedMutation() {
  const [snapshot, setSnapshot] = useState<Snapshot>({ pending: null, blocked: false });
  const [guard] = useState(() => createMutationGuard(setSnapshot));
  return { ...guard, ...snapshot };
}
