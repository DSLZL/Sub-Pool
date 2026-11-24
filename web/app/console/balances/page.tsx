"use client";

import { useEffect, useState } from "react";
import { Send, SearchCheck } from "lucide-react";
import { DateTimeField } from "@/components/date-time-field";
import { CursorNavigation } from "@/components/cursor-navigation";
import {
  AdminButton,
  Badge,
  Button,
  Callout,
  Card,
  Checkbox,
  Drawer,
  GateNotice,
  Grid,
  Field,
  Input,
  Select,
  KV,
  Modal,
  Mono,
  Outcome,
  PageHeader,
  Switch,
  Table,
  Tabs,
  Val,
} from "@/components/ui";
import { useApp } from "@/lib/store";
import { useScope } from "@/lib/scope";
import { useWalletData } from "@/lib/use-wallet-data";
import { displayTime } from "@/lib/connection-data";
import { walletTimeRange, walletTimeMatches, type WalletRow, type WalletOperation } from "@/lib/wallets-data";
import { estimateAmountText } from "@/lib/estimates-data";
import { canReconcileWallet, walletControl } from "@/lib/wallet-actions";
import { nativeWalletDelta } from "@sub-pool/contracts/wallet-applications";
import { WeeklyWalletDialog } from "../pools/weekly-wallet";

const REC_STATE: Record<WalletRow["recState"], { label: string; tone: "ok" | "unk" | "warn" | "bad" | "muted" }> = {
  scenario: { label: "模拟条件建议", tone: "warn" },
  insufficient: { label: "依据不足", tone: "unk" },
  stale: { label: "已失效", tone: "muted" },
  blocked: { label: "仅建议", tone: "bad" },
};

