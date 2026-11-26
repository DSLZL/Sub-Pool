import assert from "node:assert/strict";
import { test } from "node:test";
import { createTemporaryClient } from "@sub-pool/contracts/client";
import { proxyAPI } from "./api-proxy";

const publicOrigin = "https://pool.test", backendOrigin = "https://go.test";
const config = { backendOrigin, publicOrigin, allowLoopbackHTTP: false };
const cookie = "__Host-sub_pool_session=" + "a".repeat(43);
const requestID = "12".repeat(16);
const history = "/api/v1/temporary/history";
const request = (path: string, origin = publicOrigin, session = cookie) =>
  new Request(publicOrigin + path, { method: "POST", headers: {
    Origin: origin, Cookie: session, "Content-Type": "application/json", Authorization: "private",
  }, body: JSON.stringify({ after: "", limit: 25 }) });

test("retired temporary commands stop at the proxy without forwarding", async () => {
  let calls = 0;
  for (const route of ["status", "preflight", "start", "stop", "rights-change", "reminders"]) {
    const denied = await proxyAPI(request("/api/v1/temporary/" + route), config, async () => {
      calls++;
      return Response.json({});
    });
    assert.equal(denied.status, 404);
  }
  assert.equal(calls, 0);
});

test("temporary history retains the origin, cookie, shape and request-ID boundary", async () => {
  let calls = 0;
  const backend: typeof fetch = async (input, init) => {
    calls++;
    assert.equal(String(input), backendOrigin + history);
    assert.equal(new Headers(init?.headers).get("Authorization"), null);
    assert.equal(new Headers(init?.headers).get("Cookie"), cookie);
    return Response.json({ rounds: [], next_after: null }, { headers: { "X-Request-ID": requestID } });
  };
  const client = createTemporaryClient(publicOrigin, async (input, init) => proxyAPI(new Request(input, { ...init, headers: {
    Origin: publicOrigin, Cookie: cookie, "Content-Type": "application/json",
  } }), config, backend));
  assert.deepEqual(await client.history({ after: "", limit: 25 }), { rounds: [], next_after: null });
  assert.equal((await proxyAPI(request(history, "https://elsewhere.test"), config, backend)).status, 403);
  const unauthorized = await proxyAPI(request(history, publicOrigin, "invalid=header"), config, async (_input, init) => {
    assert.equal(new Headers(init?.headers).get("Cookie"), null);
    return Response.json({ defined: true, code: "UNAUTHORIZED", status: 401, data: { retry_at: null } }, { status: 401 });
  });
  assert.equal(unauthorized.status, 401);
  const wrong = await proxyAPI(request(history), config, async () => Response.json({ rounds: "wrong" }, { headers: { "X-Request-ID": requestID } }));
  assert.equal(wrong.status, 502);
  assert.equal(wrong.headers.get("X-Request-ID"), requestID);
  assert.equal(calls, 1);
});
