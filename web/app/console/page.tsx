"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowRight, Activity, HeartPulse } from "lucide-react";
import { Badge, Card, Grid, Outcome, PageHeader, Stat, Val, Callout, KV, Button, Empty } from "@/components/ui";
import { AccountCard } from "@/components/quota";
import { compactTime, jobNote } from "@/lib/accounts-data";
import { displayTime } from "@/lib/connection-data";
import { useAccountData } from "@/lib/use-account-data";
import { useApp } from "@/lib/store";
import { useRecordedUsage } from "@/lib/use-recorded-usage";
import { useWalletData } from "@/lib/use-wallet-data";
import { useTemporaryStatus } from "@/lib/use-temporary-status";
import { temporaryOverview, temporaryStateNames } from "@/lib/temporary-data";
import { useDispatchData } from "@/lib/use-dispatch-data";
import { usePricingHistory } from "@/lib/use-pricing-history";
import { overviewReview } from "@/lib/overview-data";

export default function Overview() {
  const data = useAccountData();
  const { isAdmin, accounts, rows, accountCatalogAvailable, accountCatalogError, observationError, observationsReady, readAt, admin, reload } = data;
  const recorded = useRecordedUsage(data);
  const wallets = useWalletData();
  const temporary = useTemporaryStatus();
  const dispatch = useDispatchData();
  const pricing = usePricingHistory();
  const review = overviewReview(isAdmin, pricing.operations, dispatch.control.ready ? dispatch.control.events : null);
  const { timezone } = useApp();
  const router = useRouter();
  const latestRun = admin.runs[0];
  const recentAttempt = rows.flatMap(row => row.observation?.latest_attempt ? [row.observation.latest_attempt] : [])
    .sort((a, b) => BigInt(a.id) > BigInt(b.id) ? -1 : BigInt(a.id) < BigInt(b.id) ? 1 : 0)[0];
  const attention = rows.filter(row => row.error || row.observation?.stale_warning || ["failed", "interrupted", "conflict"].includes(row.observation?.latest_attempt?.state ?? "")
    || ["source_changed", "connection_changed", "connection_unavailable", "identity_unverified", "source_mode_changed"].includes(row.observation?.freshness ?? ""))
    .map(row => ({ tone: row.observation?.latest_attempt?.state === "failed" ? "bad" as const : "warn" as const,
      text: `${row.account.name}：${row.error ?? (row.observation?.latest_attempt && row.observation.latest_attempt.state !== "succeeded" ? jobNote(row.observation.latest_attempt) : "源数据需要核对，历史窗口保留来源与时间")}`,
      href: `/console/accounts?id=${row.account.id}` }));
  const time = (value: string | null | undefined) => displayTime(value, timezone);
  const problem = accountCatalogError ?? observationError ?? admin.error ?? wallets.error ?? temporary.error ?? dispatch.error ?? dispatch.control.error ?? pricing.error;

  return (
    <>
      <PageHeader
        title="概览"
        desc={`页面读取 ${readAt ? time(readAt) : "尚未完成"}。各账号源时间单独列示；未知不会以 0 呈现。`}
        actions={
          <>
            {problem && <Button onClick={() => { void reload(); wallets.reload(); temporary.reload(); dispatch.reload(); pricing.reload(); }}>重新读取</Button>}
            {isAdmin && <Button onClick={() => router.push("/console/accounts")} icon={<Activity size={14} />}>采集状态</Button>}
          </>
        }
      />
      {problem && <Callout tone="unk">{problem}</Callout>}
      <Grid cols={4}>
        <Stat label={isAdmin ? "纳管账号" : "可见账号"} value={accountCatalogAvailable ? accounts.length : <Val v={null} />} sub={observationsReady ? `${rows.filter(row => row.observation?.freshness === "fresh").length} 个源数据新鲜；最近尝试另行判断` : "观测尚未完整读取"} />
        <Stat
          label={isAdmin ? "最近批次提交" : "所属账号最近尝试"}
          value={<span className="text-[18px]">{isAdmin ? latestRun ? compactTime(latestRun.created_at, timezone) : "暂无可确认记录" : recentAttempt ? <Outcome k={recentAttempt.state} /> : "暂无可确认记录"}</span>}
          sub={<span>{isAdmin ? latestRun ? <>成功 {latestRun.counts.succeeded} · <span className="text-bad">失败 {latestRun.counts.failed}</span> · 跳过 {latestRun.counts.skipped} · 冲突 {latestRun.counts.conflict} · 中断 {latestRun.counts.interrupted} · 排队 {latestRun.counts.queued} · 执行 {latestRun.counts.running}</> : "尚未读取到全局执行结果" : recentAttempt ? time(recentAttempt.finished_at ?? recentAttempt.started_at ?? recentAttempt.queued_at) : "仅统计当前授权账号"}</span>}
        />
        <Stat label={isAdmin ? "已记录未归属费用" : "可见成员已记录费用"} value={<Val v={isAdmin ? recorded.data?.view?.unassigned?.metrics.total_cost : recorded.data?.view?.summary.metrics.total_cost} prefix="$" />} sub={recorded.data?.view ? `${recorded.data.view.start_at} 至 ${recorded.data.view.end_at}；仅已记录账号，非完整账期或钱包扣费${recorded.data.missingAccounts.length ? `；另有 ${recorded.data.missingAccounts.length} 个可见账号无捕获` : ""}` : recorded.error ?? (recorded.ready ? "尚无可确认捕获，不代表费用为零" : "正在读取已记录消费")} />
        <Stat label="待人工核对" value={<Val v={review.count} digits={0} />} tone="unk" sub={review.text} />
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
        <h2 className="text-[15px] font-semibold">账号额度窗口</h2>
        <Link href="/console/accounts" className="text-[12.5px] text-brand hover:underline">查看全部</Link>
      </div>
      {!observationsReady && <Empty title={problem ? "账号数据不可用" : "正在读取账号与观测…"} />}
      {observationsReady && rows.length === 0 && <Empty title="当前授权范围没有纳管账号" />}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {rows.map(row => <AccountCard key={row.account.id} a={row} onClick={() => router.push(`/console/accounts?id=${row.account.id}`)} />)}
      </div>

      <Grid cols={isAdmin ? 3 : 2} className="mt-6">
        <Card
          title={isAdmin ? "参与者余额" : "我的余额"}
          desc={!wallets.allowed ? "未授予余额读取权限。" : wallets.error ?? (wallets.ready ? "最近完整原生读数；币种未核验，冻结不重复扣除。当前建议不可用，最近记录为本地请求。" : "正在完整读取授权内的钱包与请求记录…")}
          className={isAdmin ? "lg:col-span-2" : ""}
          pad={false}
          actions={<Link href="/console/balances" className="text-[12.5px] text-brand hover:underline">余额建议</Link>}
        >
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead>
                <tr className="border-b border-line text-left text-[12px] text-muted">
                  <th className="px-4 py-2 font-medium">参与者</th>
                  <th className="px-4 py-2 text-right font-medium">余额</th>
                  <th className="px-4 py-2 text-right font-medium">冻结</th>
                  <th className="px-4 py-2 text-right font-medium">当前建议</th>
                  <th className="px-4 py-2 font-medium">最近应用</th>
                </tr>
              </thead>
              <tbody>
                {wallets.rows.filter(p => p.enabled).map(p => (
                  <tr key={p.id} className="border-b border-line last:border-0">
                    <td className="px-4 py-2.5">{p.name}</td>
                    <td className="px-4 py-2.5 text-right" title={`${time(p.balanceReadAt)} · ${p.readNote}`}><Val v={p.balance} /></td>
                    <td className="px-4 py-2.5 text-right text-muted" title={time(p.balanceReadAt)}><Val v={p.frozen} /></td>
                    <td className="px-4 py-2.5 text-right"><Badge tone="unk">暂无可用建议</Badge></td>
                    <td className="px-4 py-2.5"><span className="text-faint">{p.lastApplied ? <Outcome k={p.lastApplied.outcome} /> : "尚无本地请求"}</span></td>
                  </tr>
                ))}
                {!wallets.rows.some(p => p.enabled) && <tr className="border-b border-line last:border-0"><td className="px-4 py-2.5" colSpan={5}>{wallets.error ?? (!wallets.allowed ? "未授予余额读取权限。" : wallets.ready ? "授权范围内没有启用参与者；空列表不代表余额为零。" : "尚无可确认数据；空列表不代表余额为零。")}</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>

        {isAdmin ? (
          <div className="space-y-4">
            <Card title="临时放宽" actions={<Link href="/console/temporary" className="text-[12.5px] text-brand hover:underline">详情</Link>}>
              <div className="flex items-center gap-2">
                <Badge tone="unk" dot>{temporary.status ? temporaryStateNames[temporary.status.state] : "轮次状态未知"}</Badge>
                <Badge tone="muted">{temporary.status ? "能力已读取" : temporary.error ? "读取失败" : "正在读取"}</Badge>
              </div>
              <p className="mt-2 text-[12px] text-muted">{temporary.status ? temporaryOverview(temporary.status) : temporary.error ?? "正在读取临时参与可用性；尚无可确认的轮次记录。"}</p>
            </Card>
            <Card title="系统健康" desc={`进程与采集分别判断 · 查询 ${time(admin.checkedAt)}`}>
              <KV cols={1} items={[
                ["应用进程", <Badge key="a" tone={admin.health ? "ok" : "unk"} dot>{admin.health ? "本次查询可响应" : "尚未确认"}</Badge>],
                ["数据库", <Badge key="b" tone="unk" dot>未独立检测</Badge>],
                ["采集任务", <Badge key="c" tone={admin.status?.worker_recent ? "info" : "unk"} dot>{admin.status ? `${admin.status.worker_recent ? "最近有工作心跳" : "工作心跳未确认"} · 定时${admin.status.enabled ? "启用" : "关闭"}` : "尚未读取"}</Badge>],
                ["调度控制", <Badge key="d" tone="unk" dot>{dispatch.control.statusText()}</Badge>],
                ["心跳记录", time(admin.status?.worker_seen_at)],
              ]} />
              <div className="mt-3 flex items-center gap-1.5 text-[11.5px] text-faint"><HeartPulse size={12} /> 进程健康不代表采集与远端写入均正常</div>
            </Card>
          </div>
        ) : (
          <Card title="说明">
            <Callout tone="info">你当前以只读身份访问，仅显示已授权且实际读取到的账号（{accountCatalogAvailable ? accounts.map(account => account.name).join("、") || "范围为空" : "尚未确认"}）。汇总使用同一范围；钱包独立按参与者权限读取，全局批次、模型配置及其他账号均不在此展示。{dispatch.allowed ? dispatch.error ?? dispatch.control.statusText() : "未授予调度读取权限。"}</Callout>
          </Card>
        )}
      </Grid>
    </>
  );
}
