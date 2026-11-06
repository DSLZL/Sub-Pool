import type { createObservationsClient } from "@sub-pool/contracts/client";
import type { Observation, ObservationDetail, ObservationEvent, ObservationManagement } from "@sub-pool/contracts/observations";
import { usageNanos } from "@sub-pool/contracts/usage";

type Client = ReturnType<typeof createObservationsClient>;
export type ObservationQuery = Omit<Parameters<Client["list"]>[0], "after" | "limit">;
export type ObservationSelection = { detail: ObservationDetail; management: ObservationManagement };
export type ObservationDecision = { kind: "exclude" | "restore" | "unanchor"; account_id: string; observation_id: string; revision: string; reason: string; actor_id: string };
export class ObservationReadError extends Error {}

export async function readObservationPages(query: ObservationQuery, read: (after: string) => ReturnType<Client["list"]>) {
  const records: Observation[] = [], revisions = new Map<string, string>();
  let after = "0";
  // shortcut: Read at most 2,000 records per filter; add existing-UI pagination when separately authorized.
  for (let pageNumber = 0; pageNumber < 20; pageNumber++) {
    const page = await read(after);
    if (page.time_basis !== "received_at" || usageNanos(page.query_start_at) !== usageNanos(query.start_at) || usageNanos(page.query_end_at) !== usageNanos(query.end_at)) throw new ObservationReadError("观测查询边界发生变化，请重新读取。");
    for (const record of page.records) {
      if (BigInt(record.id) <= BigInt(after)
        || query.account_id && record.account_id !== query.account_id || query.source_id && record.snapshot.source_id !== query.source_id
        || query.source_mode !== "all" && record.snapshot.source_mode !== query.source_mode
        || query.validity === "included" && record.excluded || query.validity === "excluded" && !record.excluded
        || query.validity === "manual_start" && !record.manual_start) throw new ObservationReadError("观测筛选或分页不一致，未展示部分结果。");
      const revision = revisions.get(record.account_id);
      if (revision !== undefined && revision !== record.management_revision) throw new ObservationReadError("观测维护状态在读取期间变化，请重新读取。");
      revisions.set(record.account_id, record.management_revision);
      after = record.id;
      records.push(record);
    }
    if (page.next_after === null) return records;
    if (!page.records.length || page.next_after !== after) throw new ObservationReadError("观测分页未完整返回，未展示部分结果。");
  }
  throw new ObservationReadError("记录超过本页一次读取的 2,000 条上限；请按账号、状态或来源缩小范围。未展示部分结果。");
}

export async function readObservationSelection(id: string, account: string, read: () => Promise<ObservationDetail>, state: () => Promise<ObservationManagement>): Promise<ObservationSelection> {
  const [detail, management] = await Promise.all([read(), state()]);
  if (detail.observation.id !== id || detail.observation.account_id !== account || management.account_id !== account
    || detail.observation.management_revision !== management.revision
    || detail.observation.manual_start !== (management.manual_start?.start.id === id)) throw new ObservationReadError("观测或维护版本在读取期间变化；请关闭详情后重新打开。");
  return { detail, management };
}

export async function readObservationEvents(account: string, read: (after: string) => ReturnType<Client["events"]>) {
  const events: ObservationEvent[] = [];
  let after = "0", revision = 1n;
  for (let pageNumber = 0; pageNumber < 20; pageNumber++) {
    const page = await read(after);
    if (page.account_id !== account) throw new ObservationReadError("维护历史账号不一致，无法核对结果。");
    for (const event of page.events) {
      if (event.account_id !== account || BigInt(event.id) <= BigInt(after) || BigInt(event.revision) !== revision + 1n) throw new ObservationReadError("维护历史不连续，无法核对结果。");
      after = event.id; revision = BigInt(event.revision); events.push(event);
    }
    if (page.next_after === null) return events;
    if (!page.events.length || page.next_after !== after) throw new ObservationReadError("维护历史未完整返回，无法核对结果。");
  }
  throw new ObservationReadError("维护历史超过本页核对上限；结果继续保持待核对，请勿重复提交。");
}

export function observationDecisionMatches(event: ObservationEvent, decision: ObservationDecision) {
  return event.account_id === decision.account_id && BigInt(event.revision) === BigInt(decision.revision) + 1n
    && event.actor_id === decision.actor_id && event.reason === decision.reason
    && (decision.kind === "unanchor" ? event.action === "revoke" : event.action === decision.kind && event.observation_id === decision.observation_id);
}
