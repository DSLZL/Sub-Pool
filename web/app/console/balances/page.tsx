"use client";

import { glossary } from "@/lib/glossary";
import { Timestamp } from "@/components/timestamp";
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
  Table,
  Tabs,
  Val,
} from "@/components/ui";
import { useApp } from "@/lib/store";
import { useWalletData } from "@/lib/use-wallet-data";
import { displayTime } from "@/lib/connection-data";
import { walletTimeRange, walletTimeMatches, type WalletRow, type WalletOperation } from "@/lib/wallets-data";
import { estimateAmountText } from "@/lib/estimates-data";
import { canReconcileWallet } from "@/lib/wallet-actions";
import { nativeWalletDelta } from "@sub-pool/contracts/wallet-applications";
import { WeeklyWalletDialog } from "../pools/weekly-wallet";

export default function BalancesPage() {
  const { displayTimezone, auth } = useApp();
  const [tab, setTab] = useState("recs");
  const [operationFilter, setOperationFilter] = useState({ participant: "", start: "", end: "" });
  const operationRange = walletTimeRange(operationFilter.start, operationFilter.end);
  const data = useWalletData(tab === "ops" && !operationRange.error
    ? { participant_id: operationFilter.participant, start_at: operationFilter.start.trim(), end_at: operationFilter.end.trim() } : null);
  const { isAdmin, rows: participants } = data;
  const gateOpen = data.ready && participants.some(p => data.canApply(p.view));
  const applicationStatus = data.busy ? "处理中" : !data.ready ? "待核对" : gateOpen ? "可发起" : "暂不可发起";
  const sel = data.participant, reconcile = data.operation;
  const reconciledNativeBalance = reconcile?.record.unit === "native_wallet" ? reconcile.after : null;
  const setSel = (row: WalletRow | null) => row ? void data.inspectParticipant(row.id) : data.closeDetail();
  const setReconcile = (row: WalletOperation | null) => row ? void data.inspectOperation(row.id) : data.closeDetail();
  const applyTarget = data.applyTarget;
  const [ack, setAck] = useState(false);
  const [delta, setDelta] = useState("");
  const applying = data.busy;
  const [weeklyMember, setWeeklyMember] = useState<{ id: string; name: string; mode: "recommendation" | "settings" } | null>(null);
  const openWeekly = (row: WalletRow, mode: "recommendation" | "settings") => {
    data.closeDetail();
    setWeeklyMember({ id: row.id, name: row.name, mode });
  };
  const canApply = (p: WalletRow) => data.canApply(p.view);
  const time = (value: string | null | undefined) => displayTime(value, displayTimezone, false);
  const doApply = () => void data.apply(delta.trim());
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
        title={glossary.balances}
        desc={`查看钱包与${glossary.balanceAdjustment}。`} helpTopic="wallets"
      />
      {(data.error || data.notice || !data.allowed || !data.ready) && <Callout tone={data.error ? "bad" : "unk"}>{data.error ?? data.notice ?? (!data.allowed ? "未授予余额读取权限。" : "正在读取钱包与请求记录。")}</Callout>}

      {isAdmin && (
        <div className="mb-4 space-y-2">
          {data.ready && !data.busy && !gateOpen && (
            <GateNotice gate={glossary.balanceAdjustment}>
              {participants.length === 0 ? `本页没有已绑定的钱包。请在「参与者与额度池」绑定 ${glossary.sub2api} 用户。` : <>
                {participants.every(p => !p.view.latest_read) && "本页钱包尚未取得读数。"}
                请在钱包详情刷新余额与冻结额读数，并在「{glossary.adjustmentRecords}」核对未决请求。
              </>}
            </GateNotice>
          )}
          <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted" role="status" aria-label={`${glossary.balanceAdjustment}状态`}>
            <Badge tone={gateOpen ? "ok" : data.ready && !data.busy ? "warn" : "unk"} dot>本页{glossary.balanceAdjustment} · {applicationStatus}</Badge>
            {gateOpen && <span>服务端派发前会重新核对来源授权和钱包状态。</span>}
          </div>
        </div>
      )}

      <Tabs
        value={tab}
        onChange={(value) => { setTab(value); data.reload(); }}
        tabs={[
          { id: "recs", label: "当前建议" },
          { id: "ops", label: glossary.adjustmentRecords },
          ...(isAdmin ? [{ id: "auto", label: "自动调整" }] : []),
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
              { key: "r", label: "读取时间", render: (p) => <Timestamp short className="num text-[12px] text-muted" value={p.balanceReadAt} note={p.readNote} /> },
              { key: "rec", label: "周额度建议", align: "right", render: (p) => isAdmin
                ? <Button size="sm" onClick={event => { event.stopPropagation(); openWeekly(p, "recommendation"); }}>周额度建议</Button>
                : "—" },
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
                        title={`对当前 ${glossary.sub2api} 用户提交原生${glossary.balanceAdjustment}`}
                        onClick={() => { setAck(false); setDelta(""); void data.prepareApplication(p.id); }}
                      >
                        {glossary.adjustBalance}
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
        <Card pad={false} desc="谁、依据哪次建议、何时尝试以及最终结果。本地记录成功不等于远端已确认。" title={`${glossary.balanceAdjustment}记录`}>
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
            empty={data.operationsError ?? (data.operationsReady ? "本页没有符合筛选条件的请求。" : `正在读取${glossary.adjustmentRecords}…`)}
            rowKey={(o) => o.id}
            cols={[
              { key: "id", label: "编号", render: (o) => <Mono>{o.id}</Mono> },
              { key: "t", label: "时间", render: (o) => <Timestamp short className="num whitespace-nowrap" value={o.at} /> },
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

      {isAdmin && tab === "auto" && (
        <Card title="周额度自动调整" desc="设置适用于参与者所属的额度池。估算达标后按各自份额正式结算。" pad={false}>
          <Table rows={participants} rowKey={p => p.id} cols={[
            { key: "participant", label: "参与者", render: p => p.name },
            { key: "settings", label: "发放设置", render: p => <Button size="sm" onClick={() => openWeekly(p, "settings")}>周额度设置</Button> },
          ]} />
          <CursorNavigation after={data.walletAfter} next={data.walletNext} onChange={data.changeWalletPage} disabled={data.busy} />
        </Card>
      )}


      {/* Participant detail */}
      <Drawer open={!!sel} onClose={() => setSel(null)} title={sel ? `${sel.name} · 余额与建议` : ""}>
        {sel && (
          <div className="space-y-4">
            {isAdmin && <div className="flex flex-wrap gap-2">
              <Button disabled={data.busy || !sel.enabled || !sel.view.source_current} onClick={() => void data.refreshWallet()}>{data.busy ? "处理中…" : "刷新钱包读数"}</Button>
              <Button disabled={data.busy || !sel.enabled || !sel.view.source_current} onClick={() => openWeekly(sel, "recommendation")}>周额度建议</Button>
              <Button disabled={data.busy} onClick={() => openHistory(sel.id)}>读数与建议历史</Button>
            </div>}
            {data.participantError && <Callout tone="warn">{data.participantError}</Callout>}
            {data.participantNotice && <Callout>{data.participantNotice}</Callout>}
            <Grid cols={2}>
              <Card>
                <div className="text-[12px] text-muted">钱包余额</div>
                <div className="mt-1 text-[22px] font-semibold"><Val v={sel.balance} /></div>
                <div className="text-[12px] text-muted">读取于 {time(sel.balanceReadAt)}。{sel.readNote}</div>
              </Card>
              <Card>
                <div className="text-[12px] text-muted">冻结余额</div>
                <div className="mt-1 text-[22px] font-semibold"><Val v={sel.frozen} /></div>
                <div className="text-[12px] text-muted">按原值展示，不从余额中再扣一次</div>
              </Card>
            </Grid>
            <KV
              items={[
                ["最近应用请求", sel.lastApplied ? time(sel.lastApplied.at) : "尚无本地请求"],
                ["最近结果", sel.lastApplied ? <Outcome key="o" k={sel.lastApplied.outcome} /> : "—"],
              ]}
            />
          </div>
        )}
      </Drawer>

      <Modal open={!!history} onClose={data.closeDetail} title={history ? `${history.participant.name} · 读数与建议历史` : ""} wide>
        {history && <div className="space-y-4">
          <Callout>历史记录保留当时的绑定和依据；筛选不会改变当前建议、应用资格或未决请求。普通用户的{glossary.adjustmentRecords}仍按授权范围展示。</Callout>
          <Tabs value={historyKind} onChange={kind => { setHistoryKind(kind); setHistorySelection(null); }} tabs={[{ id: "wallet", label: "钱包读数" }, { id: "recommendation", label: "历史建议" }]} />
          <Grid cols={2}>
            <DateTimeField label="历史开始时间（含）" value={historyFilter.start} onChange={start => filterHistory(start, historyFilter.end)} />
            <DateTimeField label="历史结束时间（不含）" value={historyFilter.end} onChange={end => filterHistory(historyFilter.start, end)} />
          </Grid>
          {historyRange.error && <Callout tone="warn">{historyRange.error}</Callout>}
          {historyKind === "wallet" ? <Table rows={history.walletReads.filter(row => walletTimeMatches(row.attempted_at, historyRange))} rowKey={row => row.id} cols={[
            { key: "id", label: "读数", render: row => <Mono>#{row.id}</Mono> },
            { key: "attempt", label: "尝试时间", render: row => <Timestamp short value={row.attempted_at} /> },
            { key: "read", label: "读取时间", render: row => <Timestamp short value={row.read_at} /> },
            { key: "balance", label: "余额", render: row => <Val v={row.balance.state === "reported" ? row.balance.value : null} /> },
            { key: "frozen", label: "冻结", render: row => <Val v={row.frozen_balance.state === "reported" ? row.frozen_balance.value : null} /> },
            { key: "state", label: "读取结果", render: row => <Badge tone={row.read.state === "available" ? "info" : "warn"}>{row.read.state === "available" ? "读取已返回" : "未取得读数"}</Badge> },
            { key: "detail", label: "", render: row => <Button size="sm" onClick={() => setHistorySelection(row.id)}>查看读数 #{row.id}</Button> },
          ]} /> : <Table rows={history.recommendations.filter(row => walletTimeMatches(row.generated_at, historyRange))} rowKey={row => row.id} cols={[
            { key: "id", label: "建议", render: row => <Mono>#{row.id}</Mono> },
            { key: "at", label: "生成时间", render: row => <Timestamp short value={row.generated_at} /> },
            { key: "trigger", label: "触发", render: row => row.trigger === "manual_recompute" ? "手动重算" : row.trigger === "wallet_refresh" ? "钱包刷新" : "依据变更" },
            { key: "target", label: "当时目标", render: row => <Val v={row.target === null ? null : estimateAmountText(row.target)} /> },
            { key: "state", label: "当时状态", render: row => <Badge tone={row.complete ? "warn" : "unk"}>{row.complete ? "模拟条件建议" : "依据不足"}</Badge> },
            { key: "detail", label: "", render: row => <Button size="sm" onClick={() => setHistorySelection(row.id)}>查看建议 #{row.id}</Button> },
          ]} />}
          <CursorNavigation after={history.after[historyPage]} next={history.nextAfter[historyPage]} first="0" disabled={!!historyRange.error}
            onChange={after => { setHistorySelection(null); data.changeHistoryPage(historyPage, after); }} />
          {historicalRead && <Card title={`历史读数 #${historicalRead.id}`}><KV items={[
            ["尝试时间", <Mono key="attempt"><Timestamp value={historicalRead.attempted_at} /></Mono>],
            ["读取时间", <Mono key="read"><Timestamp value={historicalRead.read_at} empty="未取得" /></Mono>],
            ["来源", <Mono key="source">{historicalRead.source_id}</Mono>],
            ["绑定 / 参与者版本 / 连接版本", `${historicalRead.binding_id} / ${historicalRead.participant_revision} / ${historicalRead.connection_revision}`],
            [`${glossary.sub2api} 用户`, historicalRead.upstream_user_id],
            ["单位", historicalRead.unit === "simulated_wallet_usd" ? "模拟钱包 USD" : "原生钱包，币种未核验"],
            ["余额字段依据", historicalRead.balance.reason], ["冻结字段依据", historicalRead.frozen_balance.reason],
            ["读取结果", historicalRead.read.reason],
          ]} /></Card>}
          {historicalRecommendation && <Card title={`历史建议 #${historicalRecommendation.id}`}><KV items={[
            ["生成时间", <Mono key="at"><Timestamp value={historicalRecommendation.generated_at} /></Mono>],
            ["有效至", <Mono key="expiry"><Timestamp value={historicalRecommendation.expires_at} empty="无已核验有效期" /></Mono>],
            ["绑定 / 参与者版本", `${historicalRecommendation.binding_id} / ${historicalRecommendation.participant_revision}`],
            ["规则版本", historicalRecommendation.rules_version], ["依据摘要", <span key="basis" className="break-all"><Mono>{historicalRecommendation.basis_hash}</Mono></span>],
            ["当时限制", historicalRecommendation.global.limitations.join(" · ")],
          ]} />
            <ul className="divide-y divide-line">{historicalRecommendation.contributions.map(row => <li key={row.account_id} className="py-2 text-[12px] text-muted"><Mono>{row.account_id}</Mono> · {row.state} · <Val v={row.amount === null ? null : estimateAmountText(row.amount)} /></li>)}</ul>
          </Card>}
        </div>}
      </Modal>

      {/* Apply */}
      {isAdmin && weeklyMember && <WeeklyWalletDialog key={`${auth.epoch}:${weeklyMember.mode}:${weeklyMember.id}`} mode={weeklyMember.mode} member={weeklyMember} time={time}
        onApplied={data.reload} close={() => setWeeklyMember(null)} />}

      <Modal
        open={!!applyTarget}
        onClose={() => !applying && data.closeDetail()}
        title="调整原生钱包余额"
        footer={
          <>
            <Button onClick={() => data.closeDetail()} disabled={applying}>
              取消
            </Button>
            <Button variant="primary" disabled={!ack || applying || !applyTarget || !canApply(applyTarget) || !nativeWalletDelta.safeParse(delta.trim()).success} onClick={doApply}>
              {applying ? "写入并读回中…" : "确认调整"}
            </Button>
          </>
        }
      >
        {applyTarget && (
          <div className="space-y-3">
            <KV
              items={[
                ["参与者", applyTarget.name],
                [`${glossary.sub2api} 用户`, <Mono key="u">{applyTarget.upstreamUserId}</Mono>],
                ["当前余额", <Val key="b" v={applyTarget.balance} />],
                ["冻结余额", <Val key="f" v={applyTarget.frozen} />],
              ]}
            />
            <Field label="余额增减额" hint={`正数增加，负数扣减。增加余额遵循 ${glossary.sub2api} 的管理员充值返利设置。`}>
              <Input inputMode="decimal" value={delta} maxLength={22} disabled={applying}
                onChange={event => { setDelta(event.target.value); setAck(false); }} />
            </Field>
            <Callout tone="warn">{data.actionError ?? `${glossary.balanceAdjustment}使用 ${glossary.sub2api} 原生接口。结果未知时，从${glossary.adjustmentRecords}只读核对原操作。`}</Callout>
            <Checkbox checked={ack} onChange={setAck} disabled={!canApply(applyTarget)} label="确认调整用户和余额增减额。" />
          </div>
        )}
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
                [reconciledNativeBalance !== null ? `${glossary.sub2api} 余额读回` : "本地已确认钱包读数",
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
