import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { createObservationsClient } from "./client";
import { observation, observationManagement, observationsOutputs } from "./observations";
import { usageSummary } from "./usage";

const id = "a".repeat(21) + "A", otherID = "b".repeat(21) + "A";
const at = "2026-11-07T12:00:00.0000001Z", later = "2026-11-07T12:01:00.0000002Z", reset = "2026-11-07T13:00:00Z";
const record = {
  id: "9007199254740993", job_id: "1", account_id: id, management_revision: "2", excluded: false, manual_start: true, review_reason: null, reviewed_at: null,
  snapshot: { source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive", source_at: at, received_at: "2026-11-07T12:00:00.000001Z",
    windows: [{ kind: "short", duration_seconds: "10800", used_percent: "0", reset_at: reset, reset_expired: false }] },
};
const end = { ...record, id: "9007199254740994", manual_start: false, job_id: "2", snapshot: { ...record.snapshot, source_at: later, received_at: "2026-11-07T12:01:00.000001Z" } };
const event = { id: "1", account_id: id, revision: "2", action: "start", observation_id: null, start_observation_id: record.id, end_observation_id: end.id,
  window_kind: "short", reason: "Verified native endpoints", actor_id: id, occurred_at: "2026-11-07T12:02:00Z" };
const state = { account_id: id, revision: "2", recalculation_state: "not_implemented", manual_start: { event, start: record, end,
  period: { kind: "short", start_at: "2026-11-07T10:00:00Z", end_at: reset, duration_seconds: "10800", basis: "native_reset_and_duration" }, usable: true, reason: "VERIFIED_LOCAL_RANGE" } };
const query = { account_id: "", source_id: "", source_mode: "all" as const, validity: "all" as const, start_at: "2026-11-07T00:00:00Z", end_at: "2026-11-08T00:00:00Z", after: "0", limit: 3 };

