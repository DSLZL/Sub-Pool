import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { authVersion } from "@sub-pool/contracts/auth";
import { createAuthClient } from "@sub-pool/contracts/client";
import { connectionVersion } from "@sub-pool/contracts/connection";
import { dispatchControlVersion } from "@sub-pool/contracts/dispatch";
import { proxyAPI } from "./api-proxy";
import { connectionFailure } from "./connection-data";
import { authFailure } from "./session";

const config = { backendOrigin: "http://127.0.0.1:8081", publicOrigin: "http://127.0.0.1:3000", allowLoopbackHTTP: true };
const session = {
  user: { id: "A".repeat(22), username: "admin", role: "admin", revision: "1" },
  created_at: "2026-11-06T00:00:00Z", server_time: "2026-11-06T01:00:00Z",
  idle_expires_at: "2026-11-06T08:00:00Z", absolute_expires_at: "2026-11-13T00:00:00Z",
};

test("write grant routes validate the password boundary and return only the safe receipt", async () => {
  const path = "/api/v1/connection/write-grants";
  const hash = "c".repeat(64);
  const view = { source_id: "A".repeat(22), connection_revision: "2", upstream_writes_disabled: true,
    confirmation_text: "Confirm current write authority.", confirmation_sha256: hash,
    grants: ["pricing", "scheduling", "wallet"].map(capability => ({ capability, grant: null })) };
  const input = { source_id: view.source_id, connection_revision: "2", capability: "pricing", expected_grant_id: "0",
    enabled: true, confirmation_sha256: hash, current_password: "TEST_ONLY_PASSWORD" };
  const post = (value: unknown, origin = config.publicOrigin) => new Request(config.publicOrigin + path + "/update", {
    method: "POST", body: JSON.stringify(value), headers: { Origin: origin, "Content-Type": "application/json" },
  });
  const forbiddenFetch: typeof fetch = async () => { assert.fail("invalid grant request reached Go"); };
  assert.equal((await proxyAPI(post({ ...input, expected_grant_id: null }), config, forbiddenFetch)).status, 400);
  assert.equal((await proxyAPI(post({ ...input, token: "unexpected" }), config, forbiddenFetch)).status, 400);
  assert.equal((await proxyAPI(post(input, "https://other.test"), config, forbiddenFetch)).status, 403);
  assert.equal((await proxyAPI(new Request(config.publicOrigin + path + "/update"), config, forbiddenFetch)).status, 405);
  const read = await proxyAPI(new Request(config.publicOrigin + path), config, async (url, init) => {
    assert.equal(url, config.backendOrigin + path);
    assert.equal(init?.method, "GET");
    return Response.json(view);
  });
  assert.deepEqual(await read.json(), view);
  let calls = 0;
  const denied = await proxyAPI(post(input), config, async (url, init) => {
    calls++;
    assert.equal(url, config.backendOrigin + path + "/update");
    assert.equal(init?.method, "POST");
    return Response.json({ defined: true, code: "CURRENT_PASSWORD_INVALID", status: 403, message: input.current_password,
      data: { retry_at: null } }, { status: 403, headers: { "X-Request-ID": "0".repeat(32) } });
  });
  assert.equal(denied.status, 403);
  assert.equal(calls, 1);
  assert.equal(denied.headers.get("X-Request-ID"), "0".repeat(32));
  const error = await denied.json();
  assert.equal(error.code, "CURRENT_PASSWORD_INVALID");
  assert.ok(!JSON.stringify(error).includes(input.current_password));
  const leaked = await proxyAPI(post(input), config, async () => Response.json({ ...view, password: input.current_password }));
  assert.equal(leaked.status, 502);
  assert.ok(!(await leaked.text()).includes(input.current_password));
});

