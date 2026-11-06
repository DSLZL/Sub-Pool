import type { AttributedUsage } from "@sub-pool/contracts/participants";
import { usageNanos } from "@sub-pool/contracts/usage";
import { readCaptureSelections } from "@/lib/pools-data";

type CaptureReader = Parameters<typeof readCaptureSelections>[1];
export type UsageWindow = { start_at: string; end_at: string };
export type UsageData = { view: AttributedUsage | null; records: AttributedUsage["records"]; byParticipant: Record<string, AttributedUsage>; missingAccounts: string[] };

export function usageWindow(days: 7 | 14, now = new Date()): UsageWindow {
  const start = new Date(now);
  start.setUTCHours(0, 0, 0, 0);
  start.setUTCDate(start.getUTCDate() - days + 1);
  return { start_at: start.toISOString(), end_at: now.toISOString() };
}

export function millionTokens(value: string | null | undefined) {
  if (value == null) return null;
  if (!/^\d+$/.test(value)) throw new Error("Invalid token count");
  const padded = value.padStart(7, "0"), fraction = padded.slice(-6).replace(/0+$/, "");
  return `${padded.slice(0, -6)}${fraction ? `.${fraction}` : ""}`;
}

export function usageTrend(window: UsageWindow, buckets: AttributedUsage["buckets"]) {
  const labels: string[] = [], values: (string | null)[] = [];
  for (let day = Date.parse(window.start_at); day < Date.parse(window.end_at); day += 86400000) {
    const date = new Date(day).toISOString().slice(0, 10);
    labels.push(date);
    values.push(buckets.find(bucket => bucket.start_at.slice(0, 10) === date)?.metrics.actual_cost ?? null);
  }
  return { labels, values };
}

const cursorAfter = (next: string, previous: string) => {
  const [capture, fact] = next.split(".").map(BigInt), [oldCapture, oldFact] = previous === "0" ? [0n, 0n] : previous.split(".").map(BigInt);
  return capture > oldCapture || capture === oldCapture && fact > oldFact;
};
const metadata = ({ records: _records, next_after: _next, ...view }: AttributedUsage) => JSON.stringify(view);

export async function readUsageData(
  accounts: { id: string }[], captures: CaptureReader,
  read: (ids: string[], participant: string, after: string) => Promise<AttributedUsage>, window: UsageWindow,
): Promise<UsageData> {
  const { selections, missingAccounts } = await readCaptureSelections(accounts, captures);
  if (!selections.length) return { view: null, records: [], byParticipant: {}, missingAccounts };
  const ids = selections.map(item => item.id), records: AttributedUsage["records"] = [], byParticipant: UsageData["byParticipant"] = {};
  const captureTimes: bigint[] = [];
  for (const item of selections) {
    const at = usageNanos(item.captured_at);
    if (at === null) throw new Error("Invalid capture observation time");
    captureTimes.push(at);
  }
  const oldest = captureTimes.reduce((a, b) => a < b ? a : b), newest = captureTimes.reduce((a, b) => a > b ? a : b);
  const valid = (view: AttributedUsage, participant: string) => {
    if (usageNanos(view.start_at) !== usageNanos(selections[0].start_at) || usageNanos(view.end_at) !== usageNanos(selections[0].end_at)
      || usageNanos(view.query_start_at ?? "") !== usageNanos(window.start_at) || usageNanos(view.query_end_at ?? "") !== usageNanos(window.end_at)
      || usageNanos(view.oldest_capture_at) !== oldest || usageNanos(view.newest_capture_at) !== newest
      || view.grain !== "day" || view.groups.some(group => !selections.some(item => item.account_id === group.account_id) || participant && group.participant_id !== participant)
      || view.records.some(record => !selections.some(item => item.id === record.capture_id && item.account_id === record.account_id) || participant && record.attribution.participant_id !== participant)) throw new Error("Usage selection changed");
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
      if (JSON.stringify(results[index].groups) !== JSON.stringify(view.groups.filter(group => group.participant_id === batch[index]))) throw new Error("Participant coverage changed");
      byParticipant[batch[index]] = results[index];
    }
  }
  return { view, records, byParticipant, missingAccounts };
}
