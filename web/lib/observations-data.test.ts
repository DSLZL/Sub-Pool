import assert from "node:assert/strict";
import { test } from "node:test";
import { observationsOutputs } from "@sub-pool/contracts/observations";
import { usageSummary } from "@sub-pool/contracts/usage";
import { observationDecisionMatches, ObservationReadError, readObservationEvents, readObservationPages, readObservationSelection, type ObservationQuery } from "./observations-data";

const account = "a".repeat(21) + "A", other = "b".repeat(21) + "A";
const query: ObservationQuery = { account_id: "", source_id: "", source_mode: "all", validity: "all", start_at: "1970-01-01T00:00:00Z", end_at: "9998-12-31T23:59:59Z" };
function record(id: string, account_id = account, revision = "1") {
  return observationsOutputs.review.parse({ id, job_id: id, account_id, management_revision: revision, excluded: false, manual_start: false, review_reason: null, reviewed_at: null,
    snapshot: { source_id: account, connection_revision: "1", account_revision: "1", source_mode: "passive", source_at: null, received_at: "2026-11-07T12:00:00.123456Z",
      windows: [{ kind: "short", duration_seconds: null, reset_at: null, reset_expired: false, used_percent: "0" }] } });
}
function page(records = [record("9")], next_after: string | null = null) {
  return observationsOutputs.list.parse({ records, next_after, query_start_at: query.start_at, query_end_at: query.end_at, time_basis: "received_at" });
}
const management = () => observationsOutputs.state.parse({ account_id: account, revision: "1", manual_start: null, recalculation_state: "not_implemented" });
const detail = () => observationsOutputs.get.parse({ observation: record("9"), basis: { contract_id: null, members: [], unallocated_percent: null, time_basis: "unavailable", usage_state: "unavailable", estimate_state: "unavailable" } });

