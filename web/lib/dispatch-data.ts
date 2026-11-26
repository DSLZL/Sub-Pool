import type { createDispatchClient } from "@sub-pool/contracts/client";
import type { DispatchAccounts, DispatchObservation } from "@sub-pool/contracts/dispatch";
import { checkText } from "./connection-data";

type Client = ReturnType<typeof createDispatchClient>;
type Binding = Parameters<Client["account"]>[0];
export class DispatchReadError extends Error {
  constructor(message: string, readonly refreshIdentity = false) { super(message); }
}

export async function readDispatchAccounts(read: (after: string) => ReturnType<Client["list"]>, authorityRevision: string) {
  const accounts: DispatchAccounts["accounts"] = [], ids = new Set<string>(), cursors = new Set<string>();
  let after = "", revision: string | null = null;
  // shortcut: The supplied selector has no paging control; reject above 2,000 visible accounts until it does.
  for (let number = 0; number < 20; number++) {
    const page = await read(after);
    if (page.authority_revision !== authorityRevision) throw new DispatchReadError("权限已变。", true);
    if (revision !== null && page.connection_revision !== revision) throw new DispatchReadError("连接已变。", true);
    revision = page.connection_revision;
    for (const account of page.accounts) {
      if (ids.has(account.id)) throw new DispatchReadError("账号重复。");
      ids.add(account.id); accounts.push(account);
    }
    if (page.next_after === null) return { accounts, last: page };
    if (!page.accounts.length || page.next_after !== page.accounts.at(-1)?.id || cursors.has(page.next_after)) throw new DispatchReadError("分页错误。");
    cursors.add(page.next_after); after = page.next_after;
  }
  throw new DispatchReadError("账号超限。");
}

export async function readDispatchObservation(read: () => ReturnType<Client["account"]>, input: Binding, authorityRevision: string) {
  const value = await read();
  if (value.authority_revision !== authorityRevision || value.id !== input.id || value.revision !== input.revision || value.source_id !== input.source_id || value.connection_revision !== input.connection_revision) throw new DispatchReadError("账号或权限变化。", true);
  return value;
}

export function dispatchControlText(status: DispatchAccounts | null) {
  return status ? "暂停未开放" : "暂停待核对";
}

export function dispatchReadText(check: DispatchObservation["read"]) {
  if (check.reason === "ACCOUNT_IDENTITY_CHANGED") return "资源已变。";
  if (check.reason === "RESOURCE_IDENTITY_UNVERIFIED") return "资源待核对。";
  return checkText({ ...check, id: "accounts.read", reason: check.reason });
}

export function dispatchObservationText(value: DispatchObservation | null) {
  if (!value) return "暂无调度读数";
  if (value.configured_schedulable === null) return dispatchReadText(value.read);
  return `调度：${value.configured_schedulable ? "已开启" : "已关闭"} · 原生状态：${value.native_status ?? "未知"}`;
}
