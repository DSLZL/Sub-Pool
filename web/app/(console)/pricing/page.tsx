"use client";

import { useState } from "react";
import { RotateCcw, RefreshCw, Pencil } from "lucide-react";
import {
  Badge,
  Button,
  Callout,
  CapBadge,
  Card,
  Checkbox,
  ConfirmModal,
  Field,
  Grid,
  Input,
  KV,
  Modal,
  Mono,
  Outcome,
  PageHeader,
  Select,
  Switch,
  Table,
  Tabs,
  Val,
  cx,
} from "@/components/ui";
import { useApp } from "@/lib/store";
import { accountName, modelPrices, pricingChanges, pricingGroups } from "@/lib/mock";

export default function PricingPage() {
  const { toast } = useApp();
  const [tab, setTab] = useState("groups");
  const [gid, setGid] = useState(pricingGroups[0].id);
  const g = pricingGroups.find((x) => x.id === gid)!;
  const [editKind, setEditKind] = useState<null | "fast" | "model" | "long">(null);
  const [ack, setAck] = useState(false);
  const [fast, setFast] = useState("1.5");
  const [longOn, setLongOn] = useState(true);
  const [revert, setRevert] = useState<string | null>(null);

  const writable = g.runMode === "standard";

  return (
    <>
      <PageHeader
        title="分组计价"
        tag="S2A-PRICE-001 ~ 007"
        desc="通过 Sub2API 原生分组能力调整计价。修改会影响该分组内全部实际调用者，并且必须读回确认。当前改价不会重写历史请求。"
      />
      <Callout tone="warn" className="mb-4" title="计价写入能力：未验证">
        <span className="inline-flex items-center gap-2">
          <CapBadge c="unverified" /> 需确认 standard 模式下写入后读回生效。simple 模式可能忽略计价字段，2xx 不代表生效。
        </span>
      </Callout>
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "groups", label: "目标分组" },
          { id: "changes", label: "变更记录" },
        ]}
      />

      {tab === "groups" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[280px_1fr]">
          <Card title="上游分组" pad={false}>
            <ul className="divide-y divide-line">
              {pricingGroups.map((x) => (
                <li key={x.id}>
                  <button onClick={() => setGid(x.id)} className={cx("w-full px-4 py-3 text-left", gid === x.id ? "bg-brand-soft" : "hover:bg-surface-2")}>
                    <div className="font-mono text-[13px] font-medium">{x.name}</div>
                    <div className="mt-0.5 text-[12px] text-muted">
                      {x.users} 位调用者 · 关联 {x.visibleLinks}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
          <div className="space-y-4">
            <Card
              title={<Mono>{g.name}</Mono>}
              desc="原生分组不是本地额度池"
              actions={
                <Button size="sm" icon={<RefreshCw size={12} />} onClick={() => toast({ tone: "info", title: "已重新读取分组配置" })}>
                  重新读取
                </Button>
              }
            >
              <KV
                cols={3}
                items={[
                  ["关联纳管账号", g.accounts.map(accountName).join("、")],
                  ["影响调用者", `${g.users} 位（含非本系统成员）`],
                  ["运行模式", g.runMode ? <Badge key="r" tone="ok">{g.runMode}</Badge> : <Badge key="r" tone="unk">无法确认</Badge>],
                  ["配置来源", g.source],
                  ["关联可见性", g.visibleLinks === "完整" ? "完整" : <Badge key="v" tone="warn">部分可见，可能存在其他关联</Badge>],
                ]}
              />
              {!writable && <Callout tone="bad" className="mt-3">无法确认运行模式，此分组的计价写入已禁用。</Callout>}
            </Card>
            <Grid cols={2}>
              <Card title="FAST 计费规则" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable} onClick={() => setEditKind("fast")}>调整</Button>}>
                <div className="text-[18px] font-semibold">{g.fast}</div>
              </Card>
              <Card title="长上下文计费" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable} onClick={() => setEditKind("long")}>调整</Button>}>
                <div className="text-[18px] font-semibold">{g.longCtx === "未知" ? <Val v={null} /> : g.longCtx}</div>
              </Card>
            </Grid>
            <Card title="模型价格" desc="USD / 百万 Token · 缺价不补 0" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable} onClick={() => setEditKind("model")}>调整</Button>} pad={false}>
              <Table
                rows={modelPrices}
                rowKey={(m) => m.model}
                cols={[
                  { key: "m", label: "模型", render: (m) => <Mono>{m.model}</Mono> },
                  { key: "i", label: "输入", align: "right", render: (m) => <Val v={m.input} digits={3} /> },
                  { key: "o", label: "输出", align: "right", render: (m) => <Val v={m.output} digits={3} /> },
                  { key: "c", label: "缓存读", align: "right", render: (m) => <Val v={m.cache} digits={3} /> },
                  { key: "s", label: "来源", render: (m) => <span className="text-[12px] text-muted">{m.source}</span> },
                ]}
              />
            </Card>
          </div>
        </div>
      )}

      {tab === "changes" && (
        <div className="space-y-4">
          {pricingChanges.map((c) => (
            <Card
              key={c.id}
              title={
                <span className="flex items-center gap-2">
                  <Mono>{c.id}</Mono> {c.kind}
                </span>
              }
              desc={`${c.at} · ${c.by}`}
              actions={
                <>
                  {c.targets.some((t) => t.state === "unknown" || t.state === "failed") && (
                    <Button size="sm" icon={<RefreshCw size={12} />} onClick={() => toast({ tone: "info", title: "已重新读回", body: "openai-north 仍无法确认，保持结果未知。" })}>
                      读回核对
                    </Button>
                  )}
                  <Button size="sm" icon={<RotateCcw size={12} />} disabled={!c.revertable} title={c.revertable ? undefined : "存在冲突或无可信前状态，不可撤回"} onClick={() => setRevert(c.id)}>
                    撤回
                  </Button>
                </>
              }
              pad={false}
            >
              <Table
                rows={c.targets}
                rowKey={(t) => t.group}
                cols={[
                  { key: "g", label: "目标分组", render: (t) => <Mono>{t.group}</Mono> },
                  { key: "s", label: "状态", render: (t) => <Outcome k={t.state} /> },
                  { key: "n", label: "说明", render: (t) => <span className="text-muted">{t.note || "—"}</span> },
                ]}
              />
            </Card>
          ))}
          <Callout tone="info">多组变更不保证原子性；检测到外部改动会停止后续写入。读后再写仍可能存在竞态，本系统不宣称完全消除覆盖风险。</Callout>
        </div>
      )}

      <Modal
        open={!!editKind}
        onClose={() => setEditKind(null)}
        title={editKind === "fast" ? "调整 FAST 计费规则" : editKind === "long" ? "调整长上下文计费" : "调整模型价格"}
        footer={
          <>
            <Button onClick={() => setEditKind(null)}>取消</Button>
            <Button
              variant="primary"
              disabled={!ack}
              onClick={() => {
                setEditKind(null);
                setAck(false);
                toast({ tone: "ok", title: "已写入并读回确认", body: `${g.name}：读回值与提交一致。变更记录 PC-15。` });
              }}
            >
              写入并读回
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          {editKind === "fast" && (
            <Field label="FAST 倍率" hint="由管理员独立确定，本系统不提供推荐值。">
              <Input type="number" step="0.1" min="0" value={fast} onChange={(e) => setFast(e.target.value)} />
            </Field>
          )}
          {editKind === "long" && <Switch checked={longOn} onChange={setLongOn} label="启用长上下文计费" desc="该开关不改变真实订阅额度。" />}
          {editKind === "model" && (
            <>
              <Field label="模型">
                <Select options={modelPrices.map((m) => m.model)} />
              </Field>
              <Grid cols={3}>
                <Field label="输入">
                  <Input type="number" step="0.001" placeholder="1.250" />
                </Field>
                <Field label="输出">
                  <Input type="number" step="0.001" placeholder="10.000" />
                </Field>
                <Field label="缓存读">
                  <Input type="number" step="0.001" placeholder="0.125" />
                </Field>
              </Grid>
            </>
          )}
          <Callout tone="warn">
            将影响 <Mono>{g.name}</Mono> 内全部 {g.users} 位调用者，包括非本系统成员。写入前会记录当前值作为撤回依据。
          </Callout>
          <Checkbox checked={ack} onChange={setAck} label="我了解此修改影响整个分组，且不会重写历史费用。" />
        </div>
      </Modal>

      <ConfirmModal
        open={!!revert}
        onClose={() => setRevert(null)}
        onConfirm={() => toast({ tone: "ok", title: `${revert} 已撤回`, body: "openai-team：已恢复为修改前的值并读回确认。" })}
        title={`撤回 ${revert ?? ""}`}
        confirmLabel="撤回"
      >
        <p>将 openai-team 的 FAST 规则从 1.5× 恢复为修改前记录的值。</p>
        <p className="text-muted">若读取时发现该值已被外部修改，撤回会被拒绝。不会修改历史费用。</p>
      </ConfirmModal>
    </>
  );
}
