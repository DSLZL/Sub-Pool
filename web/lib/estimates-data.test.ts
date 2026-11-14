import assert from "node:assert/strict";
import { test } from "node:test";
import { createEstimatesClient } from "@sub-pool/contracts/client";
import { estimateRecord, type EstimateCurrent } from "@sub-pool/contracts/estimates";
import { managedView } from "@sub-pool/contracts/accounts";
import { estimateCard, estimateConfigurationMatches, estimateHistoryFilter, estimateRecomputeInput, estimateRecomputeMatches, estimateSave, readEstimateData, readEstimateHistory, readEstimateRecord, reconcileEstimateConfiguration, reconcileEstimateRecompute } from "./estimates-data";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A";
function record(result = "10", revision = "2") {
  return estimateRecord.parse({ id: result, account_id: id, generated_at: "2026-11-07T12:00:00.0000002Z", rules_version: "gate-003-rules-v1",
    configuration: { account_id: id, revision, saved_at: "2026-11-07T12:00:00.0000001Z", assumptions: { plan: "empirical_min_v1", cost_basis: "standard_usd", keep_ratio: "0.812345678901234567", premise: "Declared premise is retained exactly", windows: ["short"], source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive" } },
    trigger: "configuration", policy_revision: "1", management_revision: "1", observation_head: "3", capture_head: "0", state: "evidence_incomplete", remaining: null, currency: "USD", complete: false, settlement_eligible: false, model_calibrated: false,
    limitations: ["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"],
    windows: [{ kind: "short", basis: "official", period: { kind: "short", start_at: "2026-11-07T10:00:00Z", end_at: "2026-11-07T13:00:00Z", duration_seconds: "10800", basis: "native_reset_and_duration" }, manual_start_at: null, manual_end_at: null, observation_ids: ["1", "2", "3"], current_observation_id: "3", source_at: "2026-11-07T12:00:00Z", oldest_sample_at: "2026-11-07T11:58:00Z", current_used_percent: "0", state: "evidence_incomplete", capacity: null, remaining: null, range_min: null, range_max: null }] });
}
function current(latest = record()): EstimateCurrent { return { configuration: latest.configuration, latest, current_state: latest.state, current_remaining: null, needs_recalculation: false, checked_at: "2026-11-07T12:00:01Z" }; }
const account = { id, name: "Own recorded estimate account" };
const reference = managedView.parse({ connection_revision: "1", account: {
  id, revision: "1", source_id: id, upstream_id: "1", name: account.name, monitoring: false, source_mode: "passive", source_current: true, connection_available: true, observed_connection_revision: "1", identity_state: "verified", collection_state: "paused",
  snapshot: { account: { upstream_id: "1", name: "Synthetic source", platform: "openai", type: "oauth", status: "active", schedulable: true, eligibility: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" }, runtime: { last_used_at: null, rate_limited_at: null, rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, error_present: false } },
  read: { id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 }, last_attempt_at: "2026-11-07T12:00:00Z", observed_at: "2026-11-07T12:00:00Z", created_at: "2026-11-07T12:00:00Z", saved_at: "2026-11-07T12:00:00Z",
} });
const declared = { keep_ratio: "0.812345678901234567", premise: "Declared premise is retained exactly" };

test("estimate reads preserve large numeric history, scoped bindings and unavailable money through the real client", async () => {
  const records = [record("9"), record("10"), record("9007199254740993")], calls: string[] = [];
  const client = createEstimatesClient("https://pool.test", async (input, init) => {
    const request = new Request(input, init), body = await request.json(); calls.push(request.url);
    assert.equal(init?.cache, "no-store"); assert.equal(init?.credentials, "same-origin"); assert.equal(init?.redirect, "error");
    if (request.url.endsWith("/current")) return Response.json(current(records[2]));
    assert.equal(body.account_id, id);
    return Response.json({ account_id: id, records: body.after === "0" ? records.slice(0, 2) : records.slice(2), next_after: body.after === "0" ? "10" : null });
  });
  const data = await readEstimateData([account], client, true, new AbortController().signal);
  assert.deepEqual(data.history.map(value => value.id), ["9007199254740993", "10", "9"]); assert.equal(calls.length, 3);
  const entry = data.entries[0]; assert.ok(entry);
  const card = estimateCard(entry); assert.equal(card.capacity, null); assert.equal(card.remaining, null); assert.match(card.basis, /金额仍未知/); assert.match(card.basis, /源时间/); assert.match(card.cycle, /10800 秒/);
  assert.equal(card.premise, records[0]?.configuration.assumptions?.premise); assert.match(card.basis, /原始使用 0%/); assert.match(card.basis, /选用观测 1、2、3/);
  calls.length = 0; await readEstimateData([account], client, false, new AbortController().signal); assert.equal(calls.length, 1);
});

test("failed, changing, duplicate or excessive estimate pages never become partial history", async () => {
  const ten = record();
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: other, records: [], next_after: null })));
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: id, records: [record("11")], next_after: null })));
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: id, records: [record("9")], next_after: null })));
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: id, records: [ten, ten], next_after: null })));
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: id, records: [ten], next_after: "9" })));
  let reads = 0;
  await assert.rejects(readEstimateHistory(id, "2001", async after => {
    reads++; const start = Number(after), count = Math.min(100, 2001 - start), records = Array.from({ length: count }, (_, offset) => record(String(start + offset + 1)));
    return { account_id: id, records, next_after: start + count < 2001 ? String(start + count) : null };
  }), /2,000/); assert.equal(reads, 21);
  const client = createEstimatesClient("https://pool.test", async (input, init) => {
    const body = await new Request(input, init).json(); if (body.account_id === other) throw new TypeError("second account unavailable"); return Response.json(current());
  });
  await assert.rejects(readEstimateData([account, { id: other, name: "Other" }], client, false, new AbortController().signal));
});

