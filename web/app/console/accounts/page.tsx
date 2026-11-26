"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
import { Suspense, useEffect, useRef, useState } from "react";
import { requestFailureText } from "@/lib/request-id";
import { useSearchParams } from "next/navigation";
import { RefreshCw } from "lucide-react";
import { createAccountsClient, createMonitoringClient } from "@sub-pool/contracts/client";
import type { AccountModelConfiguration, ManagedAccount } from "@sub-pool/contracts/accounts";
import type { CollectionJob, CollectionRun } from "@sub-pool/contracts/monitoring";
import {
  AdminButton, Badge, Callout, Card, Drawer, Empty, FreshTag, KV, Modal, PageHeader, Segmented, Table, Tabs, Outcome, Select, Field, Button, Mono, Val,
} from "@/components/ui";
import { AccountCard, StatusBadges, WindowRow } from "@/components/quota";
import { accountReadInput, bindAccountRefresh, jobNote, modelRows, quotaSource, quotaWindow, readRunJobs } from "@/lib/accounts-data";
import { checkText, connectionFailure, displayTime } from "@/lib/connection-data";
import { useAccountData } from "@/lib/use-account-data";
import { usePoolData } from "@/lib/use-pool-data";
import { useDispatchData } from "@/lib/use-dispatch-data";
import { dispatchObservationText } from "@/lib/dispatch-data";
import { useApp } from "@/lib/store";
import { AccountAllocation } from "@/components/account-allocation";

function Counts({ run }: { run: CollectionRun }) {
  return <div className="flex flex-wrap gap-1">
    <Badge tone="ok">成功 {run.counts.succeeded}</Badge>
    {run.counts.failed !== "0" && <Badge tone="bad">失败 {run.counts.failed}</Badge>}
    {run.counts.skipped !== "0" && <Badge tone="muted">跳过 {run.counts.skipped}</Badge>}
    {run.counts.conflict !== "0" && <Badge tone="warn">冲突 {run.counts.conflict}</Badge>}
    {run.counts.interrupted !== "0" && <Badge tone="unk">中断 {run.counts.interrupted}</Badge>}
    {run.counts.queued !== "0" && <Badge tone="info">排队 {run.counts.queued}</Badge>}
    {run.counts.running !== "0" && <Badge tone="info">执行 {run.counts.running}</Badge>}
  </div>;
}

