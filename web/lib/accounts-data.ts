import { glossary } from "@/lib/glossary";
import type { ManagedAccount, AccountModelConfiguration, ManagedAccountView } from "@sub-pool/contracts/accounts";
import type { AccountObservation, CollectionJob, CollectionRun, QuotaWindow } from "@sub-pool/contracts/monitoring";
import { displayTime } from "./connection-data";

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

export function bindAccountRefresh(account: ManagedAccount, connectionRevision: string, value: ManagedAccountView) {
  const next = value.account;
  if (value.connection_revision !== connectionRevision || !next.source_current
    || BigInt(next.revision) !== BigInt(account.revision) + 1n
    || (["id", "source_id", "upstream_id", "name", "monitoring", "source_mode", "created_at", "saved_at"] as const).some(key => next[key] !== account[key])
    || (next.read.state === "available" ? next.observed_connection_revision !== connectionRevision
      : next.observed_at !== account.observed_at || next.observed_connection_revision !== account.observed_connection_revision || JSON.stringify(next.snapshot) !== JSON.stringify(account.snapshot))) {
    throw new Error("Changed account status binding");
  }
  return next;
}

export function quotaWindow(row: AccountRow, kind: QuotaWindow["kind"]) {
  return row.observation?.snapshot?.windows.find(window => window.kind === kind) ?? null;
}

export function quotaSource(row: AccountRow) {
  if (row.observation?.snapshot?.simulation) return "模拟查询";
  if (row.observation?.snapshot?.source_mode === "passive") return "被动快照";
  return row.account.source_mode === "active" ? "待观察" : "被动快照";
}

export function windowTitle(window: QuotaWindow) {
  return (window.kind === "short" ? "短窗口" : "7 天窗口")
    + (window.duration_seconds === null ? " · 时长未知" : ` · ${BigInt(window.duration_seconds) % 3600n === 0n ? `${BigInt(window.duration_seconds) / 3600n} 小时` : `${window.duration_seconds} 秒`}`);
}

export function compactTime(value: string | null | undefined, timezone: string | null | undefined) {
  return displayTime(value, timezone, "short");
}

export function modelRows(value: AccountModelConfiguration) {
  const keys = new Set([...value.models.map(model => model.id), ...(value.mapping ?? []).map(item => item.from)]);
  const mapping = new Map((value.mapping ?? []).map(item => [item.from, item.to]));
  return [...keys].map(requested => ({ requested, mapped: mapping.get(requested) ?? null }));
}

export const accountStates: Record<ManagedAccount["collection_state"], string> = {
  paused: "监控暂停", source_changed: "旧来源", connection_unavailable: "连接不可用", connection_changed: "连接待核对",
  read_unavailable: "账号不可读", unverified: "身份未确认", shadow: "影子资源", unsupported: "类型不支持", conflict: "身份冲突", awaiting_collector: "待采集",
};
const jobReasons: Record<string, string> = {
  SIMULATED_READ_ONLY_SNAPSHOT: "旧模拟窗口", SIMULATION_PROTOCOL_UNVERIFIED: "模拟未保存",
  VERIFIED_PASSIVE_SNAPSHOT: "已存快照", SNAPSHOT_MISSING: "窗口未知", PAUSED: "监控暂停",
  ACTIVE_QUERY_SIDE_EFFECT: `主动查询可能触发${glossary.sub2api} 重置，未执行`, COOLDOWN: "等待间隔",
  RATE_LIMITED: `${glossary.sub2api} 限流，未绕过`, SOURCE_CHANGED: "来源已变", CONNECTION_CHANGED: "连接已变",
  ACCOUNT_CHANGED: "账号已变", POLICY_CHANGED: "监控已变", ALREADY_RUNNING: "采集中", INTERRUPTED: "任务中断",
  WORKER_INTERRUPTED: "任务中断",
  FORBIDDEN: `${glossary.sub2api}无读取权`, UNAUTHORIZED: `${glossary.sub2api} 凭据无效`, TIMEOUT: `${glossary.sub2api} 读取超时`, NETWORK_ERROR: `${glossary.sub2api}网络异常`,
  IDENTITY_CHANGED: "身份已变", IDENTITY_UNVERIFIED: "身份待核对", NO_QUOTA_WINDOWS: "无可信窗口",
  ACTOR_UNAVAILABLE: "授权失效", RESOURCE_IDENTITY_CHANGED: `${glossary.sub2api}资源已变`,
  CONNECTION_UNAVAILABLE: "凭据不可用", QUEUED: "已排队",
};
export function jobNote(job: CollectionJob) { return jobReasons[job.reason] ?? job.reason; }
