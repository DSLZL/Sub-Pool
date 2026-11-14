import type { ManagedAccount } from "@sub-pool/contracts/accounts";
import type { createAccountsClient, createMaintenanceClient } from "@sub-pool/contracts/client";
import { maintenanceInput, maintenancePlan, maintenanceRulesVersion, type MaintenanceInput, type MaintenancePlan } from "@sub-pool/contracts/maintenance";
import { usageNanos } from "@sub-pool/contracts/usage";
import { connectionFailure, readManagedPages } from "./connection-data";

type Client = ReturnType<typeof createMaintenanceClient>;
export class MaintenanceReadError extends Error {}

export function maintenanceRange(account_id: string, kind: MaintenanceInput["kind"], from: string, until: string, now = Date.now()): MaintenanceInput {
  const input = maintenanceInput.safeParse({ account_id, kind, from, until, rules_version: maintenanceRulesVersion });
  if (!input.success) throw new MaintenanceReadError("请选择账号，并输入有效且开始早于结束的 UTC 时间范围（以 Z 结尾）。");
  const end = usageNanos(input.data.until);
  if (end === null || end > BigInt(now) * 1000000n) throw new MaintenanceReadError("维护时间范围不可在未来。");
  return input.data;
}

export async function readMaintenanceAccounts(client: Pick<ReturnType<typeof createAccountsClient>, "list">, signal?: AbortSignal): Promise<ManagedAccount[]> {
  let count = 0;
  const read = async (after: string) => {
    const page = await client.list({ after, search: "", limit: 100 }, { signal }); count += page.accounts.length;
    if (count > 2000) throw new MaintenanceReadError("账号超过此页 2000 条读取上限，尚未取得完整维护范围。");
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
      if (page.account_id !== account.id || page.plans.length > 1 || !page.plans.length && (after !== "0" || page.next_after !== null)) throw new MaintenanceReadError("维护计划续页范围不完整，请重新读取。");
      let previous = BigInt(after);
      for (const plan of page.plans) {
        if (plan.scope.account_id !== account.id || BigInt(plan.id) <= previous || ids.has(plan.id)) throw new MaintenanceReadError("维护计划范围或游标不一致，请重新读取。");
        previous = BigInt(plan.id); ids.add(plan.id);
        bytes += new TextEncoder().encode(JSON.stringify(plan)).length;
        if (plans.length === 2000 || bytes > 32 * 1024 * 1024) throw new MaintenanceReadError("维护历史超过此页 2000 条或 32 MiB 上限，无法完整呈现。");
        plans.push(plan);
      }
      if (page.next_after === null) break;
      if (page.next_after !== page.plans.at(-1)?.id || BigInt(page.next_after) <= BigInt(after)) throw new MaintenanceReadError("维护计划游标不可继续，请重新读取。");
      after = page.next_after;
    }
  }
  return plans.sort((a, b) => BigInt(a.id) > BigInt(b.id) ? -1 : 1);
}

export function checkMaintenancePreflight(input: MaintenanceInput, value: MaintenancePlan, actor: string) {
  if (value.scope.account_id !== input.account_id || usageNanos(value.scope.from) !== usageNanos(input.from) || usageNanos(value.scope.until) !== usageNanos(input.until)
    || value.kind !== input.kind || value.rules_version !== input.rules_version || value.created_by !== actor || !["ready", "blocked"].includes(value.state)) {
    throw new MaintenanceReadError("预检回执未匹配本次账号、范围与规则，请只读核对已保存计划。");
  }
}

export function checkMaintenanceReceipt(expected: MaintenancePlan, value: MaintenancePlan) {
  const before = maintenancePlan.parse(expected), after = maintenancePlan.parse(value);
  for (const key of ["id", "scope", "kind", "rules_version", "basis", "preview", "difference", "issues", "limitations", "created_by", "created_at", "expires_at"] as const) {
    if (JSON.stringify(before[key]) !== JSON.stringify(after[key])) throw new MaintenanceReadError("计划回执与已审阅依据不一致，请只读核对。");
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

export const maintenanceKinds = { rebuild: "派生周期索引重建", cycles: "检查与纠正周期分段", upgrade: "规则升级后重新测算" };
export function maintenanceIssue(issue: MaintenancePlan["issues"][number]) {
  const messages = {
    OBSERVATION_LIMIT: "范围内收到的观测超过 2000 条，扫描不完整；请缩小日期范围。",
    NO_LOCAL_OBSERVATIONS: "范围内没有已保存的原始观测，不能补造历史。",
    SOURCE_PROVENANCE_INVALID: "观测来源或版本依据不完整，保持原始事实不变。",
    RECEIPT_MISMATCH: "原始记录的收到时间不一致，不能推定所属范围。",
    PERIOD_EVIDENCE_MISSING: "缺少可核验的原生周期、复位时间或时长依据。",
    SOURCE_TIME_CONFLICT: "同一来源时间的实际百分比存在冲突，不能自动选择。",
    NO_NATIVE_PERIODS: "没有可用于重建的原生周期，排除记录不计入索引。",
    RULE_UPGRADE_UNAVAILABLE: "第二套独立规则尚未核准，规则升级与历史金额重算不可用。",
    RULES_UNVERIFIED: "该规则版本尚未核准，不生成推测的周期结果。",
    NO_PRIOR_PROJECTION: "此范围没有先前应用的周期索引；预检结果尚未应用。",
    PERIOD_INDEX_DIFFERENCE: "本次预检与此前本地派生索引不同，旧版本将保留。",
    REMOTE_OPERATION_UNRESOLVED: "计价或账号暂停操作正在执行或结果未知；请先核对原操作，再重新预检。",
  };
  const titles = {
    OBSERVATION_LIMIT: "观测数量超过上限", NO_LOCAL_OBSERVATIONS: "没有保留的观测", SOURCE_PROVENANCE_INVALID: "来源依据不完整",
    RECEIPT_MISMATCH: "收到时间不一致", PERIOD_EVIDENCE_MISSING: "原生周期依据不足", SOURCE_TIME_CONFLICT: "同一来源时间存在冲突",
    NO_NATIVE_PERIODS: "没有可重建的原生周期", RULE_UPGRADE_UNAVAILABLE: "规则升级不可用", RULES_UNVERIFIED: "规则尚未核准",
    NO_PRIOR_PROJECTION: "尚无先前派生索引", PERIOD_INDEX_DIFFERENCE: "本地派生索引存在差异",
    REMOTE_OPERATION_UNRESOLVED: "原生操作尚未核对",
  };
  return { title: titles[issue.code], description: messages[issue.code] + (issue.observation_id === null ? "" : " · 观测 " + issue.observation_id)
    + (issue.window_kind === null ? "" : " · " + (issue.window_kind === "short" ? "原生短窗" : "原生长窗")) };
}

export function maintenanceFailure(error: unknown) {
  const result = connectionFailure(error);
  return { ...result, text: error instanceof MaintenanceReadError ? error.message : result.status === 400 ? "账号、UTC 范围或维护参数不符合当前契约，请核对后重试。" : result.text };
}
