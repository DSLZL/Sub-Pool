import assert from "node:assert/strict";
import { test } from "node:test";
import { ValidationError } from "@orpc/contract";
import { createWalletsClient } from "./client";
import { walletRead, walletRecommendation, walletsOutputs, walletsVersion } from "./wallets";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A";
const check = { state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 };
const reported = (value: string) => ({ value, state: "reported", reason: "NATIVE_FIELD_REPORTED" });
const receipt = {
  id: "9007199254740993", participant_id: id, binding_id: "1", source_id: other, connection_revision: "2", attempted_at: "2026-11-07T12:00:00.0000001Z", participant_revision: "1", upstream_user_id: "9223372036854775807", read_at: "2026-11-07T12:00:00.0000002Z",
  balance: reported("-17.000000000000000001"), frozen_balance: reported("0"), read: { id: "users.read", ...check }, field_authority: { id: "users.balance.read", ...check }, unit: "native_wallet",
};
const contribution = { account_id: other, account_revision: "1", current_contract_id: "1", historical_contract_head: "1", share_percent: "25", estimate_id: null, estimate_state: "not_configured", observation_head: "0", capture_head: "0", state: "evidence_incomplete", amount: null };
const recommendation = {
  id: "10", participant_id: id, binding_id: "1", generated_at: "2026-11-07T12:00:00Z", rules_version: "gate-003-rules-v1", trigger: "manual_recompute", participant_revision: "1", basis_hash: "a".repeat(64), expires_at: null,
  global: { state: "evidence_incomplete", source_count: 1, limitations: ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] },
  contributions: [contribution], target: null, complete: false, applicable: false,
};
const view = { participant_id: id, name: "Member", participant_revision: "1", binding_id: "1", source_id: other, upstream_user_id: receipt.upstream_user_id, enabled: true, source_current: true, connection_revision: "2", checked_at: "2026-11-07T13:00:00Z", latest_read: receipt, last_confirmed_read: receipt, read_references_current: true, latest_recommendation: null, contributions: [contribution], visibility: "authorized_sources", current_state: "unavailable", needs_recalculation: null, current_target: null, applicable: false, difference: null, last_application: null, application_state: "not_implemented" };

