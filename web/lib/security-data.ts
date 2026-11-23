import { ORPCError } from "@orpc/client";
import { authErrorData } from "@sub-pool/contracts/auth";
import type { createSecurityClient } from "@sub-pool/contracts/client";
import type { SecurityConfig, SourceRule } from "@sub-pool/contracts/security";
import { usageNanos } from "@sub-pool/contracts/usage";
import { connectionFailure } from "./connection-data";
import { requestFailureText } from "./request-id";

type Client = ReturnType<typeof createSecurityClient>;
export type SecurityRange = Pick<Parameters<Client["loginAudit"]>[0], "from" | "until">;
export type SecurityChange = ({ kind: "source" } & Parameters<Client["updateSource"]>[0])
  | ({ kind: "config" } & Parameters<Client["updateConfig"]>[0]);
export class SecurityReadError extends Error {}

export function securityRange(now = Date.now()): SecurityRange {
  const until = new Date(now + 1), from = new Date(now); from.setUTCHours(0, 0, 0, 0);
  return { from: from.toISOString(), until: until.toISOString() };
}

async function readPages<T>(read: (after: string) => Promise<{ rows: T[]; next: string | null }>, key: (row: T) => string, initial: string) {
  const rows: T[] = [], ids = new Set<string>(), cursors = new Set([initial]);
  let after = initial;
  for (;;) {
    const page = await read(after);
    if (!page.rows.length && (after !== initial || page.next !== null)) throw new SecurityReadError("续页为空，尚未取得完整安全记录。");
    for (const row of page.rows) {
      const id = key(row);
      if (ids.has(id)) throw new SecurityReadError("安全记录重复，尚未取得完整列表。");
      ids.add(id); rows.push(row);
    }
    if (rows.length > 2000) throw new SecurityReadError("当前安全记录超过 2000 条，无法在此页完整呈现。");
    if (page.next === null) return rows;
    const last = page.rows.at(-1);
    if (rows.length === 2000 || cursors.has(page.next) || last === undefined || page.next !== key(last)) throw new SecurityReadError("安全记录游标不可继续，尚未取得完整列表。");
    cursors.add(page.next); after = page.next;
  }
}

function checkAuditRows(rows: { id: string; occurred_at: string }[], range: SecurityRange, after: string) {
  let previous = BigInt(after);
  for (const row of rows) {
    const id = BigInt(row.id), time = Date.parse(row.occurred_at);
    if (id <= previous || time < Date.parse(range.from) || time >= Date.parse(range.until)) throw new SecurityReadError("审计返回了其他范围或过期游标的记录。");
    previous = id;
  }
}

export async function readSecurityLogins(client: Pick<Client, "loginAudit">, range: SecurityRange, signal?: AbortSignal) {
  const read = (after: string) => client.loginAudit({ ...range, after, limit: 100, outcome: "all" }, { signal });
  const first = await read("0"), counts = first.counts;
  const events = await readPages(async after => {
    const page = after === "0" ? first : await read(after);
    checkAuditRows(page.events, range, after);
    if ((["success", "failure", "limited", "blocked"] as const).some(key => counts[key] !== page.counts[key])) throw new SecurityReadError("读取期间登录统计已变化，请重新读取核对。");
    return { rows: page.events, next: page.next_after };
  }, row => row.id, "0");
  const observed = { success: 0n, failure: 0n, limited: 0n, blocked: 0n };
  for (const event of events) observed[event.outcome]++;
  for (const key of ["success", "failure", "limited", "blocked"] as const) {
    if (observed[key] !== BigInt(counts[key])) throw new SecurityReadError("登录列表和统计尚未一致，请重新读取核对。");
  }
  return { events, counts };
}

export async function readSecuritySources(client: Pick<Client, "sources">, signal?: AbortSignal) {
  return readPages(async after => {
    const page = await client.sources({ after, state: "all", limit: 100 }, { signal });
    return { rows: page.rules, next: page.next_after };
  }, row => row.ip, "");
}

export async function readSecurityManagement(client: Pick<Client, "managementAudit">, range: SecurityRange, signal?: AbortSignal) {
  return readPages(async after => {
    const page = await client.managementAudit({ ...range, after, limit: 100 }, { signal });
    checkAuditRows(page.events, range, after); return { rows: page.events, next: page.next_after };
  }, row => row.id, "0");
}

export function checkSourceReceipt(change: Extract<SecurityChange, { kind: "source" }>, value: SourceRule, actor: string) {
  const expiryMatches = change.expires_at === undefined ? value.expires_at === null
    : value.expires_at !== null && usageNanos(value.expires_at) === usageNanos(change.expires_at);
  if (value.ip !== change.ip || value.blocked !== change.blocked || value.actor_id !== actor
    || BigInt(value.revision) !== BigInt(change.revision) + 1n
    || (change.blocked && (value.reason !== (change.reason ?? null) || !expiryMatches))) {
    throw new SecurityReadError("来源操作回执尚未核对，请只读刷新后确认。");
  }
}
export function checkConfigReceipt(change: Extract<SecurityChange, { kind: "config" }>, value: SecurityConfig) {
  if (value.network_diagnostics_enabled !== change.network_diagnostics_enabled || BigInt(value.revision) !== BigInt(change.revision) + 1n) throw new SecurityReadError("诊断策略回执尚未核对，请只读刷新后确认。");
}

export function securityFailure(error: unknown) {
  if (error instanceof ORPCError && error.defined && error.status === 409 && authErrorData.safeParse(error.data).success) {
    if (error.code === "SELF_SOURCE_PROTECTED") return { status: 409, text: requestFailureText(error, "服务端保护当前管理会话的来源及 TCP 对端，请重新读取后核对。") };
    if (error.code === "NETWORK_DIAGNOSTICS_DISABLED") return { status: 409, text: requestFailureText(error, "可选网络诊断已关闭，不影响正常登录。") };
  }
  const result = connectionFailure(error);
  return { ...result, text: error instanceof SecurityReadError ? error.message : result.text };
}
