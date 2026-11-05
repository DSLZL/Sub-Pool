import assert from "node:assert/strict";
import { test } from "node:test";
import { proxyAuth } from "./auth-proxy";

const config = { backendOrigin: "http://127.0.0.1:8081", publicOrigin: "http://127.0.0.1:3000", allowLoopbackHTTP: true };
const session = {
  user: { id: "A".repeat(22), username: "admin", role: "admin", revision: "1" },
  created_at: "2026-11-06T00:00:00Z", server_time: "2026-11-06T01:00:00Z",
  idle_expires_at: "2026-11-06T08:00:00Z", absolute_expires_at: "2026-11-13T00:00:00Z",
};
function request(action: string, method = "GET", body?: string, headers?: Record<string, string>) {
  return new Request(config.publicOrigin + "/api/v1/auth/" + action, { method, body, headers });
}

test("auth proxy forwards one session cookie to a fixed destination and strips identity headers", async () => {
  const cookie = "sub_pool_dev_session=" + "A".repeat(43);
  let calls = 0;
  const response = await proxyAuth(request("session", "GET", undefined, { Cookie: cookie + "; other=private", Authorization: "secret", "X-User-Role": "admin" }), config, async (input, init) => {
    calls++;
    assert.equal(String(input), config.backendOrigin + "/api/v1/auth/session");
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("cookie"), cookie);
    assert.equal(headers.get("authorization"), null);
    assert.equal(headers.get("x-user-role"), null);
    assert.equal(init?.redirect, "error");
    assert.equal(init?.cache, "no-store");
    return Response.json(session);
  });
  assert.equal(response.status, 200); assert.equal(calls, 1);
  assert.equal(response.headers.get("cache-control"), "no-store");
});

test("auth proxy rejects untrusted browser input and ambiguous cookies before dispatch", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; return Response.json(session); };
  for (const origin of ["", "null", "https://attacker.test"]) {
    assert.equal((await proxyAuth(request("login", "POST", "{}", { Origin: origin, "Content-Type": "application/json" }), config, fetcher)).status, 403);
  }
  assert.equal((await proxyAuth(request("session?extra=%GG"), config, fetcher)).status, 400);
  assert.equal((await proxyAuth(request("session?"), config, fetcher)).status, 400);
  assert.equal((await proxyAuth(request("bootstrap", "POST", "{}"), config, fetcher)).status, 404);
  assert.equal((await proxyAuth(request("login", "POST", " ".repeat(4097), { Origin: config.publicOrigin, "Content-Type": "application/json" }), config, fetcher)).status, 400);
  const invalid = await proxyAuth(request("session", "GET", undefined, { Cookie: "sub_pool_dev_session=" + "A".repeat(43) + "; sub_pool_dev_session=bad value" }), config, fetcher);
  assert.equal(invalid.status, 401); assert.ok(invalid.headers.get("set-cookie")?.includes("Max-Age=0"));
  assert.equal(calls, 0);
});

test("auth proxy never leaks raw errors or cookies from malformed success and never retries", async () => {
  const malformed = await proxyAuth(request("session"), config, async () => Response.json({ ...session, secret: "SECRET_SENTINEL" }));
  assert.equal(malformed.status, 502); assert.ok(!(await malformed.text()).includes("SECRET_SENTINEL"));
  const error = await proxyAuth(request("session"), config, async () => Response.json({ defined: true, code: "UNAUTHORIZED", status: 401, message: "SECRET_SENTINEL", data: { retry_at: null } }, { status: 401 }));
  assert.equal(error.status, 401); assert.ok(!(await error.text()).includes("SECRET_SENTINEL"));
  const cookie = "sub_pool_dev_session=" + "A".repeat(43) + "; Path=/; Domain=attacker.test; HttpOnly; SameSite=Lax";
  const badCookie = await proxyAuth(request("session"), config, async () => Response.json(session, { headers: { "Set-Cookie": cookie } }));
  assert.equal(badCookie.status, 502); assert.equal(badCookie.headers.get("set-cookie"), null);
  let calls = 0;
  const lost = await proxyAuth(request("logout", "POST", "{}", { Origin: config.publicOrigin, "Content-Type": "application/json" }), config, async () => { calls++; throw new TypeError("SECRET_SENTINEL"); });
  assert.equal(lost.status, 502); assert.equal(calls, 1); assert.ok(!(await lost.text()).includes("SECRET_SENTINEL"));
});