test("wallets preserve native separate fields and forbid qualified amounts or hidden global projections", () => {
  assert.equal(walletRead.parse(receipt).balance.value, "-17.000000000000000001");
  for (const altered of [
    { ...receipt, balance: reported("1e2") }, { ...receipt, frozen_balance: reported("-1") }, { ...receipt, balance: reported("-0") },
    { ...receipt, balance: { ...reported("0"), state: "unverified" } }, { ...receipt, field_authority: { ...receipt.field_authority, state: "unverified" } },
    { ...receipt, read_at: null }, { ...receipt, read_at: "2026-11-07T12:00:00Z" }, { ...receipt, notes: "private" },
  ]) assert.equal(walletRead.safeParse(altered).success, false);
  assert.ok(walletRecommendation.safeParse(recommendation).success);
  for (const altered of [{ ...recommendation, target: "0" }, { ...recommendation, complete: true }, { ...recommendation, applicable: true }, { ...recommendation, contributions: [contribution, contribution] }, { ...recommendation, contributions: [] }]) assert.equal(walletRecommendation.safeParse(altered).success, false);
  assert.ok(walletsOutputs.get.safeParse(view).success);
  for (const altered of [{ ...view, latest_recommendation: recommendation }, { ...view, needs_recalculation: true }, { ...view, current_state: "recalculation_required" }, { ...view, latest_read: { ...receipt, binding_id: "2" } }, { ...view, current_target: "0" }]) assert.equal(walletsOutputs.get.safeParse(altered).success, false);
  const history = { participant_id: id, kind: "recommendation", wallet_reads: [], recommendations: [{ ...recommendation, id: "9" }, recommendation], next_after: "10" };
  assert.ok(walletsOutputs.history.safeParse(history).success);
  assert.equal(walletsOutputs.history.safeParse({ ...history, recommendations: [...history.recommendations].reverse() }).success, false);
  assert.equal(walletsOutputs.history.safeParse({ ...history, next_after: "11" }).success, false);
  for (const invalid of ["bad", "", "1e2", "1.0", "NaN", "0x10", "+1", "0", "01", "-1", "9223372036854775808", "9".repeat(1000)]) {
    for (const field of ["latest_read", "last_confirmed_read"] as const) {
      assert.equal(walletsOutputs.get.safeParse({ ...view, [field]: { ...receipt, id: invalid } }).success, false, `${field}: ${invalid}`);
    }
    for (const kind of ["wallet", "recommendation"] as const) {
      const row = kind === "wallet" ? receipt : recommendation;
      const rows = [{ ...row, id: "9007199254740992" }, { ...row, id: "9007199254740993" }];
      const page = { participant_id: id, kind, wallet_reads: kind === "wallet" ? rows : [], recommendations: kind === "recommendation" ? rows : [], next_after: null };
      assert.ok(walletsOutputs.history.safeParse(page).success);
      for (const index of [0, 1]) {
        const altered = rows.map((value, i) => i === index ? { ...value, id: invalid } : value);
        assert.equal(walletsOutputs.history.safeParse({ ...page, wallet_reads: kind === "wallet" ? altered : [], recommendations: kind === "recommendation" ? altered : [] }).success, false, `${kind}[${index}]: ${invalid}`);
      }
    }
  }
  assert.ok(walletsOutputs.get.safeParse({ ...view, latest_read: { ...receipt, id: "9223372036854775807" }, last_confirmed_read: { ...receipt, id: "9007199254740992" } }).success);
  assert.equal(walletsOutputs.get.safeParse({ ...view, latest_read: { ...receipt, id: "9007199254740992" } }).success, false);
});

test("wallet clients reject malformed response revisions once with a validation error", async () => {
  for (const invalid of ["bad", "1e2"]) {
    for (const field of ["latest_read", "last_confirmed_read"] as const) {
      let calls = 0;
      const client = createWalletsClient("https://example.test", async () => {
        calls++; return Response.json({ ...view, [field]: { ...receipt, id: invalid } });
      });
      await assert.rejects(client.get({ participant_id: id }), ValidationError);
      assert.equal(calls, 1);
    }
    for (const kind of ["wallet", "recommendation"] as const) {
      const row = kind === "wallet" ? receipt : recommendation;
      for (const index of [0, 1]) {
        const rows = [{ ...row, id: "1" }, { ...row, id: "2" }].map((value, i) => i === index ? { ...value, id: invalid } : value);
        let calls = 0;
        const client = createWalletsClient("https://example.test", async () => {
          calls++; return Response.json({ participant_id: id, kind, wallet_reads: kind === "wallet" ? rows : [], recommendations: kind === "recommendation" ? rows : [], next_after: null });
        });
        await assert.rejects(client.history({ participant_id: id, kind, after: "0", limit: 100 }), ValidationError);
        assert.equal(calls, 1);
      }
    }
  }
});

test("wallet client rejects overrides before dispatch and never replays an unknown local receipt", async () => {
  let calls = 0;
  const client = createWalletsClient("https://example.test", async (input, init) => {
    calls++; const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/wallets/refresh");
    assert.equal(request.method, "POST"); assert.equal(request.cache, "no-store"); assert.equal(request.redirect, "error");
    assert.equal(init?.credentials, "same-origin");
    throw new TypeError("connection lost after possible receipt commit");
  });
  const expected = { participant_id: id, participant_revision: "1", binding_id: "1", connection_revision: "2", source_id: other, latest_id: "0" };
  await assert.rejects(client.refresh({ ...expected, latest_id: "01" })); assert.equal(calls, 0);
  await assert.rejects(client.refresh(expected)); assert.equal(calls, 1);
  const forged = createWalletsClient("https://example.test", async () => Response.json({ ...receipt, frozen_balance: reported("-1") }));
  await assert.rejects(forged.refresh(expected));
});

