"use client";

import { useEffect, useState, useSyncExternalStore } from "react";
import { createHelpClient } from "@sub-pool/contracts/client";
import { HelpController, initialHelp } from "./help-data";
import type { AuthSnapshot, SessionController } from "./session";

export function useHelpData(auth: AuthSnapshot, session: SessionController) {
  const [controller] = useState(() => new HelpController(
    () => createHelpClient(window.location.origin), session.getSnapshot, () => session.revalidateIdentity(),
  ));
  const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot, () => initialHelp);
  useEffect(() => {
    controller.reset();
    if (auth.status === "authenticated") void controller.load();
    return () => controller.dispose();
  }, [controller, auth.status, auth.epoch]);
  useEffect(() => {
    const focus = () => { if (document.visibilityState === "visible") void controller.load(); };
    window.addEventListener("focus", focus); document.addEventListener("visibilitychange", focus);
    return () => { window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", focus); };
  }, [controller]);
  return { ...state, load: controller.load, markRead: controller.markRead };
}
