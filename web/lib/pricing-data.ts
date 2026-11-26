import { uiCopy } from "@/lib/glossary";
import type { createPricingClient } from "@sub-pool/contracts/client";
import type { PricingGroup, PricingGroups, PricingControl, PricingOperation, PricingDefault } from "@sub-pool/contracts/pricing";
import { checkText } from "./connection-data";

type Client = ReturnType<typeof createPricingClient>;
export class PricingReadError extends Error {}
export function pricingReadText(check: PricingGroups["read"]) {
  if (check.reason === "GROUP_NOT_VERIFIED") return "分组待核对。";
  return checkText({ ...check, id: "pricing.read", reason: check.reason });
}

export async function readPricingDefault(read: () => ReturnType<Client["default"]>, sourceID: string, revision: string, model: string) {
  const result = await read();
  if (result.source_id !== sourceID || result.revision !== revision || result.model !== model) throw new PricingReadError("价格已变。");
  return result;
}

export function pricingDefaultText(value: PricingDefault | null, model: string, error: string | null) {
  if (error) return error;
  if (!value || value.model !== model) return "查询默认价…";
  if (value.read.state !== "available") return pricingReadText(value.read);
  if (!value.found || !value.values) return "默认价未知。";
  const { input_price, output_price, cache_read_price } = value.values;
  return `默认价 USD/token · 输入 ${input_price ?? "—"} · 输出 ${output_price ?? "—"} · 缓存读 ${cache_read_price ?? "—"}`;
}

export function pricingRuleText(rule: NonNullable<NonNullable<PricingGroup["configuration"]>["model_rules"]>[number]) {
  const scalar = Object.entries(rule.values).map(([name, value]) => `${name}=${value ?? (rule.unreported.includes(name as typeof rule.unreported[number]) ? "未返回" : "未设置")}`);
  const details = Object.entries(rule.native_details ?? {}).map(([name, value]) => `${name}=${value.replaceAll("\n", " ")}`);
  return `分组配置（${rule.billing_mode || "未设计费方式"}）；${[...scalar, ...details].join("； ")}${rule.unprojected.length ? `；未完整读取：${rule.unprojected.join("、")}` : ""}`;
}

export async function readPricingGroups(read: (page: number) => ReturnType<Client["groups"]>, sourceID: string, revision: string) {
  const groups: PricingGroups["groups"] = [], ids = new Set<string>();
  let total: string | null = null;
  // shortcut: The supplied group selector has no paging control; fail above 2,000 visible groups until it does.
  for (let number = 1; number <= 20; number++) {
    const page = await read(number);
    if (page.source_id !== sourceID || page.revision !== revision || page.page !== number) throw new PricingReadError("分组已变，请重读。");
    if (page.read.state !== "available") return { groups: [], last: page };
    if (page.total === null || total !== null && total !== page.total) throw new PricingReadError("分组范围已变。");
    total = page.total;
    for (const group of page.groups) {
      if (ids.has(group.id)) throw new PricingReadError("分组重复。");
      ids.add(group.id); groups.push(group);
    }
    if (page.next_page === null) {
      if (BigInt(groups.length) !== BigInt(total)) throw new PricingReadError("分组读取不齐。");
      return { groups, last: page };
    }
    if (page.next_page !== number + 1) throw new PricingReadError("分页待核对。");
  }
  throw new PricingReadError("分组超限。");
}

