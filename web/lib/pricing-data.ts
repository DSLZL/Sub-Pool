import type { createPricingClient } from "@sub-pool/contracts/client";
import type { PricingGroup, PricingGroups, PricingControl, PricingOperation, PricingDefault } from "@sub-pool/contracts/pricing";
import { checkText } from "./connection-data";

type Client = ReturnType<typeof createPricingClient>;
export class PricingReadError extends Error {}
export function pricingReadText(check: PricingGroups["read"]) {
  if (check.reason === "GROUP_NOT_VERIFIED") return "尚未验证目标分组，未读取关联账号。";
  return checkText({ ...check, id: "pricing.read", reason: check.reason });
}

export async function readPricingDefault(read: () => ReturnType<Client["default"]>, sourceID: string, revision: string, model: string) {
  const result = await read();
  if (result.source_id !== sourceID || result.revision !== revision || result.model !== model) throw new PricingReadError("默认价格来源、版本或模型已变化，请重新读取。");
  return result;
}

export function pricingDefaultText(value: PricingDefault | null, model: string, error: string | null) {
  if (error) return error;
  if (!value || value.model !== model) return "正在查询所选模型的原生默认价格；不会用于填充或覆盖输入。";
  if (value.read.state !== "available") return pricingReadText(value.read);
  if (!value.found || !value.values) return "原生默认目录没有返回该模型价格；保持未知，不补 0。";
  const reasoning = value.native_details?.reasoning_effort_multipliers;
  return `原生默认目录（USD/token，非分组最终价）：${Object.entries(value.values).map(([name, amount]) => `${name}=${amount ?? (value.unreported.includes(name as typeof value.unreported[number]) ? "未返回" : "未设置")}`).join("；")}；推理倍率 ${reasoning?.replaceAll("\n", " ") ?? "未完整读取"}。未重建历史计价。`;
}

export function pricingRuleText(rule: NonNullable<NonNullable<PricingGroup["configuration"]>["model_rules"]>[number]) {
  const scalar = Object.entries(rule.values).map(([name, value]) => `${name}=${value ?? (rule.unreported.includes(name as typeof rule.unreported[number]) ? "未返回" : "未设置")}`);
  const details = Object.entries(rule.native_details ?? {}).map(([name, value]) => `${name}=${value.replaceAll("\n", " ")}`);
  return `原生分组配置（${rule.billing_mode || "计费方式未设置"}）；${[...scalar, ...details].join("； ")}${rule.unprojected.length ? `；尚未完整读取：${rule.unprojected.join("、")}` : ""}；配置数值并非最终扣费。`;
}

export async function readPricingGroups(read: (page: number) => ReturnType<Client["groups"]>, sourceID: string, revision: string) {
  const groups: PricingGroups["groups"] = [], ids = new Set<string>();
  let total: string | null = null;
  // shortcut: The supplied group selector has no paging control; fail above 2,000 visible groups until it does.
  for (let number = 1; number <= 20; number++) {
    const page = await read(number);
    if (page.source_id !== sourceID || page.revision !== revision || page.page !== number) throw new PricingReadError("分组来源或配置版本已变化，请重新读取。");
    if (page.read.state !== "available") return { groups: [], last: page };
    if (page.total === null || total !== null && total !== page.total) throw new PricingReadError("可见分组范围已变化，请重新读取。");
    total = page.total;
    for (const group of page.groups) {
      if (ids.has(group.id)) throw new PricingReadError("分组分页存在重复对象，请重新读取。");
      ids.add(group.id); groups.push(group);
    }
    if (page.next_page === null) {
      if (BigInt(groups.length) !== BigInt(total)) throw new PricingReadError("分组列表未完整读取，请重新读取。");
      return { groups, last: page };
    }
    if (page.next_page !== number + 1) throw new PricingReadError("分组分页无法确认，请重新读取。");
  }
  throw new PricingReadError("可见分组超过当前界面读取范围，未展示部分列表。");
}

