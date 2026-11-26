import { ORPCError } from "@orpc/client";
import { authErrorData } from "@sub-pool/contracts/auth";
import { backupFile, backupFileFormat, backupStreamFormat, backupMaxFileBytes, legacyBackupContent, restorePlan, type BackupArchive, type BackupFile, type RestorePlan, type RestoreChecks } from "@sub-pool/contracts/backups";
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
  if (!file.size || file.size > backupMaxFileBytes) throw new BackupReadError("选备份文件（≤12 MiB）。");
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (bytes.length !== file.size || bytes.length > backupMaxFileBytes) throw new BackupReadError("文件不可读。");
  let content: string;
  try { content = new TextDecoder("utf-8", { fatal: true }).decode(bytes); } catch { throw new BackupReadError("备份无效。"); }
  if (!legacyBackupContent.safeParse(content).success) throw new BackupReadError("备份格式有误。");
  return { name: file.name, content, sha256: await backupHash(content) };
}
export async function checkBackupFile(value: BackupFile, expected?: BackupArchive) {
  const file = backupFile.parse(value);
  if (expected && JSON.stringify(file.archive) !== JSON.stringify(expected)) throw new BackupReadError("文件不符。");
  if (!file.content || await backupHash(file.content) !== file.archive.file_sha256) throw new BackupReadError("SHA-256 不符。");
  return { ...file, content: file.content };
}

export async function inspectBackupUpload(file: File) {
  const marker = "SUB-POOL-PG-BACKUP/";
  const header = await file.slice(0, marker.length + 2).text();
  if (header === marker + "2\n") return { file, format: backupStreamFormat };
  if (header === marker + "1\n") {
    const { sha256 } = await readBackupUpload(file);
    return { file, format: backupFileFormat, sha256 };
  }
  throw new BackupReadError(header.startsWith(marker) ? "版本不支持。" : "选择 Sub-Pool 备份。");
}

export async function uploadBackup(file: File, id: string, signal: AbortSignal) {
  const response = await fetch("/api/v1/backups/preflight-file", {
    method: "POST", headers: { "Content-Type": "application/octet-stream", "X-Backup-Request-ID": id },
    body: file, signal, credentials: "same-origin", cache: "no-store", redirect: "error",
  });
  const value: unknown = await response.json();
  if (response.ok) return restorePlan.parse(value);
  if (value && typeof value === "object" && "defined" in value && value.defined === true && "code" in value
    && typeof value.code === "string" && "status" in value && value.status === response.status
    && "message" in value && typeof value.message === "string" && "data" in value && authErrorData.safeParse(value.data).success) {
    throw new ORPCError(value.code, { defined: true, status: response.status, message: value.message, data: value.data });
  }
  throw new BackupReadError("上传待核对。");
}
export async function readBackups(client: Pick<Client, "list">, signal?: AbortSignal) {
  const archives: BackupArchive[] = [], ids = new Set<string>(); let after = "", previous: bigint | null = null, keyAvailable: boolean | null = null, bytes = 0;
  for (;;) {
    const page = await client.list({ after, limit: 100 }, { signal });
    if (keyAvailable !== null && keyAvailable !== page.key_available || after && !page.archives.length) throw new BackupReadError("分页错误。");
    keyAvailable = page.key_available;
    for (const archive of page.archives) {
      const at = usageNanos(archive.created_at);
      if (at === null || ids.has(archive.id) || previous !== null && at > previous) throw new BackupReadError("顺序异常。");
      ids.add(archive.id); previous = at; bytes += new TextEncoder().encode(JSON.stringify(archive)).length;
      // shortcut: the supplied table has no pagination controls; keep a whole bounded catalog until the UI supports paging.
      if (archives.length === 2000 || bytes > 32 * 1024 * 1024) throw new BackupReadError("记录超限。");
      archives.push(archive);
    }
    if (page.next_after === null) return { archives, keyAvailable };
    if (!page.archives.length || page.next_after !== page.archives.at(-1)?.id || page.next_after === after) throw new BackupReadError("无法续页。");
    after = page.next_after;
  }
}
export function checkRestoreReceipt(expected: RestorePlan, received: RestorePlan) {
  for (const key of ["id", "created_at", "expires_at", "file_sha256", "source_bytes", "target_basis", "before_archive_id", "cleared_tables", "source"] as const) {
    if (JSON.stringify(expected[key]) !== JSON.stringify(received[key])) throw new BackupReadError("恢复待核对。");
  }
}
export function checkRestoreChecks(plan: RestorePlan, checks: RestoreChecks) {
  if (plan.state !== "applied" || checks.plan_id !== plan.id || checks.applied_at !== plan.finished_at || JSON.stringify(checks.source) !== JSON.stringify(plan.source)) throw new BackupReadError("结果不符。");
}
export function restoreCanApply(plan: RestorePlan | null, beforeDownloaded: string | null, now = Date.now()) {
  return plan !== null && plan.state === "ready" && plan.blockers.length === 0 && plan.before_archive_id !== null
    && (plan.source_bytes ? plan.before_downloaded === true : beforeDownloaded === plan.before_archive_id)
    && Date.parse(plan.expires_at) > now;
}
export const backupBlockers: Record<RestorePlan["blockers"][number], string> = {
  COLLECTION_ACTIVE: "当前采集中", MAIL_ACTIVE: "有待发邮件", MAINTENANCE_ACTIVE: "有待办维护",
  REMOTE_WRITE_UNCONFIRMED: "写入待核对", BACKUP_COLLECTION_ACTIVE: "备份含采集",
  BACKUP_MAIL_ACTIVE: "含待发邮件", BACKUP_MAINTENANCE_ACTIVE: "含待办维护",
  BACKUP_REMOTE_WRITE_UNCONFIRMED: "含未决写入", BACKUP_SOURCE_BLOCKED: "将阻断访问",
  WEEKLY_WALLET_HISTORY_CHANGED: "缺额度历史",
};
export const restoreReasons: Record<RestorePlan["reason"], string> = {
  READY: "请下载副本", RESTORE_BLOCKED: "预检被阻止", APPLYING: "恢复中",
  APPLIED: "已恢复，请登录", PLAN_EXPIRED: "预检过期", BASIS_CHANGED: "数据变化，需预检",
  BUSY_STATE_CHANGED: "任务变化，需预检", WORKER_INTERRUPTED: "恢复中断",
  RESTORE_FAILED: "恢复待核对", AUTHORIZATION_EXPIRED: "授权已过期",
  TARGET_REPLACED: "目标已变",
};
export function backupFailure(error: unknown) {
  if (error instanceof BackupReadError) return { status: 0, text: error.message };
  const messages = { BACKUP_INVALID: [400, "查文件和密钥。"], BACKUP_SCHEMA_INCOMPATIBLE: [409, "结构不兼容。"],
    BACKUP_LIMIT_EXCEEDED: [413, "备份超限。"], RESTORE_BUSY: [409, "数据库忙。"] } as const;
  if (error instanceof ORPCError && error.defined && Object.hasOwn(messages, error.code) && authErrorData.safeParse(error.data).success) {
    const [status, text] = messages[error.code as keyof typeof messages];
    if (status === error.status) return { status, text: requestFailureText(error, text) };
  }
  const result = connectionFailure(error);
  return { ...result, text: result.status === 400 ? requestFailureText(error, "查文件参数。") : result.text };
}
