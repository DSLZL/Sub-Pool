"use client";

import { useState } from "react";
import { RefreshCw, Pencil } from "lucide-react";
import { Badge, Button, Callout, CapBadge, Card, Empty, Grid, KV, Mono, PageHeader, Table, Tabs, Val, cx } from "@/components/ui";
import { displayTime } from "@/lib/connection-data";
import { pricingReadText } from "@/lib/pricing-data";
import { usePricingData } from "@/lib/use-pricing-data";
import { useApp } from "@/lib/store";

export default function PricingPage() {
  const { timezone } = useApp();
  const data = usePricingData();
  const [tabState, setTab] = useState({ owner: "", tab: "groups" });
  const tab = tabState.owner === data.owner ? tabState.tab : "groups";
  if (!data.allowed) return <Callout tone="info">上游分组计价及关联关系仅向当前管理员提供；普通功能授权不包含全局配置读取。</Callout>;
  const detail = data.detail?.snapshot, configuration = detail?.configuration;
  const selected = data.groups.find(group => group.id === data.groupID);
  const group = detail?.group;
  const problem = data.error ?? (data.targets && data.targets.result.last.read.state !== "available" ? pricingReadText(data.targets.result.last.read) : detail && detail.read.state !== "available" ? pricingReadText(detail.read) : null);
  const accounts = !detail ? "尚未读取" : detail.accounts_read.state !== "available" ? pricingReadText(detail.accounts_read) : data.detail?.accounts.map(account => account.name).join("、") || "本次可见范围无关联账号";
  const rows = configuration?.model_rules?.map((rule, index) => ({ key: String(index), model: rule.models.join("、"), input: rule.values.input_price, output: rule.values.output_price, cache: rule.values.cache_read_price,
    source: `原生分组配置${rule.unreported.length ? "；部分字段未返回" : ""}${rule.unprojected.length ? "；复杂规则尚未核验" : "；完整生效价未核验"}` })) ?? [];
  const empty = !detail ? data.loading ? "正在读取分组配置。" : problem ?? "尚未选择可读取的分组。" : detail.read.state !== "available" ? pricingReadText(detail.read) : !configuration?.model_rules ? "当前凭据未返回模型计价字段，不能判断是否配置覆盖。" : "原生分组未报告模型覆盖；继承后的实际价格尚未核验。";
  const writeReason = "原生计价写入权限、受控冲突及写后核验尚未具备，保持关闭。";
  const boolean = (value: boolean | null | undefined) => value === null || value === undefined ? "未知" : value ? "开" : "关";

  return (
    <>
      <PageHeader
        title="分组计价"
        tag="S2A-PRICE-001 ~ 007"
        desc="读取 Sub2API 原生分组当前配置。分组变更可能影响本系统外调用者；写入尚未开放，当前配置不用于重算历史费用。"
      />
      <Callout tone="warn" className="mb-4" title="计价写入能力：未验证">
        <span className="inline-flex items-center gap-2">
          <CapBadge c="unverified" /> 当前仅提供读取；standard 模式不代表写入授权，simple 模式可能不返回计价字段，2xx 不代表改价生效。
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
                ["配置来源", configuration ? `分组原生读取${configuration.unreported.length ? "（部分计价字段未返回）" : ""} · ${displayTime(detail?.checked_at, timezone)}` : "尚未读取可确认的原生配置"],
                ["关联可见性", <Badge key="v" tone="warn">当前凭据可见范围，全局完整性未核验</Badge>],
              ]} />
              <Callout tone={problem ? "bad" : "warn"} className="mt-3">{problem ?? writeReason}</Callout>
            </Card>
            <Grid cols={2}>
              <Card title="FAST 计费规则" actions={<Button size="sm" icon={<Pencil size={12} />} disabled title={writeReason}>调整</Button>}>
                <div className="text-[18px] font-semibold">强制 FAST：{boolean(configuration?.force_openai_fast)}；免费 FAST：{boolean(configuration?.free_openai_fast)}</div>
              </Card>
              <Card title="长上下文计费" actions={<Button size="sm" icon={<Pencil size={12} />} disabled title={writeReason}>调整</Button>}>
                <div className="text-[18px] font-semibold">{configuration?.long_context_pricing_enabled === null || configuration?.long_context_pricing_enabled === undefined ? <Val v={null} /> : configuration.long_context_pricing_enabled ? "原生设置：开启" : "原生设置：关闭"}</div>
              </Card>
            </Grid>
            <Card title="模型价格" desc="原生配置字段 · 单位与完整生效价尚未核验 · 空值及缺价不补 0" actions={<Button size="sm" icon={<Pencil size={12} />} disabled title={writeReason}>调整</Button>} pad={false}>
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
          <Callout tone="info">本系统尚未提供计价变更历史、重试及托管撤回；此处没有读取历史记录。多组变更不保证原子性，原生接口没有已核验的无竞态覆盖保证。</Callout>
        </div>
      )}
    </>
  );
}
