import { ORPCError } from "@orpc/client";
import { authErrorData } from "@sub-pool/contracts/auth";
import { backupFile, backupMaxFileBytes, type BackupArchive, type BackupFile, type RestorePlan, type RestoreChecks } from "@sub-pool/contracts/backups";
import type { createBackupsClient } from "@sub-pool/contracts/client";
import { usageNanos } from "@sub-pool/contracts/usage";
import { connectionFailure } from "./connection-data";
import { requestFailureText } from "./request-id";

type Client = ReturnType<typeof createBackupsClient>;
export class BackupReadError extends Error {}

export function backupRequestID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
export async function backupHash(content: string) {
  return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(content))), v => v.toString(16).padStart(2, "0")).join("");
}
export async function readBackupUpload(file: Pick<File, "name" | "size" | "arrayBuffer">) {
  if (!file.size || file.size > backupMaxFileBytes) throw new BackupReadError("请选择不超过 12 MiB 的本系统备份文件。");
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (bytes.length !== file.size || bytes.length > backupMaxFileBytes) throw new BackupReadError("文件读取不完整，请重新选择。");
  let content: string;
  try { content = new TextDecoder("utf-8", { fatal: true }).decode(bytes); } catch { throw new BackupReadError("文件不是有效的本系统备份文本。"); }
  if (!backupFile.shape.content.safeParse(content).success) throw new BackupReadError("文件格式不符合当前备份契约；文件名或扩展名不能代替校验。");
  return { name: file.name, content, sha256: await backupHash(content) };
}
export async function checkBackupFile(value: BackupFile, expected?: BackupArchive) {
  const file = backupFile.parse(value);
  if (expected && JSON.stringify(file.archive) !== JSON.stringify(expected)) throw new BackupReadError("文件与所选备份记录不一致，请重新读取。");
  if (await backupHash(file.content) !== file.archive.file_sha256) throw new BackupReadError("下载内容的 SHA-256 与服务端记录不一致。");
  return file;
}
export async function readBackups(client: Pick<Client, "list">, signal?: AbortSignal) {
  const archives: BackupArchive[] = [], ids = new Set<string>(); let after = "", previous: bigint | null = null, keyAvailable: boolean | null = null, bytes = 0;
  for (;;) {
    const page = await client.list({ after, limit: 100 }, { signal });
    if (keyAvailable !== null && keyAvailable !== page.key_available || after && !page.archives.length) throw new BackupReadError("备份续页状态不一致，请重新读取全部记录。");
    keyAvailable = page.key_available;
    for (const archive of page.archives) {
      const at = usageNanos(archive.created_at);
      if (at === null || ids.has(archive.id) || previous !== null && at > previous) throw new BackupReadError("备份记录重复或顺序不一致，未展示部分结果。");
      ids.add(archive.id); previous = at; bytes += new TextEncoder().encode(JSON.stringify(archive)).length;
      // shortcut: the supplied table has no pagination controls; keep a whole bounded catalog until the UI supports paging.
      if (archives.length === 2000 || bytes > 32 * 1024 * 1024) throw new BackupReadError("备份记录超过此页 2000 条或 32 MiB 上限，尚未取得完整列表。");
      archives.push(archive);
    }
    if (page.next_after === null) return { archives, keyAvailable };
    if (!page.archives.length || page.next_after !== page.archives.at(-1)?.id || page.next_after === after) throw new BackupReadError("备份游标不完整，请重新读取。");
    after = page.next_after;
  }
}
export function checkRestoreReceipt(expected: RestorePlan, received: RestorePlan) {
  for (const key of ["id", "created_at", "expires_at", "file_sha256", "target_basis", "before_archive_id", "source"] as const) {
    if (JSON.stringify(expected[key]) !== JSON.stringify(received[key])) throw new BackupReadError("恢复回执与已审阅的文件、目标或快照不一致，请只读核对。");
  }
}
export function checkRestoreChecks(plan: RestorePlan, checks: RestoreChecks) {
  if (plan.state !== "applied" || checks.plan_id !== plan.id || checks.applied_at !== plan.finished_at || JSON.stringify(checks.source) !== JSON.stringify(plan.source)) throw new BackupReadError("恢复后检查未匹配当前已完成记录。");
}
export function restoreCanApply(plan: RestorePlan | null, beforeDownloaded: string | null, now = Date.now()) {
  return plan !== null && plan.state === "ready" && plan.blockers.length === 0 && plan.before_archive_id !== null && beforeDownloaded === plan.before_archive_id && Date.parse(plan.expires_at) > now;
}
export const backupBlockers: Record<RestorePlan["blockers"][number], string> = {
  COLLECTION_ACTIVE: "当前有待处理采集任务", MAIL_ACTIVE: "当前有待处理邮件", MAINTENANCE_ACTIVE: "当前有待处理维护",
  REMOTE_WRITE_UNCONFIRMED: "当前远端写入未确认未派发", BACKUP_COLLECTION_ACTIVE: "备份内有待处理采集任务",
  BACKUP_MAIL_ACTIVE: "备份内有待处理邮件", BACKUP_MAINTENANCE_ACTIVE: "备份内有待处理维护",
  BACKUP_REMOTE_WRITE_UNCONFIRMED: "备份内远端写入未确认未派发", BACKUP_SOURCE_BLOCKED: "备份会阻断当前请求来源",
  WEEKLY_WALLET_HISTORY_CHANGED: "备份缺少当前周额度发放或追缴记录",
};
export const restoreReasons: Record<RestorePlan["reason"], string> = {
  READY: "预检已保存，确认前请下载下方的恢复前快照", RESTORE_BLOCKED: "预检存在阻塞，不能恢复", APPLYING: "服务端正在恢复，尚未确认完成",
  APPLIED: "服务端已完成本地恢复，请重新登录并读取检查", PLAN_EXPIRED: "预检已过期，请手动重新预检", BASIS_CHANGED: "当前业务数据已变化，请手动重新预检",
  BUSY_STATE_CHANGED: "任务状态已变化，请手动重新预检", WORKER_INTERRUPTED: "恢复进程中断，服务端已记失败且未重放",
  RESTORE_FAILED: "服务端恢复失败；请核对记录和恢复前快照", AUTHORIZATION_EXPIRED: "执行期间授权失效，服务端已记失败",
  TARGET_REPLACED: "目标已由另一次恢复替换，此计划失效",
};
export function backupFailure(error: unknown) {
  if (error instanceof BackupReadError) return { status: 0, text: error.message };
  const messages = { BACKUP_INVALID: [400, "文件内容、保护密钥或完整性不符合当前要求。"], BACKUP_SCHEMA_INCOMPATIBLE: [409, "备份与当前业务结构不兼容。"],
    BACKUP_LIMIT_EXCEEDED: [413, "备份内容超过服务端上限；没有应用部分数据。"], RESTORE_BUSY: [409, "当前有其他服务或数据库客户端，无法独占恢复。"] } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, text: requestFailureText(error, text) };
  }
  const result = connectionFailure(error);
  return { ...result, text: result.status === 400 ? requestFailureText(error, "请求不符合当前备份契约，请核对文件与参数。") : result.text };
}
