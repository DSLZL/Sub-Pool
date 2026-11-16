import assert from "node:assert/strict";
import { test } from "node:test";
import { proxyAPI } from "./api-proxy";

const config = { backendOrigin: "https://backend.example", publicOrigin: "https://pool.example", allowLoopbackHTTP: false };
const key = "a1".repeat(32);

async function probe(headers: Record<string, string>, sourceProxyKey?: string) {
  let calls = 0;
  let forwarded: Headers | undefined;
  const fetcher: typeof fetch = async (_input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    calls++;
    forwarded = new Headers(init?.headers);
    return Response.json({ defined: true, code: "UNAUTHORIZED", status: 401, message: "denied", data: { retry_at: null } }, { status: 401 });
  };
  const response = await proxyAPI(new Request(config.publicOrigin + "/api/v1/auth/session", { headers }), { ...config, sourceProxyKey }, fetcher);
  return { calls, forwarded, response };
}

test("source headers remain untrusted without explicit server configuration", async () => {
  const result = await probe({ "x-forwarded-for": "198.51.100.1", "x-sub-pool-client-ip": "198.51.100.2", "x-sub-pool-proxy-key": key });
  assert.equal(result.calls, 1);
  assert.equal(result.forwarded?.get("x-forwarded-for"), null);
  assert.equal(result.forwarded?.get("x-sub-pool-proxy-key"), null);
  assert.equal(result.forwarded?.get("x-sub-pool-client-ip"), null);
});

test("authenticated edge forwards only its single validated address", async () => {
  for (const ip of ["198.51.100.23", "2001:db8::5", "::ffff:192.0.2.2"]) {
    const result = await probe({ "x-forwarded-for": "203.0.113.99", "x-sub-pool-client-ip": ip, "x-sub-pool-proxy-key": key, authorization: "Bearer browser-supplied" }, key);
    assert.equal(result.calls, 1);
    assert.equal(result.response.status, 401);
    assert.equal(result.forwarded?.get("x-forwarded-for"), ip);
    for (const header of ["x-sub-pool-proxy-key", "x-sub-pool-client-ip", "authorization"]) assert.equal(result.forwarded?.get(header), null);
    assert.ok(!(await result.response.text()).includes(key));
    assert.ok(!([...result.response.headers.values()].join()).includes(key));
  }
});

test("missing, malformed, duplicate and incorrect edge proof cannot dispatch", async () => {
  for (const proof of [undefined, "", "b2".repeat(32), "a1".repeat(31), key.toUpperCase(), key + "," + key, "a1".repeat(31) + "áá"]) {
    const result = await probe({ "x-sub-pool-client-ip": "198.51.100.2", ...(proof === undefined ? {} : { "x-sub-pool-proxy-key": proof }) }, key);
    assert.equal(result.response.status, 403);
    assert.equal(result.calls, 0);
  }
});

test("authenticated edge must supply exactly one valid IP without ports, zones or chains", async () => {
  for (const ip of [undefined, "", "unknown", "127.1", "127.0.0.999", "127.0.0.1:123", "[::1]", "fe80::1%lo", "198.51.100.2, 198.51.100.3"]) {
    const result = await probe({ "x-sub-pool-proxy-key": key, ...(ip === undefined ? {} : { "x-sub-pool-client-ip": ip }) }, key);
    assert.equal(result.response.status, 403);
    assert.equal(result.calls, 0);
  }
});

test("invalid configured source key fails closed instead of disabling source authentication", async () => {
  for (const configured of ["", "a1".repeat(31), key.toUpperCase(), "not-a-key"]) {
    const result = await probe({ "x-sub-pool-proxy-key": key, "x-sub-pool-client-ip": "198.51.100.2" }, configured);
    assert.equal(result.response.status, 503);
    assert.equal(result.calls, 0);
  }
});
