import { ORPCError } from "@orpc/client";
import { requestFailureText } from "./request-id";
import { authErrorData } from "@sub-pool/contracts/auth";
import type { ManagedAccount, ManagedAccountPage } from "@sub-pool/contracts/accounts";
import type { CapabilityCheck, ConnectionConfiguration, ConnectionImpact, DiscoveredAccount, Discovery } from "@sub-pool/contracts/connection";
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
    BAD_REQUEST: [400, "请核对地址、凭据、名称或设置内容。"],
    UNAUTHORIZED: [401, "会话已失效，请重新登录。"],
    FORBIDDEN: [403, "当前身份没有管理权限。"],
    CURRENT_PASSWORD_INVALID: [403, "当前管理员密码不正确。"],
    SOURCE_BLOCKED: [403, "当前来源已被阻断，请联系管理员核对。"],
    NOT_FOUND: [404, "对象已变化，请重新读取后核对。"],
    CONFLICT: [409, "配置或对象已变化，请重新读取后再操作。"],
    OBSERVATION_RANGE_UNVERIFIED: [409, "所选观测尚未证明处于同一原生周期，或身份、新鲜度条件已变化；未保存起点，请重新核对。"],
    PARTICIPANT_EXIT_BLOCKED: [409, "该参与者仍有未结或未确认的资金操作；请先核对处理，未执行退出。"],
    PARTICIPANT_EXIT_LIMIT: [409, "该参与者关联的当前合同超过 10,000 份，无法在同一次操作中完整退出。"],
    POOL_CHANGE_BLOCKED: [409, "池内参与者仍有未结或未确认的资金操作；请先核对处理，未保存池变更。"],
    POOL_MEMBER_LIMIT: [409, "单份额度池合同最多包含 10,000 名成员。"],
    CONNECTION_NOT_CONFIGURED: [409, "请先保存连接地址和管理凭据。"],
    CONNECTION_REVERIFICATION_REQUIRED: [409, "恢复后需要重新验证当前连接；请先执行连接测试。"],
    SECRET_STORAGE_UNAVAILABLE: [503, "服务暂时无法安全保存或读取凭据，请联系管理员。"],
    TOO_MANY_REQUESTS: [429, "操作暂时受限，请稍后重新核对。"],
  } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, text: requestFailureText(error, text) };
  }
  return { status: 0, text: requestFailureText(error, "操作结果尚未确认。请重新读取当前状态核对；勿重复提交刚才的操作。") };
}

export const capabilityNames: Record<CapabilityCheck["id"], string> = {
  health: "服务可达", "version.read": "部署版本", "accounts.read": "账号读取", "usage.read": "用量读取",
  "wallet.read": "钱包读取", "pricing.read": "计价读取", run_mode: "运行模式", management_auth: "管理凭据鉴权",
  "quota.read": "额度查询", "wallet.write": "余额写入", "scheduling.write": "调度写入", "pricing.write": "计价写入",
};
const reasons: Record<CapabilityCheck["reason"], string> = {
  NOT_CONFIGURED: "尚未配置", VERIFIED_READ: "已验证此项读取", INVALID_RESPONSE: "返回格式不符合已确认契约",
  NETWORK_ERROR: "网络连接失败", TIMEOUT: "读取超时", REDIRECT_REFUSED: "目标返回重定向，未跟随",
  UPSTREAM_ERROR: "上游读取失败", UNAUTHORIZED: "管理凭据未通过认证", FORBIDDEN: "当前凭据无此读取权限",
  COMPLIANCE_REQUIRED: "上游要求先完成合规确认", ROUTE_UNAVAILABLE: "此部署未提供已确认的读取路径", RATE_LIMITED: "上游限流，请等待后核对",
  MODE_REQUIRES_JWT: "上游运行模式需要管理员 JWT", VERSION_UNVERIFIED: "当前 Sub2API 版本未经验证",
  NO_VISIBLE_RESOURCE: "没有可见对象，无法验证此项能力",
  NO_VERIFIED_ADMIN_READ: "尚无已验证的管理读取", ACCOUNT_CHECK_REQUIRED: "需逐账号独立核对", WRITE_NOT_PROBED: "写入未验证，保持关闭",
  PRICING_CHECK_REQUIRED: "计价读取仍需独立核对",
  PRICING_FIELDS_UNREPORTED: "上游未返回完整计价字段",
  VERIFIED_PASSIVE_QUOTA: "已核对被动额度快照",
  QUOTA_IDENTITY_UNVERIFIED: "当前账号的上游资源身份待核对",
  QUOTA_WINDOW_UNVERIFIED: "上游未返回带有效采样时间的额度窗口",
  QUOTA_SNAPSHOT_STALE: "被动额度快照已过期",
  ATOMIC_BALANCE_REQUIRED: "尚无原生调账读回记录",
  STANDARD_MODE_REQUIRED: "此项写入需要确认上游运行在 standard 模式",
  CONTROL_BINDING_REQUIRED: "当前连接尚未配置受控写入绑定",
  WRITE_EVIDENCE_REQUIRED: "当前部署版本尚无写入和独立读回的记录",
  VERIFIED_WRITE: "已有写入和独立读回记录",
};
export function checkText(check: CapabilityCheck, version?: string | null) {
  const target = check.id === "wallet.write" ? "用户" : check.id === "pricing.read" || check.id === "pricing.write" ? "分组" : "账号";
  const description = check.reason === "VERSION_UNVERIFIED" && version
    ? `当前 Sub2API 版本 ${version} 未经验证`
    : reasons[check.reason];
  return description + (check.http_status === null ? "" : ` · HTTP ${check.http_status}`)
    + (check.latency_ms === null ? "" : ` · ${check.latency_ms} ms`)
    + (check.evidence ? ` · ${target} #${check.evidence.upstream_id} · ${displayTime(check.evidence.verified_at, "UTC")}` : "");
}

const timeFormats = new Map<string, Intl.DateTimeFormat>();

export function displayTime(value: string | null | undefined, timezone: string | null | undefined, mode: boolean | "short" = true) {
  if (!value) return "暂无记录";
  if (timezone === undefined) return "等待读取时区…";
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
  catch { return mode === "short" ? "时间无法显示" : value + " · UTC（显示时区不可用）"; }
}
