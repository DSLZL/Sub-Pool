import type { ManagedAccount, AccountModelConfiguration } from "@sub-pool/contracts/accounts";
import type { AccountObservation, CollectionJob, CollectionRun, QuotaWindow } from "@sub-pool/contracts/monitoring";

export type AccountRow = { account: ManagedAccount; observation: AccountObservation | null; error: string | null };
type RunPage = { runs: CollectionRun[]; next_after: string | null };
type JobPage = { run: CollectionRun; jobs: CollectionJob[]; next_after: string | null };

export async function readRunHistory(read: (after: string) => Promise<RunPage>) {
  const runs: CollectionRun[] = [];
  let after = "0";
  for (;;) {
    const page = await read(after);
    for (const run of page.runs) {
      if (BigInt(run.id) <= BigInt(after)) throw new Error("Invalid run continuation");
      after = run.id; runs.push(run);
    }
    if (page.next_after === null) return runs.reverse();
    if (!page.runs.length || page.next_after !== after) throw new Error("Incomplete run history");
  }
}

export async function readRunJobs(read: (after: string) => Promise<JobPage>, id: string) {
  const jobs: CollectionJob[] = [], accounts = new Set<string>();
  let after = "0";
  for (;;) {
    const page = await read(after);
    if (page.run.id !== id) throw new Error("Changed run identity");
    for (const job of page.jobs) {
      if (job.run_id !== id || BigInt(job.id) <= BigInt(after) || accounts.has(job.account_id)) throw new Error("Invalid job binding");
      after = job.id; accounts.add(job.account_id); jobs.push(job);
    }
    if (page.next_after === null) {
      for (const state of Object.keys(page.run.counts) as CollectionJob["state"][]) {
        if (BigInt(jobs.filter(job => job.state === state).length) !== BigInt(page.run.counts[state])) throw new Error("Run changed during paginated read");
      }
      return { run: page.run, jobs };
    }
    if (!page.jobs.length || page.next_after !== after) throw new Error("Incomplete job coverage");
  }
}

export function bindObservation(account: ManagedAccount, observation: AccountObservation) {
  if (observation.account_id !== account.id || observation.requested_mode !== account.source_mode
    || observation.snapshot && (observation.snapshot.source_id !== account.source_id || BigInt(observation.snapshot.account_revision) > BigInt(account.revision))
    || observation.latest_attempt && (observation.latest_attempt.source_id !== account.source_id || BigInt(observation.latest_attempt.account_revision) > BigInt(account.revision))) {
    throw new Error("Observation binding changed");
  }
  return observation;
}

export function accountReadInput(account: ManagedAccount, connectionRevision: string) {
  return { id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: connectionRevision };
}

export function quotaWindow(row: AccountRow, kind: QuotaWindow["kind"]) {
  return row.observation?.snapshot?.windows.find(window => window.kind === kind) ?? null;
}

export function windowTitle(window: QuotaWindow) {
  return (window.kind === "short" ? "短周期窗口" : "7 天窗口")
    + (window.duration_seconds === null ? " · 时长未知" : ` · ${BigInt(window.duration_seconds) % 3600n === 0n ? `${BigInt(window.duration_seconds) / 3600n} 小时` : `${window.duration_seconds} 秒`}`);
}

export function compactTime(value: string | null | undefined, timezone: string | null) {
  if (!value) return "暂无记录";
  try { return new Intl.DateTimeFormat("zh-CN", { timeZone: timezone ?? "UTC", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value)); }
  catch { return value.slice(5, 16).replace("T", " ") + " UTC"; }
}

export function modelRows(value: AccountModelConfiguration) {
  const keys = new Set([...value.models.map(model => model.id), ...(value.mapping ?? []).map(item => item.from)]);
  const mapping = new Map((value.mapping ?? []).map(item => [item.from, item.to]));
  return [...keys].map(requested => ({ requested, mapped: mapping.get(requested) ?? null }));
}

export const accountStates: Record<ManagedAccount["collection_state"], string> = {
  paused: "监控暂停", source_changed: "历史来源已隔离", connection_unavailable: "连接不可用", connection_changed: "连接变更待核对",
  read_unavailable: "账号读取不可用", unverified: "身份未确认", shadow: "影子资源", unsupported: "账号类型不支持", conflict: "资源身份冲突", awaiting_collector: "等待实际采集",
};
const jobReasons: Record<string, string> = {
  VERIFIED_PASSIVE_SNAPSHOT: "已保存被动窗口", SNAPSHOT_MISSING: "未取得可确认窗口，未用零补齐", PAUSED: "本地监控已暂停，本批次跳过",
  ACTIVE_QUERY_SIDE_EFFECT: "主动查询可能触发上游重置，未执行", COOLDOWN: "仍在最小采集间隔内",
  RATE_LIMITED: "上游限流，未绕过", SOURCE_CHANGED: "连接来源已改变", CONNECTION_CHANGED: "连接配置已改变",
  ACCOUNT_CHANGED: "账号配置已改变", POLICY_CHANGED: "监控配置已改变", ALREADY_RUNNING: "已有待执行采集", INTERRUPTED: "后台工作中断，保留已保存事实",
  FORBIDDEN: "上游凭据无此读取权限", UNAUTHORIZED: "上游管理凭据未通过认证", TIMEOUT: "上游读取超时", NETWORK_ERROR: "上游网络读取失败",
  IDENTITY_CHANGED: "资源身份已改变", IDENTITY_UNVERIFIED: "资源身份尚未确认", NO_QUOTA_WINDOWS: "未取得可确认窗口",
  ACTOR_UNAVAILABLE: "发起会话或授权已失效，未继续执行", RESOURCE_IDENTITY_CHANGED: "上游资源身份已改变，历史记录保持隔离",
  CONNECTION_UNAVAILABLE: "连接凭据不可用", QUEUED: "等待后台执行",
};
export function jobNote(job: CollectionJob) { return jobReasons[job.reason] ?? job.reason; }