test("dispatch lifecycle proxy forwards one fixed write and rejects unverified success without retry", async () => {
  const pause = { id: "1", revision: "2", account_id: "A".repeat(21) + "Q", source_id: "B".repeat(21) + "A",
    connection_revision: "3", account_revision: "1", state: "paused", reason: "VERIFIED_PAUSE", previous_schedulable: true,
    native_updated_at: "2026-11-09T00:00:00Z", restore_at: "2026-11-09T01:00:00Z",
    created_at: "2026-11-09T00:00:00Z", updated_at: "2026-11-09T00:00:00Z" };
  const req = () => new Request(config.publicOrigin + "/api/v1/dispatch/pause", {
    method: "POST", body: "{}", headers: { Origin: config.publicOrigin, "Content-Type": "application/json" },
  });
  let calls = 0;
  const result = await proxyAPI(req(), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/dispatch/pause");
    assert.equal(init?.method, "POST"); assert.equal(init?.redirect, "error"); return Response.json(pause);
  });
  assert.equal(result.status, 200); assert.deepEqual(await result.json(), pause); assert.equal(calls, 1);
  const malformed = await proxyAPI(req(), config, async () => { calls++; return Response.json({ ...pause, credentials: "PRIVATE_SENTINEL" }); });
  assert.equal(malformed.status, 502); assert.ok(!(await malformed.text()).includes("PRIVATE_SENTINEL")); assert.equal(calls, 2);
  const lost = await proxyAPI(req(), config, async () => { calls++; throw new Error("PRIVATE_SENTINEL"); });
  assert.equal(lost.status, 502); assert.equal(calls, 3);
});
test("pool mutation conflicts retain their reason through the fixed proxy without replay", async () => {
  for (const action of ["create", "update", "disable"]) {
    let calls = 0;
    const result = await proxyAPI(business("pools/" + action, "POST", "{}"), config, async (url) => {
      calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/pools/" + action);
      return Response.json({ defined: true, code: "POOL_CHANGE_BLOCKED", status: 409, message: "PRIVATE_SENTINEL", data: { retry_at: null } }, { status: 409 });
    });
    assert.equal(result.status, 409); assert.equal(calls, 1);
    const body = await result.json(); assert.equal(body.code, "POOL_CHANGE_BLOCKED");
    assert.ok(!JSON.stringify(body).includes("PRIVATE_SENTINEL"));
    const failure = connectionFailure(new ORPCError(body.code, { defined: true, status: 409, data: body.data })); assert.equal(failure.status, 409);
    assert.match(failure.text, /未结/); assert.match(failure.text, /未保存池变更/);
  }
});

function request(action: string, method = "GET", body?: string, headers?: Record<string, string>) {
  return new Request(config.publicOrigin + "/api/v1/auth/" + action, { method, body, headers });
}

test("request correlation preserves a trusted response ID through strict proxy and client errors", async () => {
  const id = "0123456789abcdef".repeat(2);
  const passed = await proxyAPI(request("session", "GET", undefined, { "X-Request-ID": "f".repeat(32) }), config, async (_url, init) => {
    assert.equal(new Headers(init?.headers).get("X-Request-ID"), null);
    return Response.json(session, { headers: { "X-Request-ID": id } });
  });
  assert.equal(passed.headers.get("X-Request-ID"), id);
  assert.deepEqual(await passed.json(), session);

  for (const known of [true, false]) {
    const client = createAuthClient(config.publicOrigin, async (input, init) => proxyAPI(new Request(input, init), config, async () =>
      Response.json(known ? { defined: true, code: "INTERNAL_SERVER_ERROR", status: 500, message: "RAW_SECRET", data: { retry_at: null } }
        : { bad_response: "RAW_SECRET" }, { status: known ? 500 : 503, headers: { "X-Request-ID": id } })));
    await assert.rejects(client.current({}), (error: unknown) => {
      assert.ok(error instanceof ORPCError);
      assert.equal(error.status, known ? 500 : 502);
      assert.equal(error.defined, known);
      for (const text of [error.message, authFailure(error, true).message, connectionFailure(error).text]) {
        assert.ok(text.includes(id));
        assert.ok(!text.includes("RAW_SECRET"));
        assert.equal(text.includes("前后端版本不一致，请检查部署。"), !known);
      }
      if (!known) assert.deepEqual(error.data, { route: "/api/v1/auth/session", expected_version: authVersion, request_id: id });
      assert.equal(connectionFailure(error).status, 0);
      return true;
    });
  }

  const invalid = await proxyAPI(request("session"), config, async () => Response.json({ malformed: "RAW_SECRET" }, { headers: { "X-Request-ID": "RAW_SECRET" } }));
  const fallbackId = invalid.headers.get("X-Request-ID") ?? "";
  assert.match(fallbackId, /^[0-9a-f]{32}$/);
  assert.equal(invalid.status, 502);
  assert.deepEqual(await invalid.json(), {
    defined: false, code: "CONTRACT_MISMATCH", status: 502,
    message: `前后端版本不一致，请检查部署。 请求编号 ${fallbackId}`,
    data: { route: "/api/v1/auth/session", expected_version: authVersion, request_id: fallbackId },
  });
});

