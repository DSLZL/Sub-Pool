import assert from "node:assert/strict";
import { test } from "node:test";
import { createWalletsClient } from "./client";
import { walletRead, walletRecommendation, walletsOutputs } from "./wallets";

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

test("wallets preserve native separate fields and forbid qualified amounts or hidden global projections", () => {
  assert.equal(walletRead.parse(receipt).balance.value, "-17.000000000000000001");
  for (const altered of [
    { ...receipt, balance: reported("1e2") }, { ...receipt, frozen_balance: reported("-1") }, { ...receipt, balance: reported("-0") },
    { ...receipt, balance: { ...reported("0"), state: "unverified" } }, { ...receipt, field_authority: { ...receipt.field_authority, state: "unverified" } },
    { ...receipt, read_at: null }, { ...receipt, read_at: "2026-11-07T12:00:00Z" }, { ...receipt, notes: "private" },
  ]) assert.equal(walletRead.safeParse(altered).success, false);
  assert.ok(walletRecommendation.safeParse(recommendation).success);
  for (const altered of [{ ...recommendation, target: "0" }, { ...recommendation, complete: true }, { ...recommendation, applicable: true }, { ...recommendation, contributions: [contribution, contribution] }, { ...recommendation, contributions: [] }]) assert.equal(walletRecommendation.safeParse(altered).success, false);
  const view = { participant_id: id, name: "Member", participant_revision: "1", binding_id: "1", source_id: other, upstream_user_id: receipt.upstream_user_id, enabled: true, source_current: true, connection_revision: "2", checked_at: "2026-11-07T13:00:00Z", latest_read: receipt, last_confirmed_read: receipt, read_references_current: true, latest_recommendation: null, contributions: [contribution], visibility: "authorized_sources", current_state: "unavailable", needs_recalculation: null, current_target: null, applicable: false, difference: null, last_application: null, application_state: "not_implemented" };
  assert.ok(walletsOutputs.get.safeParse(view).success);
  for (const altered of [{ ...view, latest_recommendation: recommendation }, { ...view, needs_recalculation: true }, { ...view, current_state: "recalculation_required" }, { ...view, latest_read: { ...receipt, binding_id: "2" } }, { ...view, current_target: "0" }]) assert.equal(walletsOutputs.get.safeParse(altered).success, false);
  const history = { participant_id: id, kind: "recommendation", wallet_reads: [], recommendations: [{ ...recommendation, id: "9" }, recommendation], next_after: "10" };
  assert.ok(walletsOutputs.history.safeParse(history).success);
  assert.equal(walletsOutputs.history.safeParse({ ...history, recommendations: [...history.recommendations].reverse() }).success, false);
  assert.equal(walletsOutputs.history.safeParse({ ...history, next_after: "11" }).success, false);
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