test("existing window edits preserve deliberately retained exact assumptions without invented defaults", () => {
  const view = { account, current: current() }, input = estimateSave(view, "both", declared, reference);
  assert.equal(input.keep_ratio, "0.812345678901234567"); assert.equal(input.premise, "Declared premise is retained exactly"); assert.deepEqual(input.windows, ["seven_day", "short"]);
  assert.equal(Object.hasOwn(input, "source_mode"), false);
  const empty = { ...view.current, latest: null, configuration: { account_id: id, revision: "1", assumptions: null, saved_at: null } };
  assert.throws(() => estimateSave({ account, current: empty }, "short", { keep_ratio: "", premise: "" }, reference), /不能代填默认值/);
  const saved = record("11", "3"); const single = estimateSave(view, "short", declared, reference); assert.equal(estimateConfigurationMatches(saved, single), true);
  for (const changed of [{ ...single, keep_ratio: "0.8" }, { ...single, source_id: other }, { ...single, account_id: other }, { ...single, revision: "1" }, input]) assert.equal(estimateConfigurationMatches(saved, changed), false);
});

test("lost configuration acknowledgements reconcile only by scoped persisted reads without replay", async () => {
  const input = estimateSave({ account, current: current() }, "short", declared, reference), saved = record("11", "3"), calls: string[] = [];
  const client = createEstimatesClient("https://pool.test", async (input, init) => {
    const request = new Request(input, init); calls.push(request.url);
    if (request.url.endsWith("/current")) return Response.json(current(saved));
    if (request.url.endsWith("/record")) return Response.json(saved);
    assert.ok(request.url.endsWith("/history")); return Response.json({ account_id: id, records: [record(), saved], next_after: null });
  });
  const recovered = await reconcileEstimateConfiguration(input, null, client, new AbortController().signal);
  assert.equal(recovered.persisted.id, saved.id); assert.equal(recovered.changed, false); assert.equal(calls.some(path => path.endsWith("/configure")), false);
  calls.length = 0; await reconcileEstimateConfiguration(input, saved, client, new AbortController().signal); assert.equal(calls.some(path => path.endsWith("/history")), false);
  await assert.rejects(reconcileEstimateConfiguration(input, saved, { current: client.current, history: client.history, record: async () => ({ ...saved, id: "12" }) }, new AbortController().signal), /尚未读到相符/);
  await assert.rejects(reconcileEstimateConfiguration({ ...input, keep_ratio: "0.9" }, null, client, new AbortController().signal), /尚未读到相符/);
});

