import { test } from "node:test";
import assert from "node:assert/strict";
import { proxyAPI } from "./api-proxy";
import { proxyDemoAPI } from "./demo-proxy";

const config = { demoOrigin: "http://127.0.0.1:8082", backendOrigin: "http://127.0.0.1:8080", publicOrigin: "https://pool.test", allowLoopbackHTTP: true };
const synthetic = { version: "demo-v1", synthetic: true, persistent: false, production_effects: false, scenario: "overview", title: "Synthetic",
  facts: [{ label: "one", value: "0", unit: "count" }, { label: "two", value: "unknown", unit: "state" }, { label: "three", value: "0.00", unit: "native" }],
  outcome: "read_only", explanation: "No production effect", limitations: ["No live authority", "No real acceptance"] };
function request(path = "/api/v1/demo/scenario", headers: Record<string, string> = {}, body = '{"scenario":"overview"}', method = "POST") {
  return new Request(config.publicOrigin + path, { method, headers: { origin: config.publicOrigin, "content-type": "application/json", ...headers }, ...(method === "GET" ? {} : { body }) });
}

test("demo proxy uses only an explicit separate target and strips all identity/source material", async () => {
  let calls = 0;
  let target = "", options: RequestInit | undefined;
  const response = await proxyDemoAPI(request(undefined, { "x-forwarded-for": "192.0.2.1", "x-private": "do-not-forward" }), config, async (url, init) => {
    calls++; target = String(url); options = init;
    return Response.json(synthetic);
  });
  assert.equal(target, config.demoOrigin + "/api/v1/demo/scenario");
  assert.deepEqual([...new Headers(options?.headers)], [["content-type", "application/json"]]);
  assert.equal(options?.credentials, "omit"); assert.equal(options?.redirect, "error"); assert.equal(options?.cache, "no-store");
  assert.equal(new TextDecoder().decode(options?.body as Uint8Array), '{"scenario":"overview"}');
  assert.equal(calls, 1); assert.equal(response.status, 200); assert.equal(response.headers.get("cache-control"), "no-store");
  assert.deepEqual(await response.json(), synthetic); assert.equal(response.headers.get("set-cookie"), null);
});

test("demo proxy configuration failure cannot reach production or public origin", async () => {
  for (const demoOrigin of ["", config.backendOrigin, config.publicOrigin, config.demoOrigin + "/", "http://example.com", "https://u:p@example.com", config.demoOrigin + "/api"]) {
    let calls = 0;
    const response = await proxyDemoAPI(request(), { ...config, demoOrigin }, async () => { calls++; return Response.json(synthetic); });
    assert.equal(response.status, 503); assert.equal(calls, 0);
  }
});

test("demo proxy rejects real credentials and malformed requests before dispatch", async () => {
  const cases = [
    [request(undefined, { cookie: "sub_pool_dev_session=" + "a".repeat(43) }), 403],
    [request(undefined, { authorization: "Bearer private" }), 403],
    [request(undefined, { origin: "https://other.test" }), 403],
    [request(undefined, { "content-type": "text/plain" }), 400],
    [request(undefined, { "content-encoding": "gzip" }), 400],
    [request("/api/v1/demo/scenario?scenario=wallet"), 400],
    [request("/api/v1/demo/restore"), 404],
    [request(undefined, {}, "", "GET"), 405],
    [request(undefined, {}, '{"scenario":"wallet","actor":"admin"}'), 400],
    [request(undefined, {}, '{"scenario":"live"}'), 400],
    [request(undefined, {}, '{"scenario":"overview","padding":"' + "x".repeat(4096) + '"}'), 400],
  ] as const;
  for (const [input, status] of cases) {
    let calls = 0;
    assert.equal((await proxyDemoAPI(input, config, async () => { calls++; return Response.json(synthetic); })).status, status);
    assert.equal(calls, 0);
  }
});

test("demo proxy accepts only matching synthetic output and never follows redirect/cookie responses", async () => {
  for (const output of [
    { ...synthetic, synthetic: false }, { ...synthetic, persistent: true }, { ...synthetic, production_effects: true },
    { ...synthetic, scenario: "wallet", outcome: "blocked" }, { ...synthetic, outcome: "blocked" }, { ...synthetic, extra: "private" },
  ]) assert.equal((await proxyDemoAPI(request(), config, async () => Response.json(output))).status, 502);
  let calls = 0;
  const redirect = await proxyDemoAPI(request(), config, async (_url, init) => {
    calls++; assert.equal(init?.redirect, "error");
    return new Response("", { status: 302, headers: { location: config.backendOrigin } });
  });
  assert.equal(redirect.status, 502); assert.equal(calls, 1);
  assert.equal((await proxyDemoAPI(request(), config, async () => Response.json(synthetic, { headers: { "set-cookie": "private=secret" } }))).status, 502);
});

test("demo proxy sanitizes defined errors and never replaces failure with a scenario", async () => {
  const response = await proxyDemoAPI(request(), config, async () => Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "private backend detail", data: { retry_at: null } }, { status: 403 }));
  assert.equal(response.status, 403); assert.doesNotMatch(await response.text(), /private backend detail/);
  let calls = 0;
  const failure = await proxyDemoAPI(request(), config, async () => { calls++; throw new Error("network"); });
  assert.equal(failure.status, 502); assert.equal(calls, 1); assert.doesNotMatch(await failure.text(), /demo-v1/);
});

const helpCatalog = { version: "help-v1", guides: [{ id: "authority", title: "Current guide", minutes: 2, audience: "all", steps: ["Current scope"] }],
  announcements: [{ id: "current-v1", content_sha256: "a".repeat(64), title: "Current", body: "Original", published_at: "2026-11-08T00:00:00Z", read_at: null }] };

test("help fixed proxy preserves only the declared session and validates catalogue and receipts", async () => {
  for (const path of ["/api/v1/help/catalog", "/api/v1/help/read"]) {
    let calls = 0;
    const response = await proxyAPI(request(path, { cookie: "__Host-sub_pool_session=" + "a".repeat(43) + "; other=private", authorization: "Bearer private" }, path.endsWith("/catalog") ? "{}" : JSON.stringify({ id: "current-v1", content_sha256: "a".repeat(64) })), config, async (url, init) => {
      calls++; assert.equal(String(url), config.backendOrigin + path);
      const headers = new Headers(init?.headers);
      assert.equal(headers.get("cookie"), "__Host-sub_pool_session=" + "a".repeat(43));
      assert.equal(headers.get("authorization"), null); assert.equal(headers.get("origin"), config.publicOrigin);
      return Response.json(path.endsWith("/catalog") ? helpCatalog : { ...helpCatalog.announcements[0], read_at: "2026-11-08T01:00:00Z" });
    });
    assert.equal(response.status, 200); assert.equal(calls, 1);
  }
  assert.equal((await proxyAPI(request("/api/v1/help/catalog"), config, async () => Response.json({ ...helpCatalog, guides: [] }))).status, 502);
  assert.equal((await proxyAPI(request("/api/v1/help/read"), config, async () => Response.json(helpCatalog.announcements[0]))).status, 502);
});

test("help proxy rejects aliases and origin failures and never retries a dispatched acknowledgement", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; throw new Error("lost reply"); };
  assert.equal((await proxyAPI(request("/api/v1/help/read"), config, fetcher)).status, 502);
  assert.equal(calls, 1);
  assert.equal((await proxyAPI(request("/api/v1/help/retry"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("/api/v1/help/read", { origin: "https://other.test" }), config, fetcher)).status, 403);
  assert.equal(calls, 1);
});