export async function readPricingGroup(read: (page: number) => ReturnType<Client["group"]>, sourceID: string, revision: string, groupID: string) {
  const accounts: PricingGroup["accounts"] = [], ids = new Set<string>();
  let total: string | null = null, original: PricingGroup | null = null;
  // shortcut: No account paging control exists here; reject more than 2,000 visible associations.
  for (let number = 1; number <= 20; number++) {
    const page = await read(number);
    if (page.source_id !== sourceID || page.revision !== revision || page.group_id !== groupID || page.page !== number) throw new PricingReadError("所选分组已变。");
    if (page.read.state !== "available" || page.accounts_read.state !== "available") return { snapshot: page, accounts: [] };
    if (page.total === null || total !== null && total !== page.total || original !== null && (JSON.stringify(original.configuration) !== JSON.stringify(page.configuration) || JSON.stringify(original.group) !== JSON.stringify(page.group) || original.run_mode !== page.run_mode)) throw new PricingReadError("配置已变。");
    total = page.total; original ??= page;
    for (const account of page.accounts) {
      if (ids.has(account.upstream_id)) throw new PricingReadError("账号重复。");
      ids.add(account.upstream_id); accounts.push(account);
    }
    if (page.next_page === null) {
      if (BigInt(accounts.length) !== BigInt(total)) throw new PricingReadError("账号读取不齐。");
      return { snapshot: page, accounts };
    }
    if (page.next_page !== number + 1) throw new PricingReadError("账号分页异常。");
  }
  throw new PricingReadError("账号超限。");
}

export async function readPricingHistory(read: (after: string) => ReturnType<Client["history"]>, sourceID: string) {
  const operations: PricingOperation[] = [];
  let after = "0";
  // shortcut: the supplied history has no paging control; reject beyond 2,000 operations until it does.
  for (let number = 0; number < 20; number++) {
    const page = await read(after);
    for (const item of page.operations) {
      if (sourceID !== "" && item.source_id !== sourceID || BigInt(item.id) <= BigInt(operations.at(-1)?.id ?? after)) throw new PricingReadError("历史顺序异常。");
      operations.push(item);
    }
    if (page.next_after === null) return operations;
    if (!page.operations.length || page.next_after !== page.operations.at(-1)?.id || BigInt(page.next_after) <= BigInt(after)) throw new PricingReadError("无法续页。");
    after = page.next_after;
  }
  throw new PricingReadError("历史超限。");
}

export function pricingControlText(control: PricingControl | null) {
  if (!control) return "写入条件未知。";
  return {
    CONTROLLED_ENVIRONMENT: "写入已授权。",
    CONTROL_BINDING_REQUIRED: "写入控制未开。",
    SOURCE_WRITE_GRANT_REQUIRED: "计价未授权。",
    UPSTREAM_WRITES_DISABLED: "已启用禁写。",
    STANDARD_ADMIN_REQUIRED: "核对管理员。",
    TRUSTED_GROUP_STATE_REQUIRED: "分组待核对。",
    OWNED_CHANGE_OPEN: "已有未决变更。",
  }[control.reason];
}

export function pricingOperationText(operation: PricingOperation) {
  return {
    APPLY_PENDING: uiCopy.pendingResult,
    APPLIED: "已确认变更。",
    REVERT_PENDING: "撤回待核对。",
    REVERTED: "已确认撤回。",
    UPSTREAM_REJECTED: "变更被拒绝。",
    REVERT_REJECTED: "撤回被拒绝。",
    PRECONDITION_CHANGED: "条件变更，未写入。",
    SOURCE_WRITE_GRANT_REQUIRED: "未授权，未写入。",
    UPSTREAM_WRITES_DISABLED: "禁写，未派发。",
    WRITE_RESULT_UNKNOWN: "结果未知，请勿重试。",
    PROCESS_INTERRUPTED_RESULT_UNKNOWN: "中断，结果未知。",
    MANUALLY_REVIEWED_RESULT_UNKNOWN: "已结束，结果未知。",
  }[operation.reason];
}

export function checkPricingReceipt(receipt: PricingOperation, expected: { source_id: string; connection_revision: string; group_id: string; request_id: string }) {
  if (receipt.source_id !== expected.source_id || receipt.connection_revision !== expected.connection_revision || receipt.group_id !== expected.group_id || receipt.request_id !== expected.request_id) throw new PricingReadError("查变更记录。");
}

export function pricingRequestID() {
  return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16)))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

export function pricingActionAllowed(kind: "retry" | "revert" | "review", operation: PricingOperation, current: { sourceID: string; revision: string } | null) {
  if (kind === "review") return operation.state === "needs_review";
  return current !== null && operation.source_id === current.sourceID && operation.connection_revision === current.revision
    && operation.state === (kind === "retry" ? "rejected" : "applied");
}