test("first estimate configuration requires explicit exact fields and a current verified account reference", () => {
  const empty: EstimateCurrent = { configuration: { account_id: id, revision: "1", assumptions: null, saved_at: null }, latest: null, current_state: "not_configured", current_remaining: null, needs_recalculation: false, checked_at: "2026-11-07T12:00:01Z" };
  const entry = { account, current: empty }, draft = { keep_ratio: "0.812345678901234567", premise: "Explicit local scenario premise" };
  const input = estimateSave(entry, "both", draft, reference);
  assert.equal(input.keep_ratio, draft.keep_ratio); assert.equal(input.premise, draft.premise); assert.equal(input.source_id, reference.account.source_id);
  assert.equal(input.connection_revision, reference.connection_revision); assert.equal(input.account_revision, reference.account.revision); assert.equal(input.revision, "1");
  assert.deepEqual(input.windows, ["seven_day", "short"]); assert.equal(Object.hasOwn(input, "source_mode"), false);
  for (const change of [{ keep_ratio: "" }, { keep_ratio: "0" }, { keep_ratio: "1.000000000000000001" }, { keep_ratio: "0.1234567890123456789" }, { premise: "" }, { premise: "\n" }]) assert.throws(() => estimateSave(entry, "short", { ...draft, ...change }, reference));
  for (const changed of [{ ...reference, connection_revision: "2" }, { ...reference, account: { ...reference.account, id: other } }, { ...reference, account: { ...reference.account, source_current: false } }, { ...reference, account: { ...reference.account, identity_state: "unverified" as const } }]) assert.throws(() => estimateSave(entry, "short", draft, changed));
  assert.equal(empty.configuration.assumptions, null); assert.equal(draft.keep_ratio, "0.812345678901234567");
});

test("filtered history uses exact native endpoints, permits an excluded current head and rechecks its version", async () => {
  const old = record("8"), next = record("9"), head = record("10");
  const latest = estimateRecord.parse({ ...head, configuration: { ...head.configuration, assumptions: { ...head.configuration.assumptions, windows: ["seven_day"] } }, windows: head.windows.map(window => ({ ...window, kind: "seven_day", period: { ...window.period, kind: "seven_day" } })) });
  const filter = estimateHistoryFilter("short", "2026-11-07T13:00:00.000000000Z"), requests: { path: string; body: unknown }[] = [];
  let reads = 0, changed = false;
  const client = createEstimatesClient("https://pool.test", async (input, init) => {
    const request = new Request(input, init), body = await request.json(); requests.push({ path: request.url, body });
    if (request.url.endsWith("/current")) { reads++; return Response.json({ ...current(changed && reads % 2 === 0 ? record("11") : latest), current_state: reads % 2 === 0 ? "source_stale" : "evidence_incomplete" }); }
    assert.equal(body.window_kind, filter.window_kind); assert.equal(body.period_end_at, filter.period_end_at);
    return Response.json({ account_id: id, records: body.after === "0" ? [old] : [next], next_after: body.after === "0" ? "8" : null });
  });
  const data = await readEstimateData([account], client, true, new AbortController().signal, filter);
  assert.deepEqual(data.history.map(value => value.id), ["9", "8"]); assert.equal(reads, 2); assert.equal(requests.length, 4); assert.equal(data.entries[0]?.current.current_state, "source_stale");
  changed = true;
  await assert.rejects(readEstimateData([account], client, true, new AbortController().signal, filter), /版本已变化/);
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: id, records: [latest], next_after: null }), filter), /不符筛选/);
  await assert.rejects(readEstimateHistory(id, "10", async () => ({ account_id: id, records: [old], next_after: null }), { ...filter, period_end_at: "2026-11-07T13:00:00.000000001Z" }), /不符筛选/);
  assert.deepEqual(await readEstimateHistory(id, "10", async () => ({ account_id: id, records: [], next_after: null }), filter), []);
});

