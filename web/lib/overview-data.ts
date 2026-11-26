import { glossary, uiCopy } from "@/lib/glossary";
import type { PricingOperation } from "@sub-pool/contracts/pricing";
import type { DispatchEvent } from "@sub-pool/contracts/dispatch";
import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { ConnectionConfiguration } from "@sub-pool/contracts/connection";
import type { MonitoringConfig } from "@sub-pool/contracts/monitoring";

export function overviewReview(isAdmin: boolean, pricing: readonly PricingOperation[] | null, dispatch: readonly DispatchEvent[] | null) {
  const scope = isAdmin ? "本地操作" : "可见账号";
  if (dispatch === null || isAdmin && pricing === null) return { count: null, text: `${scope} 读取中。` };
  const pricingCount = isAdmin && pricing ? pricing.filter(item => item.state === "needs_review").length : 0;
  const dispatchCount = [...new Map(dispatch.map(event => [event.pause.id, event.pause])).values()].filter(pause => pause.state === "needs_review").length;
  return { count: pricingCount + dispatchCount, text: `${scope}：${isAdmin ? `计价 ${pricingCount} · ` : ""}调度 ${dispatchCount}` };
}

type FirstUseEvidence = {
  timezoneSaved: boolean | null;
  connection: ConnectionConfiguration | null;
  catalog: {
    revision: string | null;
    accounts: readonly Pick<ManagedAccount, "source_id" | "source_current" | "identity_state" | "collection_state" | "observed_connection_revision">[];
  } | null;
  policy: Pick<MonitoringConfig, "enabled" | "usage_enabled"> | null;
  participantReady: boolean | null;
  poolReady: boolean | null;
};

export function firstUseSteps({ timezoneSaved, connection, catalog, policy, participantReady, poolReady }: FirstUseEvidence) {
  const source = connection?.credential_set && connection.base_url ? connection.source_id : null;
  const configured = connection ? source !== null : null;
  const diagnostic = connection?.diagnostic;
  const tested = connection ? !!(source && diagnostic?.saved && diagnostic.source_id === source && diagnostic.revision === connection.revision
    && diagnostic.checks.some(check => check.id === "accounts.read" && check.state === "available")) : null;
  const currentAccounts = source && catalog && catalog.revision === connection?.revision
    ? catalog.accounts.filter(account => account.source_current && account.source_id === source && account.identity_state === "verified")
    : null;
  const adopted = configured === false ? false : currentAccounts && currentAccounts.length > 0;
  const scheduled = configured === false || policy?.enabled === false || policy?.usage_enabled === false ? false
    : policy?.usage_enabled === true ? currentAccounts?.some(account => account.collection_state === "awaiting_collector"
      && account.observed_connection_revision === connection?.revision) ?? null : null;
  const resources = configured === null ? null : configured && participantReady && poolReady;
  const connectionPath = "/console/connection";
  return [
    { label: "设管理员密码", done: true },
    { label: "保存时区", done: timezoneSaved, href: connectionPath, entry: "连接设置" },
    { label: `配置 ${glossary.sub2api} 连接`, done: configured, href: connectionPath, entry: `连接与采集 · ${glossary.sub2api} 连接` },
    { label: "测试连接", done: tested, href: connectionPath, entry: "能力检查" },
    { label: "纳管账号", done: adopted, href: connectionPath, entry: "纳管" },
    { label: "定时采集", done: scheduled, href: connectionPath, entry: "采集设置" },
    { label: "创建额度池", done: resources, href: "/console/pools", entry: uiCopy.pools },
  ];
}
