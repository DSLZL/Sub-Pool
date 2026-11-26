import assert from "node:assert/strict";
import { test } from "node:test";
import { createWalletApplicationsClient } from "./client";
import { simulatedWalletApplicationInput, walletApplication, walletApplicationInput, walletApplicationQuery, walletApplicationsOutputs, walletAutomation } from "./wallet-applications";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A";
const input = { request_id: other, participant_id: id, participant_revision: "1", binding_id: "1", connection_revision: "2", source_id: other, latest_id: "0", recommendation_id: "0" };
const receipt = {
  id: "9007199254740993", participant_id: id, participant_name: "Member", attempted_at: "2026-11-07T12:00:00.000001Z",
  state: "rejected", dispatch_state: "not_dispatched", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", target: null, unit: "native_wallet", visibility: "administrator",
  details: { actor_id: other, input, upstream_user_id: "9223372036854775807", evaluated_basis_hash: "a".repeat(64), recommendation_basis_hash: null,
    reasons: ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED", "WALLET_WRITE_CAPABILITY_UNVERIFIED", "TARGET_UNAVAILABLE", "RECOMMENDATION_NOT_GENERATED"] },
};

test("application receipts cannot claim remote success, money or ordinary administrator detail", () => {
  assert.ok(walletApplicationInput.safeParse(input).success);
  for (const changed of [{ ...input, target: "0" }, { ...input, complete: true }, { ...input, confirmed: true }, { ...input, latest_id: "01" }]) assert.equal(walletApplicationInput.safeParse(changed).success, false);
  assert.equal(walletApplication.parse(receipt).id, "9007199254740993");
  for (const changed of [
    { ...receipt, state: "confirmed" }, { ...receipt, dispatch_state: "unknown" }, { ...receipt, target: "0" }, { ...receipt, unit: "USD" },
    { ...receipt, visibility: "participant_summary" }, { ...receipt, details: null },
    { ...receipt, details: { ...receipt.details, input: { ...input, participant_id: other } } },
    { ...receipt, details: { ...receipt.details, recommendation_basis_hash: "b".repeat(64) } },
    { ...receipt, details: { ...receipt.details, reasons: receipt.details.reasons.slice(1) } },
  ]) assert.equal(walletApplication.safeParse(changed).success, false);
  const scoped = { ...receipt, visibility: "participant_summary", details: null };
  assert.ok(walletApplication.safeParse(scoped).success);
  const query = { participant_id: id, start_at: "2026-11-07T12:00:00.000001Z", end_at: "2026-11-07T12:00:00.000001001Z", after: "0", limit: 100 };
  assert.ok(walletApplicationQuery.safeParse(query).success);
  assert.ok(walletApplicationQuery.safeParse({ ...query, end_at: "" }).success);
  assert.ok(walletApplicationQuery.safeParse({ ...query, start_at: "" }).success);
  for (const changed of [{ ...query, start_at: query.end_at }, { ...query, limit: 101 }]) assert.equal(walletApplicationQuery.safeParse(changed).success, false);
  const page = { participant_id: id, start_at: query.start_at, end_at: query.end_at, requests: [{ ...scoped, id: "9" }, { ...scoped, id: "10" }], next_after: "10" };
  assert.ok(walletApplicationsOutputs.list.safeParse(page).success);
  assert.ok(walletApplicationsOutputs.list.safeParse({ ...page, end_at: "" }).success);
  for (const changed of [{ ...page, requests: [...page.requests].reverse() }, { ...page, end_at: query.start_at }, { ...page, requests: [page.requests[0], { ...receipt, id: "10" }] }, { ...page, next_after: "11" }]) assert.equal(walletApplicationsOutputs.list.safeParse(changed).success, false);
});

test("positive weekly receipts can confirm a matching ledger while balance readback is unavailable", () => {
  const weekly = {
    ...receipt, updated_at: receipt.attempted_at, state: "applied", dispatch_state: "confirmed", reason: "NATIVE_WALLET_ADJUSTED",
    details: { ...receipt.details, reasons: ["NATIVE_WALLET_ADJUSTED"], input: { ...input, native_delta: "0.00000001" },
      native_adjustment: { weekly_issuance: true, ledger_id: "1", balance_before: null, balance_after: null } },
  };
  assert.ok(walletApplication.safeParse(weekly).success);
  const withDetails = (changes: object) => ({ ...weekly, details: { ...weekly.details, ...changes } });
  assert.equal(walletApplication.safeParse(withDetails({
    native_adjustment: { ...weekly.details.native_adjustment, weekly_issuance: undefined },
  })).success, false);
  assert.equal(walletApplication.safeParse(withDetails({
    input: { ...weekly.details.input, native_delta: "-0.00000001" },
  })).success, false);
  assert.equal(walletApplication.safeParse(withDetails({
    native_adjustment: { ...weekly.details.native_adjustment, ledger_id: undefined },
  })).success, false);
});

test("application client validates before dispatch and never retries an uncertain local commit", async () => {
  let calls = 0;
  const client = createWalletApplicationsClient("https://example.test", async (input, init) => {
    calls++; const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/wallet-applications/request"); assert.equal(request.method, "POST");
    assert.equal(request.cache, "no-store"); assert.equal(request.redirect, "error"); assert.equal(init?.credentials, "same-origin");
    throw new TypeError("connection lost after possible local receipt commit");
  });
  await assert.rejects(client.request({ ...input, latest_id: "01" })); assert.equal(calls, 0);
  await assert.rejects(client.request(input)); assert.equal(calls, 1);
  const forged = createWalletApplicationsClient("https://example.test", async () => Response.json({ ...receipt, state: "confirmed" }));
  await assert.rejects(forged.request(input));
});


const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id };
const before = { ...simulation, user_id: "21", balance: "95", frozen_balance: "0", wallet_version: "9007199254740992", ledger_revision: "17",
  closed_through: "2026-11-07T11:59:59Z", pending_requests: "0", source_account_ids: ["1", "2"], scope_complete: true, billing_type: "standard_wallet", ledger_closed: true };
