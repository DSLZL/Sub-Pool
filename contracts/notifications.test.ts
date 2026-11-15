import assert from "node:assert/strict";
import { test } from "node:test";
import { createNotificationsClient } from "./client";
import { notification, notificationConfiguration, notificationOutputs, notificationQuery, notificationSaveInput } from "./notifications";

const id = "a".repeat(21) + "A";
const receipt = { id: "9007199254740993", kind: "test", subject_type: "system", subject_id: "test", participant_id: null, created_at: "2026-11-07T12:00:00.000001Z", started_at: "2026-11-07T12:00:00.000002Z", finished_at: "2026-11-07T12:00:00.000003Z", state: "accepted", reason: "TRANSPORT_ACCEPTED", visibility: "administrator", details: { config_revision: "1", channel: "smtp", from: "sender@owned.test", recipient: "admin@owned.test", subject: "Sub-Pool test notification", body: "Explicit test", event_reason: "EXPLICIT_ADMINISTRATOR_TEST", event_key: "test:" + id, actor_id: id, provider_id: null } };
const input = { revision: "1", enabled: false, financial_enabled: false, channel: "smtp", from: "", recipient: "", smtp: { host: "", port: 587, tls: "starttls", username: "" }, preferences: { balance_low: true, recommendation_changed: true, estimate_changed: true, collection_failed: true }, dedupe_seconds: 3600, credential_action: "keep", credential: "" };
const capabilities = ["balance_low", "recommendation_changed", "estimate_changed"].map(kind => ({ kind, available: false, effective: false, reason: "FINANCIAL_BASIS_UNAVAILABLE" }));

test("notification contract preserves exact receipts, financial unavailability and ordinary privacy", () => {
  assert.equal(notification.parse(receipt).id, "9007199254740993");
  for (const changed of [{ ...receipt, state: "delivered" }, { ...receipt, state: "read" }, { ...receipt, reason: "TRANSPORT_OUTCOME_UNKNOWN" }, { ...receipt, started_at: null }, { ...receipt, finished_at: null }, { ...receipt, visibility: "subject_summary" }, { ...receipt, details: null }, { ...receipt, details: { ...receipt.details, channel: "resend" } }]) assert.equal(notification.safeParse(changed).success, false);
  const scoped = { ...receipt, kind: "collection_failed", subject_type: "account", subject_id: id, visibility: "subject_summary", details: null, reason: "ACCEPTED" };
  assert.ok(notification.safeParse(scoped).success);
  for (const changed of [{ ...scoped, details: receipt.details }, { ...scoped, reason: "REMOTE_REJECTED" }, { ...scoped, kind: "test" }]) assert.equal(notification.safeParse(changed).success, false);
  const { credential_action, credential, ...fields } = input;
  const config = { ...fields, secret_set: false, secret_storage_available: true, saved_at: null, capabilities: [...capabilities, { kind: "collection_failed", available: true, effective: false, reason: "CONFIRMED_COLLECTION_FAILURES_ONLY" }] };
  assert.ok(notificationSaveInput.safeParse(input).success); assert.ok(notificationConfiguration.safeParse(config).success);
  assert.equal(notificationConfiguration.safeParse({ ...config, capabilities: [{ ...capabilities[0], effective: true }, ...config.capabilities.slice(1)] }).success, false);
  assert.equal(notificationConfiguration.safeParse({ ...config, credential: "private" }).success, false);
  assert.equal(notificationSaveInput.safeParse({ ...input, recipient: "admin@owned.test\r\nBcc: victim@owned.test" }).success, false);
  const query = { start_at: "2026-11-07T12:00:00.000000001Z", end_at: "2026-11-07T12:00:00.000000002Z", kind: "", participant_id: "", subject_type: "", subject_id: "", state: "", after: "0", limit: 100 };
  assert.ok(notificationQuery.safeParse(query).success); assert.equal(notificationQuery.safeParse({ ...query, end_at: "" }).success, false);
  const page = { notifications: [{ ...scoped, id: "9" }, { ...scoped, id: "10" }], next_after: "10" };
  assert.ok(notificationOutputs.list.safeParse(page).success); assert.equal(notificationOutputs.list.safeParse({ ...page, notifications: [...page.notifications].reverse() }).success, false);
});

test("notification client validates before dispatch and never replays an uncertain test request", async () => {
  let calls = 0;
  const client = createNotificationsClient("https://owned.test", async (input, init) => {
    calls++; const request = new Request(input, init);
    assert.equal(request.url, "https://owned.test/api/v1/notifications/test"); assert.equal(request.method, "POST"); assert.equal(request.redirect, "error"); assert.equal(request.cache, "no-store");
    throw new TypeError("request outcome unknown");
  });
  await assert.rejects(client.test({ request_id: id, revision: "01" })); assert.equal(calls, 0);
  await assert.rejects(client.test({ request_id: id, revision: "1" })); assert.equal(calls, 1);
  const forged = createNotificationsClient("https://owned.test", async () => Response.json({ ...receipt, state: "delivered" }));
  await assert.rejects(forged.get({ id: receipt.id }));
});

test("financial notices bind kind, subject, evidence key and conditional capability activation", () => {
  for (const [kind, event_reason] of [["balance_low", "SIMULATED_BALANCE_BELOW_TARGET"], ["recommendation_changed", "SIMULATED_RECOMMENDATION_CHANGED"], ["estimate_changed", "SIMULATED_ESTIMATE_CAPACITY_CHANGED"]] as const) {
    const wallet = kind !== "estimate_changed";
    const row = { ...receipt, kind, subject_type: wallet ? "participant" : "account", subject_id: id, participant_id: wallet ? id : null,
      details: { ...receipt.details, actor_id: null, event_reason, event_key: `financial:${kind}:9007199254740993:${kind === "balance_low" ? "0" : "9007199254740992"}` } };
    assert.ok(notification.safeParse(row).success);
    assert.ok(notification.safeParse({ ...row, visibility: "subject_summary", details: null, reason: "ACCEPTED" }).success);
    for (const change of [{ subject_type: "system" }, { participant_id: wallet ? null : id }, { details: { ...row.details, event_key: "financial:balance_low:3:4" } }, { details: { ...row.details, event_reason: "UPSTREAM_TIMEOUT" } }]) assert.equal(notification.safeParse({ ...row, ...change }).success, false);
  }
  const { credential, credential_action, ...fields } = input;
  const config = { ...fields, enabled: true, financial_enabled: true, from: "sender@owned.test", recipient: "admin@owned.test", secret_set: false, secret_storage_available: true, saved_at: null,
    capabilities: [...capabilities.map(item => ({ ...item, available: true, effective: true, reason: "SIMULATED_COMPLETE_BASIS_ONLY" })), { kind: "collection_failed", available: true, effective: true, reason: "CONFIRMED_COLLECTION_FAILURES_ONLY" }] };
  assert.ok(notificationConfiguration.safeParse(config).success);
  assert.equal(notificationConfiguration.safeParse({ ...config, financial_enabled: false }).success, false);
  assert.equal(notificationConfiguration.safeParse({ ...config, capabilities: [capabilities[0], ...config.capabilities.slice(1)] }).success, false);
  const { financial_enabled, ...obsolete } = input;
  assert.equal(notificationSaveInput.safeParse(obsolete).success, false);
});
