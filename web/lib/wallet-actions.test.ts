import { test } from "node:test";
import assert from "node:assert/strict";
import { walletsOutputs } from "@sub-pool/contracts/wallets";
import { walletApplication, walletAutomation, simulatedWalletApplicationInput } from "@sub-pool/contracts/wallet-applications";
import { canSimulateWallet, simulationApplicationInput, checkSimulationReceipt, canReconcileWallet, checkReconciliationReceipt, walletControl, checkAutomationReceipt } from "./wallet-actions";
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
  const value = view(), input = simulationApplicationInput(value, automation(), [], id(50), now), before = value.latest_read!.simulation_scope!;
  const request = { ...marker, operation_id: "b".repeat(64), user_id: "21", expected_wallet_version: "7", expected_ledger_revision: "17", expected_closed_through: before.closed_through,
    source_account_ids: ["1"], target: "1.92500000", quantum: "0.00000001" };
  return walletApplication.parse({ id: "1", participant_id: id(1), participant_name: "Member", attempted_at: at, updated_at: at, simulation: marker,
    state: "pending", dispatch_state: "pending", reason: "SIMULATED_APPLY_PENDING", unit: "simulated_wallet_usd", target: amount("77", "40"), visibility: "administrator",
    details: { actor_id: id(800), input, upstream_user_id: "21", evaluated_basis_hash: "a".repeat(64), recommendation_basis_hash: "a".repeat(64), reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_APPLY_PENDING"],
      simulation_write: { request, before, original_target: amount("77", "40"), remainder: amount("0") } } });
}

test("conditional apply uses current full-source metadata without opening native eligibility or accepting a target", () => {
  const value = view(), auto = automation();
  assert.equal(value.applicable, false); assert.equal(canSimulateWallet(value, auto, [], now), true);
  const input = simulationApplicationInput(value, auto, [], id(50), now);
  assert.deepEqual(Object.keys(input).sort(), ["participant_id", "participant_revision", "binding_id", "source_id", "connection_revision", "latest_id", "recommendation_id", "simulation", "request_id"].sort());
  assert.equal(input.latest_id, "1"); assert.equal(input.recommendation_id, "1");
  assert.equal(simulatedWalletApplicationInput.safeParse({ ...input, target: "100" }).success, false);
  assert.equal(simulatedWalletApplicationInput.safeParse({ ...input, confirmed: true }).success, false);
  assert.equal(canSimulateWallet(value, null, [], now), false);
  assert.equal(canSimulateWallet(value, auto, [], Date.parse("2026-11-15T13:00:00Z")), false);
  for (const change of [
    (v: typeof value) => { v.enabled = false; }, (v: typeof value) => { v.source_current = false; },
    (v: typeof value) => { v.visibility = "authorized_sources"; }, (v: typeof value) => { v.connection_revision = "3"; },
    (v: typeof value) => { v.read_references_current = false; }, (v: typeof value) => { v.current_target = amount("0"); },
    (v: typeof value) => { v.latest_read!.simulation_scope!.pending_requests = "1"; },
    (v: typeof value) => { v.latest_read!.simulation_scope!.frozen_balance = "0.00000001"; },
    (v: typeof value) => { v.latest_read!.simulation_scope!.scope_complete = false; },
    (v: typeof value) => { v.latest_read!.simulation_scope!.ledger_closed = false; },
    (v: typeof value) => { v.latest_read!.simulation!.epoch = id(903); },
    (v: typeof value) => { v.latest_recommendation!.simulation!.epoch = id(903); },
  ]) { const altered = structuredClone(value); change(altered); assert.equal(canSimulateWallet(altered, auto, [], now), false); }
  assert.equal(canSimulateWallet(value, auto, [pending()], now), false);
  assert.throws(() => simulationApplicationInput(value, auto, [pending()], id(51), now), /没有提交/);
});

