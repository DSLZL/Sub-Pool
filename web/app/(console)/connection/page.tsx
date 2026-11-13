"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createAccountsClient, createConnectionClient, createMonitoringClient } from "@sub-pool/contracts/client";
import type { ConnectionConfiguration, ConnectionDiagnostic, DiscoveredAccount, SystemSettings } from "@sub-pool/contracts/connection";
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
import { capabilityNames, checkText, connectionFailure, displayTime, readDiscoveredPages, readManagedPages } from "@/lib/connection-data";

type WorkerStatus = Awaited<ReturnType<ReturnType<typeof createMonitoringClient>["status"]>>;
type Candidates = Awaited<ReturnType<typeof readDiscoveredPages>>;
function capState(state?: string): Cap {
  return state === "available" ? "available" : state === "denied" ? "noperm" : state === "unsupported" ? "unsupported" : "unverified";
}

export default function ConnectionPage() {
  const { toast, refreshSession, refreshSettings, isAdmin } = useApp();
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
  const [clearConfirm, setClearConfirm] = useState(false);
  const urlChanged = !!saved?.source_id && url !== saved.base_url;
  const [accs, setAccs] = useState<ManagedAccount[]>([]);
  const [candidates, setCandidates] = useState<Candidates | null>(null);
  const [q, setQ] = useState("");
  const [addOpen, setAddOpen] = useState<DiscoveredAccount | ManagedAccount | null>(null);
  const [addName, setAddName] = useState("");
  const editingName = !!addOpen && "revision" in addOpen;
  const [job, setJob] = useState({ enabled: false, interval: "5", boost: false, stale: "30" });
  const [tz, setTz] = useState("");
  const request = useRef({ alive: false, generation: 0, abort: new AbortController(), busy: false });
  const begin = useCallback(() => {
    request.current.abort.abort(); request.current.abort = new AbortController();
    return { generation: ++request.current.generation, signal: request.current.abort.signal };
  }, []);
  const current = useCallback((generation: number) => request.current.alive && request.current.generation === generation, []);
  const showFailure = useCallback((error: unknown, mutation: boolean) => {
    const failure = connectionFailure(error);
    setNotice(previous => !mutation && previous?.unknown ? previous : { text: failure.status === 0 && !mutation ? "读取未完成，请重新读取当前状态。" : failure.text, unknown: mutation && failure.status === 0 });
    if (mutation && failure.status === 0 || failure.status === 404 || failure.status === 409) setBlocked(true);
    if (failure.status === 401 || failure.status === 403) void refreshSession();
  }, [refreshSession]);

  const load = useCallback(async (discover = false) => {
    if (request.current.busy) return;
    request.current.busy = true;
    const ticket = begin();
    setPending("read"); setReady(false); setTest(null); setNewSecret(""); setAddOpen(null); setUrlConfirm(false); setClearConfirm(false); setCandidates(null);
    try {
      const connection = createConnectionClient(window.location.origin), monitoring = createMonitoringClient(window.location.origin);
      const [config, system, monitoringPolicy, worker] = await Promise.all([
        connection.get({}, { signal: ticket.signal }), connection.settings({}, { signal: ticket.signal }),
        monitoring.config({}, { signal: ticket.signal }), monitoring.status({}, { signal: ticket.signal }),
      ]);
      const accounts = await readManagedPages(after => createAccountsClient(window.location.origin).list({ after, search: "", limit: 100 }, { signal: ticket.signal }), config.revision);
      let found: Candidates | null = null;
      if (discover && config.source_id && config.credential_set) {
        const sourceID = config.source_id;
        found = await readDiscoveredPages(page => connection.discover({ revision: config.revision, source_id: sourceID, page, search: "" }, { signal: ticket.signal }), sourceID, config.revision);
      }
      if (!current(ticket.generation)) return;
      setSaved(config); setUrl(config.base_url); setSettings(system); setTz(system.timezone); setPolicy(monitoringPolicy); void refreshSettings();
      setJob({ enabled: monitoringPolicy.enabled, interval: String(monitoringPolicy.interval_seconds / 60), boost: monitoringPolicy.near_reset_enabled, stale: String(monitoringPolicy.stale_after_seconds / 60) });
      setStatus(worker); setAccs(accounts); setCandidates(found); setReady(true); setBlocked(false);
      setNotice(previous => previous?.unknown ? previous : null);
    } catch (error) {
      if (current(ticket.generation)) { setAccs([]); showFailure(error, false); }
    } finally { if (current(ticket.generation)) { request.current.busy = false; setPending(null); } }
  }, [begin, current, showFailure, refreshSettings]);

  useEffect(() => {
    request.current.alive = true;
    if (isAdmin) void load();
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

  const runTest = (savedTarget = false) => {
    if (!saved) return;
    setTest(null);
    void operate("test", signal => createConnectionClient(window.location.origin).test({ revision: saved.revision, base_url: savedTarget ? saved.base_url : url, credential: savedTarget ? "" : newSecret }, { signal }), result => {
      setTest(result);
      if (result.saved) setSaved(previous => previous && previous.revision === result.revision ? { ...previous, diagnostic: result } : previous);
    }, false);
  };
  const saveConnection = async (clear = false) => {
    if (!saved) return false;
    const secret = newSecret; setNewSecret("");
    return operate("save", signal => createConnectionClient(window.location.origin).save({ revision: saved.revision, base_url: clear ? saved.base_url : url,
      credential_action: clear ? "clear" : secret ? "replace" : urlChanged ? "clear" : "keep", credential: clear ? "" : secret,
      confirmation: !clear && urlChanged ? "更换地址" : "" }, { signal }), result => {
      setSaved(result.configuration); setUrl(result.configuration.base_url); setTest(null); setCandidates(null); setStatus(null); setBlocked(true);
      setNotice({ text: "连接配置已保存，请重新读取以核对账号与采集状态。", unknown: false });
      toast({ tone: "ok", title: clear ? "管理凭据已清除" : "连接配置已保存", body: result.source_changed ? "旧账号和任务保留原目标，不会迁移或重放。" : "保存不代表连接测试或逐项能力已通过。" });
    });
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
      toast({ tone: "ok", title: "本地账号设置已保存", body: sourceMode === "active" ? "主动额度查询暂不可用；不会触发上游自动重置。" : "监控启停不会改变上游调度或回收余额。" });
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
    if (!policy || !/^\d+(?:\.\d+)?$/.test(job.interval) || !/^\d+(?:\.\d+)?$/.test(job.stale)) { setNotice({ text: "请填写有效采集间隔与陈旧阈值。", unknown: false }); return; }
    const interval = job.interval === String(policy.interval_seconds / 60) ? policy.interval_seconds : Number(job.interval) * 60;
    const stale = job.stale === String(policy.stale_after_seconds / 60) ? policy.stale_after_seconds : Number(job.stale) * 60;
    if (!Number.isSafeInteger(interval) || interval < 60 || interval > 86400 || !Number.isSafeInteger(stale) || stale < interval || stale > 604800 || policy.near_reset_interval_seconds > interval) {
      setNotice({ text: "采集间隔须为 1–1440 分钟，陈旧阈值不得小于间隔或大于 7 天。", unknown: false }); return;
    }
    const { saved_at: _savedAt, ...input } = policy;
    void operate("policy", signal => createMonitoringClient(window.location.origin).updateConfig({ ...input, enabled: job.enabled, interval_seconds: interval, stale_after_seconds: stale, near_reset_enabled: job.boost }, { signal }), result => {
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
    return { id, name, desc: id, state: capState(check?.state), note: check ? checkText(check) : "尚未核对已保存目标" };
  });
  const discovered = (candidates?.accounts ?? []).map(account => ({ ...account,
    managed: accs.some(value => value.source_id === saved?.source_id && value.upstream_id === account.upstream_id),
    unavailableReason: account.eligibility === "unsupported" ? "不支持独立 OpenAI OAuth 额度" : null,
  }));
  const time = (value: string | null | undefined) => displayTime(value, settings?.timezone ?? null);

  if (!isAdmin) return <Empty title="当前身份无权管理连接与采集" />;

  return (
    <>
      <PageHeader title="连接与采集" tag="S2A-CONN · S2A-ACC-001/002 · S2A-JOB-001/002 · S2A-OPS-004" desc="本系统只接入一个 Sub2API 实例。连接测试、能力检查和账号纳管都以实际返回为准。" actions={(!ready || blocked || !!notice || !!candidates && candidates.read.state !== "available") && <Button disabled={!!pending} onClick={() => void load(tab === "acc")}>{pending === "read" ? "读取中…" : "重新读取"}</Button>} />
      {notice && <Callout tone={notice.unknown ? "unk" : "warn"}>{notice.text}</Callout>}
      <Tabs
        value={tab}
        onChange={value => { if (request.current.busy) return; setTab(value); setAddOpen(null); setNewSecret(""); if (value === "acc" && !candidates && !blocked) discover(); if (value === "job" && !blocked) void load(); }}
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
                    if (urlChanged) setUrlConfirm(true);
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
                      <div className="text-[12px] text-muted">{checkText(t)} · {shownTest.saved ? "已保存目标" : "未保存参数"} · {time(shownTest.checked_at)}</div>
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </Card>
        </Grid>
      )}

      {tab === "cap" && (
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
                        { value: "active", label: "主动查询（暂不可用）" },
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
              <Switch checked={job.boost} disabled={locked} onChange={(v) => setJob({ ...job, boost: v })} label="临近重置时加密观察" desc="在窗口重置前后提高采集频率；仍遵守最小间隔和限流等待。" />
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
        open={urlConfirm}
        onClose={() => setUrlConfirm(false)}
        onConfirm={() => saveConnection()}
        disabled={locked}
        title="确认更换 Sub2API 地址"
        phrase="更换地址"
        confirmLabel="确认更换"
      >
        <p>新地址：<Mono>{url}</Mono></p>
        <ul className="list-disc space-y-1 pl-5 text-muted">
          <li>{accs.filter(account => account.source_id === saved?.source_id).length} 个原目标已纳管账号将保留旧归属，需重新核对</li>
          <li>余额写入与计价变更的影响数量未知；不会在新目标重放旧任务</li>
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
