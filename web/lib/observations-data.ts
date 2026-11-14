import type { createObservationsClient } from "@sub-pool/contracts/client";
import type { Observation, ObservationDetail, ObservationEstimateLink, ObservationRecommendationLink, ObservationUsageEvidence, ObservationEvent, ObservationManagement } from "@sub-pool/contracts/observations";
import { observationUsageMatches } from "@sub-pool/contracts/observations";
import { usageNanos } from "@sub-pool/contracts/usage";

type Client = ReturnType<typeof createObservationsClient>;
export type ObservationQuery = Omit<Parameters<Client["list"]>[0], "after" | "limit">;
export type ObservationRow = Observation & { usage_evidence?: ObservationUsageEvidence };
export type ObservationSelection = { detail: ObservationDetail; management: ObservationManagement; estimate_link: ObservationEstimateLink | null; recommendation_link: ObservationRecommendationLink | null; usage_evidence: ObservationUsageEvidence | null };
export type ObservationDecision = { kind: "exclude" | "restore" | "unanchor"; account_id: string; observation_id: string; revision: string; reason: string; actor_id: string };
export class ObservationReadError extends Error {}

export async function readObservationPages(query: ObservationQuery, read: (after: string) => Promise<Awaited<ReturnType<Client["list"]>> & { usage_evidence?: ObservationUsageEvidence[] }>, withUsage = false) {
  const records: ObservationRow[] = [], revisions = new Map<string, string>();
  let after = "0";
  // shortcut: Read at most 2,000 records per filter; add existing-UI pagination when separately authorized.
  for (let pageNumber = 0; pageNumber < 20; pageNumber++) {
    const page = await read(after);
    if (page.time_basis !== "received_at" || usageNanos(page.query_start_at) !== usageNanos(query.start_at) || usageNanos(page.query_end_at) !== usageNanos(query.end_at)) throw new ObservationReadError("观测查询边界发生变化，请重新读取。");
    if (withUsage ? page.usage_evidence?.length !== page.records.length : page.usage_evidence !== undefined) throw new ObservationReadError("观测费用依据未完整返回，未展示部分结果。");
    for (const [index, record] of page.records.entries()) {
      if (BigInt(record.id) <= BigInt(after)
        || query.account_id && record.account_id !== query.account_id || query.source_id && record.snapshot.source_id !== query.source_id
        || query.source_mode !== "all" && record.snapshot.source_mode !== query.source_mode
        || query.validity === "included" && record.excluded || query.validity === "excluded" && !record.excluded
        || query.validity === "manual_start" && !record.manual_start) throw new ObservationReadError("观测筛选或分页不一致，未展示部分结果。");
      const revision = revisions.get(record.account_id);
      if (revision !== undefined && revision !== record.management_revision) throw new ObservationReadError("观测维护状态在读取期间变化，请重新读取。");
      revisions.set(record.account_id, record.management_revision);
      after = record.id;
      const evidence = page.usage_evidence?.[index];
      if (withUsage && (!evidence || !observationUsageMatches(record, evidence))) throw new ObservationReadError("费用依据与观测或原生窗口不一致，未展示部分结果。");
      records.push(evidence ? { ...record, usage_evidence: evidence } : record);
    }
    if (page.next_after === null) return records;
    if (!page.records.length || page.next_after !== after) throw new ObservationReadError("观测分页未完整返回，未展示部分结果。");
  }
  throw new ObservationReadError("记录超过本页一次读取的 2,000 条上限；请按账号、状态或来源缩小范围。未展示部分结果。");
}

export async function readObservationSelection(id: string, account: string, read: () => Promise<ObservationDetail>, state: () => Promise<ObservationManagement>, estimate: (() => Promise<ObservationEstimateLink>) | null, recommendation: (() => Promise<ObservationRecommendationLink>) | null = null, usage: (() => Promise<ObservationUsageEvidence>) | null = null): Promise<ObservationSelection> {
  const [detail, management, estimate_link, recommendation_link, usage_evidence] = await Promise.all([read(), state(), estimate?.() ?? null, recommendation?.() ?? null, usage?.() ?? null]);
  if (detail.observation.id !== id || detail.observation.account_id !== account || management.account_id !== account
    || detail.observation.management_revision !== management.revision
    || detail.observation.manual_start !== (management.manual_start?.start.id === id)) throw new ObservationReadError("观测或维护版本在读取期间变化；请关闭详情后重新打开。");
  if (estimate_link && (estimate_link.observation_id !== id || estimate_link.account_id !== account)) throw new ObservationReadError("估算关联与所选观测不一致；请关闭详情后重新打开。");
  if (recommendation_link && (recommendation_link.observation_id !== id || recommendation_link.account_id !== account)) throw new ObservationReadError("余额建议关联与所选观测不一致；请关闭详情后重新打开。");
  if (usage_evidence && !observationUsageMatches(detail.observation, usage_evidence)) throw new ObservationReadError("用量依据与所选观测或原生窗口不一致；请关闭详情后重新打开。");
  return { detail, management, estimate_link, recommendation_link, usage_evidence };
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
