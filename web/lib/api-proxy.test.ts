import assert from "node:assert/strict";
import { test } from "node:test";
import { proxyAPI } from "./api-proxy";

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
  const response = await proxyAPI(request("session", "GET", undefined, { Cookie: cookie + "; other=private", Authorization: "secret", "X-User-Role": "admin", "X-Forwarded-For": "203.0.113.9" }), config, async (input, init) => {
    calls++;
    assert.equal(String(input), config.backendOrigin + "/api/v1/auth/session");
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("cookie"), cookie);
    assert.equal(headers.get("authorization"), null);
    assert.equal(headers.get("x-user-role"), null);
    assert.equal(headers.get("x-forwarded-for"), null);
    assert.equal(init?.redirect, "error");
    assert.equal(init?.cache, "no-store");
    return Response.json(session);
  });
  assert.equal(response.status, 200); assert.equal(calls, 1);
  assert.equal(response.headers.get("cache-control"), "no-store");
});

test("auth proxy preserves a defined source block without raw messages or replay", async () => {
  let calls = 0;
  const result = await proxyAPI(request("login", "POST", "{}", { Origin: config.publicOrigin, "Content-Type": "application/json" }), config, async () => {
    calls++;
    return Response.json({ defined: true, code: "SOURCE_BLOCKED", status: 403, message: "SECRET_SENTINEL", data: { retry_at: null } }, { status: 403 });
  });
  assert.equal(result.status, 403);
  assert.equal(calls, 1);
  const value = await result.json();
  assert.equal(value.code, "SOURCE_BLOCKED");
  assert.ok(!JSON.stringify(value).includes("SECRET_SENTINEL"));
});

test("auth proxy rejects untrusted browser input and ambiguous cookies before dispatch", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; return Response.json(session); };
  for (const origin of ["", "null", "https://attacker.test"]) {
    assert.equal((await proxyAPI(request("login", "POST", "{}", { Origin: origin, "Content-Type": "application/json" }), config, fetcher)).status, 403);
  }
  assert.equal((await proxyAPI(request("session?extra=%GG"), config, fetcher)).status, 400);
  assert.equal((await proxyAPI(request("session?"), config, fetcher)).status, 400);
  assert.equal((await proxyAPI(request("bootstrap", "POST", "{}"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("login", "POST", " ".repeat(4097), { Origin: config.publicOrigin, "Content-Type": "application/json" }), config, fetcher)).status, 400);
  const invalid = await proxyAPI(request("session", "GET", undefined, { Cookie: "sub_pool_dev_session=" + "A".repeat(43) + "; sub_pool_dev_session=bad value" }), config, fetcher);
  assert.equal(invalid.status, 401); assert.ok(invalid.headers.get("set-cookie")?.includes("Max-Age=0"));
  assert.equal(calls, 0);
});

test("auth proxy never leaks raw errors or cookies from malformed success and never retries", async () => {
  const malformed = await proxyAPI(request("session"), config, async () => Response.json({ ...session, secret: "SECRET_SENTINEL" }));
  assert.equal(malformed.status, 502); assert.ok(!(await malformed.text()).includes("SECRET_SENTINEL"));
  const error = await proxyAPI(request("session"), config, async () => Response.json({ defined: true, code: "UNAUTHORIZED", status: 401, message: "SECRET_SENTINEL", data: { retry_at: null } }, { status: 401 }));
  assert.equal(error.status, 401); assert.ok(!(await error.text()).includes("SECRET_SENTINEL"));
  const cookie = "sub_pool_dev_session=" + "A".repeat(43) + "; Path=/; Domain=attacker.test; HttpOnly; SameSite=Lax";
  const badCookie = await proxyAPI(request("session"), config, async () => Response.json(session, { headers: { "Set-Cookie": cookie } }));
  assert.equal(badCookie.status, 502); assert.equal(badCookie.headers.get("set-cookie"), null);
  let calls = 0;
  const lost = await proxyAPI(request("logout", "POST", "{}", { Origin: config.publicOrigin, "Content-Type": "application/json" }), config, async () => { calls++; throw new TypeError("SECRET_SENTINEL"); });
  assert.equal(lost.status, 502); assert.equal(calls, 1); assert.ok(!(await lost.text()).includes("SECRET_SENTINEL"));
});

