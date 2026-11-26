import type { createNotificationsClient } from "@sub-pool/contracts/client";
import { notificationQuery, notificationSaveInput } from "@sub-pool/contracts/notifications";
import type { Notification, NotificationConfiguration } from "@sub-pool/contracts/notifications";
import { usageNanos } from "@sub-pool/contracts/usage";

type Client = ReturnType<typeof createNotificationsClient>;
export type NotificationSave = Parameters<Client["save"]>[0];
export type NotificationFilter = Omit<Parameters<Client["list"]>[0], "after" | "limit">;
export const allNotificationFilters: NotificationFilter = { start_at: "", end_at: "", kind: "", state: "", participant_id: "", subject_type: "", subject_id: "" };
export type ChannelDraft = { channel: NotificationConfiguration["channel"]; host: string; port: string; tls: NotificationConfiguration["smtp"]["tls"]; username: string; from: string; recipient: string; credential: string; clear_credential: boolean };
export type TestIntent = { request_id: string; revision: string; actor_id: string; id?: string };
export class NotificationReadError extends Error {}

export function notificationFilter(input: Partial<NotificationFilter>): NotificationFilter {
  const filter = { ...allNotificationFilters, ...input };
  const validTime = (value: string) => !value || usageNanos(value) !== null && new Date(value).toISOString().slice(0, 19) === value.slice(0, 19);
  if (!notificationQuery.safeParse({ ...filter, after: "0", limit: 100 }).success || !validTime(filter.start_at) || !validTime(filter.end_at)) {
    throw new NotificationReadError("核对对象，区间最多 31 天。");
  }
  return filter;
}
export function notificationMatches(row: Notification, filter: NotificationFilter) {
  const at = usageNanos(row.created_at), start = filter.start_at ? usageNanos(filter.start_at) : null, end = filter.end_at ? usageNanos(filter.end_at) : null;
  return at !== null && (start === null || at >= start) && (end === null || at < end)
    && (!filter.kind || row.kind === filter.kind) && (!filter.state || row.state === filter.state)
    && (!filter.participant_id || row.participant_id === filter.participant_id)
    && (!filter.subject_type || row.subject_type === filter.subject_type) && (!filter.subject_id || row.subject_id === filter.subject_id);
}
export async function readNotificationPages(client: Pick<Client, "list">, admin: boolean, input: Partial<NotificationFilter> = {}, signal?: AbortSignal) {
  const filter = notificationFilter(input);
  const rows: Notification[] = [];
  let after = "0";
  for (;;) {
    const page = await client.list({ ...filter, after, limit: 100 }, { signal });
    for (const row of page.notifications) {
      if (rows.length >= 2000 || BigInt(row.id) <= BigInt(rows.at(-1)?.id ?? after)
        || (row.visibility === "administrator") !== admin || !notificationMatches(row, filter)) {
        throw new NotificationReadError("请缩小范围。");
      }
      rows.push(row);
    }
    if (page.next_after === null) return rows;
    if (page.notifications.at(-1)?.id !== page.next_after || BigInt(page.next_after) <= BigInt(after)) throw new NotificationReadError("无法续页。");
    after = page.next_after;
  }
}

export async function readNotificationDetail(selected: Notification, admin: boolean, read: () => Promise<Notification>) {
  const row = await read();
  const fields = ["id", "kind", "subject_type", "subject_id", "participant_id", "created_at", "visibility"] as const;
  const details = ["config_revision", "channel", "from", "recipient", "subject", "body", "event_reason", "event_key", "actor_id"] as const;
  if ((row.visibility === "administrator") !== admin || fields.some(key => row[key] !== selected[key])
    || details.some(key => row.details?.[key] !== selected.details?.[key])) {
    throw new NotificationReadError("范围变更。");
  }
  return row;
}

