import { glossary, uiCopy } from "@/lib/glossary";
import type { DispatchControl, DispatchEvent, DispatchHistory, DispatchPause } from "@sub-pool/contracts/dispatch";
import { DispatchReadError } from "./dispatch-data";

export async function readDispatchHistory(read: (after: string) => Promise<DispatchHistory>, authorityRevision: string) {
  const events: DispatchEvent[] = [], seen = new Set<string>();
  let after = "";
  // shortcut: The supplied table has no paging control; reject above 10,000 events until it does.
  for (let page = 0; page < 100; page++) {
    const result = await read(after);
    if (result.authority_revision !== authorityRevision) throw new DispatchReadError("权限已变。", true);
    let previous = after;
    for (const event of result.events) {
      if (seen.has(event.id) || previous !== "" && BigInt(event.id) <= BigInt(previous)) throw new DispatchReadError("分页错误。");
      seen.add(event.id); events.push(event); previous = event.id;
    }
    if (result.next_after === null) return events;
    if (!result.events.length || result.next_after !== result.events.at(-1)?.id) throw new DispatchReadError("历史不全。");
    after = result.next_after;
  }
  throw new DispatchReadError("历史超限。");
}

const reasons: Record<string, string> = {
  PAUSE_PENDING: "暂停待核对", VERIFIED_PAUSE: "已确认暂停",
  PREVIOUSLY_DISABLED: "保持停用", DEADLINE_CHANGED: "期限已变",
  READ_RETRY_PENDING: "恢复待读取",
  RESTORE_PENDING: "恢复待核对", VERIFIED_RESTORE: "已确认恢复",
  PREVIOUS_STATE_RETAINED: "保持原停用", DEADLINE_PASSED: "期限已过",
  CONTROL_BINDING_REQUIRED: "调度未授权",
  SOURCE_WRITE_GRANT_REQUIRED: "无暂停授权",
  UPSTREAM_WRITES_DISABLED: "禁写，未派发",
  LOCAL_ACCOUNT_CHANGED: "账号已变", CONNECTION_UNAVAILABLE: "连接不可用",
  NATIVE_AUTHORITY_UNVERIFIED: "核对权限和模式",
  NATIVE_READ_UNAVAILABLE: "账号不可读", NATIVE_ACCOUNT_UNVERIFIED: "账号待核对",
  NATIVE_ACCOUNT_CHANGED: "上游状态已变", WRITE_RESULT_UNKNOWN: "结果待核对",
  PROCESS_INTERRUPTED_RESULT_UNKNOWN: "中断，待核对",
  MANUALLY_REVIEWED: "跟踪已结束，恢复未知",
};
export const dispatchReasonText = (pause: DispatchPause) => reasons[pause.reason] ?? uiCopy.pendingResult;

export function dispatchStatusText(control: DispatchControl, events: DispatchEvent[], accountID?: string) {
  const latest = new Map(events.map(event => [event.pause.id, event.pause]));
  const pauses = [...latest.values()].filter(pause => !accountID || pause.account_id === accountID);
  const last = pauses.at(-1);
  if (accountID && last) return dispatchReasonText(last);
  const review = pauses.filter(pause => pause.state === "needs_review").length;
  const pending = pauses.filter(pause => ["pausing", "paused", "restoring"].includes(pause.state)).length;
  if (review || pending) return `暂停记录：${pending} 条待恢复，${review} 条待核对。`;
  return control.can_pause ? "去账号暂停。"
    : control.reason === "ADMIN_REQUIRED" ? "联系管理员。"
    : "暂停未启用。";
}

export function dispatchTimeInput(value: string | Date) {
  const date = typeof value === "string" ? new Date(value) : value;
  const pad = (number: number) => String(number).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function dispatchDeadlineInput(value: string, now = Date.now()) {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const date = new Date(value), time = date.getTime();
  return Number.isFinite(time) && dispatchTimeInput(date) === value && time > now && time <= now + 30 * 24 * 3600_000
    ? date.toISOString() : null;
}
