import assert from "node:assert/strict";
import { test } from "node:test";
import { proxyAPI } from "./api-proxy";

const config = { backendOrigin: "http://127.0.0.1:8081", publicOrigin: "http://127.0.0.1:3000", allowLoopbackHTTP: true };
const id = "a".repeat(21) + "A";
const record = { id: "9", job_id: "1", account_id: id, management_revision: "1", manual_start: false, excluded: false, review_reason: null, reviewed_at: null,
  snapshot: { source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive", source_at: null, received_at: "2026-11-07T12:00:00Z", windows: [{ kind: "short", duration_seconds: null, reset_at: null, reset_expired: false, used_percent: null }] } };
const basis = { contract_id: null, members: [], unallocated_percent: null, time_basis: "unavailable", usage_state: "unavailable", estimate_state: "unavailable" };
const state = { account_id: id, revision: "1", manual_start: null, recalculation_state: "not_implemented" };
const outputs = { list: { records: [record], next_after: null, query_start_at: "1970-01-01T00:00:00Z", query_end_at: "9998-12-31T23:59:59Z", time_basis: "received_at" },
  get: { observation: record, basis }, "estimate-link": { observation_id: "9", account_id: id, estimate: { id: "9007199254740993", generated_at: "2026-11-11T12:00:00.123456Z" } },
  events: { account_id: id, events: [], next_after: null }, state, review: record, start: state, revoke: state,
  "daily-history": { rules_version: "recorded-day-last-v1", account_id: id, timezone: "UTC", settings_revision: "3", authority_revision: "7",
    query_start_at: "2026-11-07T00:00:00Z", query_end_at: "2026-11-08T00:00:00Z", time_basis: "received_at", complete: false, settlement_eligible: false, records: [] } };
function request(action: string, method = "POST", origin = config.publicOrigin) {
  return new Request(config.publicOrigin + "/api/v1/observations/" + action, { method, body: method === "POST" ? "{}" : undefined,
    headers: { Origin: origin, "Content-Type": "application/json", Cookie: "sub_pool_dev_session=" + "A".repeat(43), Authorization: "PRIVATE", "X-User-Role": "admin", "X-Forwarded-For": "203.0.113.10" } });
}

test("all observation operations use fixed POST destinations, current cookie and exact safe outputs", async () => {
  for (const [action, output] of Object.entries(outputs)) {
    let calls = 0;
    const result = await proxyAPI(request(action), config, async (url, init) => {
      calls++;
      assert.equal(url, config.backendOrigin + "/api/v1/observations/" + action);
      assert.equal(init?.method, "POST"); assert.equal(init?.redirect, "error"); assert.equal(init?.cache, "no-store");
      const headers = new Headers(init?.headers);
      assert.equal(headers.get("cookie"), "sub_pool_dev_session=" + "A".repeat(43));
      for (const name of ["authorization", "x-user-role", "x-forwarded-for"]) assert.equal(headers.get(name), null);
      assert.equal(headers.get("origin"), config.publicOrigin);
      return Response.json(output);
    });
    assert.equal(result.status, 200); assert.equal(calls, 1);
    assert.equal(result.headers.get("cache-control"), "no-store");
    assert.deepEqual(await result.json(), output);
  }
});

test("observation proxy rejects invalid origin, methods, destinations and invented response fields", async () => {
  let calls = 0;
  for (const [input, status] of [[request("get", "GET"), 405], [request("get", "POST", "https://other.test"), 403], [request("daily-history", "GET"), 405], [request("daily-history", "POST", "https://other.test"), 403], [request("replace"), 404]] as const) {
    const result = await proxyAPI(input, config, async () => { calls++; return Response.json({}); });
    assert.equal(result.status, status);
  }
  assert.equal(calls, 0);
  for (const value of [{ ...outputs.get, credentials: "PRIVATE" }, { ...outputs.get, observation: { ...record, actual_cost: "0" } }, { ...state, recalculation_state: "completed" }]) {
    const result = await proxyAPI(request("account_id" in value ? "state" : "get"), config, async () => Response.json(value));
    assert.equal(result.status, 502);
  }
  const link = outputs["estimate-link"];
  for (const value of [{ ...link, credentials: "PRIVATE" }, { ...link, estimate: { ...link.estimate, remaining: "0" } }, { ...link, observation_id: 9 }, { ...link, estimate: { id: "10" } }]) {
    assert.equal((await proxyAPI(request("estimate-link"), config, async () => Response.json(value))).status, 502);
  }
  assert.equal((await proxyAPI(request("estimate-link"), config, async () => Response.json({ ...link, estimate: null }))).status, 200);
  for (const changed of [{ ...outputs["daily-history"], complete: true }, { ...outputs["daily-history"], settlement_eligible: true }, { ...outputs["daily-history"], credentials: "PRIVATE" }]) {
    assert.equal((await proxyAPI(request("daily-history"), config, async () => Response.json(changed))).status, 502);
  }
});

test("observation proxy validates a full 500-member historical basis within its explicit response bound", async () => {
  const members = Array.from({ length: 500 }, (_, index) => {
    const bytes = Buffer.alloc(16); bytes.writeUInt32BE(index);
    return { participant_id: bytes.toString("base64url"), name: "😀".repeat(128), share_percent: "0.2" };
  });
  const value = { observation: record, basis: { ...basis, contract_id: "1", members, unallocated_percent: "0", time_basis: "recorded_local_contract_at_source_time" } };
  assert.ok(Buffer.byteLength(JSON.stringify(value)) > 262144);
  const result = await proxyAPI(request("get"), config, async () => Response.json(value));
  assert.equal(result.status, 200);
  assert.deepEqual(await result.json(), value);
  assert.equal((await proxyAPI(request("get"), config, async () => new Response(" ".repeat(1024 * 1024 + 1)))).status, 502);
});

test("observation range errors remain typed only on this domain and uncertain decisions are never replayed", async () => {
  const body = { defined: true, code: "OBSERVATION_RANGE_UNVERIFIED", status: 409, message: "PRIVATE_UPSTREAM", data: { retry_at: null } };
  const response = await proxyAPI(request("start"), config, async () => Response.json(body, { status: 409 }));
  assert.equal(response.status, 409);
  assert.equal((await response.json()).message, "请求未完成，请核对当前状态。");
  const other = new Request(config.publicOrigin + "/api/v1/pools/create", { method: "POST", body: "{}", headers: { Origin: config.publicOrigin, "Content-Type": "application/json" } });
  assert.equal((await proxyAPI(other, config, async () => Response.json(body, { status: 409 }))).status, 502);
  let calls = 0;
  assert.equal((await proxyAPI(request("review"), config, async () => { calls++; throw new TypeError("lost acknowledgement"); })).status, 502);
  assert.equal(calls, 1);
});