export function notificationSaveBase(saved: NotificationConfiguration): NotificationSave {
  const { revision, enabled, financial_enabled, channel, from, recipient, smtp, preferences, dedupe_seconds } = saved;
  return { revision, enabled, financial_enabled, channel, from, recipient, smtp, preferences, dedupe_seconds, credential_action: "keep", credential: "" };
}
export function channelDraft(saved: NotificationConfiguration): ChannelDraft {
  return { channel: saved.channel, host: saved.smtp.host, port: String(saved.smtp.port), tls: saved.smtp.tls, username: saved.smtp.username, from: saved.from, recipient: saved.recipient, credential: "", clear_credential: false };
}
export function channelSave(saved: NotificationConfiguration, draft: ChannelDraft): NotificationSave {
  if (draft.channel === "smtp" && !/^[1-9]\d{0,4}$/.test(draft.port)) throw new NotificationReadError("端口须为 1–65535。");
  const input = notificationSaveInput.parse({ ...notificationSaveBase(saved), channel: draft.channel, from: draft.from, recipient: draft.recipient,
    smtp: draft.channel === "smtp" ? { host: draft.host, port: Number(draft.port), tls: draft.tls, username: draft.username } : saved.smtp,
    credential_action: draft.clear_credential ? "clear" : draft.credential ? "replace" : "keep", credential: draft.clear_credential ? "" : draft.credential });
  if (saved.secret_set && input.credential_action === "keep" && (saved.channel !== input.channel
    || input.channel === "smtp" && Object.entries(input.smtp).some(([key, value]) => saved.smtp[key as keyof typeof input.smtp] !== value))) {
    throw new NotificationReadError("重填或清除凭据。");
  }
  return input;
}
export function financialActivationPending(saved: NotificationConfiguration, preferences: NotificationConfiguration["preferences"], enabled: boolean) {
  const active = saved.native_source_id === null ? saved.financial_enabled : saved.native_financial_enabled;
  return !active && enabled && saved.capabilities.some(item => item.kind !== "collection_failed" && item.available && preferences[item.kind]);
}
export function policySave(saved: NotificationConfiguration, preferences: NotificationConfiguration["preferences"], seconds: string, enabled: boolean): NotificationSave {
  if (!/^[1-9]\d{1,4}$/.test(seconds)) throw new NotificationReadError("间隔须为 60–86400 秒。");
  const activate = financialActivationPending(saved, preferences, enabled);
  const nativeSource = activate ? saved.native_source_id : null;
  return notificationSaveInput.parse({ ...notificationSaveBase(saved), enabled, financial_enabled: saved.financial_enabled || activate,
    ...(nativeSource ? { activate_native_source_id: nativeSource } : {}), preferences, dedupe_seconds: Number(seconds) });
}
export function checkNotificationSave(value: NotificationConfiguration, input: NotificationSave) {
  const actual = notificationSaveBase(value);
  if (BigInt(value.revision) !== BigInt(input.revision) + 1n || value.saved_at === null
    || input.activate_native_source_id !== undefined && (value.native_source_id !== input.activate_native_source_id || !value.native_financial_enabled)
    || ["enabled", "financial_enabled", "channel", "from", "recipient", "dedupe_seconds"].some(key => actual[key as keyof NotificationSave] !== input[key as keyof NotificationSave])
    || Object.entries(input.smtp).some(([key, item]) => value.smtp[key as keyof typeof input.smtp] !== item)
    || Object.entries(input.preferences).some(([key, item]) => value.preferences[key as keyof typeof input.preferences] !== item)
    || input.credential_action === "replace" && !value.secret_set || input.credential_action === "clear" && value.secret_set) {
    throw new NotificationReadError("保存待核对。");
  }
}
export function checkTestReceipt(row: Notification, intent: TestIntent) {
  if (row.kind !== "test" || row.visibility !== "administrator" || row.details?.event_key !== "test:" + intent.request_id
    || row.details.config_revision !== intent.revision || row.details.actor_id !== intent.actor_id || intent.id && row.id !== intent.id) {
    throw new NotificationReadError("测试待核对。");
  }
  return row;
}
export async function recoverNotificationTest(client: Pick<Client, "list" | "get">, intent: TestIntent, signal?: AbortSignal) {
  const row = intent.id ? await client.get({ id: intent.id }, { signal })
    : (await readNotificationPages(client, true, { kind: "test", state: "" }, signal)).find(item => item.details?.event_key === "test:" + intent.request_id);
  return row ? checkTestReceipt(row, intent) : null;
}

export const notificationKinds = [
  { id: "balance_low", label: "余额低" },
  { id: "recommendation_changed", label: "建议变化" },
  { id: "estimate_changed", label: "估算变化" },
  { id: "collection_failed", label: "采集失败" },
] as const;
export const notificationKindLabel = {
  test: "测试邮件",
  collection_failed: "采集失败",
  temporary_near: "临时轮次近限",
  temporary_exhausted: "临时轮次耗尽",
  balance_low: "余额低",
  recommendation_changed: "钱包建议变化",
  estimate_changed: "估算变化",
};

export function notificationRecordLabel(row: Notification) {
  const label = notificationKindLabel[row.kind];
  return row.details?.event_reason.startsWith("SIMULATED_") ? `${label}（模拟）` : label;
}

const reasons: Record<Notification["reason"], string> = {
  QUEUED: "已排队", DISPATCHING: "发送中", TRANSPORT_ACCEPTED: "渠道已接受", REMOTE_REJECTED: "渠道拒绝",
  TRANSPORT_UNAVAILABLE: "连接或认证失败", MESSAGE_INVALID: "邮件无效", TRANSPORT_OUTCOME_UNKNOWN: "投递待核对", REMOTE_PROTOCOL_UNKNOWN: "回执未知",
  CHANNEL_INCOMPLETE: "配置不齐", CHANNEL_DISABLED: "通知已关闭", EVENT_DISABLED: "提醒已关闭", DUPLICATE_WINDOW: "间隔内跳过",
  CONFIGURATION_CHANGED: "配置已变", SUBJECT_CHANGED: "对象已变", SECRET_STORAGE_UNAVAILABLE: "凭据不可读", ACTOR_UNAVAILABLE: "授权已失效",
  TEMPORARY_EVENT_UNAVAILABLE: "条件已变，未发",
  FINANCIAL_NOT_ACTIVATED: "金融提醒关闭", FINANCIAL_EVENT_UNAVAILABLE: "数据不足，未发",
  WORKER_INTERRUPTED: "中断，结果未知", ACCEPTED: "渠道已接受", FAILED: "发送未完成", UNKNOWN: "投递待核对", SKIPPED: "已跳过",
};
export function notificationReason(row: Notification) { return reasons[row.reason]; }
