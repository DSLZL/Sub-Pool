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
import { useScope } from "@/lib/scope";
import { accountName, autoApply, balanceOps as initialOps, participantName, type Participant, type BalanceOp } from "@/lib/mock";

const REC_STATE: Record<Participant["recState"], { label: string; tone: "ok" | "unk" | "warn" | "bad" | "muted" }> = {
  ready: { label: "可应用", tone: "ok" },
  insufficient: { label: "依据不足", tone: "unk" },
  stale: { label: "已失效", tone: "muted" },
  temporary: { label: "临时放宽中", tone: "warn" },
  blocked: { label: "仅建议", tone: "bad" },
};

const contributions: Record<string, { account: string; amount: number | null; state: string }[]> = {
  m1: [
    { account: "a1", amount: 14.2, state: "已核验" },
    { account: "a2", amount: 9.8, state: "已核验" },
  ],
  m2: [
    { account: "a1", amount: 7.4, state: "已核验" },
    { account: "a2", amount: 8.1, state: "已核验（临时轮次）" },
  ],
  m3: [
    { account: "a1", amount: 8.0, state: "已核验" },
    { account: "a3", amount: null, state: "观测过期" },
    { account: "a4", amount: null, state: "采集失败" },
  ],
  m4: [
    { account: "a3", amount: 11.0, state: "已核验" },
    { account: "a4", amount: 9.0, state: "已核验" },
  ],
};

