import { glossary, uiCopy } from "@/lib/glossary";
import { ORPCError } from "@orpc/client";
import { requestFailureText } from "./request-id";
import { authErrorData } from "@sub-pool/contracts/auth";
import type { ManagedAccount, ManagedAccountPage } from "@sub-pool/contracts/accounts";
import type { CapabilityCheck, ConnectionConfiguration, ConnectionImpact, ConnectionWriteCapability, ConnectionWriteGrants, DiscoveredAccount, Discovery } from "@sub-pool/contracts/connection";
import { monitoringConfig, type MonitoringConfig } from "@sub-pool/contracts/monitoring";

export function monitoringDraft(policy: MonitoringConfig) {
  return { enabled: policy.enabled, interval: String(policy.interval_seconds / 60), boost: policy.near_reset_enabled,
    stale: String(policy.stale_after_seconds / 60), refreshOnChange: policy.refresh_on_change,
    nearLead: String(policy.near_reset_lead_seconds / 60), nearInterval: String(policy.near_reset_interval_seconds / 60),
    staleWarnings: policy.stale_warnings_enabled, usageEnabled: policy.usage_enabled ?? false,
    usageInterval: String((policy.usage_interval_seconds ?? policy.interval_seconds) / 60) };
}

export function monitoringSave(policy: MonitoringConfig, draft: ReturnType<typeof monitoringDraft>) {
  // Preserve an untouched fractional-minute rendering as its exact saved seconds.
  const seconds = (value: string, saved: number) => /^\d+(?:\.\d+)?$/.test(value)
    ? value === String(saved / 60) ? saved : Number(value) * 60 : NaN;
  const { saved_at: _savedAt, ...saved } = policy;
  const input = { ...saved, enabled: draft.enabled, interval_seconds: seconds(draft.interval, policy.interval_seconds),
    stale_after_seconds: seconds(draft.stale, policy.stale_after_seconds), refresh_on_change: draft.refreshOnChange,
    near_reset_enabled: draft.boost, near_reset_lead_seconds: seconds(draft.nearLead, policy.near_reset_lead_seconds),
    near_reset_interval_seconds: seconds(draft.nearInterval, policy.near_reset_interval_seconds), stale_warnings_enabled: draft.staleWarnings };
  const originalUsageInterval = policy.usage_interval_seconds ?? policy.interval_seconds;
  if (policy.usage_enabled !== undefined || draft.usageEnabled || draft.usageInterval !== String(originalUsageInterval / 60)) {
    input.usage_enabled = draft.usageEnabled;
    input.usage_interval_seconds = seconds(draft.usageInterval, originalUsageInterval);
  }
  if (!monitoringConfig.safeParse({ ...input, saved_at: policy.saved_at }).success) throw new Error("Invalid monitoring preferences");
  return input;
}

export async function readConnectionImpact(read: () => Promise<ConnectionImpact>, config: ConnectionConfiguration) {
  const impact = await read();
  if (impact.source_id !== config.source_id || impact.revision !== config.revision) throw new Error("Connection impact binding changed");
  return impact;
}

export async function readManagedPages(read: (after: string) => Promise<ManagedAccountPage>, revision: string) {
  const accounts: ManagedAccount[] = [], ids = new Set<string>(), cursors = new Set<string>();
  let after = "";
  do {
    const page = await read(after);
    if (page.connection_revision !== revision) throw new Error("Connection changed during account read");
    for (const account of page.accounts) {
      if (ids.has(account.id)) throw new Error("Repeated managed account");
      ids.add(account.id); accounts.push(account);
    }
    if (page.next_after && cursors.has(page.next_after)) throw new Error("Repeated account cursor");
    if (page.next_after) cursors.add(page.next_after);
    after = page.next_after ?? "";
  } while (after);
  return accounts;
}