test("observation usage evidence retains independent exact costs, scoped limitations and nullable absence", async () => {
  const metrics = Object.fromEntries(Object.keys(usageSummary.shape.metrics.shape).map(key => [key, null]));
  const capture = { id: "9007199254740995", captured_at: later, state: "partial", summary: { request_count: "2", metrics: { ...metrics, total_cost: "9007199254740993.000000000000000001", actual_cost: "0", account_stats_cost: null } },
    limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_SELECTED_CAPTURE", "QUOTA_CAUSALITY_UNVERIFIED", "ONLY_AUTHORIZED_PARTICIPANTS"] };
  const value = { observation_id: record.id, account_id: id, scope: "authorized_participants", basis: "native_window_interval", currency: "USD", timestamp_basis: "native_created_at", complete: false, settlement_eligible: false,
    windows: [{ kind: "short", start_at: "2026-11-07T10:00:00Z", end_at: at, reason: "RECORDED_WINDOW_USAGE", capture }] };
  let calls = 0;
  const client = createObservationsClient("https://example.test", async (input, init) => {
    calls++; const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/observations/usage-evidence"); assert.equal(request.method, "POST");
    assert.deepEqual(await request.json(), { id: record.id }); return Response.json(value);
  });
  assert.deepEqual(await client.usageEvidence({ id: record.id }), value); assert.equal(calls, 1);
  const parsed = observationsOutputs.usageEvidence.parse(value);
  assert.equal(parsed.windows[0]?.capture?.summary.metrics.actual_cost, "0");
  assert.equal(parsed.windows[0]?.capture?.summary.metrics.account_stats_cost, null);
  for (const window of [{ ...value.windows[0], reason: "NO_RECORDED_WINDOW_CAPTURE", capture: null }, { kind: "short", start_at: null, end_at: null, reason: "NATIVE_WINDOW_UNVERIFIED", capture: null }]) assert.ok(observationsOutputs.usageEvidence.safeParse({ ...value, windows: [window] }).success);
  for (const changed of [
    { ...value, complete: true }, { ...value, settlement_eligible: true }, { ...value, windows: [...value.windows, ...value.windows] },
    { ...value, windows: [{ ...value.windows[0], capture: null }] }, { ...value, windows: [{ ...value.windows[0], start_at: at }] },
    { ...value, windows: [{ ...value.windows[0], capture: { ...capture, participant_name: "PRIVATE_SENTINEL" } }] },
    { ...value, windows: [{ ...value.windows[0], capture: { ...capture, limitations: capture.limitations.slice(0, -1) } }] },
    { ...value, windows: [{ ...value.windows[0], capture: { ...capture, summary: { ...capture.summary, metrics: { ...capture.summary.metrics, total_cost: -1 } } } }] },
  ]) assert.equal(observationsOutputs.usageEvidence.safeParse(changed).success, false);
});

test("estimate links use a strict fixed read and retain immutable reference precision without money", async () => {
  const link = { observation_id: record.id, account_id: id, estimate: { id: "9007199254740995", generated_at: at } };
  let calls = 0;
  const client = createObservationsClient("https://example.test", async (input, init) => {
    calls++;
    const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/observations/estimate-link");
    assert.equal(request.method, "POST");
    assert.equal(init?.cache, "no-store");
    assert.equal(init?.credentials, "same-origin");
    assert.equal(init?.redirect, "error");
    assert.deepEqual(await request.json(), { id: record.id });
    return Response.json(link);
  });
  for (const invalid of ["0", "01", "9223372036854775808"]) await assert.rejects(client.estimateLink({ id: invalid }));
  assert.equal(calls, 0);
  assert.deepEqual(await client.estimateLink({ id: record.id }), link);
  assert.equal(calls, 1);
  assert.ok(observationsOutputs.estimateLink.safeParse({ ...link, estimate: null }).success);
  for (const changed of [{ ...link, estimate: undefined }, { ...link, estimate: { ...link.estimate, amount: "0" } }, { ...link, observation_id: Number(record.id) }, { ...link, estimate: { id: "1" } }]) assert.equal(observationsOutputs.estimateLink.safeParse(changed).success, false);
});

test("observation contracts retain raw precision and reject invented periods, fees and cursor order", () => {
  assert.equal(observation.parse(record).id, "9007199254740993");
  assert.ok(observationManagement.safeParse(state).success);
  const page = { records: [record, end], next_after: end.id, query_start_at: query.start_at, query_end_at: query.end_at, time_basis: "received_at" };
  assert.ok(observationsOutputs.list.safeParse(page).success);
  assert.equal(observationsOutputs.list.safeParse({ ...page, records: [end, record], next_after: record.id }).success, false);
  assert.equal(observationsOutputs.list.safeParse({ ...page, next_after: "2" }).success, false);
  assert.equal(observationsOutputs.list.safeParse({ ...page, query_start_at: record.snapshot.received_at, query_end_at: record.snapshot.received_at }).success, false);
  assert.equal(observationsOutputs.list.safeParse({ ...page, query_end_at: record.snapshot.received_at }).success, false);
  assert.equal(observation.safeParse({ ...record, excluded: true }).success, false);
  assert.equal(observation.safeParse({ ...record, snapshot: { ...record.snapshot, credentials: "secret" } }).success, false);
  assert.equal(observation.safeParse({ ...record, snapshot: { ...record.snapshot, windows: [{ ...record.snapshot.windows[0], used_percent: 0 }] } }).success, false);
  for (const modify of [
    (value: typeof state) => { value.manual_start.period.duration_seconds = "604800"; },
    (value: typeof state) => { value.manual_start.start.snapshot.windows[0] = { ...value.manual_start.start.snapshot.windows[0], kind: "seven_day", duration_seconds: "604800", used_percent: "0", reset_at: reset, reset_expired: false }; },
    (value: typeof state) => { value.manual_start.end.snapshot.source_at = value.manual_start.start.snapshot.source_at; },
    (value: typeof state) => { value.manual_start.start.snapshot.source_at = "2026-11-07T12:00:00.000002Z"; },
    (value: typeof state) => { value.manual_start.event.account_id = otherID; },
    (value: typeof state) => { value.manual_start.event.start_observation_id = end.id; },
    (value: typeof state) => { value.manual_start.event.revision = "1"; },
    (value: typeof state) => { value.manual_start.start.excluded = true; },
    (value: typeof state) => { value.manual_start.start.manual_start = false; },
    (value: typeof state) => { value.manual_start.end.manual_start = true; },
    (value: typeof state) => { value.manual_start.reason = "SOURCE_CHANGED"; },
    (value: typeof state) => { value.revision = "3"; },
    (value: typeof state) => { value.recalculation_state = "completed"; },
  ]) {
    const changed = structuredClone(state);
    modify(changed);
    assert.equal(observationManagement.safeParse(changed).success, false);
  }
  const detail = { observation: record, basis: { contract_id: "1", members: [{ participant_id: id, name: "😀".repeat(128), share_percent: "33.333333" }], unallocated_percent: null,
    time_basis: "recorded_local_contract_at_source_time", usage_state: "unavailable", estimate_state: "unavailable" } };
  assert.ok(observationsOutputs.get.safeParse(detail).success);
  assert.ok(observationsOutputs.get.safeParse({ ...detail, basis: { ...detail.basis, unallocated_percent: "66.666667" } }).success);
  assert.equal(observationsOutputs.get.safeParse({ ...detail, basis: { ...detail.basis, unallocated_percent: "66.666666" } }).success, false);
  assert.equal(observationsOutputs.get.safeParse({ ...detail, basis: { ...detail.basis, members: [...detail.basis.members, ...detail.basis.members] } }).success, false);
  assert.equal(observationsOutputs.get.safeParse({ ...detail, basis: { ...detail.basis, actual_cost: "0" } }).success, false);
  assert.equal(observationsOutputs.get.safeParse({ ...detail, basis: { ...detail.basis, contract_id: null } }).success, false);
  assert.equal(observationsOutputs.events.safeParse({ account_id: id, events: [event, { ...event, id: "2", account_id: otherID }], next_after: null }).success, false);
});

test("observation client uses fixed validated routes and never retries a lost mutation", async () => {
  let calls = 0;
  const client = createObservationsClient("https://example.test", async (input, init) => {
    calls++;
    const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/observations/start");
    assert.equal(request.method, "POST");
    assert.equal(request.credentials, "same-origin");
    assert.equal(request.redirect, "error");
    assert.equal(request.cache, "no-store");
    throw new TypeError("connection lost after dispatch");
  });
  const start = { account_id: id, revision: "1", start_observation_id: record.id, end_observation_id: end.id, window_kind: "short" as const, reason: "Actual native range" };
  await assert.rejects(client.start({ ...start, end_observation_id: record.id }));
  await assert.rejects(client.start({ ...start, reason: " " }));
  await assert.rejects(client.start({ ...start, reason: "😀".repeat(129) }));
  assert.equal(calls, 0);
  await assert.rejects(client.start(start));
  assert.equal(calls, 1);
  for (const invalid of [
    { ...query, start_at: "2026-11-07T00:00:00+00:00" },
    { ...query, start_at: "2026-11-07T00:00:00.1234567890Z" },
    { ...query, start_at: "1969-12-31T00:00:00Z" },
    { ...query, start_at: query.end_at },
    { ...query, after: "01" },
    { ...query, after: "9223372036854775808" },
  ]) await assert.rejects(client.list(invalid));
  assert.equal(calls, 1);
  const blocked = createObservationsClient("https://example.test", async () => Response.json({ defined: true, code: "OBSERVATION_RANGE_UNVERIFIED", status: 409, message: "Request could not be completed.", data: { retry_at: null } }, { status: 409 }));
  await assert.rejects(blocked.start(start), error => error instanceof ORPCError && error.defined && error.status === 409 && error.code === "OBSERVATION_RANGE_UNVERIFIED");
  const forged = createObservationsClient("https://example.test", async () => Response.json({ ...state, recalculation_state: "completed" }));
  await assert.rejects(forged.state({ account_id: id }));
  assert.throws(() => createObservationsClient("https://example.test/redirect"));
});


test("recommendation links retain an exact immutable chain through one strict fixed read", async () => {
  const link = { observation_id: record.id, account_id: id, recommendation: { id: "9007199254740995", participant_id: otherID, estimate_id: "9007199254740994", generated_at: at } };
  let calls = 0;
  const client = createObservationsClient("https://example.test", async (input, init) => {
    calls++;
    const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/observations/recommendation-link");
    assert.equal(request.method, "POST");
    assert.equal(init?.cache, "no-store");
    assert.equal(init?.credentials, "same-origin");
    assert.equal(init?.redirect, "error");
    assert.deepEqual(await request.json(), { id: record.id });
    return Response.json(link);
  });
  for (const invalid of ["0", "01", "9223372036854775808"]) await assert.rejects(client.recommendationLink({ id: invalid }));
  assert.equal(calls, 0);
  assert.deepEqual(await client.recommendationLink({ id: record.id }), link);
  assert.equal(calls, 1);
  assert.ok(observationsOutputs.recommendationLink.safeParse({ ...link, recommendation: null }).success);
  for (const recommendation of [undefined, { ...link.recommendation, id: 9007199254740995 }, { ...link.recommendation, estimate_id: "01" }, { ...link.recommendation, participant_id: "" }, { ...link.recommendation, target: "0" }, { ...link.recommendation, generated_at: "unknown" }]) assert.equal(observationsOutputs.recommendationLink.safeParse({ ...link, recommendation }).success, false);
  assert.equal(observationsOutputs.recommendationLink.safeParse({ ...link, amount: "0" }).success, false);
  const invalid = createObservationsClient("https://example.test", async () => { calls++; return Response.json({ ...link, recommendation: { ...link.recommendation, estimate_id: "01" } }); });
  await assert.rejects(invalid.recommendationLink({ id: record.id }));
  assert.equal(calls, 2);
});
