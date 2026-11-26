import type { DailyObservationHistory, DailyObservationInput } from "@sub-pool/contracts/observations";
import type { createParticipantsClient } from "@sub-pool/contracts/client";
import type { UsageHistory } from "@sub-pool/contracts/participants";
import { UsageReadError } from "./usage-data";
import { usageNanos } from "@sub-pool/contracts/usage";

export async function readDailyObservationHistory(input: DailyObservationInput, expected: { authority: string; timezone: string; scope: "account" | "authorized_participants" },
  read: (input: DailyObservationInput) => Promise<DailyObservationHistory>) {
  const value = await read(input);
  if (value.account_id !== input.account_id || usageNanos(value.query_start_at) !== usageNanos(input.start_at) || usageNanos(value.query_end_at) !== usageNanos(input.end_at)
    || value.authority_revision !== expected.authority || value.settings_revision !== input.settings_revision || value.timezone !== expected.timezone
    || (value.current !== null && value.current.usage.scope !== expected.scope) || value.records.some(row => row.usage.scope !== expected.scope)) {
    throw new UsageReadError("范围已变。");
  }
  return value;
}


type HistoryInput = Parameters<ReturnType<typeof createParticipantsClient>["history"]>[0];
type Selection = NonNullable<UsageHistory["history"]>["selections"][number];
type HistoryExpected = { account: string; authority: string; settingsRevision: string; timezone: string; scope: "account" | "authorized_participants" };
type HistoryReader = (input: HistoryInput) => Promise<UsageHistory>;

export async function readUsageAccountHistory(input: HistoryInput, expected: HistoryExpected,
  selections: (Pick<Selection, "id" | "account_id" | "captured_at"> & Partial<Pick<Selection, "start_at" | "end_at">>)[], read: HistoryReader) {
  const value = await read(input), metadata = value.history;
  if (!metadata || metadata.authority_revision !== expected.authority || metadata.settings_revision !== expected.settingsRevision
    || metadata.timezone !== expected.timezone || value.grain !== input.grain
    || usageNanos(value.query_start_at ?? "") !== usageNanos(input.start_at) || usageNanos(value.query_end_at ?? "") !== usageNanos(input.end_at)
    || value.unassigned_visible !== (expected.scope === "account") || metadata.selections.length !== selections.length
    || input.capture_ids.length !== selections.length || selections.some(item => item.account_id !== expected.account || !input.capture_ids.includes(item.id))
    || metadata.selections.some(item => !selections.some(selected => selected.id === item.id && selected.account_id === item.account_id
      && usageNanos(selected.captured_at) === usageNanos(item.captured_at)
      && (selected.start_at === undefined || usageNanos(selected.start_at) === usageNanos(item.start_at))
      && (selected.end_at === undefined || usageNanos(selected.end_at) === usageNanos(item.end_at))))
    || value.groups.some(group => group.account_id !== expected.account) || value.key_groups?.some(group => group.account_id !== expected.account)
    || value.records.some(record => record.account_id !== expected.account)) throw new UsageReadError("历史范围已变。");
  return value;
}

export async function readCurrentKeyUsage(daily: DailyObservationHistory | null, expected: HistoryExpected, read: HistoryReader, now = new Date()) {
  if (daily && (daily.account_id !== expected.account || daily.authority_revision !== expected.authority || daily.settings_revision !== expected.settingsRevision
    || daily.timezone !== expected.timezone || daily.current && (daily.current.observation.account_id !== expected.account || daily.current.usage.account_id !== expected.account
      || daily.current.usage.scope !== expected.scope))) throw new UsageReadError("Key 范围已变。");
  const current = daily?.current, native = current?.observation.snapshot.windows.find(window => window.kind === "seven_day");
  const window = current?.usage.windows.find(window => window.kind === "seven_day"), capture = window?.capture;
  const at = usageNanos(now.toISOString()), reset = usageNanos(native?.reset_at ?? ""), start = usageNanos(window?.start_at ?? ""), end = usageNanos(window?.end_at ?? "");
  if (!native || !window || !capture || !window.start_at || !window.end_at || !native.duration_seconds || native.reset_expired
    || at === null || reset === null || start === null || end === null || at < start || at >= reset || end > at
    || reset - BigInt(native.duration_seconds) * 1000000000n !== start || end !== usageNanos(current?.observation.snapshot.source_at ?? "")) return null;
  const value = await readUsageAccountHistory({ capture_ids: [capture.id], participant_id: "", model: "", api_key_id: "", grain: "day", after: "0", limit: 1,
    start_at: window.start_at, end_at: window.end_at }, expected, [{ id: capture.id, account_id: expected.account, captured_at: capture.captured_at }], read);
  const selection = value.history?.selections[0], capturedStart = usageNanos(selection?.start_at ?? ""), capturedEnd = usageNanos(selection?.end_at ?? "");
  if (capturedStart === null || capturedEnd === null || capturedStart > start || capturedEnd < end
    || JSON.stringify(value.summary) !== JSON.stringify(capture.summary)) throw new UsageReadError("Key 窗口不符。");
  return value;
}

export function usageKeyCosts(value: UsageHistory | null) {
  const costs = new Map(value?.key_groups?.map(group => [JSON.stringify([group.account_id, group.participant_id, group.basis, group.api_key_id]), group.metrics.total_cost]));
  return (row: UsageHistory["records"][number]) => row.fact.api_key_id === null ? null
    : costs.get(JSON.stringify([row.account_id, row.attribution.participant_id, row.attribution.basis, row.fact.api_key_id])) ?? null;
}