test("wallets-v3 accepts retained and automatic triggers without widening money or privacy", () => {
  assert.equal(walletsVersion, "wallets-v3");
  for (const trigger of ["wallet_refresh", "manual_recompute", "input_change"]) {
    assert.equal(walletRecommendation.parse({ ...recommendation, trigger }).trigger, trigger);
  }
  for (const altered of [{ ...recommendation, trigger: "applied" }, { ...recommendation, trigger: "input_change", target: "1" }, { ...recommendation, trigger: "input_change", task_id: "wallet-inputs" }]) {
    assert.equal(walletRecommendation.safeParse(altered).success, false);
  }
});

test("native-user source inventory is bounded, incomplete and administrator-only while old receipts remain valid", () => {
  const sources = {
    started_at: "2026-11-07T12:00:01Z", captured_at: "2026-11-07T12:00:02Z", state: "exhausted",
    read: { id: "usage.read", ...check }, pages_read: 1, upstream_total: "2", scanned_count: "2",
    accounts: [{ upstream_account_id: "1", managed_account_id: other }, { upstream_account_id: "9007199254740993", managed_account_id: null }],
    complete: false, limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "WALLET_DEDUCTION_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED"],
  };
  assert.ok(walletRead.safeParse(receipt).success);
  assert.ok(walletRead.safeParse({ ...receipt, usage_sources: sources }).success);
  assert.ok(walletRead.safeParse({ ...receipt, usage_sources: { ...sources, state: "partial", pages_read: 1, scanned_count: "100", upstream_total: "101", read: { id: "usage.read", ...check, state: "denied", reason: "FORBIDDEN" }, limitations: [...sources.limitations, "FORBIDDEN"] } }).success);
  for (const altered of [
    { ...sources, complete: true }, { ...sources, scanned_count: "1001" }, { ...sources, pages_read: 11 },
    { ...sources, scanned_count: "bad" }, { ...sources, upstream_total: "bad" }, { ...sources, accounts: [{ upstream_account_id: "bad", managed_account_id: null }] },
    { ...sources, accounts: [...sources.accounts].reverse() }, { ...sources, accounts: [sources.accounts[0], sources.accounts[0]] },
    { ...sources, upstream_total: "3" }, { ...sources, read: { ...sources.read, state: "denied" } },
    { ...sources, captured_at: "2026-11-07T12:00:00Z" }, { ...sources, limitations: sources.limitations.slice(1) },
    { ...sources, actual_cost: "1" }, { ...sources, accounts: [{ ...sources.accounts[0], email: "private" }] },
  ]) assert.equal(walletRead.safeParse({ ...receipt, usage_sources: altered }).success, false);
  assert.ok(walletsOutputs.get.safeParse(view).success);
  for (const key of ["latest_read", "last_confirmed_read"]) assert.equal(walletsOutputs.get.safeParse({ ...view, [key]: { ...receipt, usage_sources: sources } }).success, false);
  assert.ok(walletsOutputs.get.safeParse({ ...view, visibility: "all_recorded_sources", needs_recalculation: false, latest_read: { ...receipt, usage_sources: sources } }).success);
});


