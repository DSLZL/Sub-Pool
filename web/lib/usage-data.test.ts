import { test } from "node:test";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { participantsOutputs, type AttributedUsage } from "@sub-pool/contracts/participants";
import { LineChart } from "@/components/charts";
import { Val } from "@/components/ui";
import { millionTokens, readUsageData, usageDayCost, usageTrend, usageWindow } from "./usage-data";
import { proxyAPI } from "./api-proxy";

const id = (n: number) => { const bytes = Buffer.alloc(16); bytes.writeUInt32BE(n, 12); return bytes.toString("base64url"); };
const window = { start_at: "2026-11-01T00:00:00Z", end_at: "2026-11-03T00:00:00Z" };
const settings = { timezone: "UTC", revision: "3" };
const metrics = { input_tokens: "9007199254740993", output_tokens: "0", cache_creation_tokens: null, cache_read_tokens: null,
  cache_creation_5m_tokens: null, cache_creation_1h_tokens: null, image_input_tokens: null, image_output_tokens: null,
  total_cost: "9007199254740993.000000000000000001", actual_cost: "0", account_stats_cost: null };
const summary = { request_count: "3", metrics };
const records = ["9.9", "9.10", "10.9007199254740993"].map((cursor, index) => {
  const [capture_id, factID] = cursor.split(".");
  return { capture_id, account_id: id(index === 2 ? 2 : 1), attribution: { participant_id: id(3), binding_id: "1", participant_name: "Recorded member", basis: "local_binding_at_log_time" },
    fact: { id: factID, user_id: "21", api_key_id: "9007199254740993", group_id: null, subscription_id: null, request_id: null,
      model: "requested", upstream_model: "forwarded", upstream_response_model: null, billing_mode: null, billing_type: 0, billing_candidate: "wallet", deduction_state: "unknown",
      rate_multiplier: null, account_rate_multiplier: null, created_at: "2026-11-01T12:00:00.000000001Z", metrics } };
});
const view = (page: number, all = false): AttributedUsage => participantsOutputs.window.parse({
  start_at: window.start_at, end_at: "2026-11-04T00:00:00Z", query_start_at: window.start_at, query_end_at: window.end_at,
  oldest_capture_at: window.end_at, newest_capture_at: window.end_at, summary, assigned: summary, unassigned: null, unassigned_visible: false,
  groups: [1, 2].map((account, index) => ({ account_id: id(account), participant_id: id(3), basis: "local_binding_at_log_time", ...summary, request_count: index ? "1" : "2" })),
  grain: "day", buckets: [{ ...window, end_at: "2026-11-02T00:00:00Z", ...summary }], records: all ? records : [records[page]], next_after: all || page === 2 ? null : `${records[page].capture_id}.${records[page].fact.id}`,
  complete: false, settlement_eligible: false, currency: "USD", timestamp_basis: "native_created_at",
  limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"],
});
const accounts = [1, 2].map(n => ({ id: id(n) }));
const captures = async (account: string) => ({ captures: [{ id: account === id(1) ? "9" : "10", account_id: account, start_at: window.start_at, end_at: "2026-11-04T00:00:00Z", captured_at: window.end_at }], next_after: null });
const pageFor = (after: string) => after === "0" ? 0 : after === "9.9" ? 1 : 2;
const historyView = (page: number, all = false) => participantsOutputs.history.parse({ ...view(page, all), history: {
  basis: "selected_captures", timezone: "UTC", settings_revision: settings.revision, authority_revision: "7", selections: accounts.map(account => ({
    id: account.id === id(1) ? "9" : "10", account_id: account.id, start_at: window.start_at, end_at: "2026-11-04T00:00:00Z", captured_at: window.end_at,
  })),
} });

test("usage windows use actual UTC calendar bounds and exact million-token text", () => {
  assert.deepEqual(usageWindow(7, new Date("2026-11-07T23:00:00-08:00")), { start_at: "2026-11-02T00:00:00.000Z", end_at: "2026-11-08T07:00:00.000Z" });
  assert.equal(usageWindow(14, new Date("2026-11-08T07:00:00Z")).start_at, "2026-10-26T00:00:00.000Z");
  assert.equal(millionTokens("9007199254740993"), "9007199254.740993");
  assert.equal(millionTokens("1"), "0.000001");
  assert.equal(millionTokens("0"), "0");
  assert.equal(millionTokens(null), null);
  assert.throws(() => millionTokens("1.5"));
});

