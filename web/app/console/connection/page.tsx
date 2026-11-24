"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { ORPCError } from "@orpc/client";
import { requestFailureText } from "@/lib/request-id";
import { createAccountsClient, createConnectionClient, createMonitoringClient } from "@sub-pool/contracts/client";
import type { ConnectionConfiguration, ConnectionDiagnostic, ConnectionImpact, ConnectionWriteCapability, ConnectionWriteGrants, DiscoveredAccount, SystemSettings } from "@sub-pool/contracts/connection";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { MonitoringConfig } from "@sub-pool/contracts/monitoring";
import { Plug, ShieldCheck, Search, Plus } from "lucide-react";
import {
  Badge,
  Button,
  Callout,
  Card,
  CapBadge,
  ConfirmModal,
  Field,
  Grid,
  Input,
  KV,
  Modal,
  Mono,
  PageHeader,
  Select,
  Switch,
  Table,
  Tabs,
  Outcome,
  Empty,
} from "@/components/ui";
import type { Cap } from "@/components/ui";
import { useApp } from "@/lib/store";
import { useConsoleTemporaryStatus } from "@/lib/use-temporary-status";
import { capabilityNames, checkText, connectionFailure, displayTime, monitoringDraft, monitoringSave, readConnectionImpact, readDiscoveredPages, readManagedPages } from "@/lib/connection-data";

type WorkerStatus = Awaited<ReturnType<ReturnType<typeof createMonitoringClient>["status"]>>;
type Candidates = Awaited<ReturnType<typeof readDiscoveredPages>>;
const grantNames: Record<ConnectionWriteCapability, string> = { pricing: "计价", scheduling: "整账号调度", wallet: "钱包" };
function capState(state?: string): Cap {
  return state === "available" ? "available" : state === "denied" ? "noperm" : state === "unsupported" ? "unsupported" : "unverified";
}

