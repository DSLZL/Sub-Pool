"use client";

import { Timestamp } from "@/components/timestamp";
import { Suspense, useEffect, useRef, useState } from "react";
import { requestFailureText } from "@/lib/request-id";
import { useSearchParams } from "next/navigation";
import { RefreshCw } from "lucide-react";
import { createAccountsClient, createMonitoringClient } from "@sub-pool/contracts/client";
import type { AccountModelConfiguration, ManagedAccount } from "@sub-pool/contracts/accounts";
import type { CollectionJob, CollectionRun } from "@sub-pool/contracts/monitoring";
import {
  AdminButton, Badge, Callout, Card, Drawer, Empty, FreshTag, KV, Modal, PageHeader, Segmented, Table, Tabs, Outcome, Select, Field, Button, Mono,
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
    if (!poolData.ready) return poolData.error ? "读取未完成" : "正在读取…";
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
      setRuntimeRead({ id: account.id, pending: false, error: "此账号的来源或连接不可用于本次状态刷新；已保存的历史状态仍保留。", unknown: false }); return;
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
      setRuntimeRead({ id: account.id, pending: false, unknown: failure.status === 0, error: failure.status === 0 ? requestFailureText(failure.text, "上次运行状态刷新结果尚未确认；请重新读取已保存状态核对，不会自动重试。") : failure.text });
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
        setModelError(failure.status === 0 ? requestFailureText(failure.text, "暂时无法确认模型配置读取。") : failure.text);
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
        setRunError(failure.status === 0 ? requestFailureText(failure.text, "暂时无法确认完整执行结果。批次已记录，请重新读取；不会重新提交采集。") : failure.text);
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
      toast({ tone: "info", title: "采集请求已记录", body: `批次 ${value.id}；请以逐账号执行结果为准。` });
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
        title="账号与额度"
        desc="上游账号运行状态、额度窗口及数据新鲜度。窗口缺失不等于 0，百分比不等于金额。"
        actions={
          <>
            <Segmented value={view} onChange={setView} options={[{ id: "grid", label: "卡片" }, { id: "table", label: "列表" }]} />
            {(readProblem || notice || blocked || runError) && <Button onClick={() => { scheduling.reload(); void recover(); }} disabled={submitting || runtimeRead?.pending}>重新读取</Button>}
            <AdminButton variant="primary" icon={<RefreshCw size={14} />} onClick={() => setRunOpen(true)} disabled={!accountCatalogAvailable || submitting}>
              手动采集
            </AdminButton>
          </>
        }
      />
      {readProblem && <Callout tone="unk">{readProblem}</Callout>}
      {notice && <Callout tone={unknown ? "unk" : "warn"}>{notice}</Callout>}
      {!observationsReady && <Empty title={readProblem ? "暂时无法读取账号" : "正在读取账号与观测…"} />}
      {observationsReady && rows.length === 0 && <Empty title="当前授权范围没有纳管账号" />}
      {view === "grid" ? (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
          {rows.map(row => <AccountCard key={row.account.id} a={row} onClick={() => openAccount(row.account)} />)}
        </div>
      ) : (
        <Card pad={false}>
          <Table rows={rows} rowKey={row => row.account.id} onRow={row => openAccount(row.account)} cols={[
            { key: "n", label: "账号", render: row => <div><div className="font-medium">{row.account.name}</div><Mono>{row.account.upstream_id}</Mono></div> },
            { key: "s", label: "状态", render: row => <StatusBadges a={row.account} attempt={row.observation?.latest_attempt} /> },
            { key: "w1", label: "短周期", align: "right", render: row => quotaWindow(row, "short")?.used_percent == null ? <Badge tone="unk">未知</Badge> : <span className="num">{quotaWindow(row, "short")?.used_percent}%</span> },
            { key: "w2", label: "7 天", align: "right", render: row => !quotaWindow(row, "seven_day") ? <span className="text-faint">未返回</span> : quotaWindow(row, "seven_day")?.used_percent === null ? <Badge tone="unk">未知</Badge> : <span className="num">{quotaWindow(row, "seven_day")?.used_percent}%</span> },
            { key: "f", label: "新鲜度", render: row => <FreshTag s={row.observation?.freshness ?? "unknown"} /> },
            { key: "src", label: "来源", render: row => quotaSource(row) },
            { key: "p", label: "额度池", render: row => poolName(row.account.id) },
          ]} />
        </Card>
      )}

      <Card title={isAdmin ? "采集执行记录" : "所属账号最近执行"} className="mt-6" desc={isAdmin ? `常规频率 ${admin.config ? `${admin.config.interval_seconds} 秒` : "未知"} · 全局最近成功 ${admin.status ? time(admin.status.last_success_at) : "未知"} · 下次计划 ${admin.status ? admin.status.next_scheduled_at ? `${time(admin.status.next_scheduled_at)}（计划）` : admin.status.enabled ? "暂无待安排账号" : "已暂停" : "未知"}` : "仅显示当前授权账号摘要，不包含全局批次与其他账号"} pad={false}>
        {isAdmin ? <Table rows={admin.runs} rowKey={run => run.id} onRow={showRun} cols={[
          { key: "id", label: "批次", render: run => <Mono>{run.id}</Mono> },
          { key: "at", label: "提交时间", render: run => <Timestamp short className="num" value={run.created_at} /> },
          { key: "t", label: "触发", render: run => run.reason === "manual" ? "手动" : "定时" },
          { key: "r", label: "结果", render: run => <Counts run={run} /> },
          { key: "note", label: "状态", render: run => <span className="text-muted"><Outcome k={run.state} /></span> },
        ]} /> : <Table rows={scopedJobs} rowKey={row => row.job.id} cols={[
          { key: "id", label: "账号", render: row => <Mono>{row.account.name}</Mono> },
          { key: "at", label: "执行时间", render: row => <Timestamp short className="num" value={row.job.finished_at ?? row.job.started_at ?? row.job.queued_at} /> },
          { key: "r", label: "结果", render: row => <Outcome k={row.job.state} /> },
          { key: "note", label: "说明", render: row => <span className="text-muted">{jobNote(row.job)}</span> },
        ]} />}
      </Card>

      <Drawer open={!!selectedID} onClose={() => { cancelRuntime(); runtimeTarget.current.id = null; setSelectedID(null); }} title={sel?.account.name ?? "账号读取"}>
        {!sel && <Empty title={observationsReady ? "该账号已不可见或不存在" : "正在读取当前账号…"} />}
        {sel && (
          <>
            <Tabs value={tab} onChange={changeAccountTab} tabs={[{ id: "status", label: "运行状态" }, { id: "quota", label: "额度窗口" }, ...(isAdmin ? [{ id: "models", label: "模型配置" }] : [])]} />
            {tab === "status" && (
              <div className="space-y-4">
                <StatusBadges a={sel.account} />
                {sel.account.snapshot.runtime.error_present === true && <Callout tone="bad" title="上游报告异常">上游报告了异常。本系统不展示原始错误，也不会自动清除错误或关闭限流。</Callout>}
                <KV items={[
                  ["上游名称", <Mono key="n">{sel.account.snapshot.account.name}</Mono>],
                  ["上游 ID", <Mono key="i">{sel.account.upstream_id}</Mono>],
                  ["套餐", "上游未提供可确认套餐"], ["额度池", poolName(sel.account.id)],
                  ["最近使用", sel.account.snapshot.runtime.last_used_at ? time(sel.account.snapshot.runtime.last_used_at) : <Badge tone="unk">未知</Badge>],
                  ["本地监控", sel.account.monitoring ? "已启用（不代表采集成功）" : "已暂停"],
                  ["原生调度配置", !scheduling.allowed ? "未授予调度读取权限" : scheduling.selected && !scheduling.selected.source_current ? "来源已变化，本次未读取" : scheduling.error ?? (scheduling.loading ? "正在读取…" : dispatchObservationText(scheduling.observation))],
                  ["调度读取时间", scheduling.observation ? time(scheduling.observation.checked_at) : "尚无本次读取"],
                  ["整账号暂停 / 恢复", scheduling.allowed ? scheduling.control.statusText(sel.account.id) : "未授予调度读取权限"],
                  ["状态观测时间", time(sel.account.observed_at)], ["状态最近尝试", time(sel.account.last_attempt_at)], ["状态读取结果", runtimeRead?.pending && runtimeRead.id === sel.account.id ? "正在读取上游状态…" : runtimeRead?.error ?? checkText(sel.account.read)],
                  ["最近限流报告", time(sel.account.snapshot.runtime.rate_limited_at)], ["限流重置记录", time(sel.account.snapshot.runtime.rate_limit_reset_at)],
                  ["过载结束记录", time(sel.account.snapshot.runtime.overload_until)], ["临时不可调度记录", time(sel.account.snapshot.runtime.temp_unschedulable_until)],
                ]} />
                <Card title="数据新鲜度">
                  <KV items={[
                    ["最近获取", <span className="num" key="a">{time(sel.observation?.snapshot?.received_at)}</span>],
                    ["最近有效源观测", sel.observation?.snapshot?.source_at ? <span className="num">{time(sel.observation.snapshot.source_at)}</span> : <Badge tone="unk">无法确认源时间</Badge>],
                    ["状态", <FreshTag key="s" s={sel.observation?.freshness ?? "unknown"} />],
                    ["下次计划", sel.observation?.next_scheduled_at ? time(sel.observation.next_scheduled_at) : "无可确认计划"],
                    ["最近尝试", sel.observation?.latest_attempt ? <Outcome k={sel.observation.latest_attempt.state} /> : "暂无记录"],
                    ["尝试说明", sel.observation?.latest_attempt ? jobNote(sel.observation.latest_attempt) : "暂无记录"],
                    ["重试限制至", time(sel.observation?.latest_attempt?.retry_after)],
                  ]} />
                  {(sel.error || sel.observation?.freshness !== "fresh" || sel.observation?.latest_attempt?.state === "failed" || sel.observation?.latest_attempt?.state === "interrupted") && (
                    <Callout tone="warn" className="mt-3">{sel.error ?? "最近尝试与历史窗口分别保留；可见窗口不代表本次采集成功，后续业务仍须独立核验源时间、状态和范围。"}</Callout>
                  )}
                </Card>
                <AccountAllocation key={sel.account.id} accountID={sel.account.id} pools={poolData} />
              </div>
            )}
            {tab === "quota" && (
              <div className="space-y-4">
                <WindowRow w={quotaWindow(sel, "short")} label="短周期窗口" source={quotaSource(sel)} />
                <WindowRow w={quotaWindow(sel, "seven_day")} label="7 天窗口" source={quotaSource(sel)} />
                <Callout tone="info">{sel.observation?.snapshot?.simulation ? "当前窗口来自模拟协议，不证明原版能力。" : "已用比例来自上游额度窗口，不是美元余额。"}源观测：{time(sel.observation?.snapshot?.source_at)}；新鲜度：<FreshTag s={sel.observation?.freshness ?? "unknown"} />。本系统不会主动使用重置额度或重置卡；并非每种套餐都提供 7 天窗口。</Callout>
              </div>
            )}
            {tab === "models" && isAdmin && (
              <div className="space-y-3">
                {!models ? <Callout tone="unk" title={modelError ? "无法读取模型配置" : "正在读取模型配置…"}>
                  {modelError ?? "按当前账号、来源和配置版本核对。"}
                  {modelError && <div className="mt-2"><Button onClick={() => setReadVersion(value => value + 1)}>重新读取</Button></div>}
                </Callout> : (
                  <>
                    <Callout tone="info">账号配置：{checkText(models.account_read)}；原生目录：{checkText(models.catalog_read)}。{models.mapping_mode === "passthrough" ? "已明确开启透传。" : models.mapping_mode === "empty" ? "明确返回空映射，不代表全部禁用。" : models.mapping_mode === "configured" ? "存在明确映射，未映射模型的访问仍未证明。" : "映射方式未知。"}核对时间：{time(models.checked_at)}</Callout>
                    <Table rows={modelRows(models)} rowKey={model => model.requested} cols={[
                      { key: "r", label: "原生模型 / 映射键", render: model => <Mono>{model.requested}</Mono> },
                      { key: "m", label: "明确映射到", render: model => model.mapped ? <Mono>{model.mapped}</Mono> : <Badge tone="unk">未确定</Badge> },
                    ]} />
                  </>
                )}
                <Callout tone="warn">目录仅供参考，配置可读不代表实际访问已验证，也不证明单模型屏蔽可靠。本系统不提供临时禁用单个模型的操作。</Callout>
              </div>
            )}
          </>
        )}
      </Drawer>

      <Modal open={runOpen && isAdmin} onClose={() => { if (!submitting) setRunOpen(false); }} title="手动执行一次采集" footer={
        <>
          <Button onClick={() => setRunOpen(false)} disabled={submitting}>关闭</Button>
          {(runError || notice || blocked) && <Button onClick={() => void recover()} disabled={submitting}>重新读取</Button>}
          <Button variant="primary" onClick={() => void run()} disabled={running || blocked || !bindingReady || !accountCatalogAvailable} icon={<RefreshCw size={14} className={running ? "animate-spin" : ""} />}>
            {submitting ? "正在提交…" : running ? "采集中…" : "开始采集"}
          </Button>
        </>
      }>
        <div className="space-y-3">
          <Field label="采集范围">
            <Select value={runScope} onChange={event => setRunScope(event.target.value)} disabled={running || blocked} options={[{ value: "all", label: "全部纳管账号（跳过暂停）" }, ...accounts.map(account => ({ value: account.id, label: account.name }))]} />
          </Field>
          <Callout tone="info">手动采集不会绕过上游限流；单账号可明确读取一次暂停账号。请求被记录后仍需等待各账号结果，已有运行任务会报告冲突。</Callout>
          {!bindingReady && <Callout tone="unk">请先保存有效连接凭据，并重新读取当前连接与账号版本。</Callout>}
          {notice && <Callout tone={unknown ? "unk" : "warn"}>{notice}{unknown ? " 本页已阻止再次提交；可核对执行记录，读取不会重放这次请求。" : ""}</Callout>}
          {activeRun && <Callout tone="info">批次 <Mono>{activeRun.id}</Mono> · <Outcome k={activeRun.state} /><Counts run={activeRun} /></Callout>}
          {runError && <Callout tone="unk">{runError}</Callout>}
          {runJobs.length > 0 && <Table rows={runJobs} rowKey={job => job.id} cols={[
            { key: "a", label: "账号", render: job => accounts.find(account => account.id === job.account_id)?.name ?? job.account_id },
            { key: "r", label: "结果", render: job => <Outcome k={job.state} /> },
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