test("contract diagnostics contain only route, expected version and request ID, while transport failures stay generic", async (t) => {
  const reports: string[] = [];
  t.mock.method(console, "error", (entry: string) => reports.push(entry));
  const id = "a".repeat(32);
  const expected = [
    { route: "/api/v1/connection", expected_version: connectionVersion, request_id: id },
    { route: "/api/v1/dispatch/control", expected_version: dispatchControlVersion, request_id: id },
  ];
  for (const [index, data] of expected.entries()) {
    const request = new Request(config.publicOrigin + data.route, index === 0 ? undefined : {
      method: "POST", headers: { Origin: config.publicOrigin, "Content-Type": "application/json" }, body: "{}",
    });
    const response = await proxyAPI(request, config, async () => Response.json({ malformed: "RAW_SECRET" }, { headers: { "X-Request-ID": id } }));
    assert.equal(response.status, 502);
    assert.equal(response.headers.get("X-Request-ID"), id);
    assert.deepEqual(await response.json(), {
      defined: false, code: "CONTRACT_MISMATCH", status: 502,
      message: `前后端版本不一致，请检查部署。 请求编号 ${id}`, data,
    });
  }

  const disconnected = await proxyAPI(request("session"), config, async () => { throw new Error("RAW_SECRET"); });
  assert.equal(disconnected.status, 502);
  assert.deepEqual(await disconnected.json(), { message: "请求未完成，请核对当前状态。" });
  const truncated = await proxyAPI(request("session"), config, async () => new Response(
    new ReadableStream<Uint8Array>({ start(controller) { controller.error(new Error("RAW_SECRET")); } }),
    { headers: { "X-Request-ID": id } },
  ));
  assert.equal(truncated.status, 502);
  assert.deepEqual(await truncated.json(), {
    defined: false, code: "BAD_GATEWAY", status: 502, message: `请求未完成，请核对当前状态。 请求编号 ${id}`,
  });
  assert.deepEqual(reports.map(entry => JSON.parse(entry)), expected.map(data => ({ level: "error", event: "contract_mismatch", ...data })));
});

test("readiness proxy forwards only the fixed probe and checks status against the body", async () => {
  const url = config.publicOrigin + "/health/ready";
  for (const [status, state] of [[200, "ok"], [503, "unavailable"], [200, "unavailable"], [503, "ok"]] as const) {
    const response = await proxyAPI(new Request(url), config, async (target, init) => {
      assert.equal(target, config.backendOrigin + "/health/ready");
      assert.equal(init?.method, "GET");
      return Response.json({ status: state, scope: "readiness" }, { status });
    });
    const consistent = (status === 200) === (state === "ok");
    assert.equal(response.status, consistent ? status : 502);
    if (consistent) assert.deepEqual(await response.json(), { status: state, scope: "readiness" });
  }
});

test("auth proxy validates public and backend origins before forwarding", async () => {
  const httpsOrigin = "https://console.example.test";
  const httpsBackend = "https://go.example.test";
  const publicHTTP = "http://console.example.test";
  for (const [publicOrigin, backendOrigin, allowLoopbackHTTP, expected] of [
    [httpsOrigin, httpsBackend, false, 200],
    [httpsOrigin, config.backendOrigin, true, 200],
    [config.publicOrigin, config.backendOrigin, true, 200],
    ["http://[::1]:3000", config.backendOrigin, true, 200],
    [config.publicOrigin, httpsBackend, false, 503],
    [httpsOrigin, config.backendOrigin, false, 503],
    [publicHTTP, httpsBackend, false, 503],
    [publicHTTP, httpsBackend, true, 503],
    [httpsOrigin, publicHTTP, true, 503],
  ] as const) {
    let calls = 0;
    const request = new Request(publicOrigin + "/api/v1/auth/session");
    const response = await proxyAPI(request, { publicOrigin, backendOrigin, allowLoopbackHTTP }, async () => {
      calls++;
      return Response.json(session);
    });
    assert.equal(response.status, expected, `${publicOrigin} via ${backendOrigin} (loopback HTTP ${allowLoopbackHTTP})`);
    assert.equal(calls, expected === 200 ? 1 : 0);
  }
});

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

