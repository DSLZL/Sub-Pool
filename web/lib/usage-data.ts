import type { AttributedUsage, UsageHistory } from "@sub-pool/contracts/participants";
import { usageCalendar } from "@sub-pool/contracts/participants";
import { usageNanos } from "@sub-pool/contracts/usage";
import { readCaptureSelections } from "@/lib/pools-data";

type CaptureReader = Parameters<typeof readCaptureSelections>[1];
export type UsageWindow = { start_at: string; end_at: string };
export type UsageData = { view: UsageHistory | null; records: UsageHistory["records"]; byParticipant: Record<string, UsageHistory>; missingAccounts: string[] };
export class UsageReadError extends Error {}

export function usageWindow(days: 7 | 14, now = new Date(), timezone = "UTC"): UsageWindow {
  const calendar = usageCalendar(timezone), start = new Date(`${calendar.day(now)}T00:00:00Z`);
  start.setUTCDate(start.getUTCDate() - days + 1);
  return { start_at: calendar.boundary(start.toISOString().slice(0, 10)), end_at: now.toISOString() };
}

export function millionTokens(value: string | null | undefined) {
  if (value == null) return null;
  if (!/^\d+$/.test(value)) throw new Error("Invalid token count");
  const padded = value.padStart(7, "0"), fraction = padded.slice(-6).replace(/0+$/, "");
  return `${padded.slice(0, -6)}${fraction ? `.${fraction}` : ""}`;
}

export function usageTrend(window: UsageWindow, buckets: AttributedUsage["buckets"], timezone = "UTC") {
  const labels: string[] = [], values: (string | null)[] = [];
  const calendar = usageCalendar(timezone), day = new Date(`${calendar.day(window.start_at)}T00:00:00Z`), last = calendar.day(Date.parse(window.end_at) - 1);
  for (; day.toISOString().slice(0, 10) <= last; day.setUTCDate(day.getUTCDate() + 1)) {
    const date = day.toISOString().slice(0, 10);
    if (calendar.day(calendar.boundary(date)) !== date) continue;
    labels.push(date);
    values.push(buckets.find(bucket => calendar.day(bucket.start_at) === date)?.metrics.actual_cost ?? null);
  }
  return { labels, values };
}

const cursorAfter = (next: string, previous: string) => {
  const [capture, fact] = next.split(".").map(BigInt), [oldCapture, oldFact] = previous === "0" ? [0n, 0n] : previous.split(".").map(BigInt);
  return capture > oldCapture || capture === oldCapture && fact > oldFact;
};
const metadata = ({ records: _records, next_after: _next, ...view }: UsageHistory) => JSON.stringify(view);

export async function readUsageData(
  accounts: { id: string }[], captures: CaptureReader,
  read: (ids: string[], participant: string, after: string) => Promise<UsageHistory>, window: UsageWindow, authorityRevision: string,
  settings: { timezone: string; revision: string },
): Promise<UsageData> {
  const { selections, missingAccounts } = await readCaptureSelections(accounts, captures);
  if (!selections.length) return { view: null, records: [], byParticipant: {}, missingAccounts };
  const ids = selections.map(item => item.id), records: UsageHistory["records"] = [], byParticipant: UsageData["byParticipant"] = {};
  const captureTimes: bigint[] = [];
  const starts: bigint[] = [], ends: bigint[] = [];
  for (const item of selections) {
    const at = usageNanos(item.captured_at), start = usageNanos(item.start_at), end = usageNanos(item.end_at);
    if (at === null || start === null || end === null || start >= end) throw new Error("Invalid capture observation time or interval");
    captureTimes.push(at); starts.push(start); ends.push(end);
  }
  const oldest = captureTimes.reduce((a, b) => a < b ? a : b), newest = captureTimes.reduce((a, b) => a > b ? a : b);
  const start = starts.reduce((a, b) => a < b ? a : b), end = ends.reduce((a, b) => a > b ? a : b);
  const valid = (view: UsageHistory, participant: string) => {
    if (!view.history || view.history.timezone !== settings.timezone || view.history.settings_revision !== settings.revision
      || view.history.authority_revision !== authorityRevision || view.history.selections.length !== selections.length
      || view.history.selections.some(item => !selections.some(selected => selected.id === item.id && selected.account_id === item.account_id
        && usageNanos(selected.start_at) === usageNanos(item.start_at) && usageNanos(selected.end_at) === usageNanos(item.end_at) && usageNanos(selected.captured_at) === usageNanos(item.captured_at)))
      || usageNanos(view.start_at) !== start || usageNanos(view.end_at) !== end
      || usageNanos(view.query_start_at ?? "") !== usageNanos(window.start_at) || usageNanos(view.query_end_at ?? "") !== usageNanos(window.end_at)
      || usageNanos(view.oldest_capture_at) !== oldest || usageNanos(view.newest_capture_at) !== newest
      || view.grain !== "day" || view.groups.some(group => !selections.some(item => item.account_id === group.account_id) || participant && group.participant_id !== participant)
      || view.records.some(record => !selections.some(item => item.id === record.capture_id && item.account_id === record.account_id) || participant && record.attribution.participant_id !== participant)) throw new UsageReadError("Usage selection changed");
  };
  const view = await read(ids, "", "0");
  valid(view, "");
  let page = view, after = "0";
  for (;;) {
    valid(page, "");
    if (metadata(page) !== metadata(view)) throw new Error("Usage coverage changed between pages");
    let previous = after;
    for (const record of page.records) {
      const next = `${record.capture_id}.${record.fact.id}`;
      if (!cursorAfter(next, previous)) throw new Error("Repeated usage record");
      previous = next; records.push(record);
    }
    if (BigInt(records.length) > BigInt(view.summary.request_count)) throw new Error("Usage count changed");
    if (page.next_after === null) break;
    if (page.next_after !== previous || !cursorAfter(page.next_after, after)) throw new Error("Invalid usage continuation");
    after = page.next_after;
    page = await read(ids, "", after);
  }
  if (BigInt(records.length) !== BigInt(view.summary.request_count)) throw new Error("Incomplete usage records");
  const participants = [...new Set(view.groups.flatMap(group => group.participant_id ? [group.participant_id] : []))];
  for (let offset = 0; offset < participants.length; offset += 4) {
    const batch = participants.slice(offset, offset + 4), results = await Promise.all(batch.map(id => read(ids, id, "0")));
    for (let index = 0; index < batch.length; index++) {
      valid(results[index], batch[index]);
      if (JSON.stringify(results[index].history) !== JSON.stringify(view.history)
        || JSON.stringify(results[index].groups) !== JSON.stringify(view.groups.filter(group => group.participant_id === batch[index]))) throw new Error("Participant coverage changed");
      byParticipant[batch[index]] = results[index];
    }
  }
  return { view, records, byParticipant, missingAccounts };
}