test("usage reads all numeric record pages and keeps server amounts and participant buckets", async () => {
  const calls: { ids: string[]; participant: string; after: string }[] = [];
  const data = await readUsageData(accounts, captures, async (ids, participant, after) => {
    calls.push({ ids, participant, after });
    return historyView(pageFor(after));
  }, window, "7", settings);
  assert.deepEqual(data.records.map(record => record.fact.id), ["9", "10", "9007199254740993"]);
  assert.deepEqual(calls.map(call => [call.participant, call.after]), [["", "0"], ["", "9.9"], ["", "9.10"], [id(3), "0"]]);
  assert.ok(calls.every(call => call.ids.join() === "9,10"));
  assert.equal(data.view?.summary.metrics.total_cost, metrics.total_cost);
  assert.equal(data.byParticipant[id(3)].summary.metrics.actual_cost, "0");
  assert.deepEqual(usageTrend(window, data.byParticipant[id(3)].buckets), { labels: ["2026-11-01", "2026-11-02"], values: ["0", null] });
});

test("saved-zone windows and trends follow civil days across DST and missing midnights", () => {
  assert.equal(usageWindow(7, new Date("2026-03-10T12:00:00Z"), "America/Los_Angeles").start_at, "2026-03-04T08:00:00.000Z");
  assert.equal(usageWindow(7, new Date("2026-09-12T12:00:00Z"), "America/Santiago").start_at, "2026-09-06T04:00:00.000Z");
  assert.equal(usageWindow(7, new Date("2026-11-08T12:00:00Z"), "Asia/Kathmandu").start_at, "2026-11-01T18:15:00.000Z");
  const range = usageWindow(7, new Date("2026-11-03T12:00:00Z"), "America/Los_Angeles");
  const trend = usageTrend(range, [{ ...summary, start_at: "2026-11-01T07:00:00Z", end_at: "2026-11-02T08:00:00Z" }], "America/Los_Angeles");
  assert.deepEqual(trend.labels, ["2026-10-28", "2026-10-29", "2026-10-30", "2026-10-31", "2026-11-01", "2026-11-02", "2026-11-03"]);
  assert.deepEqual(trend.values, [null, null, null, null, "0", null, null]);
  const skipped = usageTrend({ start_at: "2011-12-29T10:00:00Z", end_at: "2011-12-31T10:00:00Z" }, [], "Pacific/Apia");
  assert.deepEqual(skipped.labels, ["2011-12-29", "2011-12-31"]);
});

test("repeated calendar dates add disjoint costs exactly and preserve unknown segments", () => {
  const timezone = "America/Goose_Bay", range = { start_at: "1988-10-30T02:00:00Z", end_at: "1988-10-31T04:00:00Z" };
  const buckets: AttributedUsage["buckets"] = [
    { ...summary, start_at: "1988-10-30T02:00:00Z", end_at: "1988-10-30T02:01:00Z", metrics: { ...metrics, actual_cost: "9007199254740993.000000000000000001", total_cost: "0" } },
    { ...summary, start_at: "1988-10-30T02:01:00Z", end_at: "1988-10-30T04:00:00Z", metrics: { ...metrics, actual_cost: "2", total_cost: "3" } },
    { ...summary, start_at: "1988-10-30T04:00:00Z", end_at: range.end_at, metrics: { ...metrics, actual_cost: "0.000000000000000009", total_cost: "0" } },
  ];
  assert.deepEqual(usageTrend(range, buckets, timezone), { labels: ["1988-10-29", "1988-10-30"], values: ["2", "9007199254740993.00000000000000001"] });
  assert.equal(usageDayCost(buckets, "1988-10-30", "total_cost", timezone), "0");
  assert.equal(usageDayCost(buckets, "1988-10-28", "actual_cost", timezone), null);
  buckets[2].metrics.actual_cost = null;
  assert.equal(usageDayCost(buckets, "1988-10-30", "actual_cost", timezone), null);
});

test("later failure, changed totals, incomplete pages and foreign capture records reject the whole read", async () => {
  for (const failure of ["network", "duplicate", "changed", "short", "foreign", "interval", "revision", "capture", "captured_at", "settings", "timezone"]) {
    await assert.rejects(readUsageData(accounts, captures, async (_ids, _participant, after) => {
      if (after === "0") return historyView(0);
      if (failure === "network") throw new Error("unavailable later page");
      const next = historyView(1);
      if (failure === "duplicate") return historyView(0);
      if (failure === "changed") next.summary.metrics.actual_cost = "0.000000000000000001";
      if (failure === "short") next.next_after = null;
      if (failure === "foreign") next.records[0].capture_id = "11";
      if (failure === "interval") next.query_end_at = "2026-11-02T00:00:00Z";
      if (failure === "revision" && next.history) next.history.authority_revision = "8";
      if (failure === "settings" && next.history) next.history.settings_revision = "4";
      if (failure === "timezone" && next.history) next.history.timezone = "Asia/Taipei";
      if (failure === "capture" && next.history) next.history.selections.pop();
      if (failure === "captured_at" && next.history) next.history.selections[0].captured_at = "2026-11-03T00:00:00.000000001Z";
      return next;
    }, window, "7", settings), error => error instanceof Error, failure);
  }
});