test("only explicit complete simulated wallet proofs can carry conditional money; readers never receive global evidence", () => {
  const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id };
  const money = { numerator: "77", denominator: "40" }, zero = { numerator: "0", denominator: "1" };
  const proof = { input_hash: "b".repeat(64), start_at: "2026-11-07T10:00:00Z", end_at: "2026-11-07T11:59:00Z", closed_through: "2026-11-07T11:59:01Z", expires_at: "2026-11-07T12:10:00Z", ledger_revision: "17", consumed: money, spent: zero, remaining: money, reserve: zero };
  const row = { ...contribution, estimate_state: "scenario", state: "scenario", amount: money, simulation, simulation_proof: proof };
  const qualified = { ...recommendation, simulation, target: money, complete: true, expires_at: proof.expires_at, contributions: [row], global: { state: "scenario", source_count: 1, limitations: ["CONDITIONAL_SIMULATION_ONLY", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] } };
  assert.ok(walletRecommendation.safeParse(qualified).success);
  assert.ok(walletRecommendation.safeParse({ ...qualified, target: zero, contributions: [{ ...row, amount: zero }] }).success);
  for (const altered of [
    { ...qualified, simulation: undefined }, { ...qualified, applicable: true }, { ...qualified, target: null },
    { ...qualified, complete: false }, { ...qualified, expires_at: qualified.generated_at },
    { ...qualified, global: { ...qualified.global, limitations: recommendation.global.limitations } },
    { ...qualified, contributions: [{ ...row, simulation: { ...simulation, epoch: other } }] },
    { ...qualified, contributions: [{ ...row, simulation_proof: undefined }] },
    { ...qualified, contributions: [{ ...row, amount: { numerator: "-1", denominator: "1" } }] },
    { ...qualified, target: { numerator: "1", denominator: "1" } },
    { ...qualified, target: { numerator: "bad", denominator: "1" } },
    { ...qualified, contributions: [{ ...row, amount: { numerator: "bad", denominator: "1" } }] },
    { ...qualified, contributions: [{ ...row, simulation_proof: { ...proof, spent: { numerator: "bad", denominator: "1" } } }] },
  ]) assert.equal(walletRecommendation.safeParse(altered).success, false);
  const admin = { ...view, visibility: "all_recorded_sources", needs_recalculation: false, current_state: "scenario", current_target: money, latest_recommendation: qualified, contributions: [row], checked_at: "2026-11-07T12:01:00Z" };
  assert.ok(walletsOutputs.get.safeParse(admin).success);
  for (const altered of [{ ...admin, visibility: "authorized_sources" }, { ...admin, checked_at: proof.expires_at }, { ...admin, current_target: zero }, { ...admin, applicable: true }]) assert.equal(walletsOutputs.get.safeParse(altered).success, false);
  const { simulation_proof: _, ...projected } = row;
  assert.ok(walletsOutputs.get.safeParse({ ...view, contributions: [projected] }).success);
  assert.equal(walletsOutputs.get.safeParse({ ...view, contributions: [row] }).success, false);
  const scope = { ...simulation, user_id: receipt.upstream_user_id, balance: "95", frozen_balance: "0", wallet_version: "7", ledger_revision: "17", closed_through: proof.closed_through, pending_requests: "0", source_account_ids: ["1"], scope_complete: true, billing_type: "standard_wallet", ledger_closed: true };
  const simulatedRead = { ...receipt, unit: "simulated_wallet_usd", simulation, simulation_scope: scope, balance: { ...reported("95"), reason: "SIMULATED_FIELD_REPORTED" }, frozen_balance: { ...reported("0"), reason: "SIMULATED_FIELD_REPORTED" } };
  assert.ok(walletRead.safeParse(simulatedRead).success);
  assert.ok(walletsOutputs.get.safeParse({ ...view, visibility: "all_recorded_sources", needs_recalculation: false, latest_read: simulatedRead, last_confirmed_read: simulatedRead }).success);
  assert.equal(walletsOutputs.get.safeParse({ ...view, latest_read: simulatedRead }).success, false);
  for (const altered of [{ ...simulatedRead, simulation: undefined }, { ...simulatedRead, unit: "native_wallet" }, { ...simulatedRead, simulation_scope: { ...scope, epoch: other } }, { ...simulatedRead, balance: reported("95") }, { ...simulatedRead, simulation_scope: { ...scope, source_account_ids: ["1", "1"] } }]) assert.equal(walletRead.safeParse(altered).success, false);
});