export default function BalancesPage() {
  const { displayTimezone, auth } = useApp();
  const [tab, setTab] = useState("recs");
  const [operationFilter, setOperationFilter] = useState({ participant: "", start: "", end: "" });
  const operationRange = walletTimeRange(operationFilter.start, operationFilter.end);
  const data = useWalletData(tab === "ops" && !operationRange.error
    ? { participant_id: operationFilter.participant, start_at: operationFilter.start.trim(), end_at: operationFilter.end.trim() } : null);
  const { accounts } = useScope();
  const { isAdmin, rows: participants } = data;
  const gateOpen = data.ready && participants.some(p => data.canApply(p.view));
  const sel = data.participant, reconcile = data.operation;
  const reconciledNativeBalance = reconcile?.record.unit === "native_wallet" ? reconcile.after : null;
  const accountNames = new Map(accounts.map(account => [account.id, account.name]));
  const setSel = (row: WalletRow | null) => row ? void data.inspectParticipant(row.id) : data.closeDetail();
  const setReconcile = (row: WalletOperation | null) => row ? void data.inspectOperation(row.id) : data.closeDetail();
  const applyTarget = data.applyTarget;
  const [ack, setAck] = useState(false);
  const [delta, setDelta] = useState("");
  const nativeAdjustment = !!applyTarget && !applyTarget.simulated;
  const applying = data.busy, auto = data.automation?.enabled ?? false;
  const [autoConfirm, setAutoConfirm] = useState<string | null>(null);
  const [weeklyMember, setWeeklyMember] = useState<{ id: string; name: string } | null>(null);
  const openWeekly = (row: WalletRow) => { data.closeDetail(); setWeeklyMember({ id: row.id, name: row.name }); };
  const canApply = (p: WalletRow) => data.canApply(p.view);
  const control = walletControl(data.automation), lastRun = control?.latest_run;
  const autoAvailable = isAdmin && data.ready && !data.busy && !data.controlUnknown && !!control && (auto || !!data.automation?.can_enable);
  const showSimulation = !!control || auto || participants.some(p => p.simulated);
  const nativeMembers = participants.filter(p => !p.simulated);
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);
  const doApply = () => void data.apply(nativeAdjustment ? delta.trim() : undefined);
  const [historyFilter, setHistoryFilter] = useState({ start: "", end: "" });
  const [historyKind, setHistoryKind] = useState("wallet"), [historySelection, setHistorySelection] = useState<string | null>(null);
  useEffect(() => { setOperationFilter({ participant: "", start: "", end: "" }); setHistoryFilter({ start: "", end: "" }); setHistorySelection(null); setWeeklyMember(null); }, [auth.epoch]);
  const historyRange = walletTimeRange(historyFilter.start, historyFilter.end);
  const visibleOps = data.operations.filter(row => (!operationFilter.participant || row.participant === operationFilter.participant) && walletTimeMatches(row.at, operationRange));
  const operationParticipants = Array.from(new Map([...data.catalog.map(p => [p.participant_id, p.name] as const), ...data.operations.map(row => [row.participant, row.participantName] as const)]));
  const history = data.history;
  const historyPage = historyKind === "wallet" ? "wallet" : "recommendation";
  const historicalRead = historyKind === "wallet" ? history?.walletReads.find(row => row.id === historySelection && walletTimeMatches(row.attempted_at, historyRange)) : null;
  const historicalRecommendation = historyKind === "recommendation" ? history?.recommendations.find(row => row.id === historySelection && walletTimeMatches(row.generated_at, historyRange)) : null;
  const openHistory = (id: string) => { setHistoryKind("wallet"); setHistorySelection(null); setHistoryFilter({ start: "", end: "" }); void data.inspectHistory(id); };
  const filterHistory = (start: string, end: string) => {
    setHistoryFilter({ start, end }); setHistorySelection(null);
    if (!walletTimeRange(start, end).error) data.filterHistory({ start_at: start.trim(), end_at: end.trim() });
  };

  return (
    <>
      <PageHeader
        title="余额建议与应用"
        tag="S2A-BAL-001 ~ 008"
        desc={data.error ?? data.notice ?? (!data.allowed ? "未授予余额读取权限。" : !data.ready ? "正在完整读取钱包与请求记录…" : participants.some(p => p.simulated) ? "模拟协议：条件应用仅限显式绑定的模拟实例；每笔结果通过独立证明核对。点击标签可重新读取记录。" : "管理员可以查看专用钱包的周额度建议，按池份额结算或启用自动应用。")}
      />

      {isAdmin && (
        <div className="mb-4 space-y-2">
          {!gateOpen && <GateNotice gate="余额写入">手动调账前请读取当前绑定用户的余额，并核对未决调账。</GateNotice>}
          <label className="inline-flex items-center gap-2 text-[12px] text-muted">
            <input type="checkbox" checked={gateOpen} disabled onChange={() => data.reload()} className="accent-[var(--brand)]" />
            当前有可发起申请的钱包
          </label>
        </div>
      )}

      <Tabs
        value={tab}
        onChange={(value) => { setTab(value); data.reload(); }}
        tabs={[
          { id: "recs", label: "当前建议" },
          { id: "ops", label: "应用记录" },
          ...(isAdmin ? [{ id: "auto", label: "自动应用" }] : []),
        ]}
      />

      {tab === "recs" && (
        <Card pad={false}>
          <Table
            rows={participants}
            rowKey={(p) => p.id}
            onRow={setSel}
            cols={[
              { key: "n", label: "参与者", render: (p) => <div><div className="font-medium">{p.name}</div><Mono>{p.upstreamUserId}</Mono></div> },
              { key: "b", label: "钱包余额", align: "right", render: (p) => <Val v={p.balance} /> },
              { key: "f", label: "冻结", align: "right", render: (p) => <span className="text-muted"><Val v={p.frozen} /></span> },
              { key: "r", label: "读取时间", render: (p) => <span className="num text-[12px] text-muted" title={p.readNote}>{time(p.balanceReadAt)}</span> },
              { key: "rec", label: "建议目标", align: "right", render: (p) => isAdmin && !p.simulated
                ? <Button size="sm" onClick={event => { event.stopPropagation(); openWeekly(p); }}>周额度建议</Button>
                : <Val v={p.recommendation} /> },
              { key: "s", label: "建议状态", render: (p) => p.simulated ? <Badge tone={REC_STATE[p.recState].tone} dot>{REC_STATE[p.recState].label}</Badge> : "—" },
              {
                key: "a",
                label: "",
                render: (p) =>
                  isAdmin && (
                    <div onClick={(e) => e.stopPropagation()} className="flex justify-end">
                      <AdminButton
                        size="sm"
                        icon={<Send size={12} />}
                        disabled={!canApply(p)}
                        title={p.simulated ? p.recReason : "对当前 Sub2API 用户提交原生调账"}
                        onClick={() => { setAck(false); setDelta(""); void data.prepareApplication(p.id); }}
                      >
                        {p.simulated ? "模拟应用" : "调整余额"}
                      </AdminButton>
                    </div>
                  ),
              },
            ]}
          />
          <CursorNavigation after={data.walletAfter} next={data.walletNext} onChange={data.changeWalletPage} disabled={data.busy} />
        </Card>
      )}

      {tab === "ops" && (
        <Card pad={false} desc="谁、依据哪次建议、何时尝试以及最终结果。本地记录成功不等于远端已确认。" title="余额应用记录">
          <div className="space-y-4 p-4">
            <Field label="参与者"><Select aria-label="筛选应用参与者" value={operationFilter.participant} onChange={e => setOperationFilter({ ...operationFilter, participant: e.target.value })} options={[{ value: "", label: "全部授权参与者" }, ...operationParticipants.map(([value, label]) => ({ value, label }))]} /></Field>
            <Grid cols={2}>
              <DateTimeField label="应用开始时间（含）" value={operationFilter.start} onChange={start => { setOperationFilter({ ...operationFilter, start }); data.changeOperationsPage("0"); }} />
              <DateTimeField label="应用结束时间（不含）" value={operationFilter.end} onChange={end => { setOperationFilter({ ...operationFilter, end }); data.changeOperationsPage("0"); }} />
            </Grid>
            {operationRange.error && <Callout tone="warn">{operationRange.error}</Callout>}
          </div>
          <Table
            rows={visibleOps}
            empty={data.operationsError ?? (data.operationsReady ? "本页没有符合筛选条件的请求。" : "正在读取应用记录…")}
            rowKey={(o) => o.id}
            cols={[
              { key: "id", label: "编号", render: (o) => <Mono>{o.id}</Mono> },
              { key: "t", label: "时间", render: (o) => <span className="num whitespace-nowrap">{time(o.at)}</span> },
              { key: "who", label: "发起", render: (o) => (isAdmin ? o.actor : "—") },
              { key: "p", label: "参与者", render: (o) => o.participantName },
              { key: "rec", label: "依据建议", render: (o) => <Mono>{o.rec}</Mono> },
              { key: "b", label: "应用前", align: "right", render: (o) => <Val v={o.before} /> },
              { key: "g", label: "目标或增减额", align: "right", render: (o) => <Val v={o.adjustment ?? o.target} /> },
              { key: "af", label: "读回", align: "right", render: (o) => <Val v={o.after} /> },
              { key: "o", label: "结果", render: (o) => <Outcome k={o.outcome} /> },
              { key: "n", label: "说明", render: (o) => <span className="text-[12px] text-muted">{o.note || "—"}</span> },
              {
                key: "x",
                label: "",
                render: (o) =>
                  isAdmin && (
                    <Button size="sm" icon={<SearchCheck size={12} />} onClick={() => setReconcile(o)}>
                      详情
                    </Button>
                  ),
              },
            ]}
          />
          <CursorNavigation after={data.operationsAfter} next={data.operationsNext} first="0" onChange={data.changeOperationsPage} disabled={data.busy} />
        </Card>
      )}

      {isAdmin && tab === "auto" && (nativeMembers.length > 0 || !showSimulation || !!data.walletNext || !!data.walletAfter) && (
        <Card title="周额度自动应用" desc="设置适用于参与者所属的额度池。估算达标后按各自份额正式结算。" pad={false}>
          <Table rows={nativeMembers} rowKey={p => p.id} cols={[
            { key: "participant", label: "参与者", render: p => p.name },
            { key: "settings", label: "发放设置", render: p => <Button size="sm" onClick={() => openWeekly(p)}>周额度设置</Button> },
          ]} />
          <CursorNavigation after={data.walletAfter} next={data.walletNext} onChange={data.changeWalletPage} disabled={data.busy} />
        </Card>
      )}

      {isAdmin && tab === "auto" && showSimulation && (
        <Grid cols={2}>
          <Card title="自动应用">
            <Switch
              checked={auto}
              onChange={() => { if (auto) void data.configureAutomation(false); else if (autoAvailable) setAutoConfirm(data.generation); }}
              label="启用自动应用"
              desc="默认关闭，仅限明确绑定的模拟实例。开启后退出登录仍继续；权限或来源变化暂停，未知结果不重发。"
              disabled={!autoAvailable}
            />
            {!data.automation?.can_enable && <p className="mt-3 text-[12px] text-warn">{auto ? "自动应用已暂停；可停用，不能在当前来源重新启用。" : "当前没有可启用的模拟绑定。"}</p>}
          </Card>
          <Card title={`最近一次运行 · ${lastRun ? time(lastRun.updated_at) : data.automation ? "暂无记录" : "尚未读取"}`}>
            <div className="flex flex-wrap gap-2">
              {lastRun ? <>
                <Badge tone="ok">已应用 <Val v={lastRun.counts.applied} digits={0} /></Badge>
                <Badge>跳过 <Val v={lastRun.counts.skipped} digits={0} /></Badge>
                <Badge tone="unk">结果未知 <Val v={lastRun.counts.unknown} digits={0} /></Badge>
                <Badge tone="bad">失败 <Val v={lastRun.counts.failed} digits={0} /></Badge>
              </> : <Badge>运行计数未知</Badge>}
            </div>
            <ul className="mt-3 space-y-1 text-[12.5px] text-muted">
              <li>· {lastRun ? `#${lastRun.id} · ${lastRun.participant_name} · 建议 #${lastRun.recommendation_id}${lastRun.counts.running ? " · 处理中" : ""}` : "尚无自动运行记录；未知数量不补零。"}</li>
              <li>· {data.automation?.execution_state === "paused" ? "当前权限或来源绑定已变化，自动应用暂停。" : data.automation?.execution_state === "waiting" ? "已启用，等待新的可处理建议。" : data.automation?.execution_state === "disabled" ? "自动应用已关闭。" : "自动执行暂不可用。"}</li>
              <li>· {lastRun ? visibleOps.find(row => row.id === lastRun.id)?.note ?? "运行记录已变化，请点击标签重新读取。" : "每次运行只含一个钱包；更多结果见应用记录。"}</li>
              <li>· 不可用或关闭不会撤销过去可能发生的钱包变更。</li>
            </ul>
          </Card>
        </Grid>
      )}

      {/* Participant detail */}
      <Drawer open={!!sel} onClose={() => setSel(null)} title={sel ? `${sel.name} · 余额与建议` : ""}>
        {sel && (
          <div className="space-y-4">
            {isAdmin && <div className="flex flex-wrap gap-2">
              <Button disabled={data.busy || !sel.enabled || !sel.view.source_current} onClick={() => void data.refreshWallet()}>{data.busy ? "处理中…" : "刷新钱包读数"}</Button>
              <Button disabled={data.busy || !sel.enabled || !sel.view.source_current} onClick={() => sel.simulated ? void data.recomputeWallet() : openWeekly(sel)}>{sel.simulated ? "重新计算建议" : "周额度建议"}</Button>
              <Button disabled={data.busy} onClick={() => openHistory(sel.id)}>读数与建议历史</Button>
            </div>}
            {data.participantError && <Callout tone="warn">{data.participantError}</Callout>}
            {data.participantNotice && <Callout>{data.participantNotice}</Callout>}
            <Grid cols={2}>
              <Card>
                <div className="text-[12px] text-muted">{sel.simulated ? "模拟钱包余额（USD）" : "钱包余额（原生 balance）"}</div>
                <div className="mt-1 text-[22px] font-semibold"><Val v={sel.balance} /></div>
                <div className="text-[12px] text-muted">读取于 {time(sel.balanceReadAt)}。{sel.readNote}</div>
              </Card>
              <Card>
                <div className="text-[12px] text-muted">{sel.simulated ? "模拟冻结余额（USD）" : "冻结余额（原生 frozen_balance）"}</div>
                <div className="mt-1 text-[22px] font-semibold"><Val v={sel.frozen} /></div>
                <div className="text-[12px] text-muted">按原值展示，不从余额中再扣一次</div>
              </Card>
            </Grid>
            <Card title="全局建议组成" desc="服务端汇总该钱包全部已核验的贡献；展示时按你的权限收窄">
              <ul className="divide-y divide-line">
                {sel.contributions
                  .map((c) => (
                    <li key={c.account} className="flex items-center justify-between gap-2 py-2 text-[13px]">
                      <span className="min-w-0 break-words" title={c.account}>{accountNames.get(c.account) ?? (c.account.slice(0, 6) + "…" + c.account.slice(-4))}</span>
                      <span className="flex items-center gap-2">
                        <span className="text-[12px] text-muted">{c.state}</span>
                        <Val v={c.amount} />
                      </span>
                    </li>
                  ))}
                {!isAdmin && <li className="py-2 text-[12px] text-faint">不在你范围内的账号贡献已隐藏</li>}
              </ul>
              <div className="mt-2 flex justify-between border-t border-line pt-2 text-[13px] font-medium">
                <span>建议目标</span>
                {sel.recommendation === null ? <Badge tone="unk">无可应用目标</Badge> : <Val v={sel.recommendation} />}
              </div>
            </Card>
            {sel.recReason && <Callout tone={REC_STATE[sel.recState].tone === "ok" ? "info" : "warn"}>{sel.recReason}</Callout>}
            <KV
              items={[
                ["建议依据时间", time(sel.view.latest_recommendation?.generated_at)],
                ["最近应用请求", sel.lastApplied ? time(sel.lastApplied.at) : "尚无本地请求"],
                ["最近结果", sel.lastApplied ? <Outcome key="o" k={sel.lastApplied.outcome} /> : "—"],
                ["余额与目标差异", <Val key="difference" v={sel.view.difference} />],
              ]}
            />
          </div>
        )}
      </Drawer>

      <Modal open={!!history} onClose={data.closeDetail} title={history ? `${history.participant.name} · 读数与建议历史` : ""} wide>
        {history && <div className="space-y-4">
          <Callout>历史记录保留当时的绑定和依据；筛选不会改变当前建议、应用资格或未决请求。普通用户的应用记录仍按授权范围展示。</Callout>
          <Tabs value={historyKind} onChange={kind => { setHistoryKind(kind); setHistorySelection(null); }} tabs={[{ id: "wallet", label: "钱包读数" }, { id: "recommendation", label: "历史建议" }]} />
          <Grid cols={2}>
            <DateTimeField label="历史开始时间（含）" value={historyFilter.start} onChange={start => filterHistory(start, historyFilter.end)} />
            <DateTimeField label="历史结束时间（不含）" value={historyFilter.end} onChange={end => filterHistory(historyFilter.start, end)} />
          </Grid>
          {historyRange.error && <Callout tone="warn">{historyRange.error}</Callout>}
          {historyKind === "wallet" ? <Table rows={history.walletReads.filter(row => walletTimeMatches(row.attempted_at, historyRange))} rowKey={row => row.id} cols={[
            { key: "id", label: "读数", render: row => <Mono>#{row.id}</Mono> },
            { key: "attempt", label: "尝试时间", render: row => <span title={row.attempted_at}>{time(row.attempted_at)}</span> },
            { key: "read", label: "读取时间", render: row => <span title={row.read_at ?? ""}>{time(row.read_at)}</span> },
            { key: "balance", label: "余额", render: row => <Val v={row.balance.state === "reported" ? row.balance.value : null} /> },
            { key: "frozen", label: "冻结", render: row => <Val v={row.frozen_balance.state === "reported" ? row.frozen_balance.value : null} /> },
            { key: "state", label: "读取结果", render: row => <Badge tone={row.read.state === "available" ? "info" : "warn"}>{row.read.state === "available" ? "读取已返回" : "未取得读数"}</Badge> },
            { key: "detail", label: "", render: row => <Button size="sm" onClick={() => setHistorySelection(row.id)}>查看读数 #{row.id}</Button> },
          ]} /> : <Table rows={history.recommendations.filter(row => walletTimeMatches(row.generated_at, historyRange))} rowKey={row => row.id} cols={[
            { key: "id", label: "建议", render: row => <Mono>#{row.id}</Mono> },
            { key: "at", label: "生成时间", render: row => <span title={row.generated_at}>{time(row.generated_at)}</span> },
            { key: "trigger", label: "触发", render: row => row.trigger === "manual_recompute" ? "手动重算" : row.trigger === "wallet_refresh" ? "钱包刷新" : "依据变更" },
            { key: "target", label: "当时目标", render: row => <Val v={row.target === null ? null : estimateAmountText(row.target)} /> },
            { key: "state", label: "当时状态", render: row => <Badge tone={row.complete ? "warn" : "unk"}>{row.complete ? "模拟条件建议" : "依据不足"}</Badge> },
            { key: "detail", label: "", render: row => <Button size="sm" onClick={() => setHistorySelection(row.id)}>查看建议 #{row.id}</Button> },
          ]} />}
          <CursorNavigation after={history.after[historyPage]} next={history.nextAfter[historyPage]} first="0" disabled={!!historyRange.error}
            onChange={after => { setHistorySelection(null); data.changeHistoryPage(historyPage, after); }} />
          {historicalRead && <Card title={`历史读数 #${historicalRead.id}`}><KV items={[
            ["尝试时间（UTC）", <Mono key="attempt">{historicalRead.attempted_at}</Mono>],
            ["读取时间（UTC）", <Mono key="read">{historicalRead.read_at ?? "未取得"}</Mono>],
            ["来源", <Mono key="source">{historicalRead.source_id}</Mono>],
            ["绑定 / 参与者版本 / 连接版本", `${historicalRead.binding_id} / ${historicalRead.participant_revision} / ${historicalRead.connection_revision}`],
            ["上游用户", historicalRead.upstream_user_id],
            ["单位", historicalRead.unit === "simulated_wallet_usd" ? "模拟钱包 USD" : "原生钱包，币种未核验"],
            ["余额字段依据", historicalRead.balance.reason], ["冻结字段依据", historicalRead.frozen_balance.reason],
            ["读取结果", historicalRead.read.reason],
          ]} /></Card>}
          {historicalRecommendation && <Card title={`历史建议 #${historicalRecommendation.id}`}><KV items={[
            ["生成时间（UTC）", <Mono key="at">{historicalRecommendation.generated_at}</Mono>],
            ["有效至（UTC）", <Mono key="expiry">{historicalRecommendation.expires_at ?? "无已核验有效期"}</Mono>],
            ["绑定 / 参与者版本", `${historicalRecommendation.binding_id} / ${historicalRecommendation.participant_revision}`],
            ["规则版本", historicalRecommendation.rules_version], ["依据摘要", <span key="basis" className="break-all"><Mono>{historicalRecommendation.basis_hash}</Mono></span>],
            ["当时限制", historicalRecommendation.global.limitations.join(" · ")],
          ]} />
            <ul className="divide-y divide-line">{historicalRecommendation.contributions.map(row => <li key={row.account_id} className="py-2 text-[12px] text-muted"><Mono>{row.account_id}</Mono> · {row.state} · <Val v={row.amount === null ? null : estimateAmountText(row.amount)} /></li>)}</ul>
          </Card>}
        </div>}
      </Modal>

      {/* Apply */}
      {isAdmin && weeklyMember && <WeeklyWalletDialog key={`${auth.epoch}:${weeklyMember.id}`} member={weeklyMember} time={time}
        onApplied={data.reload} close={() => setWeeklyMember(null)} />}

      <Modal
        open={!!applyTarget}
        onClose={() => !applying && data.closeDetail()}
        title={nativeAdjustment ? "调整原生钱包余额" : "模拟应用目标余额"}
        footer={
          <>
            <Button onClick={() => data.closeDetail()} disabled={applying}>
              取消
            </Button>
            <Button variant="primary" disabled={!ack || applying || !applyTarget || !canApply(applyTarget) || nativeAdjustment && !nativeWalletDelta.safeParse(delta.trim()).success} onClick={doApply}>
              {applying ? "写入并读回中…" : nativeAdjustment ? "确认调账" : "确认应用"}
            </Button>
          </>
        }
      >
        {applyTarget && (
          <div className="space-y-3">
            <KV
              items={[
                ["参与者", applyTarget.name],
                ["上游用户", <Mono key="u">{applyTarget.upstreamUserId}</Mono>],
                ["当前余额", <Val key="b" v={applyTarget.balance} />],
                ["冻结余额", <Val key="f" v={applyTarget.frozen} />],
                ...(!nativeAdjustment ? [
                  ["目标余额", <span key="t" className="font-semibold"><Val v={applyTarget.recommendation} /></span>] as [string, React.ReactNode],
                  ["依据", applyTarget.view.latest_recommendation ? `#${applyTarget.view.latest_recommendation.id} · ${time(applyTarget.view.latest_recommendation.generated_at)}` : "尚未生成"] as [string, React.ReactNode],
                ] : []),
              ]}
            />
            {nativeAdjustment && <Field label="余额增减额" hint="正数增加，负数扣减。增加余额遵循 Sub2API 的管理员充值返利设置。">
              <Input inputMode="decimal" value={delta} maxLength={22} disabled={applying}
                onChange={event => { setDelta(event.target.value); setAck(false); }} />
            </Field>}
            <Callout tone="warn">{data.actionError ?? (nativeAdjustment ? "调账使用 Sub2API 原生接口。提交后按流水核对本次操作，结果未知时可从应用记录只读核对。"
              : "仅作用于已绑定的模拟实例整个钱包。服务端核对当前完整建议并按来源精度量化；本次只提交一次。结果未知时禁止重发，只能读取历史并核对原操作。")}</Callout>
            <Checkbox checked={ack} onChange={setAck} disabled={!canApply(applyTarget)}
              label={nativeAdjustment ? "确认调账用户和余额增减额。" : "我已确认这是模拟实例，按当前完整正数建议提交一次条件应用。"} />
          </div>
        )}
      </Modal>

      <Modal
        open={autoConfirm === data.generation && data.ready && isAdmin}
        onClose={() => !data.busy && setAutoConfirm(null)}
        title="开启自动应用"
        footer={
          <>
            <Button onClick={() => setAutoConfirm(null)} disabled={data.busy}>取消</Button>
            <Button
              variant="primary"
              disabled={!autoAvailable || auto}
              onClick={async () => { if (await data.configureAutomation(true)) setAutoConfirm(null); }}
            >
              {data.busy ? "保存中…" : "开启"}
            </Button>
          </>
        }
      >
        <p className="text-[13px]">{data.actionError ?? "只对当前绑定的模拟来源启用。退出浏览器后继续处理新建议；启用管理员权限或来源版本变化会暂停。混用钱包、活动冻结、零目标和未知未决不会派发，请定期查看每个钱包的运行结果。"}</p>
      </Modal>

      <Modal
        open={!!reconcile}
        onClose={() => !data.busy && setReconcile(null)}
        title={`应用请求记录 ${reconcile?.id ?? ""}`}
        footer={
          <>
            <Button onClick={() => setReconcile(null)} disabled={data.busy}>稍后处理</Button>
            <Button
              disabled
              title="不接受人工覆盖原操作结果"
            >
              标记未生效
            </Button>
            <Button
              variant="primary"
              disabled={!reconcile || !isAdmin || !data.ready || data.busy || !canReconcileWallet(reconcile.record)}
              onClick={() => void data.reconcile()}
            >
              {data.busy ? "只读核对中…" : "只读核对原操作"}
            </Button>
          </>
        }
      >
        {reconcile && (
          <div className="space-y-3">
            <KV
              items={[
                ["参与者", reconcile.participantName],
                [reconcile.adjustment === null ? "目标" : "余额增减额", <Val key="t" v={reconcile.adjustment ?? reconcile.target} />],
                ["发起时间", time(reconcile.at)],
                [reconciledNativeBalance !== null ? "上游余额读回" : "本地已确认钱包读数",
                  <span key="r" className="font-semibold"><Val v={reconciledNativeBalance ?? participants.find(p => p.id === reconcile.participant)?.balance} /></span>],
              ]}
            />
            <Callout tone="unk">{data.actionError ?? reconcile.note} 本地钱包读数不能代替独立操作证明；只读核对不重发资金请求，不接受人工覆盖结果。</Callout>
          </div>
        )}
      </Modal>
    </>
  );
}