function AccountsInner() {
  const data = useAccountData();
  const poolData = usePoolData(false);
  const poolName = (accountID: string) => {
    if (!poolData.allowed) return "未授权";
    if (!poolData.ready) return poolData.error ? "读取不全" : uiCopy.loading;
    const pool = poolData.pools.find(p => p.accounts.some(a => a.id === accountID));
    return pool ? `${pool.name}${pool.enabled ? "" : "（已停用）"}` : "未在可见池中";
  };
  const { rows, accounts, isAdmin, admin, connectionRevision, accountCatalogAvailable, accountCatalogError, observationsReady, reload } = data;
  const { toast, refreshSession, displayTimezone } = useApp();
  const params = useSearchParams();
  const [view, setView] = useState("grid");
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const sel = rows.find(row => row.account.id === selectedID) ?? null;
  const [tab, setTab] = useState("status");
  const scheduling = useDispatchData(sel && tab === "status" ? sel.account.id : null);
  const [runOpen, setRunOpen] = useState(false);
  const [runScope, setRunScope] = useState("all");
  const [submitting, setSubmitting] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [activeRun, setActiveRun] = useState<CollectionRun | null>(null);
  const [runJobs, setRunJobs] = useState<CollectionJob[]>([]);
  const [runError, setRunError] = useState<string | null>(null);
  const [readVersion, setReadVersion] = useState(0);
  const [models, setModels] = useState<AccountModelConfiguration | null>(null);
  const [modelError, setModelError] = useState<string | null>(null);
  const request = useRef({ alive: false, busy: false, locked: false, generation: 0, abort: new AbortController() });
  const [runtimeRead, setRuntimeRead] = useState<{ id: string; pending: boolean; error: string | null; unknown: boolean } | null>(null);
  const runtimeRequest = useRef({ generation: 0, busy: false, locked: false, abort: new AbortController() });
  const runtimeTarget = useRef({ id: selectedID, tab, key: "" });
  runtimeTarget.current = { id: selectedID, tab, key: sel && connectionRevision ? JSON.stringify(accountReadInput(sel.account, connectionRevision)) : "" };
  const runID = activeRun?.id ?? null;
  const running = submitting || activeRun !== null && activeRun.state !== "completed";
  const bindingReady = !!admin.connection?.source_id && admin.connection.credential_set && admin.connection.revision === connectionRevision;

  useEffect(() => {
    request.current.alive = true;
    request.current.abort = new AbortController();
    return () => { request.current.alive = false; request.current.generation++; request.current.busy = false; request.current.abort.abort(); runtimeRequest.current.generation++; runtimeRequest.current.busy = false; runtimeRequest.current.abort.abort(); };
  }, []);
  useEffect(() => { const id = params.get("id"); if (id) { cancelRuntime(); setSelectedID(id); } }, [params]);

  const cancelRuntime = () => {
    runtimeRequest.current.generation++; runtimeRequest.current.abort.abort(); runtimeRequest.current.busy = false;
    setRuntimeRead(value => value?.pending ? null : value);
  };
  const refreshRuntime = async (account: ManagedAccount) => {
    if (runtimeRequest.current.busy || runtimeRequest.current.locked || !connectionRevision || !accountCatalogAvailable) return;
    if (!account.source_current || !account.connection_available) {
      setRuntimeRead({ id: account.id, pending: false, error: "连接已变。", unknown: false }); return;
    }
    runtimeRequest.current.busy = true; runtimeRequest.current.abort = new AbortController();
    const generation = ++runtimeRequest.current.generation, key = JSON.stringify(accountReadInput(account, connectionRevision));
    const current = () => request.current.alive && runtimeRequest.current.generation === generation && runtimeTarget.current.id === account.id && runtimeTarget.current.tab === "status" && runtimeTarget.current.key === key;
    setRuntimeRead({ id: account.id, pending: true, error: null, unknown: false });
    try {
      const value = await createAccountsClient(window.location.origin).refresh(accountReadInput(account, connectionRevision), { signal: runtimeRequest.current.abort.signal });
      if (!current()) return;
      bindAccountRefresh(account, connectionRevision, value);
      setRuntimeRead(null); void reload();
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      if ([0, 404, 409].includes(failure.status)) runtimeRequest.current.locked = true;
      setRuntimeRead({ id: account.id, pending: false, unknown: failure.status === 0, error: failure.status === 0 ? requestFailureText(failure.text, uiCopy.pendingResult) : failure.text });
      if ([401, 403, 404].includes(failure.status)) void refreshSession();
    } finally {
      if (runtimeRequest.current.generation === generation) {
        runtimeRequest.current.busy = false;
        if (request.current.alive) setRuntimeRead(value => value?.id === account.id && value.pending ? null : value);
      }
    }
  };
  const openAccount = (account: ManagedAccount) => {
    if (runtimeRequest.current.busy && runtimeTarget.current.id === account.id) return;
    cancelRuntime(); setSelectedID(account.id); setTab("status");
    runtimeTarget.current = { id: account.id, tab: "status", key: connectionRevision ? JSON.stringify(accountReadInput(account, connectionRevision)) : "" };
    void refreshRuntime(account);
  };
  const changeAccountTab = (value: string) => {
    if (value !== "status") cancelRuntime();
    setTab(value); runtimeTarget.current.tab = value;
    if (value === "status" && sel) void refreshRuntime(sel.account);
  };

  useEffect(() => {
    const abort = new AbortController();
    let alive = true;
    setModels(null); setModelError(null);
    if (!isAdmin || tab !== "models" || !sel || !connectionRevision) return () => { alive = false; abort.abort(); };
    const account = sel.account;
    void (async () => {
      try {
        const value = await createAccountsClient(window.location.origin).models(accountReadInput(account, connectionRevision), { signal: abort.signal });
        if (value.account_id !== account.id || value.source_id !== account.source_id || value.connection_revision !== connectionRevision) throw new Error("Changed model binding");
        if (alive) setModels(value);
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setModelError(failure.status === 0 ? requestFailureText(failure.text, "模型待核对。") : failure.text);
        if (failure.status === 401 || failure.status === 403) void refreshSession();
      }
    })();
    return () => { alive = false; abort.abort(); };
  }, [isAdmin, tab, sel?.account.id, sel?.account.revision, connectionRevision, readVersion, refreshSession]);

  useEffect(() => {
    if (!runOpen || !runID || !isAdmin) return;
    const abort = new AbortController();
    let alive = true, timer: ReturnType<typeof setTimeout> | undefined;
    setRunJobs([]); setRunError(null);
    const poll = async () => {
      if (document.visibilityState !== "visible") { timer = setTimeout(() => void poll(), 1500); return; }
      try {
        const client = createMonitoringClient(window.location.origin);
        const value = await readRunJobs(after => client.run({ id: runID, after, limit: 100 }, { signal: abort.signal }), runID);
        if (!alive) return;
        setActiveRun(value.run); setRunJobs(value.jobs);
        if (value.run.state === "completed") await reload();
        else timer = setTimeout(() => void poll(), 1500);
      } catch (error) {
        if (!alive) return;
        const failure = connectionFailure(error);
        setRunError(failure.status === 0 ? requestFailureText(failure.text, "读取批次结果。") : failure.text);
        if (failure.status === 401 || failure.status === 403) void refreshSession();
      }
    };
    void poll();
    return () => { alive = false; abort.abort(); if (timer) clearTimeout(timer); };
  }, [runOpen, runID, isAdmin, readVersion, refreshSession, reload]);

  const recover = async () => {
    if (request.current.busy || runtimeRequest.current.busy) return;
    await reload(); setReadVersion(value => value + 1);
    if (!unknown) { request.current.locked = false; setBlocked(false); setNotice(null); }
    if (!runtimeRead?.unknown) { runtimeRequest.current.locked = false; setRuntimeRead(null); }
  };
  const run = async () => {
    const connection = admin.connection;
    if (!isAdmin || request.current.busy || request.current.locked || running || !accountCatalogAvailable || !bindingReady || !connection?.source_id) return;
    request.current.busy = true; setSubmitting(true); setActiveRun(null); setRunJobs([]); setRunError(null); setNotice(null);
    const generation = ++request.current.generation;
    const current = () => request.current.alive && request.current.generation === generation;
    try {
      const value = await createMonitoringClient(window.location.origin).collect({ source_id: connection.source_id, connection_revision: connection.revision,
        all: runScope === "all", account_ids: runScope === "all" ? [] : [runScope] }, { signal: request.current.abort.signal });
      if (!current()) return;
      setActiveRun(value);
      toast({ tone: "info", title: "采集已请求", body: `批次 ${value.id}，详见各账号。` });
    } catch (error) {
      if (!current()) return;
      const failure = connectionFailure(error);
      setNotice(failure.text);
      if (failure.status === 0 || failure.status === 409 || failure.status === 404) { request.current.locked = true; setBlocked(true); }
      if (failure.status === 0) setUnknown(true);
      if (failure.status === 401 || failure.status === 403) void refreshSession();
    } finally { if (current()) { request.current.busy = false; setSubmitting(false); } }
  };
  const showRun = (run: CollectionRun) => { if (!request.current.busy) { setActiveRun(run); setRunJobs([]); setRunError(null); setRunOpen(true); setReadVersion(value => value + 1); } };
  const scopedJobs = rows.flatMap(row => row.observation?.latest_attempt ? [{ account: row.account, job: row.observation.latest_attempt }] : []);
  const readProblem = accountCatalogError ?? data.observationError ?? admin.error ?? scheduling.error ?? runtimeRead?.error;
  const time = (stamp: string | null | undefined) => displayTime(stamp, displayTimezone, false);

  return (
    <>
      <PageHeader
        title={uiCopy.quotas}
        helpTopic="connection"
        actions={
          <>
            <Segmented value={view} onChange={setView} options={[{ id: "grid", label: "卡片" }, { id: "table", label: "列表" }]} />
            {(readProblem || notice || blocked || runError) && <Button onClick={() => { scheduling.reload(); void recover(); }} disabled={submitting || runtimeRead?.pending}>{uiCopy.reload}</Button>}
            <AdminButton variant="primary" icon={<RefreshCw size={14} />} onClick={() => setRunOpen(true)} disabled={!accountCatalogAvailable || submitting}>
              手动采集
            </AdminButton>
          </>
        }
      />
      {readProblem && <Callout tone="unk">{readProblem}</Callout>}
      {notice && <Callout tone={unknown ? "unk" : "warn"}>{notice}</Callout>}
      {!observationsReady && <Empty title={readProblem ? "账号读取失败" : uiCopy.loading} />}
      {observationsReady && rows.length === 0 && <Empty title={uiCopy.noAccounts} />}
      {view === "grid" ? (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
          {rows.map(row => <AccountCard key={row.account.id} a={row} onClick={() => openAccount(row.account)} />)}
        </div>
      ) : (
        <Card pad={false}>
          <Table rows={rows} rowKey={row => row.account.id} onRow={row => openAccount(row.account)} cols={[
            { key: "n", label: uiCopy.account, render: row => <div><div className="font-medium">{row.account.name}</div><Mono>{row.account.upstream_id}</Mono></div> },
            { key: "s", label: uiCopy.status, render: row => <StatusBadges a={row.account} attempt={row.observation?.latest_attempt} /> },
            { key: "w1", label: "短周期", align: "right", render: row => quotaWindow(row, "short")?.used_percent == null ? <Badge tone="unk">未知</Badge> : <span className="num">{quotaWindow(row, "short")?.used_percent}%</span> },
            { key: "w2", label: "7 天", align: "right", render: row => !quotaWindow(row, "seven_day") ? <span className="text-faint">未返回</span> : quotaWindow(row, "seven_day")?.used_percent === null ? <Badge tone="unk">未知</Badge> : <span className="num">{quotaWindow(row, "seven_day")?.used_percent}%</span> },
            { key: "f", label: "新鲜度", render: row => <FreshTag s={row.observation?.freshness ?? "unknown"} /> },
            { key: "src", label: "来源", render: row => quotaSource(row) },
            { key: "p", label: uiCopy.pool, render: row => poolName(row.account.id) },
          ]} />
        </Card>
      )}

      <Card title={isAdmin ? "采集执行记录" : "最近采集"} className="mt-6" desc={isAdmin ? `常规频率 ${admin.config ? `${admin.config.interval_seconds} 秒` : "未知"} · 全局最近成功 ${admin.status ? time(admin.status.last_success_at) : "未知"} · 下次计划 ${admin.status ? admin.status.next_scheduled_at ? `${time(admin.status.next_scheduled_at)}（计划）` : admin.status.enabled ? "暂无采集计划" : "已暂停" : "未知"}` : "当前可见账号"} pad={false}>
        {isAdmin ? <Table rows={admin.runs} rowKey={run => run.id} onRow={showRun} cols={[
          { key: "id", label: "批次", render: run => <Mono>{run.id}</Mono> },
          { key: "at", label: "提交时间", render: run => <Timestamp short className="num" value={run.created_at} /> },
          { key: "t", label: "触发", render: run => run.reason === "manual" ? "手动" : "定时" },
          { key: "r", label: uiCopy.result, render: run => <Counts run={run} /> },
          { key: "note", label: uiCopy.status, render: run => <span className="text-muted"><Outcome k={run.state} /></span> },
        ]} /> : <Table rows={scopedJobs} rowKey={row => row.job.id} cols={[
          { key: "id", label: uiCopy.account, render: row => <Mono>{row.account.name}</Mono> },
          { key: "at", label: "执行时间", render: row => <Timestamp short className="num" value={row.job.finished_at ?? row.job.started_at ?? row.job.queued_at} /> },
          { key: "r", label: uiCopy.result, render: row => <Outcome k={row.job.state} /> },
          { key: "note", label: "说明", render: row => <span className="text-muted">{jobNote(row.job)}</span> },
        ]} />}
      </Card>

      <Drawer open={!!selectedID} onClose={() => { cancelRuntime(); runtimeTarget.current.id = null; setSelectedID(null); }} title={sel?.account.name ?? "账号读取"}>
        {!sel && <Empty title={observationsReady ? "账号不可见" : uiCopy.loading} />}
        {sel && (
          <>
            <Tabs value={tab} onChange={changeAccountTab} tabs={[{ id: "status", label: "运行状态" }, { id: "quota", label: "额度窗口" }, ...(isAdmin ? [{ id: "models", label: "模型配置" }] : [])]} />
            {tab === "status" && (
              <div className="space-y-4">
                <StatusBadges a={sel.account} />
                {sel.account.snapshot.runtime.error_present === true && <Callout tone="bad" title={`${glossary.sub2api} 报告异常`} />}
                <KV items={[
                  [`${glossary.sub2api} 名称`, <Mono key="n">{sel.account.snapshot.account.name}</Mono>],
                  [`${glossary.sub2api} ID`, <Mono key="i">{sel.account.upstream_id}</Mono>],
                  [uiCopy.pool, poolName(sel.account.id)],
                  ["最近使用", sel.account.snapshot.runtime.last_used_at ? time(sel.account.snapshot.runtime.last_used_at) : <Val key="last" v={null} />],
                  ["本地监控", sel.account.monitoring ? "已启用" : "已暂停"],
                  ["调度", !scheduling.allowed ? uiCopy.noAccess : scheduling.selected && !scheduling.selected.source_current ? "连接已变" : scheduling.error ?? (scheduling.loading ? uiCopy.loading : dispatchObservationText(scheduling.observation))],
                  ["暂停 / 恢复", scheduling.allowed ? scheduling.control.statusText(sel.account.id) : "无调度权限"],
                  [uiCopy.status, runtimeRead?.pending && runtimeRead.id === sel.account.id ? uiCopy.loading : runtimeRead?.error ?? checkText(sel.account.read)],
                  ["观测时间", time(sel.account.observed_at)],
                  ["限流重置", time(sel.account.snapshot.runtime.rate_limit_reset_at)],
                  ["过载结束", time(sel.account.snapshot.runtime.overload_until)],
                ]} />
                <Card title="采集状态">
                  <KV items={[
                    ["接收时间", <span className="num" key="a">{time(sel.observation?.snapshot?.received_at)}</span>],
                    ["快照时间", sel.observation?.snapshot?.source_at ? <span className="num">{time(sel.observation.snapshot.source_at)}</span> : <Val key="obs" v={null} />],
                    [uiCopy.status, <FreshTag key="s" s={sel.observation?.freshness ?? "unknown"} />],
                    ["下次计划", sel.observation?.next_scheduled_at ? time(sel.observation.next_scheduled_at) : "无可确认计划"],
                    ["最近尝试", sel.observation?.latest_attempt ? <Outcome k={sel.observation.latest_attempt.state} /> : uiCopy.noRecords],
                    ...(sel.observation?.latest_attempt ? [["说明", jobNote(sel.observation.latest_attempt)] as [string, string]] : []),
                    ["重试限制至", time(sel.observation?.latest_attempt?.retry_after)],
                  ]} />
                  {sel.error && <Callout tone="warn" className="mt-3">{sel.error}</Callout>}
                </Card>
                <AccountAllocation key={sel.account.id} accountID={sel.account.id} pools={poolData} />
              </div>
            )}
            {tab === "quota" && (
              <div className="space-y-4">
                <WindowRow w={quotaWindow(sel, "short")} label="短窗口" source={quotaSource(sel)} />
                <WindowRow w={quotaWindow(sel, "seven_day")} label="7 天窗口" source={quotaSource(sel)} />
                <Callout tone="info">{sel.observation?.snapshot?.simulation && "旧模拟 · "}{time(sel.observation?.snapshot?.source_at)} · <FreshTag s={sel.observation?.freshness ?? "unknown"} /></Callout>
              </div>
            )}
            {tab === "models" && isAdmin && (
              <div className="space-y-3">
                {!models ? <Callout tone="unk" title={modelError ? "模型读取失败" : "读取模型…"}>
                  {modelError ?? "核对账号配置"}
                  {modelError && <div className="mt-2"><Button onClick={() => setReadVersion(value => value + 1)}>{uiCopy.reload}</Button></div>}
                </Callout> : (
                  <>
                    <Callout tone="info">账号配置：{checkText(models.account_read)}；原生目录：{checkText(models.catalog_read)}。{models.mapping_mode === "passthrough" ? "透传已开启。" : models.mapping_mode === "empty" ? "未配置映射" : models.mapping_mode === "configured" ? "已配置映射" : "映射方式未知。"}核对时间：{time(models.checked_at)}</Callout>
                    <Table rows={modelRows(models)} rowKey={model => model.requested} cols={[
                      { key: "r", label: "模型 / 映射", render: model => <Mono>{model.requested}</Mono> },
                      { key: "m", label: "明确映射到", render: model => model.mapped ? <Mono>{model.mapped}</Mono> : <Badge tone="unk">未确定</Badge> },
                    ]} />
                  </>
                )}
              </div>
            )}
          </>
        )}
      </Drawer>

      <Modal open={runOpen && isAdmin} onClose={() => { if (!submitting) setRunOpen(false); }} title="立即采集" footer={
        <>
          <Button onClick={() => setRunOpen(false)} disabled={submitting}>关闭</Button>
          {(runError || notice || blocked) && <Button onClick={() => void recover()} disabled={submitting}>{uiCopy.reload}</Button>}
          <Button variant="primary" onClick={() => void run()} disabled={running || blocked || !bindingReady || !accountCatalogAvailable} icon={<RefreshCw size={14} className={running ? "animate-spin" : ""} />}>
            {submitting ? "提交中…" : running ? "采集中…" : "开始采集"}
          </Button>
        </>
      }>
        <div className="space-y-3">
          <Field label="采集范围">
            <Select value={runScope} onChange={event => setRunScope(event.target.value)} disabled={running || blocked} options={[{ value: "all", label: "全部启用账号" }, ...accounts.map(account => ({ value: account.id, label: account.name }))]} />
          </Field>
          <Callout tone="info">手动采集遵守 {glossary.sub2api} 可单次采集。</Callout>
          {!bindingReady && <Callout tone="unk">请重读连接。</Callout>}
          {notice && <Callout tone={unknown ? "unk" : "warn"}>{notice}{unknown ? "核对原请求。" : ""}</Callout>}
          {activeRun && <Callout tone="info">批次 <Mono>{activeRun.id}</Mono> · <Outcome k={activeRun.state} /><Counts run={activeRun} /></Callout>}
          {runError && <Callout tone="unk">{runError}</Callout>}
          {runJobs.length > 0 && <Table rows={runJobs} rowKey={job => job.id} cols={[
            { key: "a", label: uiCopy.account, render: job => accounts.find(account => account.id === job.account_id)?.name ?? job.account_id },
            { key: "r", label: uiCopy.result, render: job => <Outcome k={job.state} /> },
            { key: "n", label: "说明", render: job => <span className="text-muted">{jobNote(job)} · <Timestamp short value={job.finished_at ?? job.started_at ?? job.queued_at} />{job.retry_after && <> · 限制至 <Timestamp short value={job.retry_after} /></>}</span> },
          ]} />}
        </div>
      </Modal>
    </>
  );
}

export default function AccountsPage() {
  return <Suspense><AccountsInner /></Suspense>;
}
