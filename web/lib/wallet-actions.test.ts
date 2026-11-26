import { test } from "node:test";
import assert from "node:assert/strict";
import { walletsOutputs } from "@sub-pool/contracts/wallets";
import { walletApplication, walletAutomation, simulatedWalletApplicationInput } from "@sub-pool/contracts/wallet-applications";
import { canReconcileWallet, walletReadInput, checkWalletRefreshReceipt } from "./wallet-actions";
import { proxyAPI } from "./api-proxy";
import { walletOperation } from "./wallets-data";

const id = (n: number) => { const bytes = Buffer.alloc(16); bytes.writeUInt32BE(n, 12); return bytes.toString("base64url"); };
const marker = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id(902) };
const at = "2026-11-15T12:00:05Z", now = Date.parse(at), amount = (numerator: string, denominator = "1") => ({ numerator, denominator });
const binding = { source_id: id(901), connection_revision: "2", simulation: marker };
const automation = () => walletAutomation.parse({ enabled: false, can_enable: true, state: "available", reason: "CONDITIONAL_SIMULATION_ONLY", execution_state: "disabled",
  control: { revision: "1", binding: null, available_binding: binding, updated_at: null, latest_run: null } });
function view() {
  const field = (value: string) => ({ value, state: "reported", reason: "SIMULATED_FIELD_REPORTED" });
  const check = { state: "available", reason: "READ_OK", http_status: 200, latency_ms: 1 };
  const read = { id: "1", participant_id: id(1), binding_id: "1", ...binding, participant_revision: "1", upstream_user_id: "21",
    attempted_at: "2026-11-15T12:00:01Z", read_at: "2026-11-15T12:00:02Z", balance: field("95"), frozen_balance: field("0"),
    read: { ...check, id: "users.read" }, field_authority: { ...check, id: "users.balance.read" }, unit: "simulated_wallet_usd",
    simulation_scope: { ...marker, user_id: "21", balance: "95", frozen_balance: "0", wallet_version: "7", ledger_revision: "17",
      closed_through: "2026-11-15T12:00:00Z", pending_requests: "0", source_account_ids: ["1"], scope_complete: true, billing_type: "standard_wallet", ledger_closed: true } };
  const contribution = { account_id: id(10), account_revision: "1", current_contract_id: "1", historical_contract_head: "1", share_percent: "50", estimate_id: "1",
    estimate_state: "scenario", observation_head: "1", capture_head: "1", state: "scenario", amount: amount("77", "40"), simulation: marker,
    simulation_proof: { input_hash: "a".repeat(64), start_at: "2026-11-15T11:00:00Z", end_at: "2026-11-15T12:00:00Z", closed_through: "2026-11-15T12:00:00Z",
      expires_at: "2026-11-15T13:00:00Z", ledger_revision: "17", consumed: amount("10"), spent: amount("1"), remaining: amount("9"), reserve: amount("0") } };
  const recommendation = { id: "1", participant_id: id(1), binding_id: "1", generated_at: "2026-11-15T12:00:03Z", participant_revision: "1", rules_version: "gate-003-rules-v1",
    trigger: "wallet_refresh", basis_hash: "a".repeat(64), expires_at: "2026-11-15T13:00:00Z", global: { state: "scenario", source_count: 1,
      limitations: ["CONDITIONAL_SIMULATION_ONLY", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] }, contributions: [contribution], target: amount("77", "40"), complete: true, applicable: false, simulation: marker };
  return walletsOutputs.get.parse({ participant_id: id(1), name: "Member", participant_revision: "1", binding_id: "1", source_id: binding.source_id, upstream_user_id: "21",
    enabled: true, source_current: true, connection_revision: "2", checked_at: "2026-11-15T12:00:04Z", latest_read: read, last_confirmed_read: read, read_references_current: true,
    latest_recommendation: recommendation, contributions: [contribution], visibility: "all_recorded_sources", current_state: "scenario", needs_recalculation: false,
    current_target: amount("77", "40"), applicable: false, difference: null, last_application: null, application_state: "not_implemented" });
}
function pending() {
  const value = view(), input = simulatedWalletApplicationInput.parse({ ...walletReadInput(value), request_id: id(50), recommendation_id: "1", simulation: marker });
  const before = value.latest_read?.simulation_scope;
  assert.ok(before);
  const request = { ...marker, operation_id: "b".repeat(64), user_id: "21", expected_wallet_version: "7", expected_ledger_revision: "17", expected_closed_through: before.closed_through,
    source_account_ids: ["1"], target: "1.92500000", quantum: "0.00000001" };
  return walletApplication.parse({ id: "1", participant_id: id(1), participant_name: "Member", attempted_at: at, updated_at: at, simulation: marker,
    state: "pending", dispatch_state: "pending", reason: "SIMULATED_APPLY_PENDING", unit: "simulated_wallet_usd", target: amount("77", "40"), visibility: "administrator",
    details: { actor_id: id(800), input, upstream_user_id: "21", evaluated_basis_hash: "a".repeat(64), recommendation_basis_hash: "a".repeat(64), reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_APPLY_PENDING"],
      simulation_write: { request, before, original_target: amount("77", "40"), remainder: amount("0") } } });
}




test("automatic attribution and skipped reasons remain truthful and private summaries hide the grant", () => {
  const row = pending();
  const skipped = walletApplication.parse({ ...row, state: "rejected", dispatch_state: "not_dispatched", reason: "SIMULATION_UNRESOLVED_APPLICATION", target: null,
    details: { ...row.details, reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATION_UNRESOLVED_APPLICATION"], simulation_write: undefined, automation: { control_revision: "2" } } });
  assert.match(walletOperation(skipped).actor, /自动 #2$/); assert.match(walletOperation(skipped).note, /已跳过.*没有重复派发/);
  const publicRow = walletApplication.parse({ ...skipped, visibility: "participant_summary", reason: "SIMULATED_APPLICATION_UNAVAILABLE", details: null });
  assert.equal(walletOperation(publicRow).actor, "—"); assert.equal(walletOperation(publicRow).target, null);
});


test("wallet refresh derives only exact server-owned expectations and reject unusable wallet scopes", () => {
  const value = view(), input = walletReadInput(value);
  assert.deepEqual(input, { participant_id: value.participant_id, participant_revision: "1", binding_id: "1", connection_revision: "2", source_id: value.source_id, latest_id: "1" });
  assert.equal("target" in input, false); assert.equal("balance" in input, false);
  for (const change of [{ source_current: false }, { enabled: false }, { visibility: "authorized_sources" as const }]) assert.throws(() => walletReadInput({ ...value, ...change }));
  assert.equal(walletReadInput({ ...value, latest_read: null, last_confirmed_read: null }).latest_id, "0");
});
test("refresh receipts cannot confirm another participant, binding, connection, revision, source or stale head", () => {
  const value = view(), input = walletReadInput(value), receipt = { ...value.latest_read!, id: "2" };
  checkWalletRefreshReceipt(input, receipt);
  for (const change of [{ participant_id: id(2) }, { binding_id: "2" }, { participant_revision: "2" }, { connection_revision: "3" }, { source_id: id(3) }, { id: "1" }]) {
    assert.throws(() => checkWalletRefreshReceipt(input, { ...receipt, ...change }));
  }
});


test("legacy pending operations remain inspectable without offering reconciliation dispatch", () => {
  assert.equal(pending().state, "pending");
  assert.equal(canReconcileWallet(pending()), false);
});