test("apply and readonly reconcile receipts must retain the exact intent; uncertainty never authorizes a second apply", () => {
  const row = pending(), input = simulationApplicationInput(view(), automation(), [], id(50), now);
  checkSimulationReceipt(input, row); assert.equal(canReconcileWallet(row), true);
  const unknown = walletApplication.parse({ ...row, state: "unknown", dispatch_state: "unknown", reason: "SIMULATED_WRITE_RESULT_UNKNOWN",
    details: { ...row.details, reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_WRITE_RESULT_UNKNOWN"] } });
  checkReconciliationReceipt(row, unknown);
  assert.equal(canSimulateWallet(view(), automation(), [unknown], now), false);
  assert.throws(() => checkSimulationReceipt({ ...input, request_id: id(51) }, row), /原请求/);
  assert.throws(() => checkReconciliationReceipt(row, { ...unknown, id: "2" }), /原操作/);
  const wrong = structuredClone(unknown); assert.ok(wrong.details); wrong.details.input.source_id = id(904);
  assert.throws(() => checkReconciliationReceipt(row, wrong), /原操作/);
  assert.equal(canReconcileWallet({ ...unknown, visibility: "participant_summary", details: null }), false);
});

test("automatic controls match exact revision and binding and permit unavailable-binding disable", () => {
  const previous = walletControl(automation()); assert.ok(previous);
  const enabled = walletAutomation.parse({ ...automation(), enabled: true, execution_state: "waiting", control: { ...previous, revision: "2", binding, updated_at: at } });
  checkAutomationReceipt(previous, true, enabled);
  assert.throws(() => checkAutomationReceipt(previous, false, enabled), /控制版本/);
  const other = structuredClone(enabled); assert.ok("control" in other && other.control.binding); other.control.binding.simulation.epoch = id(905); other.control.available_binding = other.control.binding;
  assert.throws(() => checkAutomationReceipt(previous, true, other), /控制版本/);
  const saved = walletControl(enabled); assert.ok(saved);
  const disabled = walletAutomation.parse({ ...enabled, enabled: false, can_enable: false, state: "unavailable", execution_state: "disabled",
    control: { ...saved, revision: "3", available_binding: null } });
  checkAutomationReceipt(saved, false, disabled);
});

test("automatic attribution and skipped reasons remain truthful and private summaries hide the grant", () => {
  const row = pending();
  const skipped = walletApplication.parse({ ...row, state: "rejected", dispatch_state: "not_dispatched", reason: "SIMULATION_UNRESOLVED_APPLICATION", target: null,
    details: { ...row.details, reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATION_UNRESOLVED_APPLICATION"], simulation_write: undefined, automation: { control_revision: "2" } } });
  assert.match(walletOperation(skipped).actor, /自动 #2$/); assert.match(walletOperation(skipped).note, /已跳过.*没有重复派发/);
  const publicRow = walletApplication.parse({ ...skipped, visibility: "participant_summary", reason: "SIMULATED_APPLICATION_UNAVAILABLE", details: null });
  assert.equal(walletOperation(publicRow).actor, "—"); assert.equal(walletOperation(publicRow).target, null);
});

test("only fixed conditional simulate and readonly reconcile proxy replies are exposed without credential forwarding or retry", async () => {
  const publicOrigin = "https://pool.test", backendOrigin = "https://go.test", config = { publicOrigin, backendOrigin, allowLoopbackHTTP: false };
  let calls = 0;
  const request = (path: string) => new Request(publicOrigin + path, { method: "POST", headers: { Origin: publicOrigin, "Content-Type": "application/json",
    Cookie: "__Host-sub_pool_session=" + "a".repeat(43), Authorization: "private-never-forward" }, body: "{}" });
  for (const action of ["simulate", "reconcile"]) {
    const path = "/api/v1/wallet-applications/" + action;
    const result = await proxyAPI(request(path), config, async (input, init) => { calls++; assert.equal(String(input), backendOrigin + path);
      assert.equal(new Headers(init?.headers).get("Authorization"), null); assert.equal(init?.redirect, "error"); return Response.json(pending()); });
    assert.equal(result.status, 200);
    assert.equal((await proxyAPI(request(path), config, async () => { calls++; return Response.json({ ...pending(), confirmed: true }); })).status, 502);
    assert.equal((await proxyAPI(request(path), config, async () => { calls++; throw new Error("lost"); })).status, 502);
  }
  assert.equal(calls, 6);
  assert.equal((await proxyAPI(request("/api/v1/wallet-applications/request"), config, async () => { throw new Error("unused native route"); })).status, 404);
});