const connection = { revision: "1", source_id: null, base_url: "", credential_set: false, credential_kind: null,
  secret_storage_available: true, saved_at: null, diagnostic: null };
function business(path: string, method = "GET", body?: string, origin = config.publicOrigin) {
  return new Request(config.publicOrigin + "/api/v1/" + path, { method, body, headers: {
    Origin: origin, "Content-Type": "application/json", Cookie: "sub_pool_dev_session=" + "A".repeat(43),
    Authorization: "PRIVATE_AUTH", "X-User-Role": "admin", "X-Forwarded-For": "203.0.113.9",
  } });
}

test("restored connection conflicts remain definite, scoped and sanitized without retry", async () => {
  const failure = { defined: true, code: "CONNECTION_REVERIFICATION_REQUIRED", status: 409,
    message: "PRIVATE_SENTINEL", data: { retry_at: null } };
  let calls = 0;
  for (const path of ["connection/discover", "accounts/refresh"]) {
    const before = calls;
    const response = await proxyAPI(business(path, "POST", "{}"), config, async () => {
      calls++; return Response.json(failure, { status: 409 });
    });
    assert.equal(response.status, 409); assert.equal(calls, before + 1);
    const value = await response.json();
    assert.equal(value.code, failure.code); assert.equal(value.defined, true);
    assert.ok(!JSON.stringify(value).includes("PRIVATE_SENTINEL"));
    const feedback = connectionFailure(new ORPCError(value.code, value));
    assert.equal(feedback.status, 409); assert.match(feedback.text, /重新验证.*连接/);
  }
  for (const [path, method, body, value, status] of [
    ["auth/session", "GET", undefined, failure, 409],
    ["connection/discover", "POST", "{}", { ...failure, status: 500 }, 500],
    ["connection/discover", "POST", "{}", { ...failure, defined: false }, 409],
    ["connection/discover", "POST", "{}", { ...failure, data: { retry_at: null, secret: "PRIVATE_SENTINEL" } }, 409],
  ] as const) {
    const before = calls;
    const response = await proxyAPI(business(path, method, body), config, async () => {
      calls++; return Response.json(value, { status });
    });
    assert.equal(response.status, 502); assert.equal(calls, before + 1);
    assert.ok(!(await response.text()).includes("PRIVATE_SENTINEL"));
  }
});


test("connection impact is a fixed read, strips credentials and rejects unsafe or malformed success", async () => {
  const value = { revision: "1", source_id: null, checked_at: "2026-11-14T01:02:03Z", managed_accounts: "0",
    participant_bindings: "0", dispatch_open: "0", pricing_open: "0", wallet_rejected: "0", wallet_unconfirmed: "0" };
  let calls = 0;
  const result = await proxyAPI(business("connection/impact"), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/connection/impact"); assert.equal(init?.method, "GET");
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("X-User-Role"), null);
    assert.equal(headers.get("X-Forwarded-For"), null); assert.ok(headers.get("Cookie")?.startsWith("sub_pool_dev_session="));
    return Response.json(value);
  });
  assert.equal(result.status, 200); assert.equal(result.headers.get("Cache-Control"), "no-store");
  assert.deepEqual(await result.json(), value); assert.equal(calls, 1);
  const malformed = await proxyAPI(business("connection/impact"), config, async () => { calls++; return Response.json({ ...value, secret: "PRIVATE" }); });
  assert.equal(malformed.status, 502); assert.ok(!(await malformed.text()).includes("PRIVATE"));
  const fetcher: typeof fetch = async () => { calls++; return Response.json(value); };
  assert.equal((await proxyAPI(business("connection/impact", "POST", "{}"), config, fetcher)).status, 405);
  assert.equal((await proxyAPI(business("connection/impact?source=other"), config, fetcher)).status, 400);
  assert.equal(calls, 2);
  const denied = { defined: true, code: "FORBIDDEN", status: 403, message: "PRIVATE", data: { retry_at: null } };
  const forbidden = await proxyAPI(business("connection/impact"), config, async () => Response.json(denied, { status: 403 }));
  assert.equal(forbidden.status, 403); assert.ok(!(await forbidden.text()).includes("PRIVATE"));
});

