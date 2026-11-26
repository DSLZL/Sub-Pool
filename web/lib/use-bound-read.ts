"use client";

import { useEffect, useState } from "react";
import { useApp } from "./store";
import { connectionFailure } from "./connection-data";

export function useBoundRead<Key, Value>(key: Key | null, read: (key: Key, signal: AbortSignal) => Promise<Value>) {
  const { refreshSession } = useApp();
  const [state, setState] = useState<{ key: Key; value: Value | null; error: unknown } | null>(null);
  useEffect(() => {
    if (key === null) return;
    const abort = new AbortController();
    void read(key, abort.signal).then(value => {
      if (!abort.signal.aborted) setState({ key, value, error: null });
    }, error => {
      if (abort.signal.aborted) return;
      setState({ key, value: null, error });
      if ([401, 403, 404].includes(connectionFailure(error).status)) void refreshSession();
    });
    return () => abort.abort();
  }, [key, read, refreshSession]);
  return key !== null && state?.key === key ? state : null;
}
