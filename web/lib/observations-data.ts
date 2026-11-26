import type { createObservationsClient } from "@sub-pool/contracts/client";
import type { Observation, ObservationDetail, ObservationEstimateLink, ObservationRecommendationLink, ObservationUsageEvidence, ObservationEvent, ObservationManagement } from "@sub-pool/contracts/observations";
import { observationUsageMatches } from "@sub-pool/contracts/observations";
import { usageNanos } from "@sub-pool/contracts/usage";

type Client = ReturnType<typeof createObservationsClient>;
export type ObservationQuery = Omit<Parameters<Client["list"]>[0], "after" | "limit">;
export type ObservationRow = Observation & { usage_evidence?: ObservationUsageEvidence };
export type ObservationSelection = { detail: ObservationDetail; management: ObservationManagement; estimate_link: ObservationEstimateLink | null; recommendation_link: ObservationRecommendationLink | null; usage_evidence: ObservationUsageEvidence | null; events: ObservationEvent[] | null };
export type ObservationDecision = { account_id: string; observation_id: string; revision: string; reason: string; actor_id: string } & ({ kind: "exclude" | "restore" | "unanchor" } | { kind: "anchor"; end_observation_id: string; window_kind: "short" | "seven_day" });
export class ObservationReadError extends Error {}

export async function readObservationPages(query: ObservationQuery, read: (after: string) => Promise<Awaited<ReturnType<Client["list"]>> & { usage_evidence?: ObservationUsageEvidence[] }>, withUsage = false) {
  const records: ObservationRow[] = [], revisions = new Map<string, string>();
  let after = "0";
  // shortcut: Read at most 2,000 records per filter; add existing-UI pagination when separately authorized.
  for (let pageNumber = 0; pageNumber < 20; pageNumber++) {
    const page = await read(after);
    if (page.time_basis !== "received_at" || usageNanos(page.query_start_at) !== usageNanos(query.start_at) || usageNanos(page.query_end_at) !== usageNanos(query.end_at)) throw new ObservationReadError("范围变更。");
    if (withUsage ? page.usage_evidence?.length !== page.records.length : page.usage_evidence !== undefined) throw new ObservationReadError("缺少费用。");
    for (const [index, record] of page.records.entries()) {
      if (BigInt(record.id) <= BigInt(after)
        || query.account_id && record.account_id !== query.account_id || query.source_id && record.snapshot.source_id !== query.source_id
        || query.source_mode !== "all" && record.snapshot.source_mode !== query.source_mode
        || query.validity === "included" && record.excluded || query.validity === "excluded" && !record.excluded
        || query.validity === "manual_start" && !record.manual_start) throw new ObservationReadError("查询范围不符。");
      const revision = revisions.get(record.account_id);
      if (revision !== undefined && revision !== record.management_revision) throw new ObservationReadError("状态已变。");
      revisions.set(record.account_id, record.management_revision);
      after = record.id;
      const evidence = page.usage_evidence?.[index];
      if (withUsage && (!evidence || !observationUsageMatches(record, evidence))) throw new ObservationReadError("费用不符。");
      records.push(evidence ? { ...record, usage_evidence: evidence } : record);
    }
    if (page.next_after === null) return records;
    if (!page.records.length || page.next_after !== after) throw new ObservationReadError("分页不齐。");
  }
  throw new ObservationReadError("超过 2,000 条。");
}

export async function readObservationSelection(id: string, account: string, read: () => Promise<ObservationDetail>, state: () => Promise<ObservationManagement>, estimate: (() => Promise<ObservationEstimateLink>) | null, recommendation: (() => Promise<ObservationRecommendationLink>) | null = null, usage: (() => Promise<ObservationUsageEvidence>) | null = null, history: (() => Promise<ObservationEvent[]>) | null = null): Promise<ObservationSelection> {
  const [detail, management, estimate_link, recommendation_link, usage_evidence, events] = await Promise.all([read(), state(), estimate?.() ?? null, recommendation?.() ?? null, usage?.() ?? null, history?.() ?? null]);
  if (detail.observation.id !== id || detail.observation.account_id !== account || management.account_id !== account
    || detail.observation.management_revision !== management.revision
    || detail.observation.manual_start !== (management.manual_start?.start.id === id)) throw new ObservationReadError("观测已变，请重开。");
  if (estimate_link && (estimate_link.observation_id !== id || estimate_link.account_id !== account)) throw new ObservationReadError("估算不符。");
  if (recommendation_link && (recommendation_link.observation_id !== id || recommendation_link.account_id !== account)) throw new ObservationReadError("建议不符。");
  if (usage_evidence && !observationUsageMatches(detail.observation, usage_evidence)) throw new ObservationReadError("窗口不符。");
  if (events && ((events.at(-1)?.revision ?? "1") !== management.revision || events.some(event => event.account_id !== account))) throw new ObservationReadError("历史变更。");
  return { detail, management, estimate_link, recommendation_link, usage_evidence, events };
}

export function observationRangeEnds(start: Observation, rows: Observation[]) {
  const from = usageNanos(start.snapshot.source_at ?? "");
  return rows.filter(end => {
    const to = usageNanos(end.snapshot.source_at ?? "");
    return from !== null && to !== null && to > from && end.id !== start.id && end.account_id === start.account_id && !end.excluded;
  });
}

export async function readObservationEvents(account: string, read: (after: string) => ReturnType<Client["events"]>) {
  const events: ObservationEvent[] = [];
  let after = "0", revision = 1n;
  for (let pageNumber = 0; pageNumber < 20; pageNumber++) {
    const page = await read(after);
    if (page.account_id !== account) throw new ObservationReadError("账号不符。");
    for (const event of page.events) {
      if (event.account_id !== account || BigInt(event.id) <= BigInt(after) || BigInt(event.revision) !== revision + 1n) throw new ObservationReadError("历史不连续。");
      after = event.id; revision = BigInt(event.revision); events.push(event);
    }
    if (page.next_after === null) return events;
    if (!page.events.length || page.next_after !== after) throw new ObservationReadError("历史不齐。");
  }
  throw new ObservationReadError("历史超限。");
}

export function observationDecisionMatches(event: ObservationEvent, decision: ObservationDecision) {
  return event.account_id === decision.account_id && BigInt(event.revision) === BigInt(decision.revision) + 1n
    && event.actor_id === decision.actor_id && event.reason === decision.reason
    && (decision.kind === "anchor" ? event.action === "start" && event.start_observation_id === decision.observation_id && event.end_observation_id === decision.end_observation_id && event.window_kind === decision.window_kind
      : decision.kind === "unanchor" ? event.action === "revoke" : event.action === decision.kind && event.observation_id === decision.observation_id);
}