test("access proxy accepts bounded real scopes and rejects role escalation or unexpected cookies", async () => {
  const ids = Array.from({ length: 500 }, (_, i) => Buffer.from(String(i).padStart(16, "0")).toString("base64url"));
  const access = { user: { ...session.user, username: "reader", role: "user" }, all_data: false,
    features: ["usage"], account_ids: ids, participant_ids: ids };
  const input = new Request(config.publicOrigin + "/api/v1/access");
  const accepted = await proxyAPI(input, config, async (url, init) => {
    assert.equal(String(url), config.backendOrigin + "/api/v1/access");
    assert.equal(init?.method, "GET");
    return Response.json(access);
  });
  assert.equal(accepted.status, 200);
  assert.equal((await accepted.json()).account_ids.length, 500);
  for (const result of [{ ...access, all_data: true }, { ...access, password_hash: "SECRET_SENTINEL" }]) {
    const denied = await proxyAPI(input, config, async () => Response.json(result));
    assert.equal(denied.status, 502); assert.ok(!(await denied.text()).includes("SECRET_SENTINEL"));
  }
  const cookie = "sub_pool_dev_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax";
  const badCookie = await proxyAPI(input, config, async () => Response.json(access, { headers: { "Set-Cookie": cookie } }));
  assert.equal(badCookie.status, 502); assert.equal(badCookie.headers.get("set-cookie"), null);
});

test("user proxy preserves defined conflicts/not-found and never replays an uncertain save", async () => {
  const ids = Array.from({ length: 500 }, (_, i) => Buffer.from(String(i).padStart(16, "0")).toString("base64url"));
  const body = JSON.stringify({ id: session.user.id, revision: "1", username: "reader", enabled: true,
    features: ["usage"], account_ids: ids, participant_ids: ids, new_password: "" });
  const input = () => new Request(config.publicOrigin + "/api/v1/users/update", { method: "POST", body,
    headers: { Origin: config.publicOrigin, "Content-Type": "application/json" } });
  for (const [code, status] of [["CONFLICT", 409], ["NOT_FOUND", 404]] as const) {
    let calls = 0;
    const result = await proxyAPI(input(), config, async (_url, init) => {
      calls++; assert.ok((init?.body as Uint8Array).length > 4096);
      return Response.json({ defined: true, code, status, message: "SECRET_SENTINEL", data: { retry_at: null } }, { status });
    });
    assert.equal(result.status, status); assert.equal(calls, 1); assert.ok(!(await result.text()).includes("SECRET_SENTINEL"));
  }
  let calls = 0;
  const lost = await proxyAPI(input(), config, async () => { calls++; throw new TypeError("SECRET_SENTINEL"); });
  assert.equal(lost.status, 502); assert.equal(calls, 1);
});

test("user proxy rejects undeclared routes, oversized bodies, bad origins and queries before dispatch", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; return Response.json({}); };
  for (const [path, method, body, origin, expected] of [
    ["/api/v1/users/access", "GET", undefined, "", 404],
    ["/api/v1/users/promote", "POST", "{}", config.publicOrigin, 404],
    ["/api/v1/access?", "GET", undefined, "", 400],
    ["/api/v1/users/list", "GET", undefined, "", 405],
    ["/api/v1/users/list", "POST", "{}", "https://attacker.test", 403],
    ["/api/v1/users/create", "POST", " ".repeat(65537), config.publicOrigin, 400],
  ] as const) {
    const result = await proxyAPI(new Request(config.publicOrigin + path, { method, body,
      headers: { Origin: origin, "Content-Type": "application/json" } }), config, fetcher);
    assert.equal(result.status, expected);
  }
  assert.equal(calls, 0);
});
