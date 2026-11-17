"use client";

import { useState } from "react";
import { RefreshCw, Pencil, RotateCcw } from "lucide-react";
import { pricingChange, type PricingChange, type PricingOperation } from "@sub-pool/contracts/pricing";
import { Badge, Button, Callout, CapBadge, Card, Checkbox, ConfirmModal, Empty, Field, Grid, Input, KV, Modal, Mono, Outcome, PageHeader, Select, Switch, Table, Tabs, Val, cx } from "@/components/ui";
import { displayTime } from "@/lib/connection-data";
import { pricingActionAllowed, pricingControlText, pricingDefaultText, pricingOperationText, pricingReadText, pricingRuleText } from "@/lib/pricing-data";
import { usePricingData } from "@/lib/use-pricing-data";
import { useApp } from "@/lib/store";

const priceFields = ["input_price", "output_price", "cache_read_price"] as const;
type PriceValues = Record<typeof priceFields[number], string>;
type Draft = { key: string; kind: "fast" | "model" | "long"; fingerprint: string; ack: boolean; fast: string; longOn: boolean; index: number; modelIndex: number; prices: PriceValues };

export default function PricingPage() {
  const { timezone } = useApp();
  const data = usePricingData();
  const [tabState, setTab] = useState({ owner: "", tab: "groups" });
  const [draft, setDraft] = useState<Draft | null>(null);
  const [confirmation, setConfirmation] = useState<{ key: string; kind: "retry" | "revert" | "review"; operation: PricingOperation } | null>(null);
  const tab = tabState.owner === data.owner ? tabState.tab : "groups";
  if (!data.allowed) return <Callout tone="info">上游分组计价及关联关系仅向当前管理员提供；普通功能授权不包含全局配置读取。</Callout>;
  const detail = data.detail?.snapshot, configuration = data.control?.configuration ?? detail?.configuration;
  const rules = configuration?.model_rules ?? [];
  const edit = draft?.key === data.detailKey ? draft : null;
  const action = confirmation?.key === data.key && data.operations?.some(item => item.id === confirmation.operation.id && item.revision === confirmation.operation.revision) ? confirmation : null;
  const selected = data.groups.find(group => group.id === data.groupID);
  const group = detail?.group;
  const problem = data.error ?? (data.targets && data.targets.result.last.read.state !== "available" ? pricingReadText(data.targets.result.last.read) : detail && detail.read.state !== "available" ? pricingReadText(detail.read) : null);
  const accounts = !detail ? "尚未读取" : detail.accounts_read.state !== "available" ? pricingReadText(detail.accounts_read) : data.detail?.accounts.map(account => account.name).join("、") || "本次可见范围无关联账号";
  const rows = configuration?.model_rules?.map((rule, index) => ({ key: String(index), model: rule.models.join("、"), input: rule.values.input_price, output: rule.values.output_price, cache: rule.values.cache_read_price,
    source: pricingRuleText(rule) })) ?? [];
  const empty = !detail ? data.loading ? "正在读取分组配置。" : problem ?? "尚未选择可读取的分组。" : detail.read.state !== "available" ? pricingReadText(detail.read) : !configuration?.model_rules ? "当前凭据未返回模型计价字段，不能判断是否配置覆盖。" : "原生分组未报告模型覆盖；继承后的实际价格尚未核验。";
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
        title="分组计价"
        tag="S2A-PRICE-001 ~ 007"
        desc="通过 Sub2API 原生分组能力调整计价。修改可能影响本系统外调用者，必须单独读回确认；当前改价不会重写历史费用。"
      />
      <Callout tone="warn" className="mb-4" title="计价写入能力：受控条件逐次核对">
        <span className="inline-flex items-center gap-2">
          <CapBadge c={data.control?.can_apply ? "available" : data.control ? "gated" : "unverified"} /> {data.notice || "standard 模式不代表写入授权；仅当前受控来源与版本可执行。simple 模式或不完整前状态保持关闭，2xx 不代表改价生效。"}
        </span>
      </Callout>
      <Tabs value={tab} onChange={tab => setTab({ owner: data.owner, tab })} tabs={[{ id: "groups", label: "目标分组" }, { id: "changes", label: "变更记录" }]} />
      {tab === "groups" && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[280px_1fr]">
          <Card title="上游分组" pad={false}>
            <ul className="divide-y divide-line">
              {data.groups.map(item => (
                <li key={item.id}>
                  <button onClick={() => data.select(item.id)} className={cx("w-full px-4 py-3 text-left", data.groupID === item.id ? "bg-brand-soft" : "hover:bg-surface-2")}>
                    <div className="font-mono text-[13px] font-medium">{item.name}</div>
                    <div className="mt-0.5 text-[12px] text-muted">{item.account_count === null ? "关联数量未知" : `${item.account_count} 个原生关联账号`} · 全局调用者未核验</div>
                  </button>
                </li>
              ))}
              {data.groups.length === 0 && <li><Empty title={data.loading ? "正在读取分组" : problem ? "分组读取未完成" : "当前可见范围无分组"}>{problem ?? "只显示当前凭据返回的 OpenAI 原生分组；可见列表不代表全局影响范围。"}</Empty></li>}
            </ul>
          </Card>
          <div className="space-y-4">
            <Card
              title={<Mono>{group?.name ?? selected?.name ?? "分组配置"}</Mono>}
              desc="原生分组不是本地额度池"
              actions={<Button size="sm" icon={<RefreshCw size={12} />} disabled={data.loading} onClick={data.reload}>{data.loading ? "读取中…" : "重新读取"}</Button>}
            >
              <KV cols={3} items={[
                ["本次可见关联账号", accounts],
                ["影响调用者", "尚未核验（可能包含本系统外调用者）"],
                ["运行模式", detail?.run_mode ? <Badge key="r" tone={detail.run_mode === "standard" ? "ok" : "warn"}>{detail.run_mode}</Badge> : <Badge key="r" tone="unk">无法确认</Badge>],
                ["配置来源", configuration ? `分组原生读取${configuration.unreported.length ? "（部分计价字段未返回）" : ""} · 分组倍率 ${configuration.rate_multiplier ?? "未知"} · ${displayTime(data.control?.configuration ? data.control.checked_at : detail?.checked_at, timezone)}` : "尚未读取可确认的原生配置"],
                ["关联可见性", <Badge key="v" tone="warn">当前凭据可见范围，全局完整性未核验</Badge>],
              ]} />
              <Callout tone={problem ? "bad" : "warn"} className="mt-3">{problem ?? writeReason}</Callout>
            </Card>
            <Grid cols={2}>
              <Card title="FAST 计费规则" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable || rules.length === 0 || rules.length > 30} title={rules.length === 0 || rules.length > 30 ? "现有倍率输入仅适用于 1 至 30 条已报告模型规则；不会创建规则或猜测默认倍率。" : writeReason} onClick={() => open("fast")}>调整</Button>}>
                <div className="text-[18px] font-semibold">强制 FAST：{boolean(configuration?.force_openai_fast)}；免费 FAST：{boolean(configuration?.free_openai_fast)}；模型 FAST 倍率：{rules.length ? rules.map((rule, index) => `规则 ${index + 1}：${rule.values.fast_multiplier ?? "未知"}`).join("、") : "尚未报告"}</div>
              </Card>
              <Card title="长上下文计费" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={!writable} title={writeReason} onClick={() => open("long")}>调整</Button>}>
                <div className="text-[18px] font-semibold">{configuration?.long_context_pricing_enabled === null || configuration?.long_context_pricing_enabled === undefined ? <Val v={null} /> : configuration.long_context_pricing_enabled ? "原生设置：开启" : "原生设置：关闭"}</div>
              </Card>
            </Grid>
            <Card title="模型价格" desc="原生配置字段及规则 · 配置数值不是最终扣费 · 空值及缺价不补 0" actions={<Button size="sm" icon={<Pencil size={12} />} disabled={data.busy || rules.length === 0} title={rules.length ? writeReason : "当前没有可读取的原生模型规则。"} onClick={() => open("model")}>{writable ? "调整" : "查看"}</Button>} pad={false}>
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
              desc={`${displayTime(operation.updated_at, timezone)} · ${data.targets ? operation.source_id === data.targets.sourceID ? "当前来源" : "历史来源" : "保留来源"} ${operation.source_id} · 分组 ${operation.group_id} · 管理员 ${operation.actor_id}`}
              actions={<>
                {["applying", "reverting"].includes(operation.state) && <Button size="sm" icon={<RefreshCw size={12} />} disabled={data.busy} onClick={data.reload}>重新读取</Button>}
                {operation.state === "rejected" && <Button size="sm" icon={<RefreshCw size={12} />} disabled={data.busy || !remoteReady || !pricingActionAllowed("retry", operation, data.targets)} onClick={() => setConfirmation({ key: data.key, kind: "retry", operation })}>重试</Button>}
                {operation.state === "needs_review" && <Button size="sm" icon={<RefreshCw size={12} />} disabled={data.busy} onClick={() => setConfirmation({ key: data.key, kind: "review", operation })}>人工核对</Button>}
                <Button size="sm" icon={<RotateCcw size={12} />} disabled={data.busy || !remoteReady || !pricingActionAllowed("revert", operation, data.targets)} title="仅当前来源与版本、本系统已读回确认且前状态仍一致的修改可撤回" onClick={() => setConfirmation({ key: data.key, kind: "revert", operation })}>撤回</Button>
              </>} pad={false}>
              <Table rows={[operation]} rowKey={row => row.group_id} cols={[
                { key: "g", label: "目标分组", render: row => <Mono>{row.group_name}</Mono> },
                { key: "s", label: "状态", render: row => <Outcome k={({ applying: "pending", applied: "confirmed", reverting: "pending", reverted: "confirmed", rejected: "rejected", conflict: "conflict", needs_review: "unknown", reviewed: "unknown" })[row.state]} /> },
                { key: "n", label: "说明", render: row => <span className="text-muted">{pricingOperationText(row)}</span> },
              ]} />
            </Card>
          ))}
          <Callout tone="info">{data.historyError ?? (data.operations === null ? "全部来源的本地变更记录尚未完整读取。" : data.operations.length === 0 ? "全部来源的本地历史没有本系统变更记录。" : "保留全部来源的本地记录；历史来源仅可人工结束未知结果的本地托管。重试或撤回仅限当前来源与版本。多组结果不保证原子性，原生接口没有 CAS。")}</Callout>
        </div>
      )}
      <Modal open={!!edit} onClose={() => { if (!data.busy) setDraft(null); }}
        title={edit?.kind === "fast" ? "调整 FAST 计费规则" : edit?.kind === "long" ? "调整长上下文计费" : editable ? "调整模型价格" : "查看模型价格"}
        footer={<><Button disabled={data.busy} onClick={() => setDraft(null)}>取消</Button><Button variant="primary" disabled={data.busy || !editable || !edit?.ack || !validChanges} onClick={async () => { if (edit && await data.apply(changes, edit.fingerprint)) setDraft(null); }}>写入并读回</Button></>}>
        <div className="space-y-4">
          {edit?.kind === "fast" && <Field label="FAST 倍率" hint={`将设置当前分组 ${rules.length} 条已报告模型规则的原生 FAST 倍率；不改变强制/免费 FAST 开关，不猜测继承值。`} error={changes.length > 0 && !validChanges ? "请输入原生可精确往返的非负小数，不使用指数或多余末尾零。" : undefined}><Input type="number" step="any" min="0" value={edit.fast} disabled={data.busy} onChange={event => update({ fast: event.target.value })} /></Field>}
          {edit?.kind === "long" && <Switch checked={edit.longOn} onChange={longOn => update({ longOn })} disabled={data.busy} label="启用长上下文计费" desc="该开关不改变真实订阅额度。" />}
          {edit?.kind === "model" && <>
            <Field label="模型" hint={pricingDefaultText(data.defaultPrice, rules[edit.index]?.models[edit.modelIndex] ?? "", data.defaultError)}><Select value={`${edit.index}:${edit.modelIndex}`} disabled={data.busy} onChange={event => { const [index = 0, modelIndex = 0] = event.target.value.split(":").map(Number); update({ index, modelIndex, prices: valuesFor(index) }); data.selectDefault(rules[index]?.models[modelIndex] ?? ""); }} options={rules.flatMap((rule, index) => rule.models.map((model, modelIndex) => ({ value: `${index}:${modelIndex}`, label: `${index + 1} · ${model}（同规则模型共享价格）` })))} /></Field>
            <Grid cols={3}>{priceFields.map(name => <Field key={name} label={{ input_price: "输入", output_price: "输出", cache_read_price: "缓存读" }[name]} hint="原生配置数值；空值清除此覆盖。" error={edit.prices[name] !== "" && !pricingChange.safeParse({ kind: "model_price", rule_index: edit.index, price_field: name, value: edit.prices[name] }).success ? "数值不能精确提交。" : undefined}><Input type="number" step="any" min="0" value={edit.prices[name]} disabled={data.busy || !editable} onChange={event => update({ prices: { ...edit.prices, [name]: event.target.value } })} /></Field>)}</Grid>
          </>}
          <Callout tone="warn">将影响 <Mono>{group?.name ?? selected?.name}</Mono> 的整个原生分组，可能包含本系统外调用者；全局调用者数量未核验。写入前保存前状态，复杂模型规则保持原值，不重算历史费用。</Callout>
          <Checkbox checked={edit?.ack ?? false} onChange={ack => update({ ack })} disabled={data.busy} label="我了解此修改影响整个分组，且不会重写历史费用。" />
        </div>
      </Modal>
      <ConfirmModal open={!!action} onClose={() => setConfirmation(null)} disabled={data.busy}
        onConfirm={async () => action ? data.act(action.kind, action.operation) : false}
        title={`${action?.kind === "retry" ? "重试" : action?.kind === "review" ? "人工核对" : "撤回"} ${action?.operation.id ?? ""}`}
        confirmLabel={action?.kind === "retry" ? "重试" : action?.kind === "review" ? "结束本地托管，保留未知结果" : "撤回"}>
        <p>{action?.kind === "review" ? `我已独立核对此项操作（来源 ${action.operation.source_id}，分组 ${action.operation.group_name}），确认结束本地托管。原生结果仍未知，不会把它改为成功或已撤回。` : action?.kind === "retry" ? "仅重试这条明确被原生接口拒绝的修改；会重新核对原来源、版本与前状态。此操作影响整个分组及本系统外调用者。" : `将 ${action?.operation.group_name ?? "该分组"} 的本次托管修改恢复为保存的前状态；影响整个分组及本系统外调用者。`}</p>
        <p className="text-muted">若读取时发现外部修改则停止；未知结果不重放。不会修改历史费用。</p>
      </ConfirmModal>
    </>
  );
}