test("participant trend binding errors and absent captures never become a complete empty result", async () => {
  await assert.rejects(readUsageData(accounts, async account => {
    const page = await captures(account);
    page.captures[0].captured_at = "invalid";
    return page;
  }, async () => historyView(0), window, "7", settings), /capture observation time/);
  await assert.rejects(readUsageData(accounts, captures, async (_ids, participant) => {
    const result = historyView(0, true);
    if (participant) result.groups[0].participant_id = id(4);
    return result;
  }, window, "7", settings), /selection/);
  let queried = false;
  const data = await readUsageData(accounts, async () => ({ captures: [], next_after: null }), async () => { queried = true; return historyView(0); }, window, "7", settings);
  assert.equal(data.view, null);
  assert.deepEqual(data.missingAccounts, accounts.map(account => account.id));
  assert.equal(queried, false);
});

test("history binds independent account intervals and rejects a different authority on every read", async () => {
  const earlier = "2026-10-31T00:00:00Z";
  const independentCaptures = async (account: string) => {
    const page = await captures(account);
    if (account === id(2)) page.captures[0].start_at = earlier;
    return page;
  };
  const independent = () => {
    const result = historyView(0, true);
    assert.ok(result.history);
    result.history.selections[1].start_at = earlier;
    result.start_at = earlier;
    return result;
  };
  const data = await readUsageData(accounts, independentCaptures, async () => independent(), window, "7", settings);
  assert.equal(data.view?.start_at, earlier);
  assert.equal(data.records.length, 3);
  await assert.rejects(readUsageData(accounts, independentCaptures, async () => independent(), window, "8", settings), /selection/);
  await assert.rejects(readUsageData(accounts, independentCaptures, async (_ids, participant) => {
    const result = independent();
    if (participant && result.history) result.history.authority_revision = "8";
    return result;
  }, window, "7", settings), /selection/);
});

test("chart coordinates keep gaps while accessible amounts and Val retain native precision", () => {
  const exact = metrics.total_cost;
  const html = renderToStaticMarkup(createElement(LineChart, { labels: ["one", "gap", "zero"], unit: " USD", series: [{ name: "member", color: "red", values: [exact, null, "0"] }] }));
  assert.ok(html.includes(exact + " USD"));
  assert.ok(html.includes("gap 未知"));
  assert.ok(html.includes("zero 0 USD"));
  assert.equal((html.match(/<path /g) ?? []).length, 2);
  assert.ok(!/NaN|Infinity/.test(html));
  const exactHTML = renderToStaticMarkup(createElement(Val, { v: exact }));
  assert.ok(exactHTML.includes("<wbr"));
  assert.ok(exactHTML.replace(/<[^>]*>/g, "").includes(exact));
  assert.ok(renderToStaticMarkup(createElement(Val, { v: null })).includes("未知"));
});

test("window proxy validates echoed bounds and never substitutes an unwindowed view or retries", async () => {
  const config = { publicOrigin: "https://pool.example", backendOrigin: "http://127.0.0.1:18765", allowLoopbackHTTP: true };
  const request = () => new Request(config.publicOrigin + "/api/v1/participants/usage/window", { method: "POST", headers: { Origin: config.publicOrigin, "Content-Type": "application/json" }, body: "{}" });
  let calls = 0;
  const success = await proxyAPI(request(), config, async (url) => { calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/participants/usage/window"); return Response.json(view(0)); });
  assert.equal(success.status, 200);
  const invalid = view(0); delete invalid.query_start_at; delete invalid.query_end_at;
  assert.equal((await proxyAPI(request(), config, async () => Response.json(invalid))).status, 502);
  const failed = await proxyAPI(request(), config, async () => { calls++; throw new TypeError("lost read"); });
  assert.equal(failed.status, 502);
  assert.equal(calls, 2);
});

test("an explicit participant is sent on every history page and foreign participant replies fail closed", async () => {
  const calls: string[] = [];
  const data = await readUsageData(accounts, captures, async (_ids, participant, after) => {
    calls.push(participant);
    assert.equal(participant, id(3));
    return historyView(pageFor(after));
  }, window, "7", settings, id(3));
  assert.deepEqual(calls, [id(3), id(3), id(3)]);
  assert.equal(data.byParticipant[id(3)].summary.metrics.total_cost, metrics.total_cost);
  await assert.rejects(readUsageData(accounts, captures, async () => {
    const foreign = historyView(0);
    foreign.groups[0].participant_id = id(4);
    return foreign;
  }, window, "7", settings, id(3)), /Usage selection changed/);
});