test("account refresh forwards one fixed request, validates metadata and never replays a lost result", async () => {
  const id = "a".repeat(21) + "A", stamp = "2026-11-13T00:00:00Z";
  const read = { id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 };
  const value = { connection_revision: "3", account: { id, revision: "2", source_id: id, upstream_id: "1", name: "Local",
    monitoring: true, source_mode: "passive", source_current: true, identity_state: "verified", connection_available: true,
    observed_connection_revision: "3", collection_state: "awaiting_collector", read,
    snapshot: { account: { upstream_id: "1", name: "Remote", platform: "openai", type: "oauth", status: "active", schedulable: true,
      eligibility: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" }, runtime: { last_used_at: null, rate_limited_at: null,
      rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, error_present: null } },
    last_attempt_at: stamp, observed_at: stamp, created_at: stamp, saved_at: stamp } };
  const body = JSON.stringify({ id, revision: "1", source_id: id, connection_revision: "3" });
  const input = () => business("accounts/refresh", "POST", body);
  let calls = 0;
  const result = await proxyAPI(input(), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/accounts/refresh");
    assert.equal(init?.method, "POST"); assert.equal(init?.redirect, "error");
    assert.equal(new TextDecoder().decode(init?.body as Uint8Array), body);
    return Response.json(value);
  });
  assert.equal(result.status, 200); assert.deepEqual(await result.json(), value); assert.equal(calls, 1);
  const malformed = await proxyAPI(input(), config, async () => { calls++; return Response.json({ ...value, credential: "PRIVATE_SENTINEL" }); });
  assert.equal(malformed.status, 502); assert.ok(!(await malformed.text()).includes("PRIVATE_SENTINEL")); assert.equal(calls, 2);
  const lost = await proxyAPI(input(), config, async () => { calls++; throw new Error("PRIVATE_SENTINEL"); });
  assert.equal(lost.status, 502); assert.equal(calls, 3);
  const fetcher: typeof fetch = async () => { calls++; return Response.json(value); };
  assert.equal((await proxyAPI(business("accounts/refresh"), config, fetcher)).status, 405);
  assert.equal((await proxyAPI(business("accounts/refresh", "POST", body, "https://attacker.test"), config, fetcher)).status, 403);
  assert.equal(calls, 3);
});

test("connection proxy uses the fixed destination and exact safe DTO without forwarding browser authority", async () => {
  const result = await proxyAPI(business("connection/save", "POST", "{}"), config, async (url, init) => {
    assert.equal(String(url), config.backendOrigin + "/api/v1/connection/save");
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("origin"), config.publicOrigin);
    assert.equal(headers.get("cookie"), "sub_pool_dev_session=" + "A".repeat(43));
    for (const name of ["authorization", "x-user-role", "x-forwarded-for"]) assert.equal(headers.get(name), null);
    assert.equal(init?.redirect, "error"); assert.equal(init?.cache, "no-store");
    return Response.json({ configuration: connection, source_changed: false });
  });
  assert.equal(result.status, 200); assert.equal(result.headers.get("set-cookie"), null);
  const secret = await proxyAPI(business("connection"), config, async () => Response.json({ ...connection, credential: "PRIVATE_SECRET" }));
  assert.equal(secret.status, 502); assert.ok(!(await secret.text()).includes("PRIVATE_SECRET"));
  const cookie = await proxyAPI(business("settings"), config, async () => Response.json({ revision: "1", timezone: "UTC", saved_at: null },
    { headers: { "Set-Cookie": "sub_pool_dev_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax" } }));
  assert.equal(cookie.status, 502); assert.equal(cookie.headers.get("set-cookie"), null);
});

