import assert from "node:assert/strict";
import { test } from "node:test";
import { createPricingClient } from "@sub-pool/contracts/client";
import { pricingGroup, pricingGroups } from "@sub-pool/contracts/pricing";
import { proxyAPI } from "./api-proxy";

const config = { publicOrigin: "http://127.0.0.1:3000", backendOrigin: "http://127.0.0.1:8081", allowLoopbackHTTP: true };
const source_id = "A".repeat(21) + "Q", token = "c".repeat(43);
const check = { state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 };
const context = { source_id, revision: "2", checked_at: "2026-11-07T00:00:00.000000Z", run_mode: "standard", mode_read: { ...check, id: "run_mode" }, can_apply: false, write_reason: "PRICING_WRITE_UNQUALIFIED" };
const groups = pricingGroups.parse({ ...context, page: 1, total: "0", next_page: null, groups: [], read: { ...check, id: "pricing.groups.read" } });
const request = (path = "groups", body: unknown = { source_id, revision: "2", page: 1, search: "" }, origin = config.publicOrigin) => new Request(config.publicOrigin + "/api/v1/pricing/" + path, { method: "POST", headers: { Origin: origin, "Content-Type": "application/json", Cookie: `theme=dark; sub_pool_dev_session=${token}`, Authorization: "Bearer browser-secret", "x-api-key": "browser-key" }, body: JSON.stringify(body) });

test("pricing proxy and actual oRPC client isolate credentials and validate the fixed read without replay", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async (input, init) => {
    calls++; assert.equal(String(input), config.backendOrigin + "/api/v1/pricing/groups"); assert.equal(init?.method, "POST");
    const headers = new Headers(init?.headers); assert.equal(headers.get("Cookie"), `sub_pool_dev_session=${token}`); assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("x-api-key"), null);
    assert.equal(init?.redirect, "error"); assert.equal(init?.cache, "no-store");
    return Response.json(groups);
  };
  const client = createPricingClient(config.publicOrigin, async (input, init) => { const incoming = new Request(input, init); incoming.headers.set("Origin", config.publicOrigin); incoming.headers.set("Cookie", `sub_pool_dev_session=${token}`); return proxyAPI(incoming, config, fetcher); });
  assert.deepEqual(await client.groups({ source_id, revision: "2", page: 1, search: "" }), groups); assert.equal(calls, 1);
  const response = await proxyAPI(request(), config, fetcher); assert.equal(response.status, 200); assert.equal(response.headers.get("Cache-Control"), "no-store"); assert.equal(response.headers.get("Set-Cookie"), null);
  for (const incoming of [request("bulk-force", {}), request("automatic-retry", {}), request("groups?x=1"), request("groups", {}, "https://attacker.test"), new Request(config.publicOrigin + "/api/v1/pricing/groups", { method: "GET" })]) {
    const before: number = calls; assert.notEqual((await proxyAPI(incoming, config, fetcher)).status, 200); assert.equal(calls, before);
  }
});

test("pricing proxy rejects fabricated capability and secret/cookie responses while preserving declared connection failures", async () => {
  let calls = 0;
  for (const value of [{ ...groups, can_apply: true }, { ...groups, total: "1" }, { ...groups, secret: "raw-upstream" }]) {
    const response = await proxyAPI(request(), config, async () => { calls++; return Response.json(value); }); assert.equal(response.status, 502); assert.equal((await response.text()).includes("raw-upstream"), false);
  }
  assert.equal(calls, 3);
  for (const [code, status] of [["CONNECTION_NOT_CONFIGURED", 409], ["SECRET_STORAGE_UNAVAILABLE", 503]] as const) {
    const response = await proxyAPI(request(), config, async () => Response.json({ defined: true, code, status, data: { retry_at: null }, message: "private-native-secret" }, { status }));
    assert.equal(response.status, status); assert.equal((await response.text()).includes("private-native-secret"), false);
  }
  const cookie = await proxyAPI(request(), config, async () => Response.json(groups, { headers: { "Set-Cookie": "private=value" } })); assert.equal(cookie.status, 502);
});

test("pricing projection response budget permits native base rules above the unrelated 256 KiB limit", async () => {
  const values = { input_price: "0.00000125", output_price: "0", cache_write_price: null, cache_write_1h_price: null, cache_read_price: null, image_input_price: null, image_output_price: null, per_request_price: null, fast_multiplier: "2", flex_multiplier: null };
  const rule = { models: Array.from({ length: 100 }, (_, index) => "native-model-" + index + "x".repeat(40)), billing_mode: "token", values, unreported: [], unprojected: ["intervals"] };
  const detail = pricingGroup.parse({ ...context, page: 1, total: "0", next_page: null, group_id: "9", group: { id: "9", name: "Native group", status: "active", account_count: "0" }, configuration: { rate_multiplier: "1", force_openai_fast: true, free_openai_fast: false, long_context_pricing_enabled: true, model_rules: Array.from({ length: 100 }, () => rule), unreported: [], projection: "base_fields_only", unit: "native_config_value" }, read: { ...check, id: "pricing.group.read" }, accounts: [], accounts_read: { ...check, id: "pricing.accounts.read" } });
  assert.ok(JSON.stringify(detail).length > 262144);
  const response = await proxyAPI(request("group", { source_id, revision: "2", group_id: "9", page: 1 }), config, async () => Response.json(detail)); assert.equal(response.status, 200);
});

test("pricing proxy validates controlled-operation receipts and never retries native mutations", async () => {
  const operation = { id: "1", revision: "2", actor_id: source_id, request_id: source_id, source_id, connection_revision: "2", group_id: "9", group_name: "Owned group",
    changes: [{ kind: "fast", force_fast: true, free_fast: false, value: null }], state: "needs_review", reason: "WRITE_RESULT_UNKNOWN", before_fingerprint: "a".repeat(64), after_fingerprint: null, created_at: "2026-11-10T00:00:00Z", updated_at: "2026-11-10T00:00:01Z" };
  for (const action of ["apply", "operation", "retry", "revert", "review"]) {
    const response = await proxyAPI(request(action), config, async (_input, init) => {
      const headers = new Headers(init?.headers); assert.equal(headers.get("Cookie"), `sub_pool_dev_session=${token}`); assert.equal(headers.get("Authorization"), null);
      return Response.json(operation);
    });
    assert.equal(response.status, 200); assert.deepEqual(await response.json(), operation);
  }
  for (const bad of [{ ...operation, state: "reverted" }, { ...operation, raw_native: "secret" }]) {
    const response = await proxyAPI(request("apply"), config, async () => Response.json(bad)); assert.equal(response.status, 502); assert.ok(!(await response.text()).includes("secret"));
  }
  let calls = 0;
  const response = await proxyAPI(request("apply"), config, async () => { calls++; throw new TypeError("response lost"); });
  assert.equal(response.status, 502); assert.equal(calls, 1);
});
