"use client";

import { useState } from "react";
import { Zap, Square, Pencil } from "lucide-react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Checkbox,
  ConfirmModal,
  Field,
  Grid,
  Input,
  KV,
  Modal,
  Outcome,
  PageHeader,
  Switch,
  Table,
  Tabs,
  Textarea,
  Val,
  cx,
} from "@/components/ui";
import { useApp } from "@/lib/store";
import { accountName, participantName, tempRound, tempSettlements } from "@/lib/mock";

const checks = [
  { k: "数据新鲜度", ok: true, note: "Alpha 主账号、Alpha 副账号 15 分钟内有有效观测" },
  { k: "参与者绑定", ok: true, note: "3 位参与者均已绑定唯一上游用户" },
  { k: "官方周期", ok: true, note: "两个账号的当前周期与重置时间已确认" },
  { k: "运行模式", ok: true, note: "standard（11-04 13:58 检查）" },
  { k: "钱包计费路径", ok: true, note: "相关分组按余额计费" },
  { k: "钱包整体管理范围", ok: false, note: "郑一鸣 的钱包混用自助充值（不在此池，不影响本轮）" },
];

export default function TemporaryPage() {
  const { toast } = useApp();
  const [tab, setTab] = useState("round");
  const [round, setRound] = useState<typeof tempRound | null>(tempRound);
  const [settles, setSettles] = useState(tempSettlements);
  const [startOpen, setStartOpen] = useState(false);
  const [mode, setMode] = useState<"carry" | "no_carry">("carry");
  const [informed, setInformed] = useState(false);
  const [understand, setUnderstand] = useState(false);
  const [endOpen, setEndOpen] = useState(false);
  const [adjust, setAdjust] = useState<(typeof tempSettlements)[number] | null>(null);
  const [adjVal, setAdjVal] = useState("");
  const [adjNote, setAdjNote] = useState("");

  return (
    <>
      <PageHeader
        title="临时放宽"
        tag="S2A-TEMP-001 ~ 010"
        desc="在一个明确轮次内临时放宽余额建议。它只影响本地建议和按授权应用的钱包目标，不会增加上游订阅容量。"
        actions={
          !round || round.status !== "active" ? (
            <Button variant="primary" icon={<Zap size={14} />} onClick={() => setStartOpen(true)}>
              开启新轮次
            </Button>
          ) : (
            <Button variant="danger" icon={<Square size={13} />} onClick={() => setEndOpen(true)}>
              提前终止
            </Button>
          )
        }
      />

      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "round", label: "当前轮次" },
          { id: "settle", label: "后续权益处理" },
        ]}
      />

      {tab === "round" &&
        (round ? (
          <div className="space-y-4">
            <Grid cols={3}>
              <Card>
                <div className="text-[12px] text-muted">轮次</div>
                <div className="mt-1 flex items-center gap-2">
                  <span className="text-[20px] font-semibold">{round.id}</span>
                  <Badge tone={round.status === "active" ? "warn" : "muted"} dot>
                    {round.status === "active" ? "进行中" : round.status === "terminated" ? "已提前终止" : "已结束"}
                  </Badge>
                </div>
                <div className="mt-1 text-[12px] text-muted">
                  {round.mode === "carry" ? "结转模式" : "不结转模式"} · {round.startedAt} 由 {round.startedBy} 开启
                </div>
              </Card>
              <Card className="lg:col-span-2">
                <div className="text-[12px] text-muted">预计结束依据</div>
                <div className="mt-1 text-[14px] font-medium">{round.endBasis}</div>
                <div className="mt-1 text-[12px] text-muted">首个相关账号确认官方换周期或最早截止到达时，全局恢复普通建议；其余账号的原周期分别处理。</div>
              </Card>
            </Grid>

            <Card title="逐账号进度" pad={false}>
              <Table
                rows={round.accounts}
                rowKey={(a) => a.account}
                cols={[
                  { key: "a", label: "账号", render: (a) => accountName(a.account) },
                  { key: "e", label: "原周期结束", render: (a) => <span className="num">{a.cycleEnd}</span> },
                  { key: "c", label: "轮次内真实消费", align: "right", render: (a) => <Val v={a.consumed} prefix="$" /> },
                  { key: "p", label: "进度", render: (a) => a.progress },
                  { key: "s", label: "后续处理", render: (a) => <Outcome k={a.state} /> },
                ]}
              />
            </Card>

            <Grid cols={2}>
              <Card title="恢复状态" desc="两件事分别追踪">
                <ul className="space-y-3 text-[13px]">
                  <li className="flex items-center justify-between gap-3">
                    <span>普通建议已恢复</span>
                    <Badge tone={round.status === "active" ? "muted" : "ok"}>{round.status === "active" ? "未到时" : "是"}</Badge>
                  </li>
                  <li className="flex items-center justify-between gap-3">
                    <span>远端高余额已收回</span>
                    <Badge tone={round.status === "active" ? "muted" : "unk"}>{round.status === "active" ? "未到时" : "待核对"}</Badge>
                  </li>
                </ul>
                <Callout tone="warn" className="mt-3">
                  自动应用关闭、采集暂停、零目标或写入失败时，远端余额可能仍处于放宽状态，需要人工处理。
                </Callout>
              </Card>
              <Card title="用满提醒">
                <Switch
                  checked={round.fullReminder}
                  onChange={(v) => {
                    setRound({ ...round, fullReminder: v });
                    toast({ tone: "info", title: v ? "已开启本轮用满提醒" : "已停止本轮用满提醒", body: v ? undefined : "不会再发送本轮的新提醒。" });
                  }}
                  label="接近用满时提醒管理员"
                  desc="每个轮次需单独开启。基于观测判断，不承诺瞬时通知。"
                />
                <p className="mt-3 text-[12px] text-muted">最近发送：11-03 19:02 · 成功</p>
              </Card>
            </Grid>

            <Card title="受影响参与者">
              <div className="flex flex-wrap gap-2">
                {round.affected.map((m) => (
                  <Badge key={m}>{participantName(m)}</Badge>
                ))}
              </div>
              <p className="mt-2 text-[12px] text-muted">普通用户只能在本人建议中看到临时影响，看不到全局轮次与其他账号进度。</p>
            </Card>
          </div>
        ) : (
          <Card>
            <p className="text-[13px] text-muted">当前没有进行中的临时轮次。</p>
          </Card>
        ))}

      {tab === "settle" && (
        <Card pad={false} title="待结算、异常与完成记录" desc="证据不足时挂起，不补造未采样的尾部消费；各账号进度不合成为整体成功。">
          <Table
            rows={settles}
            rowKey={(s) => s.id}
            cols={[
              { key: "r", label: "轮次", render: (s) => s.round },
              { key: "a", label: "账号 / 原周期", render: (s) => <div><div>{accountName(s.account)}</div><div className="num text-[12px] text-muted">{s.cycle}</div></div> },
              { key: "m", label: "成员", render: (s) => participantName(s.member) },
              {
                key: "v",
                label: "权益调整",
                align: "right",
                render: (s) => (s.adj === null ? <Val v={null} /> : <span className={cx("num", s.adj < 0 ? "text-bad" : "text-ok")}>{s.adj > 0 ? "+" : ""}{s.adj.toFixed(2)}</span>),
              },
              { key: "s", label: "状态", render: (s) => <Outcome k={s.state} /> },
              { key: "why", label: "依据 / 原因", render: (s) => <span className="text-[12px] text-muted">{s.reason || "—"} · {s.evidenceAt}</span> },
              {
                key: "x",
                label: "",
                render: (s) =>
                  s.state === "confirmed" && s.account === "a1" && (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Pencil size={12} />}
                      onClick={() => {
                        setAdjust(s);
                        setAdjVal(String(s.adj ?? ""));
                        setAdjNote("");
                      }}
                    >
                      修订
                    </Button>
                  ),
              },
            ]}
          />
        </Card>
      )}

      {/* Start */}
      <Modal
        open={startOpen}
        onClose={() => setStartOpen(false)}
        title="开启临时放宽轮次"
        wide
        footer={
          <>
            <Button onClick={() => setStartOpen(false)}>取消</Button>
            <Button
              variant="primary"
              disabled={!understand || (mode === "no_carry" && !informed)}
              onClick={() => {
                setRound({ ...tempRound, id: "R-08", mode, status: "active", startedAt: "2026-11-04 14:21" });
                setStartOpen(false);
                toast({ tone: "ok", title: "轮次 R-08 已开启", body: "开启不代表已应用高余额，请在余额页面查看应用状态。" });
              }}
            >
              开启轮次
            </Button>
          </>
        }
      >
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <div className="space-y-3">
            <div className="text-[13px] font-semibold">启用前核验</div>
            <ul className="space-y-2">
              {checks.map((c) => (
                <li key={c.k} className="flex items-start gap-2 text-[12.5px]">
                  <Badge tone={c.ok ? "ok" : "warn"}>{c.ok ? "通过" : "注意"}</Badge>
                  <div>
                    <div className="font-medium">{c.k}</div>
                    <div className="text-muted">{c.note}</div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
          <div className="space-y-4">
            <div className="text-[13px] font-semibold">选择模式</div>
            {(
              [
                ["carry", "结转", "本轮借用的权益在后续周期补偿或扣减。"],
                ["no_carry", "不结转", "不新增后续补偿；需确认已告知参与者。"],
              ] as const
            ).map(([id, label, desc]) => (
              <button
                key={id}
                onClick={() => setMode(id)}
                className={cx("w-full rounded-md border p-3 text-left", mode === id ? "border-brand bg-brand-soft" : "border-line hover:bg-surface-2")}
              >
                <div className="text-[13px] font-medium">{label}</div>
                <div className="text-[12px] text-muted">{desc}</div>
              </button>
            ))}
            <Callout tone="info">影响范围：Alpha 共享池 · 2 个账号 · 3 位参与者的全局钱包。轮次开启后不能静默切换模式。</Callout>
            {mode === "no_carry" && <Checkbox checked={informed} onChange={setInformed} label="我已告知所有受影响参与者（此勾选仅记录业务确认，系统不会逐人发邮件）" />}
            <Checkbox checked={understand} onChange={setUnderstand} label="我了解：结束建议不等于收回上游余额，需单独核对。" />
          </div>
        </div>
      </Modal>

      <ConfirmModal
        open={endOpen}
        onClose={() => setEndOpen(false)}
        onConfirm={() => {
          if (round) setRound({ ...round, status: "terminated" });
          toast({ tone: "warn", title: `轮次 ${round?.id} 已终止`, body: "普通建议已恢复；远端余额需在余额页面核对。" });
        }}
        title={`提前终止轮次 ${round?.id ?? ""}`}
        phrase={round?.id}
        confirmLabel="终止轮次"
        danger
      >
        <ul className="list-disc space-y-1 pl-5">
          <li>保留本轮全部消费事实与合同</li>
          <li>取消本轮尚未处理的后续结转</li>
          <li>终止不可撤销；同一官方周期可另开新轮，旧轮不会复活</li>
          <li>恢复钱包仍需核对，与自然结束分别记录</li>
        </ul>
      </ConfirmModal>

      <Modal
        open={!!adjust}
        onClose={() => setAdjust(null)}
        title="修订当前结转权益"
        footer={
          <>
            <Button onClick={() => setAdjust(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={!adjNote.trim() || isNaN(parseFloat(adjVal))}
              onClick={() => {
                setSettles((ss) => ss.map((s) => (s.id === adjust?.id ? { ...s, adj: parseFloat(adjVal), reason: `人工修订：${adjNote}` } : s)));
                toast({ tone: "ok", title: "权益已修订", body: "已生成新建议。清除权益不等于清零远端钱包。" });
                setAdjust(null);
              }}
            >
              保存修订
            </Button>
          </>
        }
      >
        {adjust && (
          <div className="space-y-3">
            <KV items={[["成员", participantName(adjust.member)], ["周期", adjust.cycle], ["当前值", adjust.adj?.toFixed(2) ?? "—"]]} />
            <Field label="修订后（输入 0 以清除）">
              <Input type="number" step="0.01" value={adjVal} onChange={(e) => setAdjVal(e.target.value)} />
            </Field>
            <Field label="说明（必填）">
              <Textarea value={adjNote} onChange={(e) => setAdjNote(e.target.value)} />
            </Field>
            <Callout tone="info">只修改该成员在当前周期的权益，不改变合同、真实消费或其他成员。</Callout>
          </div>
        )}
      </Modal>
    </>
  );
}
