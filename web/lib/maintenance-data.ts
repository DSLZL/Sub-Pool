import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { createAccountsClient, createMaintenanceClient } from "@sub-pool/contracts/client";
import { maintenanceHistoryRulesVersion, maintenanceInput, maintenancePlan, maintenanceRulesVersion, type MaintenanceInput, type MaintenancePlan } from "@sub-pool/contracts/maintenance";
import { usageNanos } from "@sub-pool/contracts/usage";
import { connectionFailure, readManagedPages } from "./connection-data";
import { requestFailureText } from "./request-id";

type Client = ReturnType<typeof createMaintenanceClient>;
export class MaintenanceReadError extends Error {}

export function maintenanceRange(account_id: string, kind: MaintenanceInput["kind"], from: string, until: string, now = Date.now()): MaintenanceInput {
  const input = maintenanceInput.safeParse({ account_id, kind, from, until, rules_version: kind === "cycles" ? maintenanceRulesVersion : maintenanceHistoryRulesVersion });
  if (!input.success) throw new MaintenanceReadError("选账号、UTC 区间。");
  const end = usageNanos(input.data.until);
  if (end === null || end > BigInt(now) * 1000000n) throw new MaintenanceReadError("区间含未来时间。");
  return input.data;
}

export async function readMaintenanceAccounts(client: Pick<ReturnType<typeof createAccountsClient>, "list">, signal?: AbortSignal): Promise<ManagedAccount[]> {
  let count = 0;
  const read = async (after: string) => {
    const page = await client.list({ after, search: "", limit: 100 }, { signal }); count += page.accounts.length;
    if (count > 2000) throw new MaintenanceReadError("账号超限。");
    return page;
  };
  const first = await read("");
  return readManagedPages(after => after === "" ? Promise.resolve(first) : read(after), first.connection_revision);
}

export async function readMaintenancePlans(client: Pick<Client, "plans">, accounts: Pick<ManagedAccount, "id">[], signal?: AbortSignal) {
  const plans: MaintenancePlan[] = [], ids = new Set<string>(); let bytes = 0;
  for (const account of accounts) {
    let after = "0";
    for (;;) {
      // shortcut: full projections fit this bounded view; use a summary contract when retained history exceeds its memory limit.
      const page = await client.plans({ account_id: account.id, after, limit: 1 }, { signal });
      if (page.account_id !== account.id || page.plans.length > 1 || !page.plans.length && (after !== "0" || page.next_after !== null)) throw new MaintenanceReadError("分页不全。");
      let previous = BigInt(after);
      for (const plan of page.plans) {
        if (plan.scope.account_id !== account.id || BigInt(plan.id) <= previous || ids.has(plan.id)) throw new MaintenanceReadError("查询范围不符。");
        previous = BigInt(plan.id); ids.add(plan.id);
        bytes += new TextEncoder().encode(JSON.stringify(plan)).length;
        if (plans.length === 2000 || bytes > 32 * 1024 * 1024) throw new MaintenanceReadError("维护超限。");
        plans.push(plan);
      }
      if (page.next_after === null) break;
      if (page.next_after !== page.plans.at(-1)?.id || BigInt(page.next_after) <= BigInt(after)) throw new MaintenanceReadError("续页失败。");
      after = page.next_after;
    }
  }
  return plans.sort((a, b) => BigInt(a.id) > BigInt(b.id) ? -1 : 1);
}

export function checkMaintenancePreflight(input: MaintenanceInput, value: MaintenancePlan, actor: string) {
  if (value.scope.account_id !== input.account_id || usageNanos(value.scope.from) !== usageNanos(input.from) || usageNanos(value.scope.until) !== usageNanos(input.until)
    || value.kind !== input.kind || value.rules_version !== input.rules_version || value.created_by !== actor || !["ready", "blocked"].includes(value.state)) {
    throw new MaintenanceReadError("预检不符。");
  }
}

export function checkMaintenanceReceipt(expected: MaintenancePlan, value: MaintenancePlan) {
  const before = maintenancePlan.parse(expected), after = maintenancePlan.parse(value);
  for (const key of ["id", "scope", "kind", "rules_version", "basis", "preview", "difference", "issues", "limitations", "created_by", "created_at", "expires_at"] as const) {
    if (JSON.stringify(before[key]) !== JSON.stringify(after[key])) throw new MaintenanceReadError("计划不符。");
  }
}

