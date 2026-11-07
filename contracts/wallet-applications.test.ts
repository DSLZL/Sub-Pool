import assert from "node:assert/strict";
import { test } from "node:test";
import { createWalletApplicationsClient } from "./client";
import { walletApplication, walletApplicationInput, walletApplicationQuery, walletApplicationsOutputs, walletAutomation } from "./wallet-applications";

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
  for (const changed of [{ ...query, end_at: "" }, { ...query, start_at: query.end_at }, { ...query, limit: 101 }]) assert.equal(walletApplicationQuery.safeParse(changed).success, false);
  const page = { participant_id: id, start_at: query.start_at, end_at: query.end_at, requests: [{ ...scoped, id: "9" }, { ...scoped, id: "10" }], next_after: "10" };
  assert.ok(walletApplicationsOutputs.list.safeParse(page).success);
  for (const changed of [{ ...page, requests: [...page.requests].reverse() }, { ...page, end_at: query.start_at }, { ...page, requests: [page.requests[0], { ...receipt, id: "10" }] }, { ...page, next_after: "11" }]) assert.equal(walletApplicationsOutputs.list.safeParse(changed).success, false);
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

test("automation reports a strict disabled unavailable boundary without an enabling input", async () => {
  const unavailable = { enabled: false, can_enable: false, state: "unavailable", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", execution_state: "not_implemented" };
  assert.deepEqual(walletAutomation.parse(unavailable), unavailable);
  for (const changed of [{ ...unavailable, enabled: true }, { ...unavailable, can_enable: true }, { ...unavailable, execution_state: "running" }, { ...unavailable, last_run: "confirmed" }]) assert.equal(walletAutomation.safeParse(changed).success, false);
  let calls = 0;
  const client = createWalletApplicationsClient("https://example.test", async (input, init) => {
    calls++; const request = new Request(input, init); assert.equal(request.url, "https://example.test/api/v1/wallet-applications/automation"); assert.equal(request.method, "POST"); assert.equal(await request.text(), "{}");
    return Response.json(unavailable);
  });
  await assert.rejects(client.automation({ enabled: true } as never)); assert.equal(calls, 0);
  assert.deepEqual(await client.automation({}), unavailable); assert.equal(calls, 1);
  const forged = createWalletApplicationsClient("https://example.test", async () => Response.json({ ...unavailable, enabled: true }));
  await assert.rejects(forged.automation({}));
});
