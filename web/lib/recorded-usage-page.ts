import type { createParticipantsClient } from "@sub-pool/contracts/client";
import { usageNanos } from "@sub-pool/contracts/usage";
import { UsageReadError, type UsageData, type UsageWindow } from "./usage-data";
import { RecordedSelectionError } from "./pools-data";

type UsageReader = ReturnType<typeof createParticipantsClient>["recorded"];
export type UsageCursor = { after: string; basis: string };
export type RecordedUsagePage = UsageData & { basis: string };

export function verifyRecordedUsageWindow(view: UsageData["view"], window: UsageWindow,
  settings: { timezone: string; revision: string; authority: string }) {
  if (view && (view.history?.timezone !== settings.timezone || view.history?.settings_revision !== settings.revision ||
    view.history?.authority_revision !== settings.authority || usageNanos(view.query_start_at ?? "") !== usageNanos(window.start_at) ||
    usageNanos(view.query_end_at ?? "") !== usageNanos(window.end_at))) {
    throw new UsageReadError("Recorded usage scope changed");
  }
}

export async function readRecordedUsagePage(
  read: UsageReader, accounts: { id: string }[], window: UsageWindow,
  settings: { timezone: string; revision: string; authority: string },
  participant: string, cursor: UsageCursor | null, signal: AbortSignal, limit: 0 | 100 = 100,
): Promise<RecordedUsagePage> {
  const account_ids = accounts.map(account => account.id);
  if (account_ids.length > 20) throw new RecordedSelectionError("请选择一个账号查询。当前一次可查看最多 20 个账号。");
  if (account_ids.length === 0) return { view: null, records: [], byParticipant: {}, missingAccounts: [], basis: "" };
  const input = { account_ids, ...window, grain: "day" as const, model: "", api_key_id: "", participant_id: participant };
  const response = await read({ ...input, after: cursor?.after ?? "0", basis: cursor?.basis ?? "", limit }, { signal });
  if (cursor && response.basis !== cursor.basis) throw new UsageReadError("Recorded usage pagination basis changed");
  verifyRecordedUsageWindow(response.view, window, settings);
  const byParticipant: UsageData["byParticipant"] = {};
  const ids = new Set(response.view?.groups.flatMap(group => group.participant_id === null ? [] : [group.participant_id]));
  const selection = JSON.stringify(response.view?.history?.selections ?? []);
  for (const id of ids) {
    if (id === participant && response.view) {
      byParticipant[id] = response.view;
      continue;
    }
    const result = await read({ ...input, participant_id: id, after: "0", basis: "", limit: 0 }, { signal });
    verifyRecordedUsageWindow(result.view, window, settings);
    if (!result.view || JSON.stringify(result.view.history?.selections) !== selection) {
      throw new RecordedSelectionError("采集记录正在更新，请重新查询。");
    }
    byParticipant[id] = result.view;
  }
  return { view: response.view, records: response.view?.records ?? [], byParticipant, missingAccounts: response.missing_accounts, basis: response.basis };
}
