import { glossary } from "@/lib/glossary";
import { walletExpected, type WalletView, type WalletExpected, type WalletRead } from "@sub-pool/contracts/wallets";
import { nativeWalletApplicationInput, type WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { WalletReadError } from "./wallets-data";

export function canAdjustNativeWallet(view: WalletView, operations: WalletApplication[]) {
  const read = view.latest_read;
  return view.enabled && view.source_current && view.read_references_current && view.visibility === "all_recorded_sources"
    && read?.unit === "native_wallet" && read.read.state === "available" && read.field_authority.state === "available"
    && read.balance.value !== null && read.frozen_balance.value !== null
    && !operations.some(row => row.participant_id === view.participant_id && (row.state === "pending" || row.state === "unknown"));
}

export function nativeAdjustmentInput(view: WalletView, operations: WalletApplication[], requestID: string, delta: string) {
  if (!canAdjustNativeWallet(view, operations)) throw new WalletReadError(`请核对未决${glossary.balanceAdjustment}。`);
  return nativeWalletApplicationInput.parse({ ...walletReadInput(view), request_id: requestID,
    recommendation_id: view.latest_recommendation?.id ?? "0", native_delta: delta });
}

export function checkNativeAdjustmentReceipt(input: ReturnType<typeof nativeAdjustmentInput>, row: WalletApplication) {
  const detail = row.details;
  if (row.unit !== "native_wallet" || row.visibility !== "administrator" || !detail || !("native_adjustment" in detail)
    || Object.entries(input).some(([key, value]) => detail.input[key as keyof typeof input] !== value)) {
    throw new WalletReadError(`${glossary.balanceAdjustment} 回执不符，核对 ${glossary.adjustmentRecords}。`);
  }
}

export function canReconcileWallet(row: WalletApplication) {
  return row.visibility === "administrator" && row.unit === "native_wallet" && !!row.details && "native_adjustment" in row.details
    && (row.state === "pending" || row.state === "unknown");
}

export function checkReconciliationReceipt(previous: WalletApplication, row: WalletApplication) {
  if (previous.unit !== row.unit
    || ["id", "participant_id", "participant_name", "attempted_at", "unit", "simulation", "target"].some(key =>
      JSON.stringify(previous[key as keyof typeof previous]) !== JSON.stringify(row[key as keyof typeof row]))
    || !previous.details || !row.details || ["actor_id", "input", "simulation_write", "automation"].some(key =>
      JSON.stringify(previous.details![key as keyof typeof previous.details]) !== JSON.stringify(row.details![key as keyof typeof row.details]))) {
    throw new WalletReadError("回执不符。");
  }
}

export function walletReadInput(view: WalletView) {
  if (!view.enabled || !view.source_current || view.visibility !== "all_recorded_sources") {
    throw new WalletReadError("钱包不可用。");
  }
  return walletExpected.parse({ participant_id: view.participant_id, participant_revision: view.participant_revision,
    binding_id: view.binding_id, connection_revision: view.connection_revision, source_id: view.source_id, latest_id: view.latest_read?.id ?? "0" });
}
export function checkWalletRefreshReceipt(input: WalletExpected, row: WalletRead) {
  if (["participant_id", "participant_revision", "binding_id", "connection_revision", "source_id"].some(key =>
    row[key as keyof WalletRead] !== input[key as keyof WalletExpected]) || BigInt(row.id) <= BigInt(input.latest_id)) {
    throw new WalletReadError("读数不符，查历史。");
  }
}