export async function readDiscoveredPages(read: (page: number) => Promise<Discovery>, sourceID: string, revision: string) {
  const accounts: DiscoveredAccount[] = [], ids = new Set<string>();
  let pageNumber = 1, total: string | null = null;
  for (;;) {
    const page = await read(pageNumber);
    if (page.source_id !== sourceID || page.revision !== revision || page.page !== pageNumber) throw new Error("Discovery binding changed");
    if (page.read.state !== "available") return { accounts: [], read: page.read };
    if (page.total === null || total !== null && total !== page.total) throw new Error("Discovery coverage changed");
    total = page.total;
    for (const account of page.accounts) {
      if (ids.has(account.upstream_id)) throw new Error("Repeated discovered account");
      ids.add(account.upstream_id); accounts.push(account);
    }
    if (page.next_page === null) {
      if (BigInt(accounts.length) !== BigInt(total)) throw new Error("Incomplete discovery coverage");
      return { accounts, read: page.read };
    }
    if (page.next_page !== pageNumber + 1) throw new Error("Invalid discovery continuation");
    pageNumber = page.next_page;
  }
}

export function connectionFailure(error: unknown) {
  const messages = {
    BAD_REQUEST: [400, "请核对连接。"],
    UNAUTHORIZED: [401, uiCopy.signIn],
    FORBIDDEN: [403, "需要管理员。"],
    CURRENT_PASSWORD_INVALID: [403, "密码错误。"],
    SOURCE_BLOCKED: [403, "访问被阻断。"],
    NOT_FOUND: [404, "对象变化，请重读。"],
    CONFLICT: [409, "数据变化，请重读。"],
    OBSERVATION_RANGE_UNVERIFIED: [409, "周期不符。"],
    PARTICIPANT_EXIT_BLOCKED: [409, "资金未结，无法退出。"],
    PARTICIPANT_EXIT_LIMIT: [409, "合同超限。"],
    POOL_CHANGE_BLOCKED: [409, "资金未结，未保存。"],
    POOL_MEMBER_LIMIT: [409, "合同最多 10,000 人。"],
    CONNECTION_NOT_CONFIGURED: [409, "先保存连接。"],
    CONNECTION_REVERIFICATION_REQUIRED: [409, "先测试连接。"],
    SECRET_STORAGE_UNAVAILABLE: [503, "凭据不可用。"],
    TOO_MANY_REQUESTS: [429, "请稍后重试。"],
  } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, text: requestFailureText(error, text) };
  }
  return { status: 0, text: requestFailureText(error, "核对原操作。") };
}

export function sourceWriteAvailability(
  configuration: ConnectionConfiguration | null,
  grants: ConnectionWriteGrants | null,
  capability: ConnectionWriteCapability,
): "authorized" | "closed" | "unknown" {
  if (!configuration || !grants || configuration.source_id !== grants.source_id || configuration.revision !== grants.connection_revision) return "unknown";
  if (!configuration.source_id || !configuration.credential_set || !configuration.credential_kind || !configuration.secret_storage_available || grants.upstream_writes_disabled) return "closed";
  const grant = grants.grants.find(entry => entry.capability === capability)?.grant;
  return grant?.revoked_at === null ? "authorized" : "closed";
}

