"use client";

import { useEffect, useState } from "react";
import { createDemoClient } from "@sub-pool/contracts/client";
import type { DemoScenario } from "@sub-pool/contracts/demo";

export function useDemoData(active: boolean, scenario: DemoScenario["scenario"] = "overview") {
  const [state, setState] = useState<{ scenario: DemoScenario["scenario"]; data: DemoScenario | null; pending: boolean; error: string | null }>({ scenario, data: null, pending: false, error: null });
  useEffect(() => {
    if (!active) { setState({ scenario, data: null, pending: false, error: null }); return; }
    let current = true;
    const abort = new AbortController();
    setState({ scenario, data: null, pending: true, error: null });
    void createDemoClient(window.location.origin).scenario({ scenario }, { signal: abort.signal }).then(data => {
      if (current) setState({ scenario, data, pending: false, error: null });
    }).catch(() => {
      if (current) setState({ scenario, data: null, pending: false, error: "演示不可用。" });
    });
    return () => { current = false; abort.abort(); };
  }, [active, scenario]);
  return !active ? { data: null, pending: false, error: null }
    : state.scenario === scenario ? state : { data: null, pending: true, error: null };
}
