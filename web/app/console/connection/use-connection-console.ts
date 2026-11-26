"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { useCallback, useEffect, useState } from "react";
import { ORPCError } from "@orpc/client";
import { requestFailureText } from "@/lib/request-id";
import { createAccountsClient, createConnectionClient, createMonitoringClient } from "@sub-pool/contracts/client";
import type { ConnectionConfiguration, ConnectionDiagnostic, ConnectionImpact, ConnectionWriteCapability, ConnectionWriteGrants, DiscoveredAccount, SystemSettings } from "@sub-pool/contracts/connection";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { MonitoringConfig } from "@sub-pool/contracts/monitoring";
import type { Cap } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useUpstreamWriteReload } from "@/lib/use-upstream-write-availability";
import { capabilityNames, checkDetails, checkSummary, checkText, connectionFailure, displayTime, monitoringDraft, monitoringSave, readConnectionImpact, readDiscoveredPages, readManagedPages } from "@/lib/connection-data";
import { useGuardedMutation } from "@/lib/use-guarded-mutation";

type WorkerStatus = Awaited<ReturnType<ReturnType<typeof createMonitoringClient>["status"]>>;
type Candidates = Awaited<ReturnType<typeof readDiscoveredPages>>;
export const grantNames: Record<ConnectionWriteCapability, string> = { pricing: "计价", scheduling: "调度", wallet: "钱包" };
function capState(state?: string): Cap {
  return state === "available" ? "available" : state === "denied" ? "noperm" : state === "unsupported" ? "unsupported" : "unverified";
}

