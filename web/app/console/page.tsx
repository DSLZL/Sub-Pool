"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowRight, Activity, Check, HeartPulse } from "lucide-react";
import { createPoolsClient } from "@sub-pool/contracts/client";
import { Badge, Card, Grid, Outcome, PageHeader, Stat, Val, Callout, KV, Button, Empty } from "@/components/ui";
import { AccountCard } from "@/components/quota";
import { compactTime, jobNote } from "@/lib/accounts-data";
import { connectionFailure, displayTime } from "@/lib/connection-data";
import { useAccountData } from "@/lib/use-account-data";
import { useApp } from "@/lib/store";
import { useRecordedUsage } from "@/lib/use-recorded-usage";
import { useWalletData } from "@/lib/use-wallet-data";
import { CursorNavigation } from "@/components/cursor-navigation";
import { useDispatchData } from "@/lib/use-dispatch-data";
import { usePricingHistory } from "@/lib/use-pricing-history";
import { firstUseSteps, overviewReview } from "@/lib/overview-data";
import { readPoolPages } from "@/lib/pools-data";

export default function Overview() {
  const data = useAccountData();
  const { isAdmin, accounts, rows, accountCatalogAvailable, accountCatalogError, observationError, observationsReady, readAt, admin, reload } = data;
  const recorded = useRecordedUsage(data);
  const wallets = useWalletData();
  const dispatch = useDispatchData();
  const pricing = usePricingHistory();
  const review = overviewReview(isAdmin, pricing.operations, dispatch.control.ready ? dispatch.control.events : null);
  const { auth, displayTimezone, settingsRevision, settingsSavedAt, refreshSession, refreshSettings } = useApp();
  const router = useRouter();
  const setupConnection = isAdmin ? admin.connection : null;
  const source = setupConnection?.credential_set ? setupConnection.source_id : null;
  const participantReady = source && data.participantCatalogAvailable
    ? data.participantCatalog.some(member => member.enabled && member.source_current && member.binding.source_id === source)
    : null;
  const poolBasis = useMemo(() => setupConnection && source && participantReady
    ? { epoch: auth.epoch, source, revision: setupConnection.revision } : null,
  [auth.epoch, setupConnection, source, participantReady]);
  const [poolRead, setPoolRead] = useState<{ basis: NonNullable<typeof poolBasis>; ready: boolean | null } | null>(null);
  useEffect(() => {
    if (!poolBasis) return;
    const abort = new AbortController();
    const client = createPoolsClient(window.location.origin);
    void readPoolPages(after => client.list({ after, search: "", limit: 100 }, { signal: abort.signal })).then(pools => {
      if (!abort.signal.aborted) setPoolRead({
        basis: poolBasis, ready: pools.some(pool => pool.enabled && pool.source_current && pool.source_id === poolBasis.source),
      });
    }, error => {
      if (abort.signal.aborted) return;
      setPoolRead({ basis: poolBasis, ready: null });
      if ([401, 403, 404].includes(connectionFailure(error).status)) void refreshSession();
    });
    return () => abort.abort();
  }, [poolBasis, refreshSession]);
  const currentPoolRead = poolRead?.basis === poolBasis ? poolRead : null;
  const setup = isAdmin ? firstUseSteps({
    timezoneSaved: settingsRevision === null ? null : settingsSavedAt !== null,
    connection: setupConnection,
    catalog: accountCatalogAvailable ? { revision: data.connectionRevision, accounts } : null,
    policy: admin.config,
    participantReady,
    poolReady: currentPoolRead?.ready ?? null,
  }) : [];
  const setupError = isAdmin ? data.participantCatalogError ?? (currentPoolRead?.ready === null ? "池待核对。"
    : displayTimezone === null ? "时区待核对。" : null) : null;
  const setupFailed = !!(setupError ?? admin.error ?? accountCatalogError);
  const showSetup = setup.some(step => step.done === false || (setupFailed && step.done === null));
  const readerWithoutAccounts = !isAdmin && (accountCatalogAvailable && accounts.length === 0
    || auth.status === "authenticated" && !!auth.access && !auth.access.features.includes("accounts"));
  const latestRun = admin.runs[0];
  const recentAttempt = rows.flatMap(row => row.observation?.latest_attempt ? [row.observation.latest_attempt] : [])
    .sort((a, b) => BigInt(a.id) > BigInt(b.id) ? -1 : BigInt(a.id) < BigInt(b.id) ? 1 : 0)[0];
  const attention = rows.filter(row => row.error || row.observation?.stale_warning || ["failed", "interrupted", "conflict"].includes(row.observation?.latest_attempt?.state ?? "")
    || ["source_changed", "connection_changed", "connection_unavailable", "identity_unverified", "source_mode_changed"].includes(row.observation?.freshness ?? ""))
    .map(row => ({ tone: row.observation?.latest_attempt?.state === "failed" ? "bad" as const : "warn" as const,
      text: `${row.account.name}：${row.error ?? (row.observation?.latest_attempt && row.observation.latest_attempt.state !== "succeeded" ? jobNote(row.observation.latest_attempt) : "数据待核对")}`,
      href: `/console/accounts?id=${row.account.id}` }));
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);
  const problem = accountCatalogError ?? observationError ?? admin.error ?? wallets.error ?? dispatch.error ?? dispatch.control.error ?? pricing.error ?? setupError;

  return (
    <>
      <PageHeader
        title="概览"
        desc={readAt ? `读取于 ${time(readAt)}` : uiCopy.loading} helpTopic="authority"
        actions={
          <>
            {problem && <Button onClick={() => {
              void reload(); wallets.reload(); dispatch.reload(); pricing.reload();
              if (isAdmin) { void refreshSettings(); void data.reloadParticipants(); }
            }}>{uiCopy.reload}</Button>}
            {isAdmin && <Button onClick={() => router.push("/console/accounts")} icon={<Activity size={14} />}>采集状态</Button>}
          </>
        }
      />
      {problem && <Callout tone="unk">{problem}</Callout>}
      <Grid cols={4}>
        <Stat label={isAdmin ? "纳管账号" : "可见账号"} value={accountCatalogAvailable ? accounts.length : <Val v={null} />} sub={observationsReady ? `${rows.filter(row => row.observation?.freshness === "fresh").length} 个数据新鲜` : "观测不齐"} />
        <Stat
          label={isAdmin ? "批次时间" : "最近尝试"}
          value={<span className="text-[18px]">{isAdmin ? latestRun ? compactTime(latestRun.created_at, displayTimezone) : uiCopy.noRecords : recentAttempt ? <Outcome k={recentAttempt.state} /> : uiCopy.noRecords}</span>}
          sub={<span>{isAdmin ? latestRun ? <>成功 {latestRun.counts.succeeded} · <span className="text-bad">失败 {latestRun.counts.failed}</span> · 跳过 {latestRun.counts.skipped} · 冲突 {latestRun.counts.conflict} · 中断 {latestRun.counts.interrupted} · 排队 {latestRun.counts.queued} · 执行 {latestRun.counts.running}</> : "暂无结果" : recentAttempt ? time(recentAttempt.finished_at ?? recentAttempt.started_at ?? recentAttempt.queued_at) : "授权账号"}</span>}
        />
        <Stat label={isAdmin ? "未归属费用" : "成员费用"} value={<Val v={isAdmin ? recorded.data?.view?.unassigned?.metrics.total_cost : recorded.data?.view?.summary.metrics.total_cost} prefix="$" />} sub={recorded.data?.view ? `${time(recorded.data.view.start_at)} 至 ${time(recorded.data.view.end_at)}${recorded.data.missingAccounts.length ? `；另有 ${recorded.data.missingAccounts.length} 个账号待采集` : ""}` : recorded.error ?? (recorded.ready ? "暂无费用" : `读取中${glossary.recordedConsumption}`)} />
        <Stat label={uiCopy.pendingReview} value={<Val v={review.count} digits={0} />} tone={review.count === 0 ? undefined : "unk"} sub={review.count === null ? uiCopy.loading : review.count ? <>{isAdmin && <><Link href="/console/pricing" className="text-brand hover:underline">核对计价</Link> · </>}<Link href="/console/dispatch" className="text-brand hover:underline">核对暂停</Link></> : uiCopy.noRecords} />
      </Grid>

      {attention.length > 0 && (
        <Card title="需要关注" className="mt-4" pad={false}>
          <ul className="divide-y divide-line">
            {attention.map((a, i) => (
              <li key={i}>
                <Link href={a.href} className="flex items-center gap-3 px-4 py-2.5 text-[13px] hover:bg-surface-2/60">
                  <span className={`h-2 w-2 shrink-0 rounded-full ${{ bad: "bg-bad", warn: "bg-warn", unk: "bg-unk" }[a.tone]}`} />
                  <span className="flex-1">{a.text}</span>
                  <ArrowRight size={14} className="text-faint" />
                </Link>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <div className="mt-6 mb-3 flex items-center justify-between">
        <h2 className="text-[15px] font-semibold">额度窗口</h2>
        <Link href="/console/accounts" className="text-[12.5px] text-brand hover:underline">查看全部</Link>
      </div>
      {showSetup && (
        <Card title="开始使用" className="mb-4">
          <ol className="divide-y divide-line">
            {setup.map(step => (
              <li key={step.label} className="flex flex-wrap items-center justify-between gap-2 py-2 text-[13px]">
                <span className="font-medium">{step.label}</span>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone={step.done ? "ok" : step.done === null ? "unk" : "muted"}>
                    {step.done && <Check size={12} aria-hidden />}
                    {step.done ? "已完成" : step.done === null ? uiCopy.pendingReview : "待完成"}
                  </Badge>
                  {!step.done && step.href && <Link href={step.href} className="text-[12.5px] text-brand hover:underline">{step.entry}</Link>}
                </div>
              </li>
            ))}
          </ol>
        </Card>
      )}
      {!showSetup && !readerWithoutAccounts && !observationsReady && <Empty title={problem ? "账号不可用" : uiCopy.loading} />}
      {!showSetup && (readerWithoutAccounts || observationsReady && rows.length === 0) &&
        <Empty title={uiCopy.noAccounts}>{!isAdmin && "联系管理员。"}</Empty>}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {rows.map(row => <AccountCard key={row.account.id} a={row} onClick={() => router.push(`/console/accounts?id=${row.account.id}`)} />)}
      </div>

      <Grid cols={isAdmin ? 3 : 2} className="mt-6">
        <Card
          title={isAdmin ? "参与者余额" : "我的余额"}
          desc={!wallets.allowed ? "无钱包权限。" : wallets.error ?? (wallets.ready ? "确认读数" : "读取余额…")}
          className={isAdmin ? "self-start lg:col-span-2" : ""}
          pad={false}
          actions={<Link href="/console/balances" className="text-[12.5px] text-brand hover:underline">余额建议</Link>}
        >
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead>
                <tr className="border-b border-line text-left text-[12px] text-muted">
                  <th className="px-4 py-2 font-medium">{uiCopy.participant}</th>
                  <th className="px-4 py-2 text-right font-medium">余额</th>
                  <th className="px-4 py-2 text-right font-medium">冻结</th>
                  <th className="px-4 py-2 font-medium">最近应用</th>
                </tr>
              </thead>
              <tbody>
                {wallets.rows.filter(p => p.enabled).map(p => (
                  <tr key={p.id} className="border-b border-line last:border-0">
                    <td className="px-4 py-2.5">{p.name}</td>
                    <td className="px-4 py-2.5 text-right" title={`${time(p.balanceReadAt)} · ${p.readNote}`}><Val v={p.balance} /></td>
                    <td className="px-4 py-2.5 text-right text-muted" title={time(p.balanceReadAt)}><Val v={p.frozen} /></td>
                    <td className="px-4 py-2.5"><span className="text-faint">{p.lastApplied ? <Outcome k={p.lastApplied.outcome} /> : "暂无请求"}</span></td>
                  </tr>
                ))}
                {!wallets.rows.some(p => p.enabled) && <tr className="border-b border-line last:border-0"><td className="px-4 py-2.5" colSpan={4}>{wallets.error ?? (!wallets.allowed ? "无钱包权限。" : wallets.ready ? "暂无参与者。" : "暂无读数。")}</td></tr>}
              </tbody>
            </table>
          </div>
          <CursorNavigation after={wallets.walletAfter} next={wallets.walletNext} onChange={wallets.changeWalletPage} />
        </Card>

        {isAdmin ? (
          <div className="space-y-4">
            <Card title="系统健康" desc={` 查询于 ${time(admin.checkedAt)}`}>
              <KV cols={1} items={[
                ["应用进程", <Badge key="a" tone={admin.health ? "ok" : "unk"} dot>{admin.health ? "查询正常" : "未确认"}</Badge>],
                ["采集任务", <Badge key="c" tone={admin.status?.worker_recent ? "info" : "unk"} dot>{admin.status ? `${admin.status.worker_recent ? "任务活跃" : "任务待核对"} · 定时${admin.status.enabled ? "启用" : "关闭"}` : "未读取"}</Badge>],
                ["调度控制", <Badge key="d" tone="unk" dot className="max-w-full whitespace-normal!">{dispatch.control.statusText()}</Badge>],
                ["心跳记录", time(admin.status?.worker_seen_at)],
              ]} />
              <div className="mt-3 flex items-center gap-1.5 text-[11.5px] text-faint"><HeartPulse size={12} /> </div>
            </Card>
          </div>
        ) : (
          <Card title="说明">
            <Callout tone="info">{dispatch.allowed ? dispatch.error ?? dispatch.control.statusText() : "无调度权限。"}</Callout>
          </Card>
        )}
      </Grid>
    </>
  );
}
