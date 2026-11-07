import assert from "node:assert/strict";
import { test } from "node:test";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import type { TemporaryStatus } from "@sub-pool/contracts/temporary";
import { proxyAPI } from "./api-proxy";

const config = { backendOrigin: "http://127.0.0.1:8088", publicOrigin: "http://127.0.0.1:3000", allowLoopbackHTTP: true };
const status: TemporaryStatus = { can_start: false, state: "unavailable", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", round_state: "not_implemented", rights_state: "not_implemented", reminders_available: false };
const cookie = "sub_pool_dev_session=" + "A".repeat(43);
const request = (path = "status", body = "{}", method = "POST", origin = config.publicOrigin) => new Request(`${config.publicOrigin}/api/v1/temporary/${path}`, {
  method, ...(method === "GET" ? {} : { body }), headers: { Origin: origin, "Content-Type": "application/json", Cookie: cookie + "; unrelated=private", Authorization: "private", "X-Forwarded-For": "private" },
});

test("temporary availability uses one fixed read-only POST with isolated headers and a strict output", async () => {
  let calls = 0;
  const result = await proxyAPI(request(), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/temporary/status");
    assert.equal(init?.method, "POST"); assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error"); assert.equal(init?.credentials, "omit");
    assert.equal(await new Request(url, init).text(), "{}");
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("cookie"), cookie); assert.equal(headers.get("origin"), config.publicOrigin);
    assert.equal(headers.get("authorization"), null); assert.equal(headers.get("x-forwarded-for"), null);
    return Response.json(status);
  });
  assert.equal(result.status, 200); assert.equal(result.headers.get("cache-control"), "no-store");
  assert.equal(result.headers.get("set-cookie"), null); assert.deepEqual(await result.json(), status); assert.equal(calls, 1);
});

test("temporary proxy rejects undeclared mutations and malformed transport before dispatch", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; return Response.json(status); };
  for (const [path, body, method, origin, expected] of [
    ["start", "{}", "POST", config.publicOrigin, 404], ["stop", "{}", "POST", config.publicOrigin, 404],
    ["rights/update", "{}", "POST", config.publicOrigin, 404], ["reminders/update", "{}", "POST", config.publicOrigin, 404],
    ["status", "", "GET", config.publicOrigin, 405], ["status?", "{}", "POST", config.publicOrigin, 400],
    ["status?scope=private", "{}", "POST", config.publicOrigin, 400], ["status", "{}", "POST", "https://attacker.test", 403],
    ["status", " ".repeat(65537), "POST", config.publicOrigin, 400],
  ] as const) assert.equal((await proxyAPI(request(path, body, method, origin), config, fetcher)).status, expected);
  const ambiguous = request(); ambiguous.headers.set("Cookie", cookie + "; " + cookie);
  assert.equal((await proxyAPI(ambiguous, config, fetcher)).status, 401);
  assert.equal(calls, 0);
});

test("temporary failures cannot expose raw data, cookies, a fabricated enabled state or replay", async () => {
  for (const value of [{ ...status, can_start: true }, { ...status, round_state: "active" }, { ...status, rights_state: "settled" }, { ...status, reminders_available: true }, { ...status, secret: "PRIVATE_BODY" }]) {
    const result = await proxyAPI(request(), config, async () => Response.json(value));
    assert.equal(result.status, 502); assert.ok(!(await result.text()).includes("PRIVATE_BODY"));
  }
  const oversized = await proxyAPI(request(), config, async () => new Response(" ".repeat(262145)));
  assert.equal(oversized.status, 502);
  const injected = await proxyAPI(request(), config, async () => Response.json(status, { headers: { "Set-Cookie": cookie + "; Path=/; HttpOnly; SameSite=Lax" } }));
  assert.equal(injected.status, 502); assert.equal(injected.headers.get("set-cookie"), null);
  for (const [code, codeStatus] of [["UNAUTHORIZED", 401], ["FORBIDDEN", 403], ["SOURCE_BLOCKED", 403], ["NOT_FOUND", 404], ["TOO_MANY_REQUESTS", 429]] as const) {
    let calls = 0;
    const denied = await proxyAPI(request(), config, async () => { calls++; return Response.json({ defined: true, code, status: codeStatus, message: "PRIVATE_BODY", data: { retry_at: null } }, { status: codeStatus }); });
    assert.equal(denied.status, codeStatus); assert.ok(!(await denied.text()).includes("PRIVATE_BODY")); assert.equal(calls, 1);
  }
  let calls = 0;
  const lost = await proxyAPI(request(), config, async () => { calls++; throw new TypeError("PRIVATE_BODY"); });
  assert.equal(lost.status, 502); assert.ok(!(await lost.text()).includes("PRIVATE_BODY")); assert.equal(calls, 1);
});

test("shared temporary client and UI proxy agree without permitting forged start inputs", async () => {
  let calls = 0;
  const client = createTemporaryClient(config.publicOrigin, async (input, init) => {
    const current = new Request(input, init); current.headers.set("Origin", config.publicOrigin);
    return proxyAPI(current, config, async () => { calls++; return Response.json(status); });
  });
  assert.deepEqual(await client.status({}), status); assert.equal(calls, 1);
  const forged: Record<string, never> = {};
  Object.assign(forged, { can_start: true, mode: "no_carry", informed: true });
  await assert.rejects(client.status(forged)); assert.equal(calls, 1);
});