export function useConnectionConsole() {
  const { toast, refreshSession, refreshSettings, isAdmin, displayTimezone } = useApp();
  const reloadWriteAvailability = useUpstreamWriteReload();
  const [tab, setTab] = useState("conn");
  const [saved, setSaved] = useState<ConnectionConfiguration | null>(null);
  const [policy, setPolicy] = useState<MonitoringConfig | null>(null);
  const [settings, setSettings] = useState<SystemSettings | null>(null);
  const [status, setStatus] = useState<WorkerStatus | null>(null);
  const lastRun = status?.latest_run;
  const [ready, setReady] = useState(false);
  const { pending, blocked, setBlocked, activate, isActive, isBusy, run } = useGuardedMutation();
  const [notice, setNotice] = useState<{ text: string; unknown: boolean } | null>(null);
  const [url, setUrl] = useState("");
  const [newSecret, setNewSecret] = useState("");
  const [test, setTest] = useState<ConnectionDiagnostic | null>(null);
  const [urlConfirm, setUrlConfirm] = useState(false);
  const [impact, setImpact] = useState<ConnectionImpact | null>(null);
  const [clearConfirm, setClearConfirm] = useState(false);
  const [writeGrants, setWriteGrants] = useState<ConnectionWriteGrants | null>(null);
  const [grantConfirm, setGrantConfirm] = useState<ConnectionWriteCapability | null>(null);
  const [grantPassword, setGrantPassword] = useState("");
  const grantView = writeGrants?.source_id === saved?.source_id && writeGrants?.connection_revision === saved?.revision ? writeGrants : null;
  const selectedGrant = grantView?.grants.find(status => status.capability === grantConfirm)?.grant;
  const expectedGrantID = selectedGrant && selectedGrant.revoked_at === null ? selectedGrant.id : "0";
  const enablingGrant = expectedGrantID === "0";
  const urlChanged = !!saved?.source_id && url !== saved.base_url;
  const [accs, setAccs] = useState<ManagedAccount[]>([]);
  const [candidates, setCandidates] = useState<Candidates | null>(null);
  const [q, setQ] = useState("");
  const [addOpen, setAddOpen] = useState<DiscoveredAccount | ManagedAccount | null>(null);
  const [addName, setAddName] = useState("");
  const editingName = !!addOpen && "revision" in addOpen;
  const [job, setJob] = useState({ enabled: false, interval: "5", boost: false, stale: "30", refreshOnChange: false, nearLead: "", nearInterval: "", staleWarnings: false, usageEnabled: false, usageInterval: "5" });
  const [tz, setTz] = useState("");
  const showFailure = useCallback((error: unknown, mutation: boolean) => {
    const failure = connectionFailure(error);
    const unknown = failure.status === 0;
    const feedback = unknown ? requestFailureText(failure.text, mutation ? uiCopy.pendingResult : uiCopy.readFailed) : failure.text;
    setNotice(previous => !mutation && previous?.unknown ? previous : { text: feedback, unknown: mutation && unknown });
    if (mutation && failure.status === 0 || failure.status === 404 || failure.status === 409) setBlocked(true);
    const passwordRejected = error instanceof ORPCError && error.defined && error.code === "CURRENT_PASSWORD_INVALID";
    if (failure.status === 401 || failure.status === 403 && !passwordRejected) void refreshSession();
  }, [refreshSession, setBlocked]);

  const load = useCallback((discover = false, keepNotice = false) => run({
    name: "read",
    recoveryRead: true,
    action: async signal => {
      setReady(false);
      setTest(null);
      setNewSecret("");
      setAddOpen(null);
      setUrlConfirm(false);
      setImpact(null);
      setClearConfirm(false);
      setCandidates(null);
      setWriteGrants(null);
      setGrantConfirm(null);
      setGrantPassword("");
      const connection = createConnectionClient(window.location.origin), monitoring = createMonitoringClient(window.location.origin);
      const [config, system, monitoringPolicy, worker, grants] = await Promise.all([
        connection.get({}, { signal }), connection.settings({}, { signal }),
        monitoring.config({}, { signal }), monitoring.status({}, { signal }),
        connection.writeGrants({}, { signal }).catch(() => null),
      ]);
      const accounts = await readManagedPages(after => createAccountsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal }), config.revision);
      let found: Candidates | null = null;
      if (discover && config.source_id && config.credential_set) {
        const sourceID = config.source_id;
        found = await readDiscoveredPages(page => connection.discover({ revision: config.revision, source_id: sourceID, page, search: "" }, { signal }), sourceID, config.revision);
      }
      return { config, system, monitoringPolicy, worker, grants, accounts, found };
    },
    apply: ({ config, system, monitoringPolicy, worker, grants, accounts, found }) => {
      setSaved(config);
      setUrl(config.base_url);
      setSettings(system);
      setTz(system.timezone);
      setPolicy(monitoringPolicy);
      void refreshSettings();
      setJob(monitoringDraft(monitoringPolicy));
      setStatus(worker);
      setAccs(accounts);
      setCandidates(found);
      setReady(true);
      setBlocked(false);
      setWriteGrants(grants);
      setNotice(previous => previous?.unknown || keepNotice ? previous : null);
    },
    onFailure: error => {
      setAccs([]);
      showFailure(error, false);
    },
  }), [run, refreshSettings, setBlocked, showFailure]);

  useEffect(() => {
    const deactivate = activate();
    if (isAdmin) void load();
    else {
      setWriteGrants(null);
      setGrantConfirm(null);
      setGrantPassword("");
    }
    return deactivate;
  }, [activate, load, isAdmin]);

  async function operate<T>(name: string, action: (signal: AbortSignal) => Promise<T>, apply: (value: T) => void, mutation = true) {
    if (!ready || !isAdmin) return false;
    return run({
      name,
      action: signal => {
        setNotice(null);
        return action(signal);
      },
      apply,
      onFailure: error => showFailure(error, mutation),
    });
  }

  const updateWriteGrant = async () => {
    if (!grantConfirm || !grantView?.source_id || !grantPassword) return false;
    const capability = grantConfirm, enabled = enablingGrant;
    const input = {
      source_id: grantView.source_id, connection_revision: grantView.connection_revision, capability,
      expected_grant_id: expectedGrantID, enabled,
      confirmation_sha256: grantView.confirmation_sha256, current_password: grantPassword,
    };
    setGrantPassword("");
    let readAgain = false;
    const confirmed = await operate("grant", async signal => {
      const client = createConnectionClient(window.location.origin);
      try {
        const receipt = await client.updateWriteGrant(input, { signal });
        const fresh = await client.writeGrants({}, { signal });
        const written = receipt.grants.find(status => status.capability === capability)?.grant;
        const observed = fresh.grants.find(status => status.capability === capability)?.grant;
        if (receipt.source_id !== input.source_id || receipt.connection_revision !== input.connection_revision ||
            fresh.source_id !== input.source_id || fresh.connection_revision !== input.connection_revision ||
            !written || !observed || written.id !== observed.id || (written.revoked_at === null) !== enabled ||
            (observed.revoked_at === null) !== enabled || (!enabled && written.id !== expectedGrantID)) {
          throw new Error("Write grant read-back changed");
        }
        return fresh;
      } catch (error) {
        const { status } = connectionFailure(error);
        readAgain = !signal.aborted && (status === 0 || status === 404 || status === 409);
        throw error;
      }
    }, fresh => {
      setWriteGrants(fresh);
      toast({ tone: "ok", title: enabled ? "已授权" : "已撤销", body: grantNames[capability] });
    });
    if (readAgain && isActive() && isAdmin) await load(false, true);
    if (confirmed || readAgain) reloadWriteAvailability();
    return confirmed;
  };

  const runTest = (savedTarget = false) => {
    if (!saved) return;
    setTest(null);
    void operate("test", signal => createConnectionClient(window.location.origin).test({ revision: saved.revision, base_url: savedTarget ? saved.base_url : url, credential: savedTarget ? "" : newSecret }, { signal }), result => {
      setTest(result);
      if (result.saved) setSaved(previous => previous && previous.revision === result.revision ? { ...previous, diagnostic: result } : previous);
    }, false);
  };
  const saveConnection = async (clear = false) => {
    if (!saved || !clear && urlChanged && (!impact || impact.source_id !== saved.source_id || impact.revision !== saved.revision)) return false;
    const secret = newSecret; setNewSecret("");
    const savedConnection = await operate("save", signal => createConnectionClient(window.location.origin).save({ revision: saved.revision, base_url: clear ? saved.base_url : url,
      credential_action: clear ? "clear" : secret ? "replace" : urlChanged ? "clear" : "keep", credential: clear ? "" : secret,
      confirmation: !clear && urlChanged ? "更换地址" : "" }, { signal }), result => {
      setSaved(result.configuration); setUrl(result.configuration.base_url); setTest(null); setCandidates(null); setStatus(null); setImpact(null); setBlocked(true);
      setWriteGrants(null);
      setNotice({ text: "连接已保存。", unknown: false });
      toast({ tone: "ok", title: clear ? "凭据已清除" : "已保存", body: result.source_changed ? "旧记录保留。" : "请测试连接。" });
    });
    reloadWriteAvailability();
    return savedConnection;
  };
  const discover = () => {
    if (!saved?.source_id || !saved.credential_set) { setNotice({ text: "先保存连接。", unknown: false }); return; }
    const sourceID = saved.source_id;
    void operate("discover", signal => readDiscoveredPages(page => createConnectionClient(window.location.origin).discover({ revision: saved.revision, source_id: sourceID, page, search: "" }, { signal }), sourceID, saved.revision), setCandidates, false);
  };
  const updateAccount = (account: ManagedAccount, monitoring: boolean, sourceMode: "active" | "passive", name = account.name) => {
    return operate("account", signal => createAccountsClient(window.location.origin).update({ id: account.id, revision: account.revision, name, monitoring, source_mode: sourceMode }, { signal }), result => {
      if (result.connection_revision !== saved?.revision) { setBlocked(true); setNotice({ text: "已保存，请重读。", unknown: false }); return; }
      setAccs(values => values.map(value => value.id === result.account.id ? result.account : value));
      setAddOpen(null);
      toast({ tone: "ok", title: "账号已保存" });
    });
  };
  const adopt = async () => {
    const name = addName.trim();
    if (!saved?.source_id || !addOpen || !name) return;
    if ("revision" in addOpen) {
      await updateAccount(addOpen, addOpen.monitoring, addOpen.source_mode, name);
      return;
    }
    const sourceID = saved.source_id;
    await operate("adopt", signal => createAccountsClient(window.location.origin).adopt({ source_id: sourceID, connection_revision: saved.revision,
      upstream_id: addOpen.upstream_id, name, monitoring: true, source_mode: "passive" }, { signal }), result => {
      if (!result.account) { setNotice({ text: checkText(result.read), unknown: false }); return; }
      const account = result.account;
      setAccs(values => [...values, account]); setAddOpen(null);
      toast({ tone: "ok", title: "账号已纳管", body: "账号已保存。" });
    });
  };
  const editName = (account: ManagedAccount) => {
    if (!isAdmin || isBusy() || !ready || blocked) return;
    setNotice(null); setAddOpen(account); setAddName(account.name);
  };
  const saveJob = () => {
    if (!policy) return;
    let input: ReturnType<typeof monitoringSave>;
    try { input = monitoringSave(policy, job); }
    catch {
      setNotice({ text: "核对间隔。", unknown: false }); return;
    }
    void operate("policy", signal => createMonitoringClient(window.location.origin).updateConfig(input, { signal }), result => {
      setPolicy(result); setStatus(null); toast({ tone: "ok", title: "已保存", body: "查看任务。" });
    }).then(saved => { if (saved) void load(); });
  };
  const saveTimezone = () => {
    if (!settings) return;
    void operate("timezone", signal => createConnectionClient(window.location.origin).updateSettings({ revision: settings.revision, timezone: tz }, { signal }), result => {
      setSettings(result); setTz(result.timezone); void refreshSettings(); toast({ tone: "ok", title: "已保存时区" });
    });
  };
  const locked = !!pending || !ready || blocked;
  const testing = pending === "test";
  const credSet = saved?.credential_set ?? false;
  const shownTest = test ?? saved?.diagnostic;
  const capabilities = Object.entries(capabilityNames).map(([id, name]) => {
    const check = saved?.diagnostic?.checks.find(value => value.id === id);
    return {
      id, name, state: capState(check?.state),
      note: check ? checkSummary(check, saved?.diagnostic?.version) : "连接待核对",
      details: check ? checkDetails(check) : "",
    };
  });
  const discovered = (candidates?.accounts ?? []).map(account => ({ ...account,
    managed: accs.some(value => value.source_id === saved?.source_id && value.upstream_id === account.upstream_id),
    unavailableReason: account.eligibility === "unsupported" ? "无独立额度" : null,
  }));
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);

  const requestSave = () => {
    if (!urlChanged || !saved) {
      void saveConnection();
      return;
    }
    setImpact(null);
    void operate("impact", signal => readConnectionImpact(
      () => createConnectionClient(window.location.origin).impact({}, { signal }), saved,
    ), result => {
      setImpact(result);
      setUrlConfirm(true);
    }, false);
  };

  const setAddress = (value: string) => {
    setUrl(value);
    setTest(null);
  };
  const setCredential = (value: string) => {
    setNewSecret(value);
    setTest(null);
  };
  const openGrant = (capability: ConnectionWriteCapability) => {
    setGrantConfirm(capability);
    setGrantPassword("");
    setNotice(null);
  };
  const closeGrant = () => {
    setGrantConfirm(null);
    setGrantPassword("");
  };
  const closeAddressChange = () => {
    setUrlConfirm(false);
    setImpact(null);
  };
  const openAdoption = (account: DiscoveredAccount) => {
    setAddOpen(account);
    setAddName(account.name.slice(0, 128).trim());
  };
  const closeAccount = () => {
    if (!isBusy()) setAddOpen(null);
  };
  const reloadGrants = () => {
    void load();
    reloadWriteAvailability();
  };

  const changeTab = (value: string) => {
    if (isBusy()) return;
    setTab(value);
    setAddOpen(null);
    setNewSecret("");
    closeGrant();
    if (value === "acc" && !candidates && !blocked) discover();
    if (value === "job" && !blocked) void load();
  };

  return {
    isAdmin, tab, pending, notice, changeTab,
    canReload: !ready || blocked || !!notice || !!candidates && candidates.read.state !== "available",
    reload: () => void load(tab === "acc"),
    connection: {
      saved, url, newSecret, urlChanged, credSet, locked, shownTest, testing, pending, time,
      setAddress, setCredential, openCredentialClear: () => setClearConfirm(true), runTest, requestSave,
    },
    capability: { grantView, pending, time, locked, saved, capabilities, reloadGrants, openGrant, runTest },
    accounts: { accs, pending, ready, locked, editName, status, updateAccount, candidates, discovered, q, setQ, openAdoption },
    collection: { job, locked, setJob, saveJob, status, time, lastRun },
    settings: { tz, locked, setTz, settings, saveTimezone },
    dialogs: {
      grantConfirm, closeGrant, updateWriteGrant, locked, grantPassword, grantView, enablingGrant, saved, setGrantPassword, notice,
      urlConfirm, closeAddressChange, saveConnection, impact, url, time,
      clearConfirm, closeCredentialClear: () => setClearConfirm(false), addOpen, closeAccount, editingName, pending, addName, adopt, setAddName,
    },
  };
}

export type ConnectionConsole = ReturnType<typeof useConnectionConsole>;
