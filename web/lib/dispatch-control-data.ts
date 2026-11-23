import type { DispatchControl, DispatchEvent, DispatchHistory, DispatchPause } from "@sub-pool/contracts/dispatch";
import { DispatchReadError } from "./dispatch-data";

export async function readDispatchHistory(read: (after: string) => Promise<DispatchHistory>, authorityRevision: string) {
  const events: DispatchEvent[] = [], seen = new Set<string>();
  let after = "";
  // shortcut: The supplied table has no paging control; reject above 10,000 events until it does.
  for (let page = 0; page < 100; page++) {
    const result = await read(after);
    if (result.authority_revision !== authorityRevision) throw new DispatchReadError("授权已变化，请重新读取暂停记录。", true);
    let previous = after;
    for (const event of result.events) {
      if (seen.has(event.id) || previous !== "" && BigInt(event.id) <= BigInt(previous)) throw new DispatchReadError("暂停记录分页不一致，请重新读取。");
      seen.add(event.id); events.push(event); previous = event.id;
    }
    if (result.next_after === null) return events;
    if (!result.events.length || result.next_after !== result.events.at(-1)?.id) throw new DispatchReadError("暂停记录分页不完整，请重新读取。");
    after = result.next_after;
  }
  throw new DispatchReadError("暂停记录超过本页一万条的读取上限，尚不能确认完整历史。");
}

const reasons: Record<string, string> = {
  PAUSE_PENDING: "暂停操作已登记，结果待确认", VERIFIED_PAUSE: "已独立读回确认暂停",
  PREVIOUSLY_DISABLED: "暂停前已经停止调度，没有改写上游", DEADLINE_CHANGED: "恢复时间已修改",
  READ_RETRY_PENDING: "原生读取暂时失败，尚未发送恢复写入；后台稍后重读",
  RESTORE_PENDING: "恢复操作已登记，结果待确认", VERIFIED_RESTORE: "已独立读回确认恢复",
  PREVIOUS_STATE_RETAINED: "保留暂停前的停用状态，没有开启上游调度", DEADLINE_PASSED: "恢复时间已过，未发送暂停写入",
  CONTROL_BINDING_REQUIRED: "当前来源尚未启用独占调度控制，未发送写入",
  SOURCE_WRITE_GRANT_REQUIRED: "当前整账号调度授权缺失或已变化，本次操作未派发",
  UPSTREAM_WRITES_DISABLED: "紧急禁写已开启，本次操作未派发",
  LOCAL_ACCOUNT_CHANGED: "本地账号或来源版本已变化，需人工核对", CONNECTION_UNAVAILABLE: "当前连接不可用，需人工核对",
  NATIVE_AUTHORITY_UNVERIFIED: "无法核验当前原生管理员权限与运行模式，需人工核对",
  NATIVE_READ_UNAVAILABLE: "无法读取当前原生账号，需人工核对", NATIVE_ACCOUNT_UNVERIFIED: "原生账号身份、版本或状态无法确认，需人工核对",
  NATIVE_ACCOUNT_CHANGED: "原生账号已被外部修改，未覆盖外部状态", WRITE_RESULT_UNKNOWN: "原生写入结果未知，停止自动重试，需人工核对",
  PROCESS_INTERRUPTED_RESULT_UNKNOWN: "操作因进程中断而结果未知，停止自动重试，需人工核对",
  MANUALLY_REVIEWED: "管理员已确认完成人工核对；此记录不表示上游已恢复",
};
export const dispatchReasonText = (pause: DispatchPause) => reasons[pause.reason] ?? "结果尚不能确认，请重新读取。";

export function dispatchStatusText(control: DispatchControl, events: DispatchEvent[], accountID?: string) {
  const latest = new Map(events.map(event => [event.pause.id, event.pause]));
  const pauses = [...latest.values()].filter(pause => !accountID || pause.account_id === accountID);
  const last = pauses.at(-1);
  if (accountID && last) return dispatchReasonText(last);
  const review = pauses.filter(pause => pause.state === "needs_review").length;
  const pending = pauses.filter(pause => ["pausing", "paused", "restoring"].includes(pause.state)).length;
  if (review || pending) return `暂停记录：${pending} 条待完成恢复，${review} 条需人工核对；原生当前配置需单独读取。`;
  return control.can_pause ? "受控暂停已启用；可在账号暂停页面操作。"
    : control.reason === "ADMIN_REQUIRED" ? "仅管理员可操作整账号暂停；此处显示授权范围内的记录。"
    : "当前来源未启用受控暂停；历史记录不代表原生当前配置。";
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
