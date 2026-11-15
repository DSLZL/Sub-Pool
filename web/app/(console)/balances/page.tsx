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

const REC_STATE: Record<WalletRow["recState"], { label: string; tone: "ok" | "unk" | "warn" | "bad" | "muted" }> = {
  scenario: { label: "模拟条件建议", tone: "warn" },
  insufficient: { label: "依据不足", tone: "unk" },
  stale: { label: "已失效", tone: "muted" },
  blocked: { label: "仅建议", tone: "bad" },
};

export default function BalancesPage() {
  const { toast, timezone } = useApp();
  const data = useWalletData();
  const { isAdmin, rows: participants, operations: visibleOps } = data;
  const [tab, setTab] = useState("recs");
  const gateOpen = data.ready && participants.some(p => p.view.applicable);
  const sel = data.participant, reconcile = data.operation;
  const setSel = (row: WalletRow | null) => row ? void data.inspectParticipant(row.id) : data.closeDetail();
  const setReconcile = (row: WalletOperation | null) => row ? void data.inspectOperation(row.id) : data.closeDetail();
  const [applyTarget, setApplyTarget] = useState<WalletRow | null>(null);
  const [ack, setAck] = useState(false);
  const applying = false, auto = data.automation?.enabled ?? false;
  const [autoConfirm, setAutoConfirm] = useState(false);
  const canApply = (p: WalletRow) => isAdmin && data.ready && p.view.applicable;
  const time = (value: string | null | undefined) => displayTime(value, timezone);
  const doApply = () => toast({ tone: "warn", title: "余额应用不可用", body: "尚无已核验的正数目标，没有提交远端写入。" });

  return (
    <>
      <PageHeader
        title="余额建议与应用"
        tag="S2A-BAL-001 ~ 008"
        desc={data.error ?? (!data.allowed ? "未授予余额读取权限。" : !data.ready ? "正在完整读取钱包与请求记录…" : participants.some(p => p.simulated) ? "模拟协议验收：余额与冻结独立显示，条件建议使用实际扣款单位；财务应用关闭。点击标签可重新读取本地记录。" : "原生钱包余额与冻结分开显示，币种未核验；当前无可应用目标。点击标签可重新读取本地记录。")}
      />

      {isAdmin && (
        <div className="mb-4 space-y-2">
          {!gateOpen && <GateNotice gate="余额写入">钱包整体管理范围、standard 计费路径与冻结处理规则尚未全部通过核验，应用按钮保持关闭。</GateNotice>}
          <label className="inline-flex items-center gap-2 text-[12px] text-muted">
            <input type="checkbox" checked={gateOpen} disabled onChange={() => data.reload()} className="accent-[var(--brand)]" />
            余额写入资格由服务端核验，当前不可用
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
                        title={!gateOpen ? "服务端应用资格不可用" : p.recReason}
                        onClick={() => setApplyTarget(p)}
                      >
                        应用
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
              onChange={() => { if (data.automation?.can_enable) setAutoConfirm(true); }}
              label="启用自动应用"
              desc="首次交付默认关闭。开启后只处理已验证范围；混用钱包、活动冻结、零目标和结果未知一律跳过。"
              disabled={!data.automation?.can_enable}
            />
            {!gateOpen && <p className="mt-3 text-[12px] text-warn">余额写入门禁未通过，无法开启。</p>}
          </Card>
          <Card title={`最近一次运行 · ${data.automation ? "暂无可确认记录" : "尚未读取"}`}>
            <div className="flex flex-wrap gap-2">
              <Badge tone="ok">已应用 <Val v={null} /></Badge>
              <Badge tone="muted">跳过 <Val v={null} /></Badge>
              <Badge tone="unk">结果未知 <Val v={null} /></Badge>
              <Badge tone="bad">失败 <Val v={null} /></Badge>
            </div>
            <ul className="mt-3 space-y-1 text-[12.5px] text-muted">
              <li>· {data.automation?.execution_state === "not_implemented" ? "自动执行暂不可用。" : "尚未确认自动执行状态。"}</li>
              <li>· 当前余额写入条件未通过，开关保持关闭。</li>
              <li>· 本地拒绝请求不计作自动运行或远端成功。</li>
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
                ["最近应用请求", sel.lastApplied ? `${time(sel.lastApplied.at)} · 未派发` : "尚无本地请求"],
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
        onClose={() => !applying && setApplyTarget(null)}
        title="应用目标余额"
        footer={
          <>
            <Button onClick={() => setApplyTarget(null)} disabled={applying}>
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
            <Callout tone="warn">写入针对该用户的整个全局钱包。若期间发生实时扣费或外部修改，结果可能与预期不同。本系统不保证跨用户事务或 CAS。</Callout>
            <Checkbox checked={ack} onChange={setAck} disabled={!gateOpen} label="我已确认本系统对该钱包具有完整管理范围，且目标为当前有效的正数建议。" />
          </div>
        )}
      </Modal>

      <Modal
        open={autoConfirm}
        onClose={() => setAutoConfirm(false)}
        title="开启自动应用"
        footer={
          <>
            <Button onClick={() => setAutoConfirm(false)}>取消</Button>
            <Button
              variant="primary"
              disabled={!data.automation?.can_enable}
              onClick={() => {
                setAutoConfirm(false);
                data.reload();
              }}
            >
              开启
            </Button>
          </>
        }
      >
        <p className="text-[13px]">自动应用不会处理：混用钱包、活动冻结、零目标、结果未知未核对的用户。这不是无人值守的完整闭环，仍需定期查看运行结果。</p>
      </Modal>

      <Modal
        open={!!reconcile}
        onClose={() => setReconcile(null)}
        title={`应用请求记录 ${reconcile?.id ?? ""}`}
        footer={
          <>
            <Button onClick={() => setReconcile(null)}>稍后处理</Button>
            <Button
              disabled
            >
              标记未生效
            </Button>
            <Button
              variant="primary"
              disabled
            >
              确认已生效
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
            <Callout tone="unk">{reconcile.note} 本地钱包读数不构成远端应用凭证；当前没有可接受的人工确认操作。</Callout>
          </div>
        )}
      </Modal>
    </>
  );
}
