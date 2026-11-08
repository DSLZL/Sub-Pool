import { test } from "node:test";
import assert from "node:assert/strict";
import { proxyAPI } from "./api-proxy";

const origin = "https://pool.example", path = "/api/v1/participants/usage/history";
const config = { publicOrigin: origin, backendOrigin: "http://127.0.0.1:32000", allowLoopbackHTTP: true };
const id = "a".repeat(21) + "A", from = "2028-02-01T00:00:00Z", split = "2028-03-01T00:00:00Z", until = "2028-04-01T00:00:00Z", at = "2028-04-01T01:00:00Z";
const metrics = { input_tokens: null, output_tokens: null, cache_creation_tokens: null, cache_read_tokens: null,
  cache_creation_5m_tokens: null, cache_creation_1h_tokens: null, image_input_tokens: null, image_output_tokens: null, total_cost: null, actual_cost: null, account_stats_cost: null };
const summary = { request_count: "0", metrics };
const history = { start_at: from, end_at: until, query_start_at: from, query_end_at: until, oldest_capture_at: at, newest_capture_at: at,
  history: { basis: "selected_captures", timezone: "UTC", settings_revision: "1", authority_revision: "2", selections: [
    { id: "9", account_id: id, start_at: from, end_at: split, captured_at: at }, { id: "10", account_id: id, start_at: split, end_at: until, captured_at: at }] },
  summary, assigned: summary, unassigned: null, unassigned_visible: false, groups: [], grain: "month", buckets: [], records: [], next_after: null,
  complete: false, settlement_eligible: false, currency: "USD", timestamp_basis: "native_created_at",
  limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"] };
const input = { capture_ids: ["9", "10"], participant_id: "", model: "", api_key_id: "", grain: "month", after: "0", limit: 100, start_at: from, end_at: until };
const request = (change: RequestInit = {}, suffix = "") => new Request(origin + path + suffix, { method: "POST",
  headers: { Origin: origin, "Content-Type": "application/json", Cookie: "__Host-sub_pool_session=" + "a".repeat(43) + "; sp.role=admin", Authorization: "Bearer forged", "X-API-Key": "forged" },
  body: JSON.stringify(input), ...change });

test("historical usage proxy keeps one fixed origin, isolates cookies and validates descriptive receipts", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async (url, init) => {
    calls++;
    assert.equal(String(url), config.backendOrigin + path);
    assert.equal(init?.method, "POST");
    assert.equal(init?.cache, "no-store");
    assert.equal(init?.redirect, "error");
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("cookie"), "__Host-sub_pool_session=" + "a".repeat(43));
    assert.equal(headers.get("authorization"), null);
    assert.equal(headers.get("x-api-key"), null);
    assert.equal(headers.get("origin"), origin);
    assert.deepEqual(await new Response(init?.body).json(), input);
    return Response.json(history);
  };
  const response = await proxyAPI(request(), config, fetcher);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.deepEqual(await response.json(), history);
  assert.equal(calls, 1);
  for (const bad of [{ ...history, complete: true }, { ...history, history: { ...history.history, authority_revision: "" } }, { ...history, history: undefined }]) {
    const invalid = await proxyAPI(request(), config, async () => { calls++; return Response.json(bad); });
    assert.equal(invalid.status, 502);
  }
  assert.equal(calls, 4);
});

test("historical usage proxy rejects wrong method, origin, query and media before dispatch", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; throw new Error("unexpected"); };
  for (const [req, status] of [
    [request({ method: "GET", body: undefined }), 405],
    [request({ headers: { Origin: "https://untrusted.example", "Content-Type": "application/json" } }), 403],
    [request({}, "?hidden=%GG"), 400],
    [request({ headers: { Origin: origin, "Content-Type": "text/plain" } }), 400],
  ] as const) assert.equal((await proxyAPI(req, config, fetcher)).status, status);
  assert.equal(calls, 0);
});
