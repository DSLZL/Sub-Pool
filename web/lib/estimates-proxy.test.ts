import assert from "node:assert/strict";
import { test } from "node:test";
import { estimateRecord } from "@sub-pool/contracts/estimates";
import { proxyAPI } from "./api-proxy";

const id = "a".repeat(21) + "A", origin = "https://pool.test", backend = "https://go.test";
const saved = estimateRecord.parse({ id: "1", account_id: id, generated_at: "2026-11-07T12:00:00Z", rules_version: "gate-003-rules-v1", configuration: { account_id: id, revision: "2", saved_at: "2026-11-07T11:00:00Z", assumptions: { plan: "empirical_min_v1", cost_basis: "standard_usd", keep_ratio: "0.8", premise: "Independently declared fixture only", windows: ["short"], source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive" } }, trigger: "configuration", policy_revision: "1", management_revision: "1", observation_head: "0", capture_head: "0", state: "insufficient_samples", windows: [{ kind: "short", basis: "official", period: null, manual_start_at: null, manual_end_at: null, observation_ids: [], current_observation_id: null, source_at: null, oldest_sample_at: null, current_used_percent: null, state: "insufficient_samples", capacity: null, remaining: null, range_min: null, range_max: null }], remaining: null, currency: "USD", complete: false, settlement_eligible: false, model_calibrated: false, limitations: ["INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"] });
const current = { configuration: saved.configuration, latest: saved, current_state: saved.state, current_remaining: null, needs_recalculation: false, checked_at: "2026-11-07T12:00:01Z" };
const config = { backendOrigin: backend, publicOrigin: origin, allowLoopbackHTTP: false };
function request(action: string, extra: HeadersInit = {}) { return new Request(origin + "/api/v1/estimates/" + action, { method: "POST", headers: { Origin: origin, "Content-Type": "application/json", Cookie: "other=unrelated; __Host-sub_pool_session=" + "a".repeat(43), Authorization: "do-not-forward", "X-Forwarded-For": "198.51.100.1", ...extra }, body: JSON.stringify({ account_id: id }) }); }

test("estimate proxy exposes only fixed POSTs and preserves scoped unavailable outputs and typed errors", async () => {
  let calls = 0;
  for (const action of ["current", "record", "history", "configure", "recompute"]) {
    const result = await proxyAPI(request(action), config, async (input, init) => {
      calls++; assert.equal(String(input), backend + "/api/v1/estimates/" + action); assert.equal(init?.method, "POST"); assert.equal(init?.redirect, "error"); assert.equal(init?.cache, "no-store");
      const headers = new Headers(init?.headers); assert.equal(headers.get("Cookie"), "__Host-sub_pool_session=" + "a".repeat(43)); assert.equal(headers.get("Origin"), origin); assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("X-Forwarded-For"), null);
      return Response.json(action === "current" ? current : action === "history" ? { account_id: id, records: [saved], next_after: null } : saved);
    });
    assert.equal(result.status, 200); assert.equal(result.headers.get("Cache-Control"), "no-store"); assert.equal(result.headers.get("Set-Cookie"), null);
  }
  assert.equal(calls, 5);
  const fetcher: typeof fetch = async () => { calls++; return Response.json(current); };
  assert.equal((await proxyAPI(request("arbitrary"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("current?target=https://other.test"), config, fetcher)).status, 400);
  assert.equal((await proxyAPI(request("current", { Origin: "https://evil.test" }), config, fetcher)).status, 403); assert.equal(calls, 5);
  const conflict = await proxyAPI(request("configure"), config, async () => Response.json({ defined: true, code: "CONFLICT", status: 409, message: "private", data: { retry_at: null } }, { status: 409 })); assert.equal(conflict.status, 409); assert.equal((await conflict.json()).message.includes("private"), false);
});

test("estimate proxy rejects fabricated money, cookie issuance and oversized history without replay", async () => {
  let calls = 0;
  for (const response of [Response.json({ ...saved, remaining: { numerator: "0", denominator: "1" } }), Response.json(saved, { headers: { "Set-Cookie": "other=secret" } }), Response.json({ defined: true, code: "OBSERVATION_RANGE_UNVERIFIED", status: 409, data: { retry_at: null } }, { status: 409 })]) {
    const result = await proxyAPI(request("configure"), config, async () => { calls++; return response; }); assert.equal(result.status, 502);
  }
  assert.equal(calls, 3);
  const large = new Response(JSON.stringify({ account_id: id, records: [saved], next_after: null }) + " ".repeat(1024 * 1024));
  assert.equal((await proxyAPI(request("history"), config, async () => { calls++; return large; })).status, 502); assert.equal(calls, 4);
});
