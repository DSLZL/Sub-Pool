import type { WalletView } from "@sub-pool/contracts/wallets";
import { walletRecommendation } from "@sub-pool/contracts/wallets";
import type { WalletApplication } from "@sub-pool/contracts/wallet-applications";
import { simulatedWalletApplicationInput } from "@sub-pool/contracts/wallet-applications";
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

export function checkAutomationReceipt(previous: NonNullable<ReturnType<typeof walletControl>>, enabled: boolean, value: WalletAutomation) {
  const control = walletControl(value);
  if (!control || control.revision !== String(BigInt(previous.revision) + 1n) || value.enabled !== enabled
    || enabled && JSON.stringify(control.binding) !== JSON.stringify(previous.available_binding)) {
    throw new WalletReadError("自动化回执未匹配原控制版本；请重新读取状态，禁止自动重试。");
  }
}

export function canReconcileWallet(row: WalletApplication) {
  return row.visibility === "administrator" && row.unit === "simulated_wallet_usd" && (row.state === "pending" || row.state === "unknown");
}

export function checkReconciliationReceipt(previous: WalletApplication, row: WalletApplication) {
  if (previous.unit !== "simulated_wallet_usd" || row.unit !== "simulated_wallet_usd"
    || ["id", "participant_id", "participant_name", "attempted_at", "unit", "simulation", "target"].some(key =>
      JSON.stringify(previous[key as keyof typeof previous]) !== JSON.stringify(row[key as keyof typeof row]))
    || !previous.details || !row.details || ["actor_id", "input", "simulation_write", "automation"].some(key =>
      JSON.stringify(previous.details![key as keyof typeof previous.details]) !== JSON.stringify(row.details![key as keyof typeof row.details]))) {
    throw new WalletReadError("核对回执未匹配原操作；没有确认结果，请继续只读核对。");
  }
}
