import assert from "node:assert/strict";
import { test } from "node:test";
import { createAPIKeysClient } from "@sub-pool/contracts/client";
import { proxyAPI } from "./api-proxy";

const config = { backendOrigin: "http://127.0.0.1:18663", publicOrigin: "http://127.0.0.1:18665", allowLoopbackHTTP: true };
const id = "A".repeat(22), at = "2026-11-08T00:00:00.000001Z";
const key = { id, name: "report client", mode: "read", prefix: "spk_live_" + id, revision: "1",
  created_at: at, rotated_at: null, last_used_at: null, revoked_at: null };
const issuance = { key, secret: key.prefix + "_" + "A".repeat(43) };
const cookie = "sub_pool_dev_session=" + "B".repeat(43);
const request = (action: string, body: unknown = {}, method = "POST") => new Request(config.publicOrigin + "/api/v1/api-keys/" + action,
  { method, headers: { "Origin": config.publicOrigin, "Cookie": cookie, "Content-Type": "application/json" }, ...(method === "POST" ? { body: JSON.stringify(body) } : {}) });

test("Key proxy forwards only the browser cookie to fixed Go routes and validates one-time lifecycle outputs", async () => {
  const seen: string[] = [];
  const client = createAPIKeysClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init);
    browser.headers.set("Cookie", cookie); browser.headers.set("Origin", config.publicOrigin);
    browser.headers.set("Authorization", "Bearer forged-machine-identity"); browser.headers.set("X-Forwarded-For", "203.0.113.1");
    return proxyAPI(browser, config, async (url, forwarded) => {
      assert.ok(String(url).startsWith(config.backendOrigin + "/api/v1/api-keys/"));
      const headers = new Headers(forwarded?.headers); assert.equal(headers.get("Cookie"), cookie);
      assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("X-Forwarded-For"), null);
      assert.equal(forwarded?.cache, "no-store"); assert.equal(forwarded?.redirect, "error");
      const action = String(url).split("/").at(-1); assert.ok(action); seen.push(action);
      if (action === "access") {
        assert.equal(forwarded?.method, "GET"); assert.equal(forwarded?.body, undefined);
        return Response.json({ user: { id, username: "reader", role: "user", revision: "1" }, all_data: false, features: [], account_ids: [], participant_ids: [] });
      }
      assert.equal(forwarded?.method, "POST"); assert.equal(headers.get("Origin"), config.publicOrigin);
      const value = action === "create" ? issuance : action === "rotate" ? { ...issuance, key: { ...key, revision: "2", rotated_at: at } }
        : action === "revoke" ? { ...key, revision: "3", rotated_at: at, revoked_at: at } : { keys: [key], next_after: null };
      return Response.json(value);
    });
  });
  assert.equal((await client.access({})).user.id, id);
  assert.equal((await client.create({ name: key.name, mode: "read" })).secret, issuance.secret);
  assert.equal((await client.rotate({ id, revision: "1" })).key.revision, "2");
  assert.equal((await client.revoke({ id, revision: "2" })).revoked_at, at);
  assert.equal(JSON.stringify(await client.list({ after: "", limit: 100 })).includes(issuance.secret), false);
  assert.deepEqual(seen, ["access", "create", "rotate", "revoke", "list"]);
});

test("Key proxy rejects undeclared paths, methods, queries and untrusted browser mutations before dispatch", async () => {
  let calls = 0; const fetcher: typeof fetch = async () => { calls++; return Response.json(issuance); };
  const noOrigin = request("create"); noOrigin.headers.delete("Origin");
  const encoded = request("create"); encoded.headers.set("Content-Encoding", "gzip");
  const duplicate = request("create"); duplicate.headers.set("Cookie", cookie + "; " + cookie);
  const query = new Request(config.publicOrigin + "/api/v1/api-keys/create?hidden=%GG", { method: "POST", headers: noOrigin.headers, body: "{}" });
  for (const [req, status] of [[noOrigin, 403], [encoded, 400], [duplicate, 401], [query, 400],
    [request("create", {}, "GET"), 405], [request("catalog"), 405], [request("future-write"), 404]] as const) {
    assert.equal((await proxyAPI(req, config, fetcher)).status, status);
  }
  assert.equal(calls, 0);
});

test("Key proxy keeps lost or malformed issuance uncertain, never retries and never leaks an undeclared secret", async () => {
  let calls = 0;
  const lost = createAPIKeysClient(config.publicOrigin, async (input, init) => {
    const req = new Request(input, init); req.headers.set("Cookie", cookie); req.headers.set("Origin", config.publicOrigin);
    return proxyAPI(req, config, async () => { calls++; throw new Error("private transport detail"); });
  });
  await assert.rejects(lost.create({ name: key.name, mode: "read" })); assert.equal(calls, 1);
  const metadataLeak = await proxyAPI(request("list"), config, async () => Response.json({ keys: [{ ...key, secret: issuance.secret }], next_after: null }));
  assert.equal(metadataLeak.status, 502); assert.ok(!(await metadataLeak.text()).includes(issuance.secret));
  const unexpectedCookie = await proxyAPI(request("create"), config, async () => Response.json(issuance, { headers: { "Set-Cookie": cookie + "; Path=/; HttpOnly; SameSite=Lax" } }));
  assert.equal(unexpectedCookie.status, 502); assert.ok(!(await unexpectedCookie.text()).includes(issuance.secret));
  for (const code of ["UNAUTHORIZED", "FORBIDDEN", "SOURCE_BLOCKED", "NOT_FOUND", "CONFLICT"] as const) {
    const status = code === "UNAUTHORIZED" ? 401 : code === "NOT_FOUND" ? 404 : code === "CONFLICT" ? 409 : 403;
    const response = await proxyAPI(request("revoke"), config, async () => Response.json({ defined: true, code, status,
      message: "private detail", data: { retry_at: null } }, { status }));
    assert.equal(response.status, status); assert.ok(!(await response.text()).includes("private detail"));
  }
});