export async function readPricingGroup(read: (page: number) => ReturnType<Client["group"]>, sourceID: string, revision: string, groupID: string) {
  const accounts: PricingGroup["accounts"] = [], ids = new Set<string>();
  let total: string | null = null, original: PricingGroup | null = null;
  // shortcut: No account paging control exists here; reject more than 2,000 visible associations.
  for (let number = 1; number <= 20; number++) {
    const page = await read(number);
    if (page.source_id !== sourceID || page.revision !== revision || page.group_id !== groupID || page.page !== number) throw new PricingReadError("所选分组或来源已变化，请重新读取。");
    if (page.read.state !== "available" || page.accounts_read.state !== "available") return { snapshot: page, accounts: [] };
    if (page.total === null || total !== null && total !== page.total || original !== null && (JSON.stringify(original.configuration) !== JSON.stringify(page.configuration) || JSON.stringify(original.group) !== JSON.stringify(page.group) || original.run_mode !== page.run_mode)) throw new PricingReadError("分组配置或关联范围在读取期间变化，请重新读取。");
    total = page.total; original ??= page;
    for (const account of page.accounts) {
      if (ids.has(account.upstream_id)) throw new PricingReadError("关联账号分页存在重复对象，请重新读取。");
      ids.add(account.upstream_id); accounts.push(account);
    }
    if (page.next_page === null) {
      if (BigInt(accounts.length) !== BigInt(total)) throw new PricingReadError("关联账号未完整读取，请重新读取。");
      return { snapshot: page, accounts };
    }
    if (page.next_page !== number + 1) throw new PricingReadError("关联账号分页无法确认，请重新读取。");
  }
  throw new PricingReadError("关联账号超过当前界面读取范围，未展示部分结果。");
}

export async function readPricingHistory(read: (after: string) => ReturnType<Client["history"]>, sourceID: string) {
  const operations: PricingOperation[] = [];
  let after = "0";
  // shortcut: the supplied history has no paging control; reject beyond 2,000 operations until it does.
  for (let number = 0; number < 20; number++) {
    const page = await read(after);
    for (const item of page.operations) {
      if (sourceID !== "" && item.source_id !== sourceID || BigInt(item.id) <= BigInt(operations.at(-1)?.id ?? after)) throw new PricingReadError("计价历史来源或分页顺序无法确认，请重新读取。");
      operations.push(item);
    }
    if (page.next_after === null) return operations;
    if (!page.operations.length || page.next_after !== page.operations.at(-1)?.id || BigInt(page.next_after) <= BigInt(after)) throw new PricingReadError("计价历史分页无法确认，请重新读取。");
    after = page.next_after;
  }
  throw new PricingReadError("计价历史超过当前界面读取范围，未展示部分记录。");
}

export function pricingControlText(control: PricingControl | null) {
  if (!control) return "写入条件尚未读取，保持关闭。";
  return {
    CONTROLLED_ENVIRONMENT: "当前来源与版本已绑定受控写入；分组可能影响本系统外调用者，仍需逐次确认。原生接口没有 CAS，读后再写存在竞态。",
    CONTROL_BINDING_REQUIRED: "当前来源及版本尚未绑定受控写入，保持关闭。",
    STANDARD_ADMIN_REQUIRED: "尚未确认当前原生管理员及 standard 模式，保持关闭。",
    TRUSTED_GROUP_STATE_REQUIRED: "未取得完整、可信且带版本的分组前状态，保持关闭。",
    OWNED_CHANGE_OPEN: "此分组已有未收口的托管变更，请先在变更记录中撤回或人工核对。",
  }[control.reason];
}

export function pricingOperationText(operation: PricingOperation) {
  return {
    APPLY_PENDING: "已保存意图，结果尚未确认；请重新读取记录，勿重复提交。",
    APPLIED: "单次写入后已独立读回确认；未改写历史费用。",
    REVERT_PENDING: "已保存撤回意图，原生结果尚未确认。",
    REVERTED: "已恢复本次托管修改前的值，并独立读回确认。",
    UPSTREAM_REJECTED: "原生接口明确拒绝；仅当前来源、版本和原前状态仍一致时可手动重试。",
    REVERT_REJECTED: "原生接口明确拒绝撤回；保留原托管记录。",
    PRECONDITION_CHANGED: "前状态、目标身份或受控条件已变化，停止写入；不会覆盖观察到的外部修改。",
    WRITE_RESULT_UNKNOWN: "写入结果未知，可能已生效；不能自动或直接重试。",
    PROCESS_INTERRUPTED_RESULT_UNKNOWN: "进程中断后保留为结果未知，没有重放原生写入。",
    MANUALLY_REVIEWED_RESULT_UNKNOWN: "已由管理员结束本地托管；原生结果仍未知，未声明成功或撤回。",
  }[operation.reason];
}

export function checkPricingReceipt(receipt: PricingOperation, expected: { source_id: string; connection_revision: string; group_id: string; request_id: string }) {
  if (receipt.source_id !== expected.source_id || receipt.connection_revision !== expected.connection_revision || receipt.group_id !== expected.group_id || receipt.request_id !== expected.request_id) throw new PricingReadError("计价回执与本次请求不符，请读取变更记录；不要重复写入。");
}

export function pricingRequestID() {
  return btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16)))).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

export function pricingActionAllowed(kind: "retry" | "revert" | "review", operation: PricingOperation, current: { sourceID: string; revision: string } | null) {
  if (kind === "review") return operation.state === "needs_review";
  return current !== null && operation.source_id === current.sourceID && operation.connection_revision === current.revision
    && operation.state === (kind === "retry" ? "rejected" : "applied");
}