const write = { ...simulation, operation_id: "c".repeat(64), user_id: "21", expected_wallet_version: before.wallet_version, expected_ledger_revision: "17",
  expected_closed_through: before.closed_through, source_account_ids: ["1", "2"], target: "2.26923076", quantum: "0.00000001" };
const intent = { request: write, before, original_target: { numerator: "59", denominator: "26" }, remainder: { numerator: "3", denominator: "325000000" } };
const occurred = "2026-11-07T12:00:01.000001Z";
const proof = { ...simulation, operation_id: write.operation_id, user_id: "21", request_hash: "d".repeat(64), request: write, state: "applied", reason: "APPLIED",
  before, after: { ...before, balance: write.target, wallet_version: "9007199254740993", ledger_revision: "18", closed_through: occurred }, occurred_at: occurred };
const simulated = { ...receipt, updated_at: "2026-11-07T12:00:02Z", simulation, state: "applied", dispatch_state: "confirmed", reason: "SIMULATED_APPLIED", unit: "simulated_wallet_usd",
  target: { numerator: "56730769", denominator: "25000000" }, details: { ...receipt.details, input: { ...input, recommendation_id: "1", simulation }, upstream_user_id: "21",
    recommendation_basis_hash: "b".repeat(64), reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_APPLIED"], simulation_write: intent, simulation_proof: proof } };

test("simulation application binds exact quantization, closed scope, operation proof and private summary", () => {
  assert.ok(simulatedWalletApplicationInput.safeParse(simulated.details.input).success);
  assert.ok(walletApplication.safeParse(simulated).success);
  assert.equal(walletApplicationsOutputs.request.safeParse(simulated).success, false);
  for (const value of [
    { ...simulated, simulation: { ...simulation, epoch: other } }, { ...simulated, dispatch_state: "pending" },
    { ...simulated, target: { numerator: "59", denominator: "26" } }, { ...simulated, details: null },
    { ...simulated, details: { ...simulated.details, simulation_proof: undefined } },
    { ...simulated, details: { ...simulated.details, simulation_write: { ...intent, remainder: { numerator: "0", denominator: "1" } } } },
    { ...simulated, details: { ...simulated.details, simulation_proof: { ...proof, after: { ...proof.after, wallet_version: before.wallet_version } } } },
    { ...simulated, details: { ...simulated.details, simulation_proof: { ...proof, before: { ...before, balance: "94" } } } },
    { ...simulated, details: { ...simulated.details, simulation_proof: { ...proof, request: { ...write, target: "3.00000000" } } } },
    { ...simulated, visibility: "participant_summary", details: null },
  ]) assert.equal(walletApplication.safeParse(value).success, false);
  assert.ok(walletApplication.safeParse({ ...simulated, visibility: "participant_summary", details: null, target: null }).success);
  const unknown = { ...simulated, state: "unknown", dispatch_state: "unknown", reason: "SIMULATED_WRITE_RESULT_UNKNOWN",
    details: { ...simulated.details, reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_WRITE_RESULT_UNKNOWN"], simulation_proof: undefined } };
  assert.ok(walletApplication.safeParse(unknown).success);
  assert.ok(walletApplication.safeParse({ ...unknown, visibility: "participant_summary", details: null, target: null, reason: "SIMULATED_WRITE_RESULT_UNKNOWN" }).success);
  for (const [state, dispatch_state, reason] of [["rejected", "not_dispatched", "SIMULATED_APPLICATION_UNAVAILABLE"], ["failed", "confirmed_failed", "SIMULATED_APPLICATION_FAILED"]]) {
    assert.ok(walletApplication.safeParse({ ...simulated, state, dispatch_state, reason, visibility: "participant_summary", details: null, target: null }).success);
  }
  for (const bad of ["", "bad", "01", "-1"]) {
    assert.doesNotThrow(() => assert.equal(walletApplication.safeParse({ ...simulated, target: { numerator: bad, denominator: "1" } }).success, false));
    assert.doesNotThrow(() => assert.equal(walletApplication.safeParse({ ...simulated, details: { ...simulated.details, simulation_proof: { ...proof, after: { ...proof.after, wallet_version: bad } } } }).success, false));
    assert.doesNotThrow(() => assert.equal(walletApplicationsOutputs.list.safeParse({ participant_id: "", start_at: "", end_at: "", requests: [{ ...receipt, id: bad }, receipt], next_after: null }).success, false));
  }
});


test("archived automation preserves binding and truthful one-wallet run outcomes", () => {
  const binding = { source_id: other, connection_revision: "2", simulation };
  const run = { id: "10", participant_id: id, participant_name: "Member", recommendation_id: "7", control_revision: "2",
    binding, state: "applied", reason: "SIMULATED_APPLIED", attempted_at: receipt.attempted_at, updated_at: simulated.updated_at,
    counts: { total: 1, applied: 1, failed: 0, skipped: 0, unknown: 0, running: 0 } };
  const status = { enabled: true, can_enable: true, state: "available", reason: "CONDITIONAL_SIMULATION_ONLY", execution_state: "waiting",
    control: { revision: "2", binding, available_binding: binding, updated_at: receipt.attempted_at, latest_run: run } };
  assert.ok(walletAutomation.safeParse(status).success);
  for (const invalid of [
    { ...status, can_enable: false }, { ...status, enabled: false }, { ...status, state: "paused" },
    { ...status, control: { ...status.control, binding: { ...binding, connection_revision: "3" } } },
    { ...status, control: { ...status.control, latest_run: { ...run, counts: { ...run.counts, unknown: 1 } } } },
    { ...status, control: { ...status.control, latest_run: { ...run, state: "unknown" } } },
  ]) assert.equal(walletAutomation.safeParse(invalid).success, false);
  assert.ok(walletAutomation.safeParse({ ...status, can_enable: false, state: "paused", execution_state: "paused", reason: "SIMULATION_BINDING_CHANGED",
    control: { ...status.control, available_binding: null } }).success);
  assert.ok(walletApplication.safeParse({ ...simulated, details: { ...simulated.details, automation: { control_revision: "2" } } }).success);
  assert.equal(walletApplication.safeParse({ ...receipt, details: { ...receipt.details, automation: { control_revision: "2" } } }).success, false);
  assert.equal(simulatedWalletApplicationInput.safeParse({ ...simulated.details.input, automation: { control_revision: "2" } }).success, false);
  const page = { items: [run, { ...run, id: "9" }], next_after: "9" };
  assert.ok(walletApplicationsOutputs.automationHistory.safeParse(page).success);
  for (const invalid of [{ ...page, items: [...page.items].reverse() }, { ...page, next_after: "10" },
    { ...page, items: [run, run] }, { ...page, items: [{ ...run, reason: "FINANCIAL_APPLICATION_UNAVAILABLE" }] }]) {
    assert.equal(walletApplicationsOutputs.automationHistory.safeParse(invalid).success, false);
  }
  for (const bad of ["", "01", "bad"]) assert.doesNotThrow(() => assert.equal(walletApplicationsOutputs.automationHistory.safeParse({ items: [{ ...run, id: bad }, run], next_after: null }).success, false));
});
