import type { WalletView, WalletExpected, WalletRead, WalletRecommendation } from "@sub-pool/contracts/wallets";
import { walletExpected, walletRecommendation } from "@sub-pool/contracts/wallets";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { nativeWalletApplicationInput, simulatedWalletApplicationInput } from "@sub-pool/contracts/wallet-applications";
import { usageNanos } from "@sub-pool/contracts/usage";
import { WalletReadError, type WalletAutomation } from "./wallets-data";

export function walletControl(value: WalletAutomation | null) {
  return value && "control" in value ? value.control : null;
}

export function canSimulateWallet(view: WalletView, automation: WalletAutomation | null, operations: WalletApplication[], now = Date.now()) {
  const binding = walletControl(automation)?.available_binding;
  const read = view.latest_read, recommendation = view.latest_recommendation, scope = read?.simulation_scope;
  const target = walletRecommendation.shape.target.unwrap().safeParse(view.current_target);
  return !!binding && !!automation?.can_enable && view.visibility === "all_recorded_sources" && view.enabled && view.source_current
    && view.read_references_current && view.current_state === "scenario" && recommendation?.complete === true
    && target.success && BigInt(target.data.numerator) > 0n
    && (usageNanos(recommendation.expires_at ?? "") ?? 0n) > BigInt(now) * 1000000n
    && binding.source_id === view.source_id && binding.connection_revision === view.connection_revision
    && read?.simulation?.epoch === binding.simulation.epoch && recommendation.simulation?.epoch === binding.simulation.epoch
    && read.read.state === "available" && scope !== undefined && scope.scope_complete && scope.ledger_closed
    && scope.billing_type === "standard_wallet" && scope.pending_requests === "0" && /^0(?:\.0+)?$/.test(scope.frozen_balance)
    && !operations.some(row => row.participant_id === view.participant_id && (row.state === "pending" || row.state === "unknown"
      || row.details?.input.recommendation_id === recommendation.id));
}

export function simulationApplicationInput(view: WalletView, automation: WalletAutomation | null, operations: WalletApplication[], requestID: string, now = Date.now()) {
  if (!canSimulateWallet(view, automation, operations, now)) throw new WalletReadError("当前条件建议或绑定已不可用，请重新读取；没有提交应用。");
  return simulatedWalletApplicationInput.parse({ participant_id: view.participant_id, participant_revision: view.participant_revision,
    binding_id: view.binding_id, source_id: view.source_id, connection_revision: view.connection_revision, latest_id: view.latest_read!.id,
    recommendation_id: view.latest_recommendation!.id, simulation: view.latest_read!.simulation, request_id: requestID });
}

export function checkSimulationReceipt(input: ReturnType<typeof simulationApplicationInput>, row: WalletApplication) {
  if (row.unit !== "simulated_wallet_usd" || row.participant_id !== input.participant_id || row.visibility !== "administrator"
    || !row.details || row.details.automation !== undefined
    || Object.entries(input).some(([key, value]) => JSON.stringify(row.details!.input[key as keyof typeof input]) !== JSON.stringify(value))) {
    throw new WalletReadError("应用回执未匹配原请求；请只读核对历史，禁止重新派发。");
  }
}

export function canAdjustNativeWallet(view: WalletView, operations: WalletApplication[]) {
  const read = view.latest_read;
  return view.enabled && view.source_current && view.read_references_current && view.visibility === "all_recorded_sources"
    && read?.unit === "native_wallet" && read.read.state === "available" && read.field_authority.state === "available"
    && read.balance.value !== null && read.frozen_balance.value !== null
    && !operations.some(row => row.participant_id === view.participant_id && (row.state === "pending" || row.state === "unknown"));
}

export function nativeAdjustmentInput(view: WalletView, operations: WalletApplication[], requestID: string, delta: string) {
  if (!canAdjustNativeWallet(view, operations)) throw new WalletReadError("请先读取当前绑定的钱包并核对未决调账。");
  return nativeWalletApplicationInput.parse({ ...walletReadInput(view, "refresh"), request_id: requestID,
    recommendation_id: view.latest_recommendation?.id ?? "0", native_delta: delta });
}

export function checkNativeAdjustmentReceipt(input: ReturnType<typeof nativeAdjustmentInput>, row: WalletApplication) {
  const detail = row.details;
  if (row.unit !== "native_wallet" || row.visibility !== "administrator" || !detail || !("native_adjustment" in detail)
    || Object.entries(input).some(([key, value]) => detail.input[key as keyof typeof input] !== value)) {
    throw new WalletReadError("调账回执未匹配原请求，请只读核对应用记录。");
  }
}

export function checkAutomationReceipt(previous: NonNullable<ReturnType<typeof walletControl>>, enabled: boolean, value: WalletAutomation) {
  const control = walletControl(value);
  if (!control || control.revision !== String(BigInt(previous.revision) + 1n) || value.enabled !== enabled
    || enabled && JSON.stringify(control.binding) !== JSON.stringify(previous.available_binding)) {
    throw new WalletReadError("自动化回执未匹配原控制版本；请重新读取状态，禁止自动重试。");
  }
}

export function canReconcileWallet(row: WalletApplication) {
  return row.visibility === "administrator" && (row.unit === "simulated_wallet_usd" || !!row.details && "native_adjustment" in row.details)
    && (row.state === "pending" || row.state === "unknown");
}

export function checkReconciliationReceipt(previous: WalletApplication, row: WalletApplication) {
  if (previous.unit !== row.unit
    || ["id", "participant_id", "participant_name", "attempted_at", "unit", "simulation", "target"].some(key =>
      JSON.stringify(previous[key as keyof typeof previous]) !== JSON.stringify(row[key as keyof typeof row]))
    || !previous.details || !row.details || ["actor_id", "input", "simulation_write", "automation"].some(key =>
      JSON.stringify(previous.details![key as keyof typeof previous.details]) !== JSON.stringify(row.details![key as keyof typeof row.details]))) {
    throw new WalletReadError("核对回执未匹配原操作；没有确认结果，请继续只读核对。");
  }
}

export function walletReadInput(view: WalletView, kind: "refresh" | "recompute") {
  if (!view.enabled || !view.source_current || view.visibility !== "all_recorded_sources") {
    throw new WalletReadError("当前参与者或来源不可用；没有提交刷新或重算。");
  }
  return walletExpected.parse({ participant_id: view.participant_id, participant_revision: view.participant_revision,
    binding_id: view.binding_id, connection_revision: view.connection_revision, source_id: view.source_id, latest_id: (kind === "refresh" ? view.latest_read : view.latest_recommendation)?.id ?? "0" });
}
export function checkWalletRefreshReceipt(input: WalletExpected, row: WalletRead) {
  if (["participant_id", "participant_revision", "binding_id", "connection_revision", "source_id"].some(key =>
    row[key as keyof WalletRead] !== input[key as keyof WalletExpected]) || BigInt(row.id) <= BigInt(input.latest_id)) {
    throw new WalletReadError("刷新回执未匹配原钱包和读数版本；请只读核对历史。");
  }
}
export function checkWalletRecomputeReceipt(input: WalletExpected, row: WalletRecommendation) {
  if (row.participant_id !== input.participant_id || row.participant_revision !== input.participant_revision || row.binding_id !== input.binding_id
    || row.trigger !== "manual_recompute" || BigInt(row.id) <= BigInt(input.latest_id)) {
    throw new WalletReadError("重算回执未匹配原参与者和建议版本；请只读核对历史。");
  }
}