test("personal temporary inputs are strict, exact, private and optional for retained advice", () => {
  const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id };
  const row = { ...contribution, estimate_state: "scenario", state: "scenario", amount: { numerator: "1", denominator: "3" }, simulation };
  const own = { ...view, checked_at: "2026-11-07T12:01:00Z", contributions: [row] };
  assert.ok(walletsOutputs.get.safeParse(own).success);
  for (const effect of [
    { state: "ordinary" }, { state: "active", mode: "carry", cap: "0", deadline: "2026-11-07T12:10:00Z" },
    { state: "active", mode: "no_carry", cap: "9007199254740993.00000001", deadline: "2026-11-07T12:10:00.000000001Z" },
    { state: "rights", adjustment: "-0.00000001" }, { state: "rights", adjustment: "0.00000001" },
  ]) assert.deepEqual(walletsOutputs.get.parse({ ...own, contributions: [{ ...row, temporary_effect: effect }] }).contributions[0]?.temporary_effect, effect);
  const active = { state: "active", mode: "carry", cap: "0", deadline: "2026-11-07T12:10:00.000000001Z" };
  for (const effect of [
    { ...active, round_id: id }, { ...active, participants: [other] }, { ...active, cap: "-1" }, { ...active, cap: "1e2" },
    { ...active, cap: "0.000000001" }, { ...active, mode: "global" }, { ...active, deadline: "invalid" }, { ...active, deadline: "2026-11-07T12:01:00Z" },
    { state: "ordinary", adjustment: "0" }, { state: "rights", adjustment: "0" }, { state: "rights", adjustment: "-0.00000000" },
    { state: "rights", adjustment: "1e-8" }, { state: "rights", adjustment: "-0.000000001" }, { state: "rights", adjustment: "+1" },
  ]) assert.equal(walletsOutputs.get.safeParse({ ...own, contributions: [{ ...row, temporary_effect: effect }] }).success, false, JSON.stringify(effect));
  assert.equal(walletsOutputs.get.safeParse({ ...own, contributions: [{ ...contribution, temporary_effect: { state: "ordinary" } }] }).success, false);
  assert.ok(walletsOutputs.get.safeParse({ ...own, checked_at: "2026-11-07T12:10:00.000000000Z", contributions: [{ ...row, temporary_effect: active }] }).success);
  assert.equal(walletsOutputs.get.safeParse({ ...own, checked_at: active.deadline, contributions: [{ ...row, temporary_effect: active }] }).success, false);
});

test("personal active input follows its saved proof deadline while expired history remains readable", () => {
  const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id };
  const one = { numerator: "1", denominator: "1" }, zero = { numerator: "0", denominator: "1" };
  const effect = { state: "active", mode: "carry", cap: "1", deadline: "2026-11-07T12:10:00Z" };
  const proof = { input_hash: "b".repeat(64), start_at: "2026-11-07T10:00:00Z", end_at: "2026-11-07T11:59:00Z", closed_through: "2026-11-07T11:59:01Z", expires_at: effect.deadline, ledger_revision: "17", consumed: one, spent: zero, remaining: one, reserve: zero };
  const row = { ...contribution, estimate_state: "scenario", state: "scenario", amount: one, simulation, simulation_proof: proof, temporary_effect: effect };
  const saved = { ...recommendation, simulation, target: one, complete: true, expires_at: proof.expires_at, contributions: [row], global: { state: "scenario", source_count: 1, limitations: ["CONDITIONAL_SIMULATION_ONLY", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] } };
  assert.ok(walletRecommendation.safeParse(saved).success);
  assert.ok(walletsOutputs.history.safeParse({ participant_id: id, kind: "recommendation", wallet_reads: [], recommendations: [saved], next_after: null }).success);
  assert.equal(walletRecommendation.safeParse({ ...saved, generated_at: effect.deadline }).success, false);
  assert.equal(walletRecommendation.safeParse({ ...saved, contributions: [{ ...row, simulation_proof: { ...proof, expires_at: "2026-11-07T12:10:00.000000001Z" } }] }).success, false);
});
