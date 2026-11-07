import type { createNotificationsClient } from "@sub-pool/contracts/client";
import { notificationSaveInput } from "@sub-pool/contracts/notifications";
import type { Notification, NotificationConfiguration } from "@sub-pool/contracts/notifications";

type Client = ReturnType<typeof createNotificationsClient>;
export type NotificationSave = Parameters<Client["save"]>[0];
export type NotificationFilter = Pick<Parameters<Client["list"]>[0], "kind" | "state">;
export type ChannelDraft = { channel: NotificationConfiguration["channel"]; host: string; port: string; username: string; from: string; recipient: string; credential: string };
export type TestIntent = { request_id: string; revision: string; actor_id: string; id?: string };
export class NotificationReadError extends Error {}

export async function readNotificationPages(client: Pick<Client, "list">, admin: boolean, filter: NotificationFilter = { kind: "", state: "" }, signal?: AbortSignal) {
  const rows: Notification[] = [];
  let after = "0";
  for (;;) {
    const page = await client.list({ start_at: "", end_at: "", participant_id: "", subject_type: "", subject_id: "", ...filter, after, limit: 100 }, { signal });
    for (const row of page.notifications) {
      if (rows.length >= 2000 || BigInt(row.id) <= BigInt(rows.at(-1)?.id ?? after)
        || (row.visibility === "administrator") !== admin || filter.kind && row.kind !== filter.kind || filter.state && row.state !== filter.state) {
        throw new NotificationReadError("通知记录重复、范围变化或超过 2000 条读取上限；未显示部分结果，请缩小筛选范围。");
      }
      rows.push(row);
    }
    if (page.next_after === null) return rows;
    if (page.notifications.at(-1)?.id !== page.next_after || BigInt(page.next_after) <= BigInt(after)) throw new NotificationReadError("通知分页未前进，请重新读取。");
    after = page.next_after;
  }
}

export function notificationSaveBase(saved: NotificationConfiguration): NotificationSave {
  const { revision, enabled, channel, from, recipient, smtp, preferences, dedupe_seconds } = saved;
  return { revision, enabled, channel, from, recipient, smtp, preferences, dedupe_seconds, credential_action: "keep", credential: "" };
}
export function channelDraft(saved: NotificationConfiguration): ChannelDraft {
  return { channel: saved.channel, host: saved.smtp.host, port: String(saved.smtp.port), username: saved.smtp.username, from: saved.from, recipient: saved.recipient, credential: "" };
}
export function channelSave(saved: NotificationConfiguration, draft: ChannelDraft): NotificationSave {
  if (draft.channel === "smtp" && !/^[1-9]\d{0,4}$/.test(draft.port)) throw new NotificationReadError("端口必须是 1 至 65535 的整数。");
  return notificationSaveInput.parse({ ...notificationSaveBase(saved), channel: draft.channel, from: draft.from, recipient: draft.recipient,
    smtp: draft.channel === "smtp" ? { ...saved.smtp, host: draft.host, port: Number(draft.port), username: draft.username } : saved.smtp,
    credential_action: draft.credential ? "replace" : "keep", credential: draft.credential });
}
export function policySave(saved: NotificationConfiguration, preferences: NotificationConfiguration["preferences"], seconds: string): NotificationSave {
  if (!/^[1-9]\d{1,4}$/.test(seconds)) throw new NotificationReadError("重复间隔必须是 60 至 86400 秒的整数。");
  return notificationSaveInput.parse({ ...notificationSaveBase(saved), preferences, dedupe_seconds: Number(seconds) });
}
export function checkNotificationSave(value: NotificationConfiguration, input: NotificationSave) {
  const actual = notificationSaveBase(value);
  if (BigInt(value.revision) !== BigInt(input.revision) + 1n || value.saved_at === null
    || ["enabled", "channel", "from", "recipient", "dedupe_seconds"].some(key => actual[key as keyof NotificationSave] !== input[key as keyof NotificationSave])
    || Object.entries(input.smtp).some(([key, item]) => value.smtp[key as keyof typeof input.smtp] !== item)
    || Object.entries(input.preferences).some(([key, item]) => value.preferences[key as keyof typeof input.preferences] !== item)
    || input.credential_action === "replace" && !value.secret_set || input.credential_action === "clear" && value.secret_set) {
    throw new NotificationReadError("保存回执与提交内容不一致，结果仍待核对；请重新读取，勿重复提交。");
  }
}
export function checkTestReceipt(row: Notification, intent: TestIntent) {
  if (row.kind !== "test" || row.visibility !== "administrator" || row.details?.event_key !== "test:" + intent.request_id
    || row.details.config_revision !== intent.revision || row.details.actor_id !== intent.actor_id || intent.id && row.id !== intent.id) {
    throw new NotificationReadError("测试回执与当前请求不一致，结果仍待核对。");
  }
  return row;
}
export async function recoverNotificationTest(client: Pick<Client, "list" | "get">, intent: TestIntent, signal?: AbortSignal) {
  const row = intent.id ? await client.get({ id: intent.id }, { signal })
    : (await readNotificationPages(client, true, { kind: "test", state: "" }, signal)).find(item => item.details?.event_key === "test:" + intent.request_id);
  return row ? checkTestReceipt(row, intent) : null;
}

export const notificationKinds = [
  { id: "balance_low", label: "余额低且存在补充建议" },
  { id: "recommendation_changed", label: "建议变化" },
  { id: "estimate_changed", label: "估算显著变化" },
  { id: "collection_failed", label: "采集失败" },
] as const;
export const notificationKindLabel = { test: "测试邮件", collection_failed: "采集失败" };
const reasons: Record<Notification["reason"], string> = {
  QUEUED: "已持久化排队，尚未发送", DISPATCHING: "已开始发送，尚无渠道回执", TRANSPORT_ACCEPTED: "渠道已接受；不代表已送达或已读", REMOTE_REJECTED: "渠道明确拒绝了本次发送",
  TRANSPORT_UNAVAILABLE: "发送连接或认证未完成", MESSAGE_INVALID: "邮件内容未通过渠道校验", TRANSPORT_OUTCOME_UNKNOWN: "已尝试发送但未取得确定回执，不会自动重发", REMOTE_PROTOCOL_UNKNOWN: "渠道回执无法确认，不会自动重发",
  CHANNEL_INCOMPLETE: "渠道配置不完整，未发送", CHANNEL_DISABLED: "全局通知已关闭，未发送", EVENT_DISABLED: "该类提醒已关闭，未发送", DUPLICATE_WINDOW: "重复提醒间隔内，已跳过",
  CONFIGURATION_CHANGED: "配置已变化，未使用旧配置发送", SUBJECT_CHANGED: "关联对象已变化，未发送", SECRET_STORAGE_UNAVAILABLE: "服务暂时无法安全读取凭据", ACTOR_UNAVAILABLE: "请求者权限或会话已失效，未发送",
  WORKER_INTERRUPTED: "工作进程在发送期间中断，结果未知，不会自动重发", ACCEPTED: "渠道已接受；不代表已送达或已读", FAILED: "本次发送未完成", UNKNOWN: "投递结果无法确认", SKIPPED: "本次提醒已跳过",
};
export function notificationReason(row: Notification) { return reasons[row.reason]; }