export default function ConnectionPage() {
  const { toast, refreshSession, refreshSettings, isAdmin, displayTimezone } = useApp();
  const { reload: reloadTemporaryStatus } = useConsoleTemporaryStatus();
  const [tab, setTab] = useState("conn");
  const [saved, setSaved] = useState<ConnectionConfiguration | null>(null);
  const [policy, setPolicy] = useState<MonitoringConfig | null>(null);
  const [settings, setSettings] = useState<SystemSettings | null>(null);
  const [status, setStatus] = useState<WorkerStatus | null>(null);
  const lastRun = status?.latest_run;
  const [ready, setReady] = useState(false);
  const [pending, setPending] = useState<string | null>(null);
  const [blocked, setBlocked] = useState(false);
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
  const request = useRef({ alive: false, generation: 0, abort: new AbortController(), busy: false });
  const begin = useCallback(() => {
    request.current.abort.abort(); request.current.abort = new AbortController();
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const current = useCallback((generation: number) => request.current.alive && request.current.generation === generation, []);
  const showFailure = useCallback((error: unknown, mutation: boolean) => {
    const failure = connectionFailure(error);
    setNotice(previous => !mutation && previous?.unknown ? previous : { text: failure.status === 0 && !mutation ? requestFailureText(failure.text, "读取未完成，请重新读取当前状态。") : failure.text, unknown: mutation && failure.status === 0 });
    if (mutation && failure.status === 0 || failure.status === 404 || failure.status === 409) setBlocked(true);
    const passwordRejected = error instanceof ORPCError && error.defined && error.code === "CURRENT_PASSWORD_INVALID";
    if (failure.status === 401 || failure.status === 403 && !passwordRejected) void refreshSession();
  }, [refreshSession]);

  const load = useCallback(async (discover = false, keepNotice = false) => {
    if (request.current.busy) return;
    request.current.busy = true;
    const ticket = begin();
    setPending("read"); setReady(false); setTest(null); setNewSecret(""); setAddOpen(null); setUrlConfirm(false); setImpact(null); setClearConfirm(false); setCandidates(null);
    setWriteGrants(null); setGrantConfirm(null); setGrantPassword("");
    try {
      const connection = createConnectionClient(window.location.origin), monitoring = createMonitoringClient(window.location.origin);
      const [config, system, monitoringPolicy, worker, grants] = await Promise.all([
        connection.get({}, { signal: ticket.signal }), connection.settings({}, { signal: ticket.signal }),
        monitoring.config({}, { signal: ticket.signal }), monitoring.status({}, { signal: ticket.signal }),
        connection.writeGrants({}, { signal: ticket.signal }).catch(() => null),
      ]);
      const accounts = await readManagedPages(after => createAccountsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal: ticket.signal }), config.revision);
      let found: Candidates | null = null;
      if (discover && config.source_id && config.credential_set) {
        const sourceID = config.source_id;
        found = await readDiscoveredPages(page => connection.discover({ revision: config.revision, source_id: sourceID, page, search: "" }, { signal: ticket.signal }), sourceID, config.revision);
      }
      if (!current(ticket.generation)) return;
      setSaved(config); setUrl(config.base_url); setSettings(system); setTz(system.timezone); setPolicy(monitoringPolicy); void refreshSettings();
      setJob(monitoringDraft(monitoringPolicy));
      setStatus(worker); setAccs(accounts); setCandidates(found); setReady(true); setBlocked(false);
      setWriteGrants(grants);
      setNotice(previous => previous?.unknown || keepNotice ? previous : null);
    } catch (error) {
      if (current(ticket.generation)) { setAccs([]); showFailure(error, false); }
    } finally { if (current(ticket.generation)) { request.current.busy = false; setPending(null); } }
  }, [begin, current, showFailure, refreshSettings]);

  useEffect(() => {
    request.current.alive = true;
    if (isAdmin) void load();
    else { setWriteGrants(null); setGrantConfirm(null); setGrantPassword(""); }
    return () => { request.current.alive = false; request.current.generation++; request.current.busy = false; request.current.abort.abort(); };
  }, [load, isAdmin]);

  async function operate<T>(name: string, action: (signal: AbortSignal) => Promise<T>, apply: (value: T) => void, mutation = true) {
    if (request.current.busy || !ready || blocked || !isAdmin) return false;
    request.current.busy = true;
    const ticket = begin(); setPending(name); setNotice(null);
    try {
      const result = await action(ticket.signal);
      if (!current(ticket.generation)) return false;
      apply(result); return true;
    } catch (error) { if (current(ticket.generation)) showFailure(error, mutation); return false; }
    finally { if (current(ticket.generation)) { request.current.busy = false; setPending(null); } }
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
        readAgain = status === 0 || status === 404 || status === 409;
        throw error;
      }
    }, fresh => {
      setWriteGrants(fresh);
      toast({ tone: "ok", title: enabled ? "写入授权已开启" : "写入授权已撤销", body: grantNames[capability] });
    });
    if (readAgain && request.current.alive && isAdmin) await load(false, true);
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
      setNotice({ text: "连接配置已保存，请重新读取以核对账号与采集状态。", unknown: false });
      toast({ tone: "ok", title: clear ? "管理凭据已清除" : "连接配置已保存", body: result.source_changed ? "旧账号和任务保留原目标，不会迁移或重放。" : "保存不代表连接测试或逐项能力已通过。" });
    });
    reloadTemporaryStatus();
    return savedConnection;
  };
  const discover = () => {
    if (!saved?.source_id || !saved.credential_set) { setNotice({ text: "请先保存连接地址和管理凭据，再发现账号。", unknown: false }); return; }
    const sourceID = saved.source_id;
    void operate("discover", signal => readDiscoveredPages(page => createConnectionClient(window.location.origin).discover({ revision: saved.revision, source_id: sourceID, page, search: "" }, { signal }), sourceID, saved.revision), setCandidates, false);
  };
  const updateAccount = (account: ManagedAccount, monitoring: boolean, sourceMode: "active" | "passive", name = account.name) => {
    return operate("account", signal => createAccountsClient(window.location.origin).update({ id: account.id, revision: account.revision, name, monitoring, source_mode: sourceMode }, { signal }), result => {
      if (result.connection_revision !== saved?.revision) { setBlocked(true); setNotice({ text: "账号已保存，连接版本已变化，请重新读取。", unknown: false }); return; }
      setAccs(values => values.map(value => value.id === result.account.id ? result.account : value));
      setAddOpen(null);
      toast({ tone: "ok", title: "本地账号设置已保存", body: sourceMode === "active" ? status?.active_available ? "仅启用模拟协议主动查询，不代表原版能力。" : "主动额度查询暂不可用；不会触发上游自动重置。" : "监控启停不会改变上游调度或回收余额。" });
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
      toast({ tone: "ok", title: "账号已纳管", body: "已保存本地身份与被动来源；采集结果需另行核对。" });
    });
  };
  const editName = (account: ManagedAccount) => {
    if (!isAdmin || request.current.busy || !ready || blocked) return;
    setNotice(null); setAddOpen(account); setAddName(account.name);
  };
  const saveJob = () => {
    if (!policy) return;
    let input: ReturnType<typeof monitoringSave>;
    try { input = monitoringSave(policy, job); }
    catch {
      setNotice({ text: "频率与提前量须为 1–1440 分钟；临近频率不得大于常规频率，过旧阈值不得小于常规频率或大于 7 天。", unknown: false }); return;
    }
    void operate("policy", signal => createMonitoringClient(window.location.origin).updateConfig(input, { signal }), result => {
      setPolicy(result); setStatus(null); toast({ tone: "ok", title: "采集设置已保存", body: "实际执行结果由后台任务记录；暂停不代表回收远端余额。" });
    }).then(saved => { if (saved) void load(); });
  };
  const saveTimezone = () => {
    if (!settings) return;
    void operate("timezone", signal => createConnectionClient(window.location.origin).updateSettings({ revision: settings.revision, timezone: tz }, { signal }), result => {
      setSettings(result); setTz(result.timezone); void refreshSettings(); toast({ tone: "ok", title: "统计时区已保存" });
    });
  };
  const locked = !!pending || !ready || blocked;
  const testing = pending === "test";
  const credSet = saved?.credential_set ?? false;
  const shownTest = test ?? saved?.diagnostic;
  const capabilities = Object.entries(capabilityNames).map(([id, name]) => {
    const check = saved?.diagnostic?.checks.find(value => value.id === id);
    return { id, name, desc: id, state: capState(check?.state), note: check ? checkText(check, saved?.diagnostic?.version) : "尚未核对已保存目标" };
  });
  const discovered = (candidates?.accounts ?? []).map(account => ({ ...account,
    managed: accs.some(value => value.source_id === saved?.source_id && value.upstream_id === account.upstream_id),
    unavailableReason: account.eligibility === "unsupported" ? "不支持独立 OpenAI OAuth 额度" : null,
  }));
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);

  if (!isAdmin) return <Empty title="当前身份无权管理连接与采集" />;

  return (
    <>
      <PageHeader title="连接与采集" desc="本系统只接入一个 Sub2API 实例。连接测试、能力检查和账号纳管都以实际返回为准。" actions={(!ready || blocked || !!notice || !!candidates && candidates.read.state !== "available") && <Button disabled={!!pending} onClick={() => void load(tab === "acc")}>{pending === "read" ? "读取中…" : "重新读取"}</Button>} />
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      <Tabs
        value={tab}
        onChange={value => { if (request.current.busy) return; setTab(value); setAddOpen(null); setNewSecret(""); setGrantConfirm(null); setGrantPassword(""); if (value === "acc" && !candidates && !blocked) discover(); if (value === "job" && !blocked) void load(); }}
        tabs={[
          { id: "conn", label: "Sub2API 连接" },
          { id: "cap", label: "能力检查" },
          { id: "acc", label: "账号纳管" },
          { id: "job", label: "采集设置" },
          { id: "sys", label: "系统设置" },
        ]}
      />

      {tab === "conn" && (
        <Grid cols={2}>
          <Card title="连接配置" desc={`最近保存 ${time(saved?.saved_at)}`}>
            <div className="space-y-4">
              <Field label="服务地址" hint="只支持单一 Sub2API 实例，不提供平台切换。">
                <Input value={url} disabled={locked} maxLength={2048} onChange={(e) => { setUrl(e.target.value); setTest(null); }} />
              </Field>
              {urlChanged && (
                <Callout tone="warn" title="目标地址将变更">
                  更换后，现有账号映射、未完成的余额写入和本系统托管的计价修改都需要重新核对。同名或同 ID 不代表同一资源，旧写入不会被迁移或重放。
                </Callout>
              )}
              <Field label="管理凭据" hint="保存后不再显示原文，只显示是否已配置。">
                <div className="mb-2 flex items-center gap-2">
                  {credSet ? <Badge tone="ok">已配置（原文不可读取）</Badge> : <Badge tone="muted">未配置</Badge>}
                  {credSet && (
                    <button className="text-[12px] text-bad hover:underline" aria-label="清除管理凭据" disabled={locked} onClick={() => setClearConfirm(true)}>
                      清除
                    </button>
                  )}
                </div>
                <Input type="password" placeholder={credSet ? "输入新凭据以替换" : "输入管理凭据"} value={newSecret} disabled={locked} maxLength={8192} onChange={(e) => { setNewSecret(e.target.value); setTest(null); }} autoComplete="off" />
              </Field>
              <div className="flex flex-wrap gap-2">
                <Button icon={<Plug size={14} />} onClick={() => runTest()} disabled={locked || !url}>
                  {testing ? "测试中…" : "测试连接"}
                </Button>
                <Button
                  variant="primary"
                  disabled={locked || !url}
                  onClick={() => {
                    if (urlChanged && saved) {
                      setImpact(null);
                      void operate("impact", signal => readConnectionImpact(() => createConnectionClient(window.location.origin).impact({}, { signal }), saved), result => { setImpact(result); setUrlConfirm(true); }, false);
                    }
                    else void saveConnection();
                  }}
                >
                  保存
                </Button>
              </div>
            </div>
          </Card>
          <Card title="测试结果" desc="可在保存前测试未保存的参数。服务可达不代表管理能力可用。">
            {!shownTest && !testing && <p className="text-[13px] text-muted">{pending === "read" ? "正在读取已保存状态…" : "暂无已确认的测试结果。测试不保存地址或凭据。"}</p>}
            {testing && <p className="text-[13px] text-muted">正在逐步检查…</p>}
            {shownTest && !testing && (
              <ol className="space-y-2.5">
                {shownTest.checks.map((t) => (
                  <li key={t.id} className="flex items-start gap-3">
                    <Outcome k={t.state === "available" ? "success" : t.state === "failed" || t.state === "denied" ? "failed" : t.state === "unsupported" ? "skipped" : "unknown"} />
                    <div>
                      <div className="text-[13px] font-medium">{capabilityNames[t.id]}</div>
                      <div className="text-[12px] text-muted">{checkText(t, shownTest.version)} · {shownTest.saved ? "已保存目标" : "未保存参数"} · {time(shownTest.checked_at)}</div>
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </Card>
        </Grid>
      )}

      {tab === "cap" && (
        <div className="space-y-4">
        <Card title="生产写入授权" actions={<Button size="sm" disabled={!!pending} onClick={() => void load()}>重新读取授权</Button>}>
          {!grantView ? <p className="text-[13px] text-muted">当前来源的授权状态尚未读回。</p> : (
            <div className="space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[13px]">紧急禁写</span>
                <Badge tone={grantView.upstream_writes_disabled ? "warn" : "muted"}>{grantView.upstream_writes_disabled ? "已开启" : "未开启"}</Badge>
              </div>
              {grantView.upstream_writes_disabled && <Callout tone="warn">紧急禁写期间，服务暂停向上游派发生产写入。已保存授权仍可查看和管理。</Callout>}
              {!grantView.source_id && <p className="text-[13px] text-muted">请先保存 Sub2API 连接地址。</p>}
              {grantView.grants.map(({ capability, grant }) => {
                const active = !!grant && grant.revoked_at === null;
                const name = grantNames[capability];
                return (
                  <div key={capability} className="flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
                    <div className="min-w-0 space-y-1">
                      <div className="flex flex-wrap items-center gap-2 text-[13px]">{name}<Badge tone={active ? "ok" : "muted"}>{active ? "已授权" : grant ? "已撤销" : "未授权"}</Badge></div>
                      {grant && <p className="flex flex-wrap gap-x-2 text-[12px] text-muted">
                        <span className="break-all">授权人 {grant.granted_by}</span><span>{time(grant.granted_at)}</span>
                      </p>}
                      {grant?.revoked_at && <p className="flex flex-wrap gap-x-2 text-[12px] text-muted">
                        <span className="break-all">撤销人 {grant.revoked_by}</span><span>{time(grant.revoked_at)}</span>
                      </p>}
                    </div>
                    <Button size="sm" disabled={locked || !grantView.source_id} aria-label={(active ? "撤销" : "开启") + name + "授权"}
                      onClick={() => { setGrantConfirm(capability); setGrantPassword(""); setNotice(null); }}>
                      {active ? "撤销授权" : "开启授权"}
                    </Button>
                  </div>
                );
              })}
            </div>
          )}
        </Card>
        <Card
          title="逐功能能力状态"
          desc={`实例版本 ${saved?.diagnostic?.version ?? "未知"} · 运行模式 ${saved?.diagnostic?.run_mode ?? "未知"} · 不因账号列表成功推断其他接口可用`}
          actions={
            <Button size="sm" icon={<ShieldCheck size={13} />} disabled={locked || !saved?.base_url} onClick={() => runTest(true)}>
              重新检查
            </Button>
          }
          pad={false}
        >
          <Table
            rows={capabilities}
            rowKey={(c) => c.id}
            cols={[
              { key: "n", label: "能力", render: (c) => <div><div className="font-medium">{c.name}</div><div className="text-[12px] text-muted">{c.desc}</div></div> },
              { key: "s", label: "状态", render: (c) => <CapBadge c={c.state} /> },
              { key: "note", label: "说明", render: (c) => <span className="text-[12.5px] text-muted">{c.note}</span> },
            ]}
          />
          <div className="border-t border-line p-4">
            <Callout tone="info">写入能力仍需独立验证，运行模式或列表读取成功不能放行。主动额度查询暂不可用，以免触发上游自动重置；本系统不会自动提升权限或修改上游授权。</Callout>
          </div>
        </Card>
        </div>
      )}

      {tab === "acc" && (
        <div className="space-y-4">
          <Card title="已纳管账号" pad={false}>
            <Table
              rows={accs}
              empty={pending === "read" ? "正在读取已纳管账号…" : !ready ? "账号读取未完成，请重新读取。" : "暂无已纳管账号。"}
              rowKey={(a) => a.id}
              cols={[
                { key: "n", label: "本地名称", render: (a) => <span className="font-medium" title={a.source_current ? a.identity_state : "原目标账号，历史保留"} role="button" tabIndex={locked ? -1 : 0} aria-label={`编辑${a.name}的本地名称`} aria-disabled={locked} onClick={() => editName(a)} onKeyDown={event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); editName(a); } }}>{a.name}</span> },
                { key: "u", label: "上游 ID", render: (a) => <Mono>{a.upstream_id}</Mono> },
                {
                  key: "src",
                  label: "额度来源",
                  render: (a) => (
                    <Select
                      className="w-32"
                      value={a.source_mode}
                      disabled={locked}
                      aria-label={`${a.name}的额度来源`}
                      onChange={(e) => { const value = e.target.value; if (value === "active" || value === "passive") updateAccount(a, a.monitoring, value); }}
                      options={[
                        { value: "active", label: status?.active_available ? "模拟查询" : "主动查询（暂不可用）" },
                        { value: "passive", label: "被动快照" },
                      ]}
                    />
                  ),
                },
                {
                  key: "m",
                  label: "本地监控",
                  render: (a) => (
                    <Switch
                      checked={a.monitoring}
                      ariaLabel={`${a.name}的本地监控`}
                      disabled={locked}
                      onChange={(v) => updateAccount(a, v, a.source_mode)}
                    />
                  ),
                },
              ]}
            />
          </Card>
          <Card title="发现可纳管账号" desc={pending === "discover" ? "正在完整读取上游分页…" : candidates && candidates.read.state !== "available" ? checkText(candidates.read) : "读取已保存目标的账号列表；读取不到不等于已删除。"} pad={false}>
            <div className="p-4 pb-0">
              <div className="relative max-w-xs">
                <Search size={14} className="absolute top-1/2 left-2.5 -translate-y-1/2 text-faint" />
                <Input className="pl-8" placeholder="按名称或 ID 筛选" value={q} onChange={(e) => setQ(e.target.value)} />
              </div>
            </div>
            <div className="pt-3">
              <Table
                rows={discovered.filter((d) => !q || d.name.includes(q) || d.upstream_id.includes(q))}
                empty={!candidates ? "尚无完整发现结果；请先保存连接，再重新读取。" : candidates.read.state !== "available" ? "此项读取不可用，未显示部分账号。" : "本轮读取没有匹配账号。"}
                rowKey={(d) => d.upstream_id}
                cols={[
                  { key: "n", label: "上游账号", render: (d) => <div><div>{d.name}</div><Mono>{d.upstream_id}</Mono></div> },
                  { key: "t", label: "类型", render: (d) => d.type },
                  { key: "s", label: "上游状态", render: (d) => <Badge tone={d.status === "active" ? "ok" : d.status === "error" ? "bad" : "muted"}>{d.status}</Badge> },
                  {
                    key: "a",
                    label: "",
                    render: (d) =>
                      d.managed ? (
                        <Badge tone="brand">已纳管</Badge>
                      ) : d.unavailableReason ? (
                        <span className="text-[12px] text-warn">{d.unavailableReason}</span>
                      ) : (
                        <Button
                          size="sm"
                          disabled={locked}
                          icon={<Plus size={13} />}
                          onClick={() => {
                            setAddOpen(d);
                            setAddName(d.name.slice(0, 128).trim());
                          }}
                        >
                          纳管
                        </Button>
                      ),
                  },
                ]}
              />
            </div>
          </Card>
        </div>
      )}

      {tab === "job" && (
        <Grid cols={2}>
          <Card title="采集安排">
            <div className="space-y-4">
              <Switch checked={job.enabled} disabled={locked} onChange={(v) => setJob({ ...job, enabled: v })} label="启用定时采集" desc="关闭后不会自动收回远端余额；临时模式的恢复义务仍需单独处理。" />
              <Field label="常规频率（分钟）">
                <Select value={job.interval} disabled={locked} onChange={(e) => setJob({ ...job, interval: e.target.value })} options={[...new Set([job.interval, "5", "10", "15", "30", "60"])]} />
              </Field>
              <Switch checked={job.usageEnabled} disabled={locked} onChange={(v) => setJob({ ...job, usageEnabled: v })} label="定时采集用量" desc="启用定时采集后，后台按 UTC 日期接续已纳管账号的用量。" />
              <Field label="用量采集频率（分钟）">
                <Input type="number" min={1} max={1440} disabled={locked} value={job.usageInterval} onChange={(e) => setJob({ ...job, usageInterval: e.target.value })} />
              </Field>
              <Switch checked={job.refreshOnChange} disabled={locked} onChange={(v) => setJob({ ...job, refreshOnChange: v })} label="业务变化后刷新" desc="账号、连接或采集设置变化后优先重新采集，仍遵守最小间隔与限流等待。" />
              <Switch checked={job.boost} disabled={locked} onChange={(v) => setJob({ ...job, boost: v })} label="临近重置时加密观察" desc="在窗口重置前后提高采集频率；仍遵守最小间隔和限流等待。" />
              <Field label="临近重置观察提前量（分钟）">
                <Input type="number" min={1} max={1440} disabled={locked} value={job.nearLead} onChange={(e) => setJob({ ...job, nearLead: e.target.value })} />
              </Field>
              <Field label="临近重置观察频率（分钟）">
                <Input type="number" min={1} max={1440} disabled={locked} value={job.nearInterval} onChange={(e) => setJob({ ...job, nearInterval: e.target.value })} />
              </Field>
              <Switch checked={job.staleWarnings} disabled={locked} onChange={(v) => setJob({ ...job, staleWarnings: v })} label="启用数据过旧提醒" desc="关闭只停止过旧提醒，不会让数据变新，也不改变采集安排。" />
              <Field label="数据过旧提醒阈值（分钟）">
                <Input type="number" min={1} max={10080} disabled={locked} value={job.stale} onChange={(e) => setJob({ ...job, stale: e.target.value })} />
              </Field>
              <Button variant="primary" disabled={locked} onClick={saveJob}>
                保存
              </Button>
            </div>
          </Card>
          <Card title="当前生效">
            <KV
              cols={1}
              items={[
                ["状态", !status ? "尚未读取" : status.enabled ? <Badge tone="ok" dot>已启用 · 后台{status.worker_recent ? "最近在线" : "待核对"}</Badge> : <Badge tone="muted" dot>已暂停</Badge>],
                ["下次执行", <span key="n" className="num">{status ? status.next_scheduled_at ? `${time(status.next_scheduled_at)}（计划）` : status.enabled ? "暂无待安排账号" : "已暂停" : "尚未读取"}</span>],
                ["最近批次成功数", <span key="s" className="num">{lastRun?.counts.succeeded ?? "暂无任务"}</span>],
                ["最近记录（读取时）", lastRun ? `最近成功 ${time(status?.last_success_at)} · 最近批次 ${time(lastRun.created_at)} · ${lastRun.state} · 成功 ${lastRun.counts.succeeded} / 失败 ${lastRun.counts.failed} / 跳过 ${lastRun.counts.skipped} / 冲突 ${lastRun.counts.conflict} / 中断 ${lastRun.counts.interrupted} / 排队 ${lastRun.counts.queued} / 执行 ${lastRun.counts.running}` : "暂无任务"],
              ]}
            />
          </Card>
        </Grid>
      )}

      {tab === "sys" && (
        <Card title="统计时区" desc="改变显示与统计时区不会改变原始时刻或上游重置时间。" className="max-w-xl">
          <div className="space-y-4">
            <Field label="时区">
              <Select value={tz} disabled={locked} onChange={(e) => setTz(e.target.value)} options={[...new Set([...(settings ? [settings.timezone] : []), "Asia/Shanghai", "Asia/Tokyo", "UTC", "Europe/Berlin", "America/Los_Angeles"])]} />
            </Field>
            <Button variant="primary" disabled={locked} onClick={saveTimezone}>
              保存
            </Button>
          </div>
        </Card>
      )}

      <ConfirmModal
        open={grantConfirm !== null}
        onClose={() => { setGrantConfirm(null); setGrantPassword(""); }}
        onConfirm={updateWriteGrant}
        disabled={locked || !grantPassword || !grantView?.source_id}
        title={grantConfirm ? (enablingGrant ? "开启" : "撤销") + grantNames[grantConfirm] + "授权" : "写入授权"}
        danger={!enablingGrant}
        confirmLabel={enablingGrant ? "确认开启" : "确认撤销"}
      >
        <p className="break-all">当前来源 <Mono>{saved?.base_url}</Mono></p>
        <p>{grantView?.confirmation_text}</p>
        {!enablingGrant && <p>撤销生效后停止新的派发。已发送的请求仍需核对结果。</p>}
        <Field label="当前管理员密码">
          <Input type="password" aria-label="当前管理员密码" autoComplete="current-password" autoFocus maxLength={128} disabled={locked}
            value={grantPassword} onChange={event => setGrantPassword(event.target.value)} />
        </Field>
        {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      </ConfirmModal>
      <ConfirmModal
        open={urlConfirm}
        onClose={() => { setUrlConfirm(false); setImpact(null); }}
        onConfirm={() => saveConnection()}
        disabled={locked || !impact}
        title="确认更换 Sub2API 地址"
        phrase="更换地址"
        confirmLabel="确认更换"
      >
        <p>新地址：<Mono>{url}</Mono></p>
        <ul className="list-disc space-y-1 pl-5 text-muted">
          <li>{impact?.managed_accounts ?? "未知数量的"} 个原目标已纳管账号、{impact?.participant_bindings ?? "未知数量的"} 个未结束用户绑定将保留旧归属，需重新核对</li>
          <li>截至 {time(impact?.checked_at)}，本地待核对调度 {impact?.dispatch_open ?? "未知"} 条、计价 {impact?.pricing_open ?? "未知"} 条；余额申请已拒绝且未下发 {impact?.wallet_rejected ?? "未知"} 条、其他需核对 {impact?.wallet_unconfirmed ?? "未知"} 条。未记录的原生操作数量未知；旧建议与确认须重新核验，不会在新目标重放旧任务</li>
          <li>没有输入新凭据时将清除旧凭据，旧秘密不会随地址迁移</li>
        </ul>
        {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      </ConfirmModal>
      <ConfirmModal
        open={clearConfirm}
        onClose={() => setClearConfirm(false)}
        onConfirm={() => saveConnection(true)}
        disabled={locked}
        title="清除管理凭据"
        danger
        confirmLabel="清除"
      >
        清除已保存目标的凭据；正在执行的请求不会被宣称已撤回，后续读取将不可用，直到重新配置。
        {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      </ConfirmModal>
      <Modal
        open={!!addOpen}
        onClose={() => { if (!request.current.busy) setAddOpen(null); }}
        title={editingName ? "编辑本地账号名称" : "纳管上游账号"}
        footer={
          <>
            <Button disabled={!!pending} onClick={() => setAddOpen(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={locked || !addName.trim()}
              onClick={() => void adopt()}
            >
              {editingName ? "保存" : "纳管"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <KV items={[["上游 ID", <Mono key="i">{addOpen?.upstream_id}</Mono>], ["类型", addOpen && "snapshot" in addOpen ? addOpen.snapshot.account.type : addOpen?.type]]} />
          <Field label="本地显示名称">
            <Input value={addName} disabled={locked} maxLength={128} onChange={(e) => setAddName(e.target.value)} />
          </Field>
          <Callout tone="info">{editingName ? "仅修改本地显示名称，不改变上游身份、来源方式或本地监控状态。" : "纳管会核对上游身份，默认使用被动快照；主动查询暂不可用。本地编辑不会创建或重绑上游身份。"}</Callout>
          {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
        </div>
      </Modal>
    </>
  );
}
