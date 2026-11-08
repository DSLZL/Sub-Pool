"use client";

import { useEffect, useState } from "react";
import { createDemoClient } from "@sub-pool/contracts/client";
import type { DemoScenario } from "@sub-pool/contracts/demo";

export function useDemoData(active: boolean) {
  const [state, setState] = useState<{ data: DemoScenario | null; pending: boolean; error: string | null }>({ data: null, pending: false, error: null });
  useEffect(() => {
    if (!active) { setState({ data: null, pending: false, error: null }); return; }
    let current = true;
    const abort = new AbortController();
    setState({ data: null, pending: true, error: null });
    void createDemoClient(window.location.origin).scenario({ scenario: "overview" }, { signal: abort.signal }).then(data => {
      if (current) setState({ data, pending: false, error: null });
    }).catch(() => {
      if (current) setState({ data: null, pending: false, error: "独立合成演示暂不可用。真实业务与身份保持当前状态，没有改用模拟结果。" });
    });
    return () => { current = false; abort.abort(); };
  }, [active]);
  return active ? state : { data: null, pending: false, error: null };
}
