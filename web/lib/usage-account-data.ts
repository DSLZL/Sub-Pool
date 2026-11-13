import type { DailyObservationHistory, DailyObservationInput } from "@sub-pool/contracts/observations";
import { UsageReadError } from "./usage-data";
import { usageNanos } from "@sub-pool/contracts/usage";

export async function readDailyObservationHistory(input: DailyObservationInput, expected: { authority: string; timezone: string; scope: "account" | "authorized_participants" },
  read: (input: DailyObservationInput) => Promise<DailyObservationHistory>) {
  const value = await read(input);
  if (value.account_id !== input.account_id || usageNanos(value.query_start_at) !== usageNanos(input.start_at) || usageNanos(value.query_end_at) !== usageNanos(input.end_at)
    || value.authority_revision !== expected.authority || value.settings_revision !== input.settings_revision || value.timezone !== expected.timezone
    || value.records.some(row => row.usage.scope !== expected.scope)) {
    throw new UsageReadError("每日快照的账号、授权、区间或统计时区已变化，请重新读取。");
  }
  return value;
}