export default function BalancesPage() {
  const { toast } = useApp();
  const { isAdmin, participants, canAccount, canParticipant } = useScope();
  const [tab, setTab] = useState("recs");
  const [gateOpen, setGateOpen] = useState(false);
  const [ops, setOps] = useState<BalanceOp[]>(initialOps);
  const [sel, setSel] = useState<Participant | null>(null);
  const [applyTarget, setApplyTarget] = useState<Participant | null>(null);
  const [ack, setAck] = useState(false);
  const [applying, setApplying] = useState(false);
  const [auto, setAuto] = useState(autoApply.enabled);
  const [autoConfirm, setAutoConfirm] = useState(false);
  const [reconcile, setReconcile] = useState<BalanceOp | null>(null);

  const visibleOps = ops.filter((o) => isAdmin || canParticipant(o.participant));
  const hasUnknownFor = (pid: string) => ops.some((o) => o.participant === pid && (o.outcome === "unknown" || o.outcome === "reconcile"));
  const canApply = (p: Participant) => gateOpen && p.recState === "ready" && (p.recommendation ?? 0) > 0 && !hasUnknownFor(p.id);

  const doApply = () => {
    if (!applyTarget) return;
    setApplying(true);
    setTimeout(() => {
      setApplying(false);
      const outcome = applyTarget.id === "m1" ? "confirmed" : "unknown";
      setOps((os) => [
        {
          id: `bo-${5530 + os.length}`,
          at: "2026-11-04 14:21",
          actor: "admin",
          participant: applyTarget.id,
          rec: "rec-9910",
          before: applyTarget.balance,
          target: applyTarget.recommendation!,
          after: outcome === "confirmed" ? applyTarget.recommendation : null,
          outcome,
          note: outcome === "confirmed" ? "读回一致" : "上游超时；未取得读回证据",
        },
        ...os,
      ]);
      toast(
        outcome === "confirmed"
          ? { tone: "ok", title: "余额已应用并读回确认", body: `${applyTarget.name} → ${applyTarget.recommendation?.toFixed(2)}` }
          : { tone: "unk", title: "结果未知", body: "上游未在时限内返回，已标记为待人工核对。不会自动重试。" },
      );
      setApplyTarget(null);
      setAck(false);
    }, 1300);
  };

  return (
    <>
      <PageHeader
        title="余额建议与应用"
        tag="S2A-BAL-001 ~ 008"
        desc="建议基于份额、真实消费与估算生成。钱包是 Sub2API 用户的全局钱包；余额与冻结余额分开显示。"
      />

      {isAdmin && (
        <div className="mb-4 space-y-2">
          {!gateOpen && <GateNotice gate="余额写入">钱包整体管理范围、standard 计费路径与冻结处理规则尚未全部通过核验，应用按钮保持关闭。</GateNotice>}
          <label className="inline-flex items-center gap-2 text-[12px] text-muted">
            <input type="checkbox" checked={gateOpen} onChange={(e) => setGateOpen(e.target.checked)} className="accent-[var(--brand)]" />
            演示：假设余额写入门禁已通过
          </label>
        </div>
      )}

      <Tabs
        value={tab}
        onChange={setTab}
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
              { key: "r", label: "读取时间", render: (p) => <span className="num text-[12px] text-muted">{p.balanceReadAt?.slice(5) ?? "未知"}</span> },
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
                        title={!gateOpen ? "能力门禁未通过" : hasUnknownFor(p.id) ? "存在结果未知的操作，需先核对" : p.recState !== "ready" ? p.recReason : undefined}
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
              { key: "t", label: "时间", render: (o) => <span className="num whitespace-nowrap">{o.at.slice(5)}</span> },
              { key: "who", label: "发起", render: (o) => (isAdmin ? o.actor : "—") },
              { key: "p", label: "参与者", render: (o) => participantName(o.participant) },
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
                  isAdmin &&
                  o.outcome === "unknown" && (
                    <Button size="sm" icon={<SearchCheck size={12} />} onClick={() => setReconcile(o)}>
                      核对
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
              onChange={(v) => (v ? setAutoConfirm(true) : (setAuto(false), toast({ tone: "warn", title: "自动应用已关闭", body: "关闭不会撤销已经写入的余额。" })))}
              label="启用自动应用"
              desc="首次交付默认关闭。开启后只处理已验证范围；混用钱包、活动冻结、零目标和结果未知一律跳过。"
              disabled={!gateOpen}
            />
            {!gateOpen && <p className="mt-3 text-[12px] text-warn">余额写入门禁未通过，无法开启。</p>}
          </Card>
          <Card title={`最近一次运行 · ${autoApply.lastRun}`}>
            <div className="flex flex-wrap gap-2">
              <Badge tone="ok">已应用 {autoApply.results.applied}</Badge>
              <Badge tone="muted">跳过 {autoApply.results.skipped}</Badge>
              <Badge tone="unk">结果未知 {autoApply.results.unknown}</Badge>
              <Badge tone="bad">失败 {autoApply.results.failed}</Badge>
            </div>
            <ul className="mt-3 space-y-1 text-[12.5px] text-muted">
              <li>· 郑一鸣：钱包混用自助充值 → 跳过</li>
              <li>· Ana Torres：全局建议依据不足 → 跳过</li>
              <li>· Kai Müller：参与者已停用 → 跳过</li>
              <li>· 周承：上游超时 → 结果未知，后续自动应用暂停该用户</li>
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
                <div className="text-[12px] text-muted">钱包余额（原生 balance）</div>
                <div className="mt-1 text-[22px] font-semibold"><Val v={sel.balance} /></div>
                <div className="text-[12px] text-muted">读取于 {sel.balanceReadAt ?? "未知"}</div>
              </Card>
              <Card>
                <div className="text-[12px] text-muted">冻结余额（原生 frozen_balance）</div>
                <div className="mt-1 text-[22px] font-semibold"><Val v={sel.frozen} /></div>
                <div className="text-[12px] text-muted">按原值展示，不从余额中再扣一次</div>
              </Card>
            </Grid>
            <Card title="全局建议组成" desc="服务端汇总该钱包全部已核验的贡献；展示时按你的权限收窄">
              <ul className="divide-y divide-line">
                {(contributions[sel.id] ?? [])
                  .filter((c) => isAdmin || canAccount(c.account))
                  .map((c) => (
                    <li key={c.account} className="flex items-center justify-between py-2 text-[13px]">
                      <span>{accountName(c.account)}</span>
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
                ["建议依据时间", "2026-11-04 14:15"],
                ["最近应用", sel.lastApplied ? `${sel.lastApplied.at} → ${sel.lastApplied.target.toFixed(2)}` : "—"],
                ["最近结果", sel.lastApplied ? <Outcome key="o" k={sel.lastApplied.outcome} /> : "—"],
                ["钱包读数时间与建议时间", "不同时刻，未合并为同一时点"],
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
            <Button variant="primary" disabled={!ack || applying} onClick={doApply}>
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
                ["依据", "rec-9910 · 11-04 14:15"],
              ]}
            />
            <Callout tone="warn">写入针对该用户的整个全局钱包。若期间发生实时扣费或外部修改，结果可能与预期不同。本系统不保证跨用户事务或 CAS。</Callout>
            <Checkbox checked={ack} onChange={setAck} label="我已确认本系统对该钱包具有完整管理范围，且目标为当前有效的正数建议。" />
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
              onClick={() => {
                setAuto(true);
                setAutoConfirm(false);
                toast({ tone: "ok", title: "自动应用已开启", body: "下次运行时生效。" });
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
        title={`人工核对 ${reconcile?.id ?? ""}`}
        footer={
          <>
            <Button onClick={() => setReconcile(null)}>稍后处理</Button>
            <Button
              onClick={() => {
                setOps((os) => os.map((o) => (o.id === reconcile?.id ? { ...o, outcome: "failed", note: "人工核对：读回余额未变化" } : o)));
                setReconcile(null);
                toast({ tone: "bad", title: "已标记为失败" });
              }}
            >
              标记未生效
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                setOps((os) => os.map((o) => (o.id === reconcile?.id ? { ...o, outcome: "confirmed", after: o.target, note: "人工核对：读回一致" } : o)));
                setReconcile(null);
                toast({ tone: "ok", title: "已确认生效" });
              }}
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
                ["参与者", participantName(reconcile.participant)],
                ["目标", <Val key="t" v={reconcile.target} />],
                ["发起时间", reconcile.at],
                ["最新读回余额", <span key="r" className="font-semibold"><Val v={15.38} /></span>],
              ]}
            />
            <Callout tone="unk">最新读回 15.38，与目标 15.50 接近，期间可能发生了实时扣费。请结合上游余额记录判断，不要仅凭超时判定失败。</Callout>
          </div>
        )}
      </Modal>
    </>
  );
}
