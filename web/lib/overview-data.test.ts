import assert from "node:assert/strict";
import { test } from "node:test";
import { pricingOperation } from "@sub-pool/contracts/pricing";
import { dispatchHistory } from "@sub-pool/contracts/dispatch";
import type { ConnectionConfiguration } from "@sub-pool/contracts/connection";
import { firstUseSteps, overviewReview } from "./overview-data";

const source = "A".repeat(21) + "Q";
const timestamp = "2026-11-11T00:00:00Z";
const pricing = (id: string, state = "needs_review", reason = "WRITE_RESULT_UNKNOWN") => pricingOperation.parse({
  id, revision: "2", actor_id: source, request_id: source, source_id: source, connection_revision: "2", group_id: "1", group_name: "Owned group",
  changes: [{ kind: "long_context", enabled: false, value: null }], state, reason, before_fingerprint: "a".repeat(64),
  after_fingerprint: null, created_at: timestamp, updated_at: timestamp,
});
const event = (id: string, pauseID: string, state = "needs_review", reason = "WRITE_RESULT_UNKNOWN") => {
  const result = dispatchHistory.parse({
  authority_revision: "1", next_after: null, events: [{ id, actor: "administrator", action: "pause_result", occurred_at: timestamp,
    pause: { id: pauseID, revision: id, account_id: source, source_id: source, connection_revision: "2", account_revision: "1",
      state, reason, previous_schedulable: true, native_updated_at: timestamp, restore_at: "2026-11-12T00:00:00Z", created_at: timestamp, updated_at: timestamp } }],
  }).events[0];
  assert.ok(result);
  return result;
};

test("overview counts unresolved operations once and uses the latest event per pause", () => {
  const summary = overviewReview(true, [pricing("1"), pricing("2", "rejected", "UPSTREAM_REJECTED"), pricing("3", "reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN")],
    [event("1", "1"), event("2", "1"), event("3", "2"), event("4", "2", "reviewed", "MANUALLY_REVIEWED"), event("5", "3", "paused", "VERIFIED_PAUSE")]);
  assert.equal(summary.count, 2);
  assert.match(summary.text, /计价 1 · 调度 1/);
  assert.match(summary.text, /非远端实时核验/);
});

test("overview requires complete reads, preserves real zero and discards partial counts", () => {
  assert.equal(overviewReview(true, [], []).count, 0);
  for (const [pricingRecords, dispatchEvents] of [[null, []], [[], null], [[pricing("1")], null], [null, [event("1", "1")]]] as const) {
    const summary = overviewReview(true, pricingRecords, dispatchEvents);
    assert.equal(summary.count, null); assert.match(summary.text, /不能推断/);
  }
  assert.equal(overviewReview(true, [pricing("1", "reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN")], [event("1", "1"), event("2", "1", "reviewed", "MANUALLY_REVIEWED")]).count, 0);
});

test("ordinary overview counts only authorized dispatch and never includes administrator pricing", () => {
  const summary = overviewReview(false, [pricing("1")], [event("1", "1")]);
  assert.equal(summary.count, 1);
  assert.match(summary.text, /仅当前授权内/); assert.match(summary.text, /非全局数量/); assert.doesNotMatch(summary.text, /计价/);
  assert.equal(overviewReview(false, null, []).count, 0);
  assert.equal(overviewReview(false, null, null).count, null);
});

const configured: ConnectionConfiguration = {
  revision: "7", source_id: source, base_url: "https://fixture.example", credential_set: true,
  secret_storage_available: true, credential_kind: "api_key", saved_at: timestamp,
  diagnostic: {
    saved: true, source_id: source, revision: "7", checked_at: timestamp, version: null, run_mode: null,
    checks: [{ id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 }],
  },
};
const accountEvidence = {
  source_id: source, source_current: true, identity_state: "verified", collection_state: "awaiting_collector",
  observed_connection_revision: "7",
} as const;
const setup: Parameters<typeof firstUseSteps>[0] = {
  timezoneSaved: true,
  connection: configured,
  catalog: { revision: "7", accounts: [accountEvidence] },
  policy: { enabled: true, usage_enabled: true },
  participantReady: true,
  poolReady: true,
};

test("first-use steps distinguish initial defaults, pending reads and confirmed setup", () => {
  const pending = firstUseSteps({ timezoneSaved: null, connection: null, catalog: null, policy: null, participantReady: null, poolReady: null });
  assert.deepEqual(pending.map(step => step.done), [true, null, null, null, null, null, null]);
  const fresh = { ...configured, source_id: null, base_url: "", credential_set: false, credential_kind: null, saved_at: null, diagnostic: null };
  assert.deepEqual(firstUseSteps({ ...setup, timezoneSaved: false, connection: fresh }).map(step => step.done),
    [true, false, false, false, false, false, false]);
  assert.ok(firstUseSteps(setup).every(step => step.done));
});

test("first-use connection proof requires the saved revision, source and successful account reading", () => {
  const diagnostic = configured.diagnostic;
  assert.ok(diagnostic);
  const denied = { id: "accounts.read", state: "denied", reason: "FORBIDDEN", http_status: 403, latency_ms: 1 } as const;
  for (const invalid of [null, { ...diagnostic, saved: false }, { ...diagnostic, revision: "6" },
    { ...diagnostic, source_id: "B".repeat(22) }, { ...diagnostic, checks: [denied] }]) {
    const steps = firstUseSteps({ ...setup, connection: { ...configured, diagnostic: invalid } });
    assert.equal(steps[2].done, true);
    assert.equal(steps[3].done, false);
  }
});

test("first-use scheduling and resource steps require current eligible evidence", () => {
  const states = (changes: Partial<typeof setup>) => firstUseSteps({ ...setup, ...changes }).slice(4).map(step => step.done);
  assert.deepEqual(states({ catalog: { revision: "6", accounts: [accountEvidence] } }), [null, null, true]);
  assert.deepEqual(states({ catalog: { revision: "7", accounts: [{ ...accountEvidence, source_current: false }] } }), [false, false, true]);
  assert.deepEqual(states({ catalog: { revision: "7", accounts: [{ ...accountEvidence, observed_connection_revision: "6" }] } }), [true, false, true]);
  assert.deepEqual(states({ policy: { enabled: true, usage_enabled: false } }), [true, false, true]);
  assert.deepEqual(states({ policy: { enabled: false, usage_enabled: true } }), [true, false, true]);
  assert.deepEqual(states({ policy: { enabled: true } }), [true, null, true]);
  assert.deepEqual(states({ participantReady: false }), [true, true, false]);
  assert.deepEqual(states({ poolReady: null }), [true, true, null]);
});