export function maintenanceCanApply(plan: MaintenancePlan, now = Date.now()) {
  return plan.state === "ready" && plan.validity === "valid" && Date.parse(plan.expires_at) > now;
}

export function maintenanceStatus(plan: MaintenancePlan): { label: string; tone: "ok" | "info" | "muted" | "bad" | "warn" } {
  if (plan.state === "ready" && plan.validity !== "valid") return { label: "已失效", tone: "muted" };
  const states = {
    ready: { label: "可应用", tone: "info" }, blocked: { label: "预检阻塞", tone: "warn" }, queued: { label: "已排队", tone: "warn" },
    running: { label: "执行中", tone: "warn" }, applied: { label: "已应用", tone: "ok" }, expired: { label: "已失效", tone: "muted" }, failed: { label: "执行失败", tone: "bad" },
  } as const;
  return states[plan.state];
}

export function maintenanceResultText(plan: MaintenancePlan) {
  if (plan.state !== "applied") return "进度已更新。";
  return plan.preview.financial ? `已保存复算 #${plan.result_id}`
    : "索引版本" + plan.result_id + "。沿用历史金额。";
}

export const maintenanceKinds = { rebuild: "重建索引", cycles: "纠正周期", upgrade: "复算估算" };
export function maintenanceIssue(issue: MaintenancePlan["issues"][number]) {
  const messages = {
    OBSERVATION_LIMIT: "观测超限",
    NO_LOCAL_OBSERVATIONS: "暂无观测",
    SOURCE_PROVENANCE_INVALID: "来源待核对",
    RECEIPT_MISMATCH: "时间不符",
    PERIOD_EVIDENCE_MISSING: "周期缺项",
    SOURCE_TIME_CONFLICT: "同刻冲突",
    NO_NATIVE_PERIODS: "无可用周期",
    RULE_UPGRADE_UNAVAILABLE: "旧规则停用",
    RULES_UNVERIFIED: "规则未核实",
    NO_PRIOR_PROJECTION: "无历史索引",
    PERIOD_INDEX_DIFFERENCE: "索引不同",
    REMOTE_OPERATION_UNRESOLVED: "操作待核对",
    ESTIMATE_HISTORY_LIMIT: "记录超限",
    NO_ESTIMATE_HISTORY: "暂无估算",
    ESTIMATE_EVIDENCE_MISSING: "缺观测或费用",
    ESTIMATE_EVIDENCE_INVALID: "历史不符",
  };
  const titles = {
    OBSERVATION_LIMIT: "观测超限", NO_LOCAL_OBSERVATIONS: "无本地观测", SOURCE_PROVENANCE_INVALID: "缺来源数据",
    RECEIPT_MISMATCH: "时间不符", PERIOD_EVIDENCE_MISSING: "缺周期依据", SOURCE_TIME_CONFLICT: "同刻冲突",
    NO_NATIVE_PERIODS: "无可用周期", RULE_UPGRADE_UNAVAILABLE: "规则不可升级", RULES_UNVERIFIED: "规则未核准",
    NO_PRIOR_PROJECTION: "暂无索引", PERIOD_INDEX_DIFFERENCE: "索引不同",
    REMOTE_OPERATION_UNRESOLVED: "操作待核对",
    ESTIMATE_HISTORY_LIMIT: "估算超限", NO_ESTIMATE_HISTORY: "暂无估算",
    ESTIMATE_EVIDENCE_MISSING: "缺估算依据", ESTIMATE_EVIDENCE_INVALID: "资料不符",
  };
  return { title: titles[issue.code], description: messages[issue.code] + (issue.observation_id === null ? "" : " · 观测 " + issue.observation_id)
    + (issue.window_kind === null ? "" : " · " + (issue.window_kind === "short" ? "短窗" : "长窗")) + (issue.estimate_id === undefined ? "" : " · 估算 " + issue.estimate_id) };
}

export function maintenanceFailure(error: unknown) {
  const result = connectionFailure(error);
  return { ...result, text: error instanceof MaintenanceReadError ? error.message : result.status === 400 ? requestFailureText(error, "查账号和时间。") : result.text };
}
