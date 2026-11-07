import type { createDispatchClient } from "@sub-pool/contracts/client";
import type { DispatchAccounts, DispatchObservation } from "@sub-pool/contracts/dispatch";
import { checkText } from "./connection-data";

type Client = ReturnType<typeof createDispatchClient>;
type Binding = Parameters<Client["account"]>[0];
export class DispatchReadError extends Error {}

export async function readDispatchAccounts(read: (after: string) => ReturnType<Client["list"]>, authorityRevision: string) {
  const accounts: DispatchAccounts["accounts"] = [], ids = new Set<string>(), cursors = new Set<string>();
  let after = "", revision: string | null = null;
  // shortcut: The supplied selector has no paging control; reject above 2,000 visible accounts until it does.
  for (let number = 0; number < 20; number++) {
    const page = await read(after);
    if (page.authority_revision !== authorityRevision) throw new DispatchReadError("权限版本已变化，请重新读取。");
    if (revision !== null && page.connection_revision !== revision) throw new DispatchReadError("连接版本已变化，请重新读取。");
    revision = page.connection_revision;
    for (const account of page.accounts) {
      if (ids.has(account.id)) throw new DispatchReadError("账号分页存在重复对象，未展示部分列表。");
      ids.add(account.id); accounts.push(account);
    }
    if (page.next_after === null) return { accounts, last: page };
    if (!page.accounts.length || page.next_after !== page.accounts.at(-1)?.id || cursors.has(page.next_after)) throw new DispatchReadError("账号分页无法确认，请重新读取。");
    cursors.add(page.next_after); after = page.next_after;
  }
  throw new DispatchReadError("可见账号超过当前界面读取范围，未展示部分列表。");
}

export async function readDispatchObservation(read: () => ReturnType<Client["account"]>, input: Binding, authorityRevision: string) {
  const value = await read();
  if (value.authority_revision !== authorityRevision || value.id !== input.id || value.revision !== input.revision || value.source_id !== input.source_id || value.connection_revision !== input.connection_revision) throw new DispatchReadError("账号、来源或权限版本已变化，请重新读取。");
  return value;
}

export function dispatchControlText(status: DispatchAccounts | null) {
  return status ? "整账号暂停、恢复与操作历史尚未开放" : "暂停、恢复与历史尚未读取，不能确认可用性";
}

export function dispatchReadText(check: DispatchObservation["read"]) {
  if (check.reason === "ACCOUNT_IDENTITY_CHANGED") return "账号或根资源身份已变化，本次配置不可确认。";
  if (check.reason === "RESOURCE_IDENTITY_UNVERIFIED") return "当前返回未能核验原有根资源身份，本次配置未知。";
  return checkText({ ...check, id: "accounts.read", reason: check.reason });
}

export function dispatchObservationText(value: DispatchObservation | null) {
  if (!value) return "尚未读取当前原生调度配置";
  if (value.configured_schedulable === null) return dispatchReadText(value.read);
  return `原生调度配置：${value.configured_schedulable ? "已开启" : "已关闭"} · 原生状态：${value.native_status ?? "未知"}`;
}
