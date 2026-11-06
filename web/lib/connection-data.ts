import { ORPCError } from "@orpc/client";
import { authErrorData } from "@sub-pool/contracts/auth";
import type { ManagedAccount, ManagedAccountPage } from "@sub-pool/contracts/accounts";
import type { CapabilityCheck, DiscoveredAccount, Discovery } from "@sub-pool/contracts/connection";

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
    SOURCE_BLOCKED: [403, "当前来源已被阻断，请联系管理员核对。"],
    NOT_FOUND: [404, "对象已变化，请重新读取后核对。"],
    CONFLICT: [409, "配置或对象已变化，请重新读取后再操作。"],
    CONNECTION_NOT_CONFIGURED: [409, "请先保存连接地址和管理凭据。"],
    SECRET_STORAGE_UNAVAILABLE: [503, "服务暂时无法安全保存或读取凭据，请联系管理员。"],
    TOO_MANY_REQUESTS: [429, "操作暂时受限，请稍后重新核对。"],
  } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, text };
  }
  return { status: 0, text: "操作结果尚未确认。请重新读取当前状态核对；勿重复提交刚才的操作。" };
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
  MODE_REQUIRES_JWT: "当前凭据不能通过已确认路径识别运行模式", NO_VISIBLE_RESOURCE: "没有可见对象，无法验证此项能力",
  NO_VERIFIED_ADMIN_READ: "尚无已验证的管理读取", ACCOUNT_CHECK_REQUIRED: "需逐账号独立核对", WRITE_NOT_PROBED: "写入未验证，保持关闭",
  PRICING_CHECK_REQUIRED: "计价读取仍需独立核对",
};
export function checkText(check: CapabilityCheck) {
  return reasons[check.reason] + (check.http_status === null ? "" : ` · HTTP ${check.http_status}`)
    + (check.latency_ms === null ? "" : ` · ${check.latency_ms} ms`);
}

export function displayTime(value: string | null | undefined, timezone: string | null) {
  if (!value) return "暂无记录";
  const zone = timezone ?? "UTC";
  try { return new Intl.DateTimeFormat("zh-CN", { timeZone: zone, dateStyle: "short", timeStyle: "medium", hour12: false }).format(new Date(value)) + " · " + zone; }
  catch { return value + " · UTC（显示时区不可用）"; }
}