export const capabilityNames: Record<CapabilityCheck["id"], string> = {
  health: "服务可达", "version.read": "部署版本", "accounts.read": "账号读取", "usage.read": "用量读取",
  "wallet.read": "钱包读取", "pricing.read": "计价读取", run_mode: "运行模式", management_auth: "管理认证",
  "quota.read": "额度查询", "wallet.write": "余额写入", "scheduling.write": "调度写入", "pricing.write": "计价写入",
};
const reasons: Record<CapabilityCheck["reason"], string> = {
  NOT_CONFIGURED: "未配置", VERIFIED_READ: "读取已验证", INVALID_RESPONSE: "格式错误",
  NETWORK_ERROR: "网络失败", TIMEOUT: "读取超时", REDIRECT_REFUSED: "拒绝重定向",
  UPSTREAM_ERROR: `${glossary.sub2api} 读取失败`, UNAUTHORIZED: "认证失败", FORBIDDEN: "读取被拒绝",
  COMPLIANCE_REQUIRED: `${glossary.sub2api}待管理员确认`, ROUTE_UNAVAILABLE: "接口不可用", RATE_LIMITED: `${glossary.sub2api}限流，请稍候`,
  MODE_REQUIRES_JWT: `${glossary.sub2api} 需管理员 JWT`, VERSION_UNVERIFIED: `${glossary.sub2api} 版本待确认`,
  NO_VISIBLE_RESOURCE: "无可见对象",
  NO_VERIFIED_ADMIN_READ: "管理权限待验", ACCOUNT_CHECK_REQUIRED: "逐账号核对", WRITE_NOT_PROBED: "写入未验证",
  PRICING_CHECK_REQUIRED: "计价待核对",
  PRICING_FIELDS_UNREPORTED: `${glossary.sub2api}缺计价字段`,
  VERIFIED_PASSIVE_QUOTA: "已确认额度",
  QUOTA_IDENTITY_UNVERIFIED: `当前账号的 ${glossary.sub2api}身份待核对`,
  QUOTA_WINDOW_UNVERIFIED: `${glossary.sub2api}缺有效窗口`,
  QUOTA_SNAPSHOT_STALE: "额度已过期",
  ATOMIC_BALANCE_REQUIRED: `尚无原生${glossary.balanceAdjustment}读回记录`,
  STANDARD_MODE_REQUIRED: `写入前核对${glossary.sub2api} 运行在 standard 模式`,
  CONTROL_BINDING_REQUIRED: "待设置写入",
  WRITE_EVIDENCE_REQUIRED: "缺读回记录",
  VERIFIED_WRITE: "写入已验证",
};
export function checkSummary(check: CapabilityCheck, version?: string | null) {
  return check.reason === "VERSION_UNVERIFIED" && version
    ? `当前 ${glossary.sub2api} 版本 ${version} 未经验证`
    : reasons[check.reason];
}

export function checkDetails(check: CapabilityCheck) {
  const target = check.id === "wallet.write" ? "用户" : check.id === "pricing.read" || check.id === "pricing.write" ? "分组" : uiCopy.account;
  return [
    check.http_status === null ? null : `HTTP ${check.http_status}`,
    check.latency_ms === null ? null : `${check.latency_ms} ms`,
    check.evidence ? `${target} #${check.evidence.upstream_id} · ${displayTime(check.evidence.verified_at, "UTC")}` : null,
  ].filter(Boolean).join(" · ");
}

export function checkText(check: CapabilityCheck, version?: string | null) {
  return [checkSummary(check, version), checkDetails(check)].filter(Boolean).join(" · ");
}

const timeFormats = new Map<string, Intl.DateTimeFormat>();

export function displayTime(value: string | null | undefined, timezone: string | null | undefined, mode: boolean | "short" = true) {
  if (!value) return uiCopy.noRecords;
  if (timezone === undefined) return "读取时区…";
  const zone = timezone ?? "UTC";
  try {
    let format = timeFormats.get(zone);
    if (!format) {
      format = new Intl.DateTimeFormat("zh-CN", { timeZone: zone, dateStyle: "short", timeStyle: "medium", hourCycle: "h23" });
      timeFormats.set(zone, format);
    }
    const instant = new Date(value);
    if (mode === "short") {
      const { month, day, hour, minute } = Object.fromEntries(format.formatToParts(instant).map(part => [part.type, part.value]));
      return `${month.padStart(2, "0")}-${day.padStart(2, "0")} ${hour}:${minute}`;
    }
    const result = format.format(instant);
    return mode ? result + " · " + zone : result;
  }
  catch { return mode === "short" ? "时间不可用" : value + " · UTC（时区不可用）"; }
}
