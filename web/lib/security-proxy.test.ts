import assert from "node:assert/strict";
import { test } from "node:test";
import { createSecurityClient } from "@sub-pool/contracts/client";
import { proxyAPI } from "./api-proxy";
import { securityFailure } from "./security-data";

const config = { backendOrigin: "http://127.0.0.1:18663", publicOrigin: "http://127.0.0.1:18665", allowLoopbackHTTP: true };
const cookie = "sub_pool_dev_session=" + "B".repeat(43), actor = "A".repeat(22), at = "2026-11-08T01:00:00Z";
const source = { ip: "127.0.0.1", peer_ip: "127.0.0.1", kind: "peer" };
const rule = { ip: "192.0.2.1", revision: "1", blocked: true, saved_at: at, actor_id: actor, reason: "限时校验", expires_at: "2026-11-08T01:01:00Z" };
const outputs: Record<string, unknown> = {
  public: { network_diagnostics_enabled: false }, source, config: { revision: "1", network_diagnostics_enabled: false },
  "config/update": { revision: "2", network_diagnostics_enabled: true }, "sources/list": { rules: [rule], next_after: null }, "sources/update": rule,
  "network/compare": { reported_ip: rule.ip, trusted: false, server_source: source },
  "login-audit": { events: [], next_after: null, counts: { success: "0", failure: "0", limited: "0", blocked: "0" } },
  "management-audit": { events: [], next_after: null },
};
const request = (action: string, method = "POST") => new Request(config.publicOrigin + "/api/v1/security/" + action, { method,
  headers: { Origin: config.publicOrigin, Cookie: cookie, "Content-Type": "application/json" }, ...(method === "POST" ? { body: "{}" } : {}) });

test("security proxy uses the closed cookie-only routes and validates all current client operations", async () => {
  const seen: string[] = [];
  const client = createSecurityClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init); browser.headers.set("Cookie", cookie); browser.headers.set("Origin", config.publicOrigin);
    browser.headers.set("Authorization", "Bearer forged"); browser.headers.set("X-Forwarded-For", "203.0.113.55");
    const response = await proxyAPI(browser, config, async (url, forwarded) => {
      assert.ok(String(url).startsWith(config.backendOrigin + "/api/v1/security/"));
      const action = String(url).slice((config.backendOrigin + "/api/v1/security/").length), headers = new Headers(forwarded?.headers);
      seen.push(action); assert.equal(headers.get("Cookie"), cookie); assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("X-Forwarded-For"), null);
      assert.equal(forwarded?.cache, "no-store"); assert.equal(forwarded?.redirect, "error");
      assert.equal(forwarded?.method, ["public", "source", "config"].includes(action) ? "GET" : "POST");
      return Response.json(outputs[action]);
    });
    assert.equal(response.headers.get("Cache-Control"), "no-store"); return response;
  });
  assert.equal((await client.publicConfig({})).network_diagnostics_enabled, false); assert.equal((await client.source({})).kind, "peer"); await client.config({});
  await client.updateConfig({ revision: "1", network_diagnostics_enabled: true }); await client.sources({ after: "", state: "all", limit: 100 });
  const update = { ip: rule.ip, revision: "0", blocked: true, reason: rule.reason, expires_at: rule.expires_at };
  assert.deepEqual(await client.updateSource(update), rule);
  await client.compareNetwork({ reported_ip: rule.ip, consent: true });
  const range = { from: "2026-11-08T00:00:00Z", until: "2026-11-09T00:00:00Z", after: "0", limit: 100 };
  await client.loginAudit({ ...range, outcome: "all" }); await client.managementAudit(range);
  assert.equal(new Set(seen).size, 9);
});

test("security proxy rejects invalid source/secret replies, undeclared paths, cross-origin writes and laundering of security errors", async () => {
  for (const value of [{ ...source, ip: null }, { ...source, secret: "private-result" }]) {
    const response = await proxyAPI(request("source", "GET"), config, async () => Response.json(value));
    assert.equal(response.status, 502); assert.ok(!(await response.text()).includes("private-result"));
  }
  let calls = 0; const fetcher = async () => { calls++; return Response.json(outputs.public); };
  assert.equal((await proxyAPI(request("other", "GET"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("source"), config, fetcher)).status, 405);
  assert.equal((await proxyAPI(request("source?extra=1", "GET"), config, fetcher)).status, 400);
  const foreign = request("sources/update"); foreign.headers.set("Origin", "https://untrusted.example");
  assert.equal((await proxyAPI(foreign, config, fetcher)).status, 403); assert.equal(calls, 0);
  const error = { defined: true, code: "SELF_SOURCE_PROTECTED", status: 409, data: { retry_at: null }, message: "private-result" };
  const safe = await proxyAPI(request("sources/update"), config, async () => Response.json(error, { status: 409 }));
  assert.equal(safe.status, 409); assert.ok(!(await safe.text()).includes("private-result"));
  const other = new Request(config.publicOrigin + "/api/v1/api-keys/list", request("sources/list"));
  assert.equal((await proxyAPI(other, config, async () => Response.json(error, { status: 409 }))).status, 502);
});

test("a lost security write response dispatches once and remains unknown; declared self-protection stays a safe conflict", async () => {
  let writes = 0, mode = "lost";
  const client = createSecurityClient(config.publicOrigin, async (input, init) => {
    const browser = new Request(input, init); browser.headers.set("Cookie", cookie); browser.headers.set("Origin", config.publicOrigin);
    return proxyAPI(browser, config, async () => {
      writes++; if (mode === "lost") throw new Error("private transport error");
      return Response.json({ defined: true, code: "SELF_SOURCE_PROTECTED", status: 409, data: { retry_at: null } }, { status: 409 });
    });
  });
  const change = { ip: rule.ip, revision: "0", blocked: true };
  await assert.rejects(client.updateSource(change), error => { assert.equal(securityFailure(error).status, 0); return true; });
  assert.equal(writes, 1); mode = "self";
  await assert.rejects(client.updateSource(change), error => { assert.equal(securityFailure(error).status, 409); assert.match(securityFailure(error).text, /管理连接受保护/); return true; });
  assert.equal(writes, 2);
});
