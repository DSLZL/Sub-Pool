"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowRight, Activity, HeartPulse } from "lucide-react";
import { Badge, Card, Grid, Outcome, PageHeader, Stat, Val, Callout, KV, Button } from "@/components/ui";
import { AccountCard } from "@/components/quota";
import { useScope } from "@/lib/scope";
import { attribution, balanceOps, jobs, NOW, participantName, tempRound, pauses, accountName } from "@/lib/mock";

export default function Overview() {
  const { isAdmin, accounts, participants } = useScope();
  const router = useRouter();
  const failing = accounts.filter((a) => a.freshness === "failed" || a.freshness === "stale");
  const pendingOps = balanceOps.filter((o) => (o.outcome === "unknown" || o.outcome === "reconcile") && participants.some((p) => p.id === o.participant));

  const attention: { tone: "bad" | "warn" | "unk"; text: string; href: string }[] = [];
  failing.forEach((a) =>
    attention.push({
      tone: a.freshness === "failed" ? "bad" : "warn",
      text: `${a.localName}：${a.freshness === "failed" ? "采集失败" : "源数据已过期"}，相关估算与建议暂不可应用`,
      href: "/accounts",
    }),
  );
  pendingOps.forEach((o) =>
    attention.push({ tone: "unk", text: `${participantName(o.participant)} 的余额应用 ${o.id} 结果未知，需要人工核对`, href: "/balances" }),
  );
  if (isAdmin) {
    pauses
      .filter((p) => p.state === "reconcile")
      .forEach((p) => attention.push({ tone: "warn", text: `${accountName(p.account)} 恢复调度前检测到外部改动（${p.id}）`, href: "/dispatch" }));
  }

  return (
    <>
      <PageHeader
        title="概览"
        desc={`数据截至 ${NOW}（Asia/Shanghai）。所有数值保留来源与时间；未知不会以 0 呈现。`}
        actions={
          isAdmin && (
            <Button onClick={() => router.push("/accounts")} icon={<Activity size={14} />}>
              采集状态
            </Button>
          )
        }
      />

      <Grid cols={4}>
        <Stat label={isAdmin ? "纳管账号" : "可见账号"} value={accounts.length} sub={`${accounts.filter((a) => a.freshness === "fresh").length} 个数据新鲜`} />
        <Stat
          label="最近一次采集"
          value={<span className="text-[18px]">{jobs.lastRun.at.slice(11)}</span>}
          sub={
            <span>
              成功 {jobs.lastRun.ok} · <span className="text-bad">失败 {jobs.lastRun.failed}</span> · 下次 {jobs.nextRun.slice(11)}
            </span>
          }
        />
        <Stat
          label={isAdmin ? "本周期未归属消耗" : "本人本周期消费"}
          value={isAdmin ? <Val v={attribution.unattributed} prefix="$" /> : <Val v={participants[0]?.cycleSpend ?? null} prefix="$" />}
          sub={isAdmin ? "部分原因待证实，不自动摊给成员" : "标准费用口径"}
        />
        <Stat label="待人工核对" value={pendingOps.length} tone={pendingOps.length ? "unk" : undefined} sub="结果未知的远端写入" />
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
        <Link href="/accounts" className="text-[12.5px] text-brand hover:underline">
          查看全部
        </Link>
      </div>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        {accounts.map((a) => (
          <AccountCard key={a.id} a={a} onClick={() => router.push(`/accounts?id=${a.id}`)} />
        ))}
      </div>

      <Grid cols={isAdmin ? 3 : 2} className="mt-6">
        <Card
          title={isAdmin ? "参与者余额" : "我的余额"}
          desc="下游用户全局钱包；冻结余额单独列示，不再从余额中扣减"
          className={isAdmin ? "lg:col-span-2" : ""}
          pad={false}
          actions={
            <Link href="/balances" className="text-[12.5px] text-brand hover:underline">
              余额建议
            </Link>
          }
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
                {participants
                  .filter((p) => p.status === "active")
                  .map((p) => (
                    <tr key={p.id} className="border-b border-line last:border-0">
                      <td className="px-4 py-2.5">{p.name}</td>
                      <td className="px-4 py-2.5 text-right">
                        <Val v={p.balance} />
                      </td>
                      <td className="px-4 py-2.5 text-right text-muted">
                        <Val v={p.frozen} />
                      </td>
                      <td className="px-4 py-2.5 text-right">
                        {p.recommendation === null ? <Badge tone="unk">暂无可用建议</Badge> : <Val v={p.recommendation} />}
                      </td>
                      <td className="px-4 py-2.5">{p.lastApplied ? <Outcome k={p.lastApplied.outcome} /> : <span className="text-faint">—</span>}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        </Card>

        {isAdmin ? (
          <div className="space-y-4">
            <Card title="临时放宽" actions={<Link href="/temporary" className="text-[12.5px] text-brand hover:underline">详情</Link>}>
              <div className="flex items-center gap-2">
                <Badge tone="warn" dot>
                  轮次 {tempRound.id} 进行中
                </Badge>
                <Badge tone="muted">{tempRound.mode === "carry" ? "结转" : "不结转"}</Badge>
              </div>
              <p className="mt-2 text-[12px] text-muted">{tempRound.endBasis}</p>
            </Card>
            <Card title="系统健康" desc="进程存活与监控业务状态分别判断">
              <KV
                cols={1}
                items={[
                  ["应用进程", <Badge key="a" tone="ok" dot>可响应</Badge>],
                  ["数据库", <Badge key="b" tone="ok" dot>连接正常</Badge>],
                  ["采集任务", <Badge key="c" tone="warn" dot>运行中 · 1 个账号失败</Badge>],
                  ["远端写入", <Badge key="d" tone="unk" dot>1 项结果未知</Badge>],
                ]}
              />
              <div className="mt-3 flex items-center gap-1.5 text-[11.5px] text-faint">
                <HeartPulse size={12} /> 进程健康不代表采集与远端写入均正常
              </div>
            </Card>
          </div>
        ) : (
          <Card title="说明">
            <Callout tone="info">
              你当前以只读身份访问，仅能看到被授权的账号（{accounts.map((a) => a.localName).join("、")}）与本人参与者数据。汇总数据同样按此范围收窄。
            </Callout>
          </Card>
        )}
      </Grid>
    </>
  );
}
