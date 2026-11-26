"use client";

import { glossary, uiCopy } from "@/lib/glossary";
import { useState } from "react";
import { RefreshCw, Pencil, RotateCcw } from "lucide-react";
import { pricingChange, type PricingChange, type PricingOperation } from "@sub-pool/contracts/pricing";
import { Badge, Button, Callout, CapBadge, Card, Checkbox, ConfirmModal, Empty, Field, Grid, Input, KV, Modal, Mono, Outcome, PageHeader, Select, Switch, Table, Tabs, Val, cx } from "@/components/ui";
import { displayTime } from "@/lib/connection-data";
import { UpstreamWriteConditions } from "@/components/upstream-write-conditions";
import { pricingActionAllowed, pricingControlText, pricingDefaultText, pricingOperationText, pricingReadText, pricingRuleText } from "@/lib/pricing-data";
import { usePricingData } from "@/lib/use-pricing-data";
import { useApp } from "@/lib/store";

const priceFields = ["input_price", "output_price", "cache_read_price"] as const;
type PriceValues = Record<typeof priceFields[number], string>;
type Draft = { key: string; kind: "fast" | "model" | "long"; fingerprint: string; ack: boolean; fast: string; longOn: boolean; index: number; modelIndex: number; prices: PriceValues };

export default function PricingPage() {
  const { displayTimezone } = useApp();
  const data = usePricingData();
  const [tabState, setTab] = useState({ owner: "", tab: "groups" });
  const [draft, setDraft] = useState<Draft | null>(null);
  const [confirmation, setConfirmation] = useState<{ key: string; kind: "retry" | "revert" | "review"; operation: PricingOperation } | null>(null);
  const tab = tabState.owner === data.owner ? tabState.tab : "groups";
  if (!data.allowed) return <UpstreamWriteConditions capability="pricing" status="需管理员。" />;
  const detail = data.detail?.snapshot, configuration = data.control?.configuration ?? detail?.configuration;
  const rules = configuration?.model_rules ?? [];
  const edit = draft?.key === data.detailKey ? draft : null;
  const action = confirmation?.key === data.key && data.operations?.some(item => item.id === confirmation.operation.id && item.revision === confirmation.operation.revision) ? confirmation : null;
  const selected = data.groups.find(group => group.id === data.groupID);
  const group = detail?.group;
  const problem = data.error ?? (data.targets && data.targets.result.last.read.state !== "available" ? pricingReadText(data.targets.result.last.read) : detail && detail.read.state !== "available" ? pricingReadText(detail.read) : null);
  const accounts = !detail ? "未读取" : detail.accounts_read.state !== "available" ? pricingReadText(detail.accounts_read) : data.detail?.accounts.map(account => account.name).join("、") || "暂无关联账号";
  const rows = configuration?.model_rules?.map((rule, index) => ({ key: String(index), model: rule.models.join("、"), input: rule.values.input_price, output: rule.values.output_price, cache: rule.values.cache_read_price,
    source: pricingRuleText(rule) })) ?? [];
  const empty = !detail ? data.loading ? "读取分组…" : problem ?? "选择分组。" : detail.read.state !== "available" ? pricingReadText(detail.read) : !configuration?.model_rules ? "计价缺项。" : "无模型覆盖。";
  const writeReason = pricingControlText(data.control);
  const writable = !!data.control?.can_apply && !!data.control.fingerprint && !data.busy;
  const editable = writable && edit?.fingerprint === data.control?.fingerprint;
  const valuesFor = (index: number): PriceValues => Object.fromEntries(priceFields.map(name => [name, rules[index]?.values[name] ?? ""])) as PriceValues;
  const open = (kind: Draft["kind"]) => {
    if (!configuration || data.busy || kind !== "model" && !writable) return;
    setDraft({ key: data.detailKey, kind, fingerprint: data.control?.fingerprint ?? "", ack: false, fast: rules.every(rule => rule.values.fast_multiplier === rules[0]?.values.fast_multiplier) ? rules[0]?.values.fast_multiplier ?? "" : "", longOn: configuration.long_context_pricing_enabled === true, index: 0, modelIndex: 0, prices: valuesFor(0) });
    data.selectDefault(kind === "model" ? rules[0]?.models[0] ?? "" : "");
  };
  const update = (patch: Partial<Draft>) => { if (edit && !data.busy) setDraft({ ...edit, ...patch }); };
  let changes: PricingChange[] = [];
  if (edit?.kind === "long" && edit.longOn !== configuration?.long_context_pricing_enabled) changes = [{ kind: "long_context", enabled: edit.longOn, value: null }];
  if (edit?.kind === "fast") changes = rules.flatMap((rule, index) => rule.values.fast_multiplier === edit.fast ? [] : [{ kind: "model_price" as const, rule_index: index, price_field: "fast_multiplier" as const, value: edit.fast }]);
  if (edit?.kind === "model") changes = priceFields.flatMap(name => {
    const value = edit.prices[name] === "" ? null : edit.prices[name];
    return value === (rules[edit.index]?.values[name] ?? null) ? [] : [{ kind: "model_price" as const, rule_index: edit.index, price_field: name, value }];
  });
  const validChanges = changes.length > 0 && changes.length <= 30 && changes.every(item => pricingChange.safeParse(item).success);
  const remoteReady = !!data.control && ["CONTROLLED_ENVIRONMENT", "OWNED_CHANGE_OPEN"].includes(data.control.reason);
  const boolean = (value: boolean | null | undefined) => value === null || value === undefined ? "未知" : value ? "开" : "关";

  return (
    <>
      <PageHeader
        title={uiCopy.pricing}
        helpTopic="connection"
      />
      <UpstreamWriteConditions capability="pricing" status={
        <span className="inline-flex items-center gap-2">
          <CapBadge c={data.control?.can_apply ? "available" : data.control ? "gated" : "unverified"} /> {data.notice || (data.control?.can_apply ? "可写入" : writeReason)}
        </span>
      } />
      <Tabs value={tab} onChange={tab => setTab({ owner: data.owner, tab })} tabs={[{ id: "groups", label: "目标分组" }, { id: "changes", label: "变更记录" }]} />
      {tab === "groups" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[280px_1fr]">
          <Card title={`${glossary.sub2api} 分组`} pad={false}>
            <ul className="divide-y divide-line">
              {data.groups.map(item => (
                <li key={item.id}>
                  <button onClick={() => data.select(item.id)} className={cx("w-full px-4 py-3 text-left", data.groupID === item.id ? "bg-brand-soft" : "hover:bg-surface-2")}>
                    <div className="font-mono text-[13px] font-medium">{item.name}</div>
                    <div className="mt-0.5 text-[12px] text-muted">{item.account_count === null ? "关联数量未知" : `${item.account_count} 个关联账号`} </div>
                  </button>
                </li>
              ))}
              {data.groups.length === 0 && <li><Empty title={data.loading ? "正在读取分组" : problem ? uiCopy.readFailed : "暂无可见分组"}>{problem ?? ""}</Empty></li>}
            </ul>
          </Card>
          <div className="space-y-4">
            <Card
              title={<Mono>{group?.name ?? selected?.name ?? "分组配置"}</Mono>}
              actions={<Button size="sm" icon={<RefreshCw size={12} />} disabled={data.loading} onClick={data.reload}>{data.loading ? uiCopy.loading : uiCopy.reload}</Button>}
            >
              <KV cols={3} items={[
                ["可见账号", accounts],
                ["影响调用者", <Val key="affected" v={null} reason="范围待核对" />],
                ["运行模式", detail?.run_mode ? <Badge key="r" tone={detail.run_mode === "standard" ? "ok" : "warn"}>{detail.run_mode}</Badge> : <Badge key="r" tone="unk">无法确认</Badge>],
                ["分组倍率", <Val key="r" v={configuration?.rate_multiplier} reason="未返回倍率" />],
              ]} />
              <Callout tone={problem ? "bad" : "warn"} className="mt-3">{problem ?? writeReason}</Callout>
            </Card>
            <Grid cols={2}>
              <Card title="FAST 计费规则" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable || rules.length === 0 || rules.length > 30} title={rules.length === 0 || rules.length > 30 ? "可改 1 至 30 条规则。" : writeReason} onClick={() => open("fast")}>调整</Button>}>
                <div className="text-[18px] font-semibold">强制 FAST：{boolean(configuration?.force_openai_fast)}；免费 FAST：{boolean(configuration?.free_openai_fast)}；模型 FAST 倍率：{rules.length ? rules.map((rule, index) => `规则 ${index + 1}：${rule.values.fast_multiplier ?? "未知"}`).join("、") : "未报告"}</div>
              </Card>
              <Card title="长上下文计费" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable} title={writeReason} onClick={() => open("long")}>调整</Button>}>
                <div className="text-[18px] font-semibold">{configuration?.long_context_pricing_enabled === null || configuration?.long_context_pricing_enabled === undefined ? <Val v={null} /> : configuration.long_context_pricing_enabled ? "开启" : "关闭"}</div>
              </Card>
            </Grid>
            <Card title="模型价格" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={data.busy || rules.length === 0} title={rules.length ? writeReason : "暂无规则。"} onClick={() => open("model")}>{writable ? "调整" : "查看"}</Button>} pad={false}>
              <Table rows={rows} rowKey={row => row.key} empty={empty} cols={[
                { key: "m", label: "模型", render: row => <Mono>{row.model}</Mono> },
                { key: "i", label: "输入", align: "right", render: row => <Val v={row.input} digits={3} /> },
                { key: "o", label: "输出", align: "right", render: row => <Val v={row.output} digits={3} /> },
                { key: "c", label: "缓存读", align: "right", render: row => <Val v={row.cache} digits={3} /> },
                { key: "s", label: "来源", render: row => <span className="text-[12px] text-muted">{row.source}</span> },
              ]} />
            </Card>
          </div>
        </div>
      )}
      {tab === "changes" && (
        <div className="space-y-4">
          {data.operations?.slice().reverse().map(operation => (
            <Card key={operation.id}
              title={<span className="flex items-center gap-2"><Mono>{operation.id}</Mono> {operation.changes.map(change => change.kind === "fast" ? "FAST 开关" : change.kind === "long_context" ? "长上下文" : `${change.price_field}（规则 ${change.rule_index + 1}）`).join("、")}</span>}
              desc={`${displayTime(operation.updated_at, displayTimezone, false)} · ${data.targets ? operation.source_id === data.targets.sourceID ? "当前来源" : "历史来源" : "保留来源"} ${operation.source_id} · 分组 ${operation.group_id} · 管理员 ${operation.actor_id}`}
              actions={<>
                {["applying", "reverting"].includes(operation.state) && <Button size="sm" icon={<RefreshCw size={12} />} disabled={data.busy} onClick={data.reload}>{uiCopy.reload}</Button>}
                {operation.state === "rejected" && <Button size="sm" icon={<RefreshCw size={12} />} disabled={data.busy || !remoteReady || !pricingActionAllowed("retry", operation, data.targets)} onClick={() => setConfirmation({ key: data.key, kind: "retry", operation })}>重试</Button>}
                {operation.state === "needs_review" && <Button size="sm" icon={<RefreshCw size={12} />} disabled={data.busy} onClick={() => setConfirmation({ key: data.key, kind: "review", operation })}>核对</Button>}
                <Button size="sm" icon={<RotateCcw size={12} />} disabled={data.busy || !remoteReady || !pricingActionAllowed("revert", operation, data.targets)} title="撤回变更" onClick={() => setConfirmation({ key: data.key, kind: "revert", operation })}>撤回</Button>
              </>} pad={false}>
              <Table rows={[operation]} rowKey={row => row.group_id} cols={[
                { key: "g", label: "目标分组", render: row => <Mono>{row.group_name}</Mono> },
                { key: "s", label: uiCopy.status, render: row => <Outcome k={({ applying: "pending", applied: "confirmed", reverting: "pending", reverted: "confirmed", rejected: "rejected", conflict: "conflict", needs_review: "unknown", reviewed: "unknown" })[row.state]} /> },
                { key: "n", label: "说明", render: row => <span className="text-muted">{pricingOperationText(row)}</span> },
              ]} />
            </Card>
          ))}
          <Callout tone="info">{data.historyError ?? (data.operations === null ? "读取变更记录…" : data.operations.length === 0 ? "暂无变更" : "")}</Callout>
        </div>
      )}
      <Modal open={!!edit} onClose={() => { if (!data.busy) setDraft(null); }}
        title={edit?.kind === "fast" ? "调整 FAST 计费规则" : edit?.kind === "long" ? "长上下文计费" : editable ? "调整模型价格" : "查看模型价格"}
        footer={<><Button disabled={data.busy} onClick={() => setDraft(null)}>取消</Button><Button variant="primary" disabled={data.busy || !editable || !edit?.ack || !validChanges} onClick={async () => { if (edit && await data.apply(changes, edit.fingerprint)) setDraft(null); }}>写入并读回</Button></>}>
        <div className="space-y-4">
          {edit?.kind === "fast" && <Field label="FAST 倍率" hint={`设置分组 ${rules.length} 条规则的 FAST 倍率。`} error={changes.length > 0 && !validChanges ? "填写非负小数。" : undefined}><Input type="number" step="any" min="0" value={edit.fast} disabled={data.busy} onChange={event => update({ fast: event.target.value })} /></Field>}
          {edit?.kind === "long" && <Switch checked={edit.longOn} onChange={longOn => update({ longOn })} disabled={data.busy} label="长上下文计费" />}
          {edit?.kind === "model" && <>
            <Field label="模型" hint={pricingDefaultText(data.defaultPrice, rules[edit.index]?.models[edit.modelIndex] ?? "", data.defaultError)}><Select value={`${edit.index}:${edit.modelIndex}`} disabled={data.busy} onChange={event => { const [index = 0, modelIndex = 0] = event.target.value.split(":").map(Number); update({ index, modelIndex, prices: valuesFor(index) }); data.selectDefault(rules[index]?.models[modelIndex] ?? ""); }} options={rules.flatMap((rule, index) => rule.models.map((model, modelIndex) => ({ value: `${index}:${modelIndex}`, label: `${index + 1} · ${model}（共享价格）` })))} /></Field>
            <Grid cols={3}>{priceFields.map(name => <Field key={name} label={{ input_price: "输入", output_price: "输出", cache_read_price: "缓存读" }[name]} hint="留空清除覆盖" error={edit.prices[name] !== "" && !pricingChange.safeParse({ kind: "model_price", rule_index: edit.index, price_field: name, value: edit.prices[name] }).success ? "精度不符。" : undefined}><Input type="number" step="any" min="0" value={edit.prices[name]} disabled={data.busy || !editable} onChange={event => update({ prices: { ...edit.prices, [name]: event.target.value } })} /></Field>)}</Grid>
          </>}
          <Callout tone="warn">将影响 <Mono>{group?.name ?? selected?.name}</Mono> 整个分组（含外部）。</Callout>
          <Checkbox checked={edit?.ack ?? false} onChange={ack => update({ ack })} disabled={data.busy} label="确认分组影响" />
        </div>
      </Modal>
      <ConfirmModal open={!!action} onClose={() => setConfirmation(null)} disabled={data.busy}
        onConfirm={async () => action ? data.act(action.kind, action.operation) : false}
        title={`${action?.kind === "retry" ? "重试" : action?.kind === "review" ? "核对" : "撤回"} ${action?.operation.id ?? ""}`}
        confirmLabel={action?.kind === "retry" ? "重试" : action?.kind === "review" ? "结束本地跟踪" : "撤回"}>
        <p>{action?.kind === "review" ? `我已核对操作（${action.operation.source_id}，分组 ${action.operation.group_name}），确认结束跟踪。` : action?.kind === "retry" ? "重试影响整个分组。" : `将 ${action?.operation.group_name ?? "该分组"} 恢复计价，影响整个分组。`}</p>
        <p className="text-muted">有外部变更则停止。</p>
      </ConfirmModal>
    </>
  );
}
