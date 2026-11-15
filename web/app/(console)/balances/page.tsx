"use client";

import { useState } from "react";
import { Send, SearchCheck } from "lucide-react";
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
import { useWalletData } from "@/lib/use-wallet-data";
import { displayTime } from "@/lib/connection-data";
import type { WalletRow, WalletOperation } from "@/lib/wallets-data";
import { canReconcileWallet, walletControl } from "@/lib/wallet-actions";

const REC_STATE: Record<WalletRow["recState"], { label: string; tone: "ok" | "unk" | "warn" | "bad" | "muted" }> = {
  scenario: { label: "模拟条件建议", tone: "warn" },
  insufficient: { label: "依据不足", tone: "unk" },
  stale: { label: "已失效", tone: "muted" },
  blocked: { label: "仅建议", tone: "bad" },
};

export default function BalancesPage() {
  const { timezone } = useApp();
  const data = useWalletData();
  const { isAdmin, rows: participants, operations: visibleOps } = data;
  const [tab, setTab] = useState("recs");
  const gateOpen = data.ready && participants.some(p => p.view.applicable);
  const sel = data.participant, reconcile = data.operation;
  const setSel = (row: WalletRow | null) => row ? void data.inspectParticipant(row.id) : data.closeDetail();
  const setReconcile = (row: WalletOperation | null) => row ? void data.inspectOperation(row.id) : data.closeDetail();
  const applyTarget = data.applyTarget;
  const [ack, setAck] = useState(false);
  const applying = data.busy, auto = data.automation?.enabled ?? false;
  const [autoConfirm, setAutoConfirm] = useState<string | null>(null);
  const canApply = (p: WalletRow) => data.canApply(p.view);
  const control = walletControl(data.automation), lastRun = control?.latest_run;
  const autoAvailable = isAdmin && data.ready && !data.busy && !data.controlUnknown && !!control && (auto || !!data.automation?.can_enable);
  const time = (value: string | null | undefined) => displayTime(value, timezone);
  const doApply = () => void data.apply();

  return (
    <>
      <PageHeader
        title="余额建议与应用"
        tag="S2A-BAL-001 ~ 008"
        desc={data.error ?? data.notice ?? (!data.allowed ? "未授予余额读取权限。" : !data.ready ? "正在完整读取钱包与请求记录…" : participants.some(p => p.simulated) ? "模拟协议：条件应用仅限显式绑定的模拟实例；每笔结果通过独立证明核对。点击标签可重新读取记录。" : "原生钱包余额与冻结分开显示，币种未核验；当前无可应用目标。点击标签可重新读取本地记录。")}
      />

      {isAdmin && (
        <div className="mb-4 space-y-2">
          {!gateOpen && <GateNotice gate="余额写入">原生钱包写入条件尚未全部通过核验。模拟应用仅限服务端明确绑定的模拟实例，不代表原生写入资格。</GateNotice>}
          <label className="inline-flex items-center gap-2 text-[12px] text-muted">
            <input type="checkbox" checked={gateOpen} disabled onChange={() => data.reload()} className="accent-[var(--brand)]" />
            原生余额写入资格由服务端核验，当前不可用
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
              { key: "rec", label: "建议目标", align: "right", render: (p) => (p.recommendation === null ? <Badge tone="unk">无</Badge> : <span className="font-medium"><Val v={p.recommendation} /></span>) },
              { key: "s", label: "建议状态", render: (p) => <Badge tone={REC_STATE[p.recState].tone} dot>{REC_STATE[p.recState].label}</Badge> },
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
                        title={p.simulated ? p.recReason : "原生钱包应用资格不可用"}
                        onClick={() => { setAck(false); void data.prepareApplication(p.id); }}
                      >
                        {p.simulated ? "模拟应用" : "应用"}
                      </AdminButton>
                    </div>
                  ),
              },
            ]}
          />
        </Card>
      )}

      {tab === "ops" && (
        <Card pad={false} desc="谁、依据哪次建议、何时尝试以及最终结果。本地记录成功不等于远端已确认。" title="余额应用记录">
          <Table
            rows={visibleOps}
            rowKey={(o) => o.id}
            cols={[
              { key: "id", label: "编号", render: (o) => <Mono>{o.id}</Mono> },
              { key: "t", label: "时间", render: (o) => <span className="num whitespace-nowrap">{time(o.at)}</span> },
              { key: "who", label: "发起", render: (o) => (isAdmin ? o.actor : "—") },
              { key: "p", label: "参与者", render: (o) => o.participantName },
              { key: "rec", label: "依据建议", render: (o) => <Mono>{o.rec}</Mono> },
              { key: "b", label: "应用前", align: "right", render: (o) => <Val v={o.before} /> },
              { key: "g", label: "目标", align: "right", render: (o) => <Val v={o.target} /> },
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
        </Card>
      )}

      {tab === "auto" && (
        <Grid cols={2}>
          <Card title="自动应用">
            <Switch
              checked={auto}
              onChange={() => { if (auto) void data.configureAutomation(false); else if (autoAvailable) setAutoConfirm(data.generation); }}
              label="启用自动应用"
              desc="默认关闭，仅限明确绑定的模拟实例。开启后退出登录仍继续；权限或来源变化暂停，未知结果不重发。"
              disabled={!autoAvailable}
            />
            {!data.automation?.can_enable && <p className="mt-3 text-[12px] text-warn">{auto ? "自动应用已暂停；可停用，不能在当前来源重新启用。" : "当前没有可启用的模拟绑定，原生自动应用不可用。"}</p>}
          </Card>
          <Card title={`最近一次运行 · ${lastRun ? time(lastRun.updated_at) : data.automation ? "暂无记录" : "尚未读取"}`}>
            <div className="flex flex-wrap gap-2">
              <Badge tone="ok">已应用 <Val v={lastRun?.counts.applied ?? null} /></Badge>
              <Badge tone="muted">跳过 <Val v={lastRun?.counts.skipped ?? null} /></Badge>
              <Badge tone="unk">结果未知 <Val v={lastRun?.counts.unknown ?? null} /></Badge>
              <Badge tone="bad">失败 <Val v={lastRun?.counts.failed ?? null} /></Badge>
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
                    <li key={c.account} className="flex items-center justify-between py-2 text-[13px]">
                      <span title={c.account}>{c.account.slice(0, 6)}…{c.account.slice(-4)}</span>
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

      {/* Apply */}
      <Modal
        open={!!applyTarget}
        onClose={() => !applying && data.closeDetail()}
        title="模拟应用目标余额"
        footer={
          <>
            <Button onClick={() => data.closeDetail()} disabled={applying}>
              取消
            </Button>
            <Button variant="primary" disabled={!ack || applying || !applyTarget || !canApply(applyTarget)} onClick={doApply}>
              {applying ? "写入并读回中…" : "确认应用"}
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
                ["目标余额", <span key="t" className="font-semibold"><Val v={applyTarget.recommendation} /></span>],
                ["冻结余额", <Val key="f" v={applyTarget.frozen} />],
                ["依据", applyTarget.view.latest_recommendation ? `#${applyTarget.view.latest_recommendation.id} · ${time(applyTarget.view.latest_recommendation.generated_at)}` : "尚未生成"],
              ]}
            />
            <Callout tone="warn">{data.actionError ?? "仅作用于已绑定的模拟实例整个钱包。服务端核对当前完整建议并按来源精度量化；本次只提交一次。结果未知时禁止重发，只能读取历史并核对原操作。"}</Callout>
            <Checkbox checked={ack} onChange={setAck} disabled={!canApply(applyTarget)} label="我已确认这是模拟实例，按当前完整正数建议提交一次条件应用。" />
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
                ["目标", <Val key="t" v={reconcile.target} />],
                ["发起时间", time(reconcile.at)],
                ["本地已确认钱包读数", <span key="r" className="font-semibold"><Val v={participants.find(p => p.id === reconcile.participant)?.balance} /></span>],
              ]}
            />
            <Callout tone="unk">{data.actionError ?? reconcile.note} 本地钱包读数不能代替独立操作证明；只读核对不重发资金请求，不接受人工覆盖结果。</Callout>
          </div>
        )}
      </Modal>
    </>
  );
}