test("observation usage selection rejects shifted references and native intervals", async () => {
  const selected = detail();
  selected.observation.snapshot.source_at = "2026-11-07T12:00:00.123456Z";
  selected.observation.snapshot.windows = [{ kind: "short", duration_seconds: "10800", reset_at: "2026-11-07T13:00:00Z", used_percent: "10", reset_expired: false }];
  const metrics = Object.fromEntries(Object.keys(usageSummary.shape.metrics.shape).map(key => [key, null]));
  const evidence = observationsOutputs.usageEvidence.parse({ observation_id: "9", account_id: account, scope: "authorized_participants", basis: "native_window_interval", currency: "USD", timestamp_basis: "native_created_at", complete: false, settlement_eligible: false,
    windows: [{ kind: "short", start_at: "2026-11-07T10:00:00Z", end_at: selected.observation.snapshot.source_at, reason: "RECORDED_WINDOW_USAGE", capture: { id: "10", captured_at: "2026-11-07T12:01:00Z", state: "exhausted", summary: { request_count: "1", metrics: { ...metrics, total_cost: "0" } }, limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_SELECTED_CAPTURE", "QUOTA_CAUSALITY_UNVERIFIED", "ONLY_AUTHORIZED_PARTICIPANTS"] } }] });
  const read = (value = evidence) => readObservationSelection("9", account, async () => selected, async () => management(), null, null, async () => value);
  assert.equal((await read()).usage_evidence?.windows[0]?.capture?.summary.metrics.total_cost, "0");
  for (const changed of [{ ...evidence, observation_id: "10" }, { ...evidence, account_id: other }, { ...evidence, windows: [] },
    { ...evidence, windows: [{ ...evidence.windows[0]!, start_at: "2026-11-07T10:00:00.000000001Z" }] },
    { ...evidence, windows: [{ ...evidence.windows[0]!, end_at: "2026-11-07T12:00:00.123455999Z" }] },
  ]) await assert.rejects(read(changed), ObservationReadError);
  assert.equal((await readObservationSelection("9", account, async () => selected, async () => management(), null)).usage_evidence, null);
});

test("observations consume every numeric page and preserve actual zero, missing time and window evidence", async () => {
  const seen: string[] = [];
  const records = await readObservationPages(query, async after => {
    seen.push(after);
    return after === "0" ? page([record("9")], "9") : page([record("10"), record("9007199254740993")]);
  });
  assert.deepEqual(seen, ["0", "9"]);
  assert.deepEqual(records.map(record => record.id), ["9", "10", "9007199254740993"]);
  assert.equal(records[0]?.snapshot.windows[0]?.used_percent, "0");
  assert.equal(records[0]?.snapshot.windows[0]?.duration_seconds, null);
  assert.equal(records[0]?.snapshot.source_at, null);
  assert.deepEqual(await readObservationPages(query, async () => page([])), []);
});

test("observation read rejects failed later pages, shifted filters, repeated cursors and mixed management versions", async () => {
  await assert.rejects(readObservationPages(query, async after => {
    if (after === "0") return page([record("9")], "9");
    throw new TypeError("later page unavailable");
  }));
  await assert.rejects(readObservationPages(query, async after => after === "0" ? page([record("9")], "9") : page([record("10", account, "2")])), ObservationReadError);
  await assert.rejects(readObservationPages(query, async () => page([record("9")], "9")), ObservationReadError);
  await assert.rejects(readObservationPages(query, async () => ({ ...page(), query_end_at: "9998-12-30T23:59:59Z" })), ObservationReadError);
  for (const filter of [
    { account_id: other }, { source_id: other }, { source_mode: "active" as const }, { validity: "excluded" as const }, { validity: "manual_start" as const },
  ]) await assert.rejects(readObservationPages({ ...query, ...filter }, async () => page()), ObservationReadError);
  const excluded = { ...record("9"), excluded: true, review_reason: "Retained exclusion", reviewed_at: "2026-11-07T12:01:00Z" };
  await assert.rejects(readObservationPages({ ...query, validity: "included" }, async () => page([excluded])), ObservationReadError);
  let calls = 0;
  await assert.rejects(readObservationPages(query, async () => { calls++; return page([record(String(calls))], String(calls)); }), /2,000/);
  assert.equal(calls, 20);
});

test("observation detail binds current account, management revision and saved manual-start marker", async () => {
  const selected = await readObservationSelection("9", account, async () => detail(), async () => management(), null);
  assert.equal(selected.detail.basis.contract_id, null);
  for (const [id, boundAccount, changed] of [
    ["10", account, management()], ["9", other, management()], ["9", account, { ...management(), revision: "2" }],
  ] as const) await assert.rejects(readObservationSelection(id, boundAccount, async () => detail(), async () => changed, null), ObservationReadError);
  await assert.rejects(readObservationSelection("9", account, async () => ({ ...detail(), observation: { ...record("9"), manual_start: true } }), async () => management(), null), ObservationReadError);
  await assert.rejects(readObservationSelection("9", account, async () => detail(), async () => { throw new TypeError("state failed"); }, null));
});

test("estimate references bind the exact selection and distinguish no grant, absent reference and failed read", async () => {
  const reference = observationsOutputs.estimateLink.parse({ observation_id: "9", account_id: account, estimate: { id: "9007199254740993", generated_at: "2026-11-11T12:00:00.123456Z" } });
  const read = (estimate: (() => Promise<typeof reference>) | null) => readObservationSelection("9", account, async () => detail(), async () => management(), estimate);
  assert.equal((await read(null)).estimate_link, null);
  assert.deepEqual((await read(async () => reference)).estimate_link, reference);
  assert.equal((await read(async () => ({ ...reference, estimate: null }))).estimate_link?.estimate, null);
  for (const changed of [{ ...reference, observation_id: "10" }, { ...reference, account_id: other }]) await assert.rejects(read(async () => changed), ObservationReadError);
  await assert.rejects(read(async () => { throw new TypeError("supplementary read failed"); }), /supplementary read failed/);
  let resolve!: (value: typeof reference) => void;
  let finished = false;
  const pending = read(() => new Promise(yes => { resolve = yes; })).then(value => { finished = true; return value; });
  await Promise.resolve();
  assert.equal(finished, false);
  resolve(reference);
  assert.deepEqual((await pending).estimate_link, reference);
});

test("unknown-decision recovery requires complete account history and exact actor, version, action and reason", async () => {
  const event = observationsOutputs.events.parse({ account_id: account, next_after: null, events: [{ id: "9", account_id: account, revision: "2", action: "exclude", observation_id: "9", start_observation_id: null, end_observation_id: null,
    window_kind: null, reason: "Explicit exclusion", actor_id: account, occurred_at: "2026-11-07T12:01:00Z" }] }).events[0];
  assert.ok(event);
  const next = { ...event, id: "10", revision: "3", action: "restore" as const };
  const events = await readObservationEvents(account, async after => observationsOutputs.events.parse({ account_id: account, events: after === "0" ? [event] : [next], next_after: after === "0" ? "9" : null }));
  assert.deepEqual(events.map(event => event.id), ["9", "10"]);
  const decision = { kind: "exclude" as const, observation_id: "9", account_id: account, revision: "1", actor_id: account, reason: "Explicit exclusion" };
  assert.equal(observationDecisionMatches(event, decision), true);
  for (const changed of [{ ...event, actor_id: other }, { ...event, revision: "3" }, { ...event, action: "restore" as const }, { ...event, observation_id: "10" }, { ...event, reason: "Other reason" }]) assert.equal(observationDecisionMatches(changed, decision), false);
  await assert.rejects(readObservationEvents(account, async () => ({ account_id: other, events: [], next_after: null })), ObservationReadError);
  await assert.rejects(readObservationEvents(account, async () => ({ account_id: account, events: [{ ...event, revision: "3" }], next_after: null })), ObservationReadError);
  await assert.rejects(readObservationEvents(account, async after => {
    if (after === "0") return { account_id: account, events: [event], next_after: "9" };
    throw new TypeError("history page unavailable");
  }));
});


test("wallet recommendation references bind the selection and wait without converting failure to absence", async () => {
  const reference = observationsOutputs.recommendationLink.parse({ observation_id: "9", account_id: account, recommendation: { id: "9007199254740993", participant_id: other, estimate_id: "12", generated_at: "2026-11-13T12:00:00.123456Z" } });
  const read = (recommendation: (() => Promise<typeof reference>) | null) => readObservationSelection("9", account, async () => detail(), async () => management(), null, recommendation);
  assert.equal((await read(null)).recommendation_link, null);
  assert.deepEqual((await read(async () => reference)).recommendation_link, reference);
  assert.equal((await read(async () => ({ ...reference, recommendation: null }))).recommendation_link?.recommendation, null);
  for (const changed of [{ ...reference, observation_id: "10" }, { ...reference, account_id: other }]) await assert.rejects(read(async () => changed), ObservationReadError);
  await assert.rejects(read(async () => { throw new TypeError("recommendation read failed"); }), /recommendation read failed/);
  let resolve!: (value: typeof reference) => void;
  let finished = false;
  const pending = read(() => new Promise(yes => { resolve = yes; })).then(value => { finished = true; return value; });
  await Promise.resolve();
  assert.equal(finished, false);
  resolve(reference);
  assert.deepEqual((await pending).recommendation_link, reference);
});