test("invalid official-period filters cannot dispatch a current or history request", async () => {
  let requests = 0;
  const client = createEstimatesClient("https://pool.test", async () => { requests++; throw new Error("must not dispatch"); });
  for (const [kind, end] of [["all", "2026-11-07T13:00:00Z"], ["short", "2026-02-30T13:00:00Z"], ["short", "2026-11-07T13:00:00+00:00"], ["short", "1969-11-07T13:00:00Z"], ["seven_day", "2026-11-07T13:00:00.0000000001Z"]] as const) {
    await assert.rejects(readEstimateData([account], client, true, new AbortController().signal, { window_kind: kind, period_end_at: end }));
  }
  assert.equal(requests, 0); assert.deepEqual(estimateHistoryFilter("seven_day", ""), { window_kind: "seven_day", period_end_at: "" });
});

test("immutable detail and recompute acknowledgements bind account, ID, configuration and original trigger", async () => {
  const before = record(), input = estimateRecomputeInput({ account, current: current(before) }), saved = estimateRecord.parse({ ...record("11"), trigger: "manual_recompute" });
  assert.deepEqual(input, { account_id: id, revision: "2", latest_id: "10" });
  assert.equal(estimateRecomputeMatches(saved, input), true);
  for (const changed of [{ ...input, account_id: other }, { ...input, revision: "3" }, { ...input, latest_id: "11" }]) assert.equal(estimateRecomputeMatches(saved, changed), false);
  assert.equal(estimateRecomputeMatches(record("11"), input), false);
  assert.deepEqual(await readEstimateRecord(id, "11", async () => saved), saved);
  await assert.rejects(readEstimateRecord(other, "11", async () => saved));
  await assert.rejects(readEstimateRecord(id, "12", async () => saved));
  const unconfigured: EstimateCurrent = { ...current(), latest: null, configuration: { account_id: id, revision: "1", assumptions: null, saved_at: null }, current_state: "not_configured" };
  assert.throws(() => estimateRecomputeInput({ account, current: unconfigured }), /先明确保存/);
});

test("known recompute receipts recover exact immutable records while a lost acknowledgement never attributes another request or replays", async () => {
  const input = estimateRecomputeInput({ account, current: current() }), saved = estimateRecord.parse({ ...record("11"), trigger: "manual_recompute" }), calls: string[] = [];
  let mismatched = false;
  const client = createEstimatesClient("https://pool.test", async (input, init) => {
    const request = new Request(input, init); calls.push(request.url);
    if (request.url.endsWith("/current")) return Response.json(current(saved));
    if (request.url.endsWith("/record")) return Response.json(mismatched ? { ...saved, observation_head: "4" } : saved);
    assert.ok(request.url.endsWith("/history")); return Response.json({ account_id: id, records: [record(), saved], next_after: null });
  });
  const recovered = await reconcileEstimateRecompute(input, saved, client, new AbortController().signal);
  assert.equal(recovered.persisted.id, "11"); assert.equal(recovered.changed, false); assert.equal(calls.some(path => path.endsWith("/history")), false);
  mismatched = true; await assert.rejects(reconcileEstimateRecompute(input, saved, client, new AbortController().signal), /未完成核对/);
  calls.length = 0;
  await assert.rejects(reconcileEstimateRecompute(input, null, client, new AbortController().signal), /可能相符.*11.*归属/);
  assert.equal(calls.some(path => path.endsWith("/history")), true); assert.equal(calls.some(path => path.endsWith("/recompute") || path.endsWith("/configure")), false);
});
