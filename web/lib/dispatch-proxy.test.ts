import assert from "node:assert/strict";
import { test } from "node:test";
import { proxyAPI } from "./api-proxy";

const config = { backendOrigin: "http://127.0.0.1:8081", publicOrigin: "https://pool.example", allowLoopbackHTTP: true };
const cookie = "__Host-sub_pool_session=" + "A".repeat(43), id = "A".repeat(21) + "Q";
const capability = { can_pause: false, can_restore: false, automatic_restore_available: false, history_state: "unavailable", reason: "SCHEDULING_CONTROL_UNQUALIFIED", authority_revision: "1" };
const list = { ...capability, connection_revision: "3", accounts: [{ id, revision: "2", name: "Root", source_id: id, source_current: true }], next_after: null };
const input = { id, revision: "2", source_id: id, connection_revision: "3" };
const observation = { ...capability, ...input, checked_at: "2026-11-08T00:00:00Z", configured_schedulable: false, native_status: "disabled", read: { id: "dispatch.account.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 } };
const request = (path = "list", body: object = { after: "", search: "", limit: 100 }) => new Request(config.publicOrigin + "/api/v1/dispatch/" + path, { method: "POST", headers: { Origin: config.publicOrigin, "Content-Type": "application/json", Cookie: "unrelated=private; " + cookie, Authorization: "private-browser", "x-api-key": "private-key" }, body: JSON.stringify(body) });

test("dispatch proxy uses fixed validated reads and isolates browser credentials", async () => {
  let calls = 0;
  const result = await proxyAPI(request(), config, async (target, init) => {
    calls++; const forwarded = new Request(target, init);
    assert.equal(forwarded.url, config.backendOrigin + "/api/v1/dispatch/list"); assert.equal(forwarded.method, "POST");
    assert.equal(forwarded.headers.get("cookie"), cookie); assert.equal(forwarded.headers.get("Authorization"), null); assert.equal(forwarded.headers.get("x-api-key"), null); assert.equal(forwarded.headers.get("Origin"), config.publicOrigin);
    assert.deepEqual(await forwarded.json(), { after: "", search: "", limit: 100 }); assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error"); assert.equal(init?.credentials, "omit");
    return Response.json(list);
  });
  assert.equal(result.status, 200); assert.equal(result.headers.get("cache-control"), "no-store"); assert.deepEqual(await result.json(), list); assert.equal(calls, 1);
  const detail = await proxyAPI(request("account", input), config, async () => { calls++; return Response.json(observation); });
  assert.equal(detail.status, 200); assert.equal((await detail.json()).configured_schedulable, false); assert.equal(calls, 2);
});

test("dispatch proxy rejects forged capability, inconsistent unknown, raw fields and cookies without replay", async () => {
  for (const bad of [{ ...observation, can_pause: true }, { ...observation, can_restore: true }, { ...observation, authority_revision: "0" }, { ...observation, configured_schedulable: null }, { ...observation, read: { ...observation.read, state: "failed", reason: "ACCOUNT_IDENTITY_CHANGED" } }, { ...observation, credentials: "private" }]) {
    let calls = 0; const result = await proxyAPI(request("account", input), config, async () => { calls++; return Response.json(bad); }); assert.equal(result.status, 502); assert.equal(calls, 1);
  }
  const partial = { ...observation, configured_schedulable: null, native_status: null, read: { ...observation.read, state: "denied", reason: "FORBIDDEN", http_status: 403 } };
  assert.equal((await proxyAPI(request("account", input), config, async () => Response.json(partial))).status, 200);
  assert.equal((await proxyAPI(request(), config, async () => Response.json(list, { headers: { "Set-Cookie": cookie } }))).status, 502);
  for (const [code, status] of [["CONNECTION_NOT_CONFIGURED", 409], ["SECRET_STORAGE_UNAVAILABLE", 503]] as const) {
    let calls = 0; const result = await proxyAPI(request("account", input), config, async () => { calls++; return Response.json({ defined: true, code, status, message: "private-native-error", data: { retry_at: null } }, { status }); });
    assert.equal(result.status, status); assert.equal(JSON.stringify(await result.json()).includes("private-native-error"), false); assert.equal(calls, 1);
  }
});

test("dispatch proxy refuses undeclared writes and malformed browser boundaries before dispatch", async () => {
  let calls = 0; const fetcher: typeof fetch = async () => { calls++; throw new Error("must not dispatch"); };
  for (const action of ["deadline", "force", "toggle"]) assert.equal((await proxyAPI(request(action, {}), config, fetcher)).status, 404);
  const foreign = request(); foreign.headers.set("Origin", "https://foreign.example"); assert.equal((await proxyAPI(foreign, config, fetcher)).status, 403);
  const query = new Request(request().url + "?limit=1", request()); assert.equal((await proxyAPI(query, config, fetcher)).status, 400);
  assert.equal((await proxyAPI(new Request(config.publicOrigin + "/api/v1/dispatch/list"), config, fetcher)).status, 405);
  const duplicate = request(); duplicate.headers.append("Cookie", cookie); assert.equal((await proxyAPI(duplicate, config, fetcher)).status, 401);
  assert.equal(calls, 0);
});