test("business errors remain domain-bound and uncertain account saves are never replayed", async () => {
  for (const [path, code, status, expected] of [
    ["connection", "SECRET_STORAGE_UNAVAILABLE", 503, 503],
    ["accounts/list", "CONNECTION_NOT_CONFIGURED", 409, 409],
    ["monitoring/status", "SECRET_STORAGE_UNAVAILABLE", 503, 502],
    ["auth/session", "CONNECTION_NOT_CONFIGURED", 409, 502],
  ] as const) {
    let calls = 0;
    const result = await proxyAPI(business(path, path === "accounts/list" ? "POST" : "GET", path === "accounts/list" ? "{}" : undefined), config, async () => {
      calls++;
      return Response.json({ defined: true, code, status, message: "PRIVATE_SECRET", data: { retry_at: null } }, { status });
    });
    assert.equal(result.status, expected); assert.equal(calls, 1); assert.ok(!(await result.text()).includes("PRIVATE_SECRET"));
  }
  let calls = 0;
  const lost = await proxyAPI(business("accounts/update", "POST", "{}"), config, async () => { calls++; throw new TypeError("PRIVATE_SECRET"); });
  assert.equal(lost.status, 502); assert.equal(calls, 1);
});

test("new domain routes reject unknown actions, wrong methods and origins before dispatch", async () => {
  let calls = 0;
  const fetcher: typeof fetch = async () => { calls++; return Response.json({}); };
  for (const [path, method, body, origin, expected] of [
    ["connection/clear", "POST", "{}", config.publicOrigin, 404],
    ["monitoring/restart", "POST", "{}", config.publicOrigin, 404],
    ["settings", "POST", "{}", config.publicOrigin, 405],
    ["connection/discover", "GET", undefined, config.publicOrigin, 405],
    ["monitoring/config/update", "POST", "{}", "https://attacker.test", 403],
    ["accounts/list?after=private", "POST", "{}", config.publicOrigin, 400],
  ] as const) assert.equal((await proxyAPI(business(path, method, body, origin), config, fetcher)).status, expected);
  assert.equal(calls, 0);
});


test("process health is fixed GET-only liveness and cannot claim database or business health", async () => {
  const input = () => new Request(config.publicOrigin + "/health");
  let calls = 0;
  const result = await proxyAPI(input(), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/health"); assert.equal(init?.method, "GET");
    return Response.json({ status: "ok", scope: "process" });
  });
  assert.equal(result.status, 200); assert.equal(calls, 1);
  for (const extra of [{ database: "healthy" }, { scope: "all" }, { secret: "PRIVATE_BODY" }]) {
    const malformed = await proxyAPI(input(), config, async () => Response.json({ status: "ok", scope: "process", ...extra }));
    assert.equal(malformed.status, 502); assert.ok(!(await malformed.text()).includes("PRIVATE_BODY"));
  }
  const denied = await proxyAPI(new Request(config.publicOrigin + "/health", { method: "POST", body: "{}" }), config, async () => { calls++; return Response.json({}); });
  assert.equal(denied.status, 405); assert.equal(calls, 1);
});

test("model reads retain backend administrator authority and never manufacture proven access", async () => {
  let calls = 0;
  const denied = await proxyAPI(business("accounts/models", "POST", "{}"), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/accounts/models");
    assert.equal(new Headers(init?.headers).get("x-user-role"), null);
    return Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "PRIVATE_BODY", data: { retry_at: null } }, { status: 403 });
  });
  assert.equal(denied.status, 403); assert.equal(calls, 1); assert.ok(!(await denied.text()).includes("PRIVATE_BODY"));
  const check = { id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 };
  const value = { account_id: session.user.id, source_id: session.user.id, connection_revision: "1", checked_at: session.created_at,
    models: [], mapping: [], passthrough: null, mapping_mode: "empty", access_proven: false, account_read: check, catalog_read: check };
  assert.equal((await proxyAPI(business("accounts/models", "POST", "{}"), config, async () => Response.json(value))).status, 200);
  const malformed = await proxyAPI(business("accounts/models", "POST", "{}"), config, async () => Response.json({ ...value, access_proven: true }));
  assert.equal(malformed.status, 502);
});


test("observation recommendation proxy validates the immutable reference and preserves denial", async () => {
  const link = { observation_id: "9007199254740993", account_id: "A".repeat(21) + "Q", recommendation: { id: "9007199254740995", participant_id: "B".repeat(21) + "A", estimate_id: "9007199254740994", generated_at: "2026-11-13T12:00:00.123456Z" } };
  const req = () => new Request(config.publicOrigin + "/api/v1/observations/recommendation-link", { method: "POST", body: JSON.stringify({ id: link.observation_id }), headers: { Origin: config.publicOrigin, "Content-Type": "application/json" } });
  let calls = 0;
  const response = await proxyAPI(req(), config, async (url, init) => {
    calls++;
    assert.equal(String(url), config.backendOrigin + "/api/v1/observations/recommendation-link");
    assert.equal(init?.method, "POST");
    assert.equal(init?.redirect, "error");
    return Response.json(link);
  });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), link);
  const missing = await proxyAPI(req(), config, async () => { calls++; return Response.json({ ...link, recommendation: null }); });
  assert.equal(missing.status, 200);
  assert.equal((await missing.json()).recommendation, null);
  const denied = await proxyAPI(req(), config, async () => { calls++; return Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "PRIVATE_SENTINEL", data: { retry_at: null } }, { status: 403 }); });
  assert.equal(denied.status, 403);
  assert.ok(!(await denied.text()).includes("PRIVATE_SENTINEL"));
  for (const recommendation of [{ ...link.recommendation, target: "PRIVATE_SENTINEL" }, { ...link.recommendation, estimate_id: "01" }]) {
    const malformed = await proxyAPI(req(), config, async () => { calls++; return Response.json({ ...link, recommendation }); });
    assert.equal(malformed.status, 502);
    assert.ok(!(await malformed.text()).includes("PRIVATE_SENTINEL"));
  }
  assert.equal(calls, 5);
});

test("observation usage evidence proxy validates scope and never forwards a raw capture", async () => {
  const value = { observation_id: "9", account_id: session.user.id, scope: "authorized_participants", basis: "native_window_interval", currency: "USD", timestamp_basis: "native_created_at", complete: false, settlement_eligible: false, windows: [] };
  const req = () => business("observations/usage-evidence", "POST", '{"id":"9"}');
  let calls = 0;
  const good = await proxyAPI(req(), config, async (url, init) => {
    calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/observations/usage-evidence"); assert.equal(init?.method, "POST"); return Response.json(value);
  });
  assert.equal(good.status, 200); assert.deepEqual(await good.json(), value);
  for (const changed of [{ ...value, complete: true }, { ...value, raw: "PRIVATE_SENTINEL" }, { ...value, scope: "complete_wallet" }]) {
    const response = await proxyAPI(req(), config, async () => { calls++; return Response.json(changed); });
    assert.equal(response.status, 502); assert.ok(!(await response.text()).includes("PRIVATE_SENTINEL"));
  }
  const denied = await proxyAPI(req(), config, async () => { calls++; return Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "PRIVATE_SENTINEL", data: { retry_at: null } }, { status: 403 }); });
  assert.equal(denied.status, 403); assert.ok(!(await denied.text()).includes("PRIVATE_SENTINEL")); assert.equal(calls, 5);
});

test("retired wallet controls are closed while the automation history remains readable", async () => {
  const root = "/api/v1/wallet-applications/automation/";
  const request = (action: string) => new Request(config.publicOrigin + root + action, {
    method: "POST", body: "{}", headers: { Origin: config.publicOrigin, "Content-Type": "application/json" },
  });
  let calls = 0;
  const value = { items: [], next_after: null };
  const fetcher: typeof fetch = async (url, init) => {
    calls++;
    assert.equal(String(url), config.backendOrigin + root + "history");
    assert.equal(init?.method, "POST");
    return Response.json(value);
  };
  for (const action of ["enable", "disable"]) assert.equal((await proxyAPI(request(action), config, fetcher)).status, 404);
  const history = await proxyAPI(request("history"), config, fetcher);
  assert.equal(history.status, 200);
  assert.deepEqual(await history.json(), value);
  assert.equal(calls, 1);
});
