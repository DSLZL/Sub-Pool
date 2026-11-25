import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { capabilityCheck, configuration, connectionImpact, connectionOutputs, diagnostic, discoveredAccount, discovery } from "./connection";
import { createAuthClient, createConnectionClient } from "./client";

const id = "a".repeat(21) + "A";
const initial = {
  revision: "1", source_id: null, base_url: "", credential_set: false, credential_kind: null,
  secret_storage_available: true, saved_at: null, diagnostic: null,
};
test("write grant view distinguishes saved authority, revocation and emergency closure", () => {
  const hash = "ab".repeat(32);
  const grant = { id: "7", granted_by: id, granted_at: "2026-11-23T00:00:00Z",
    confirmation_sha256: hash, revoked_at: null, revoked_by: null };
  const view = { source_id: id, connection_revision: "2", upstream_writes_disabled: true,
    confirmation_text: "Review upstream risk.", confirmation_sha256: hash,
    grants: [{ capability: "pricing", grant }, { capability: "scheduling", grant: null }, { capability: "wallet", grant: null }] };
  const schema = connectionOutputs.writeGrants;
  assert.ok(schema.safeParse(view).success);
  assert.ok(schema.safeParse({ ...view, grants: [{ capability: "pricing",
    grant: { ...grant, revoked_at: "2026-11-23T01:00:00Z", revoked_by: id } }, ...view.grants.slice(1)] }).success);
  assert.equal(schema.safeParse({ ...view, source_id: null }).success, false);
  assert.equal(schema.safeParse({ ...view, grants: [view.grants[0], view.grants[0], view.grants[2]] }).success, false);
  assert.equal(schema.safeParse({ ...view, grants: [{ capability: "pricing", grant: { ...grant, revoked_by: id } }, ...view.grants.slice(1)] }).success, false);
});

test("connection output validates secret-free configuration and unknown discovery", () => {
  assert.ok(configuration.safeParse(initial).success);
  for (const value of [
    { ...initial, secret: "never" }, { ...initial, credential_set: true },
    { ...initial, source_id: id }, { ...initial, revision: 1 },
  ]) assert.equal(configuration.safeParse(value).success, false);
  assert.equal(discoveredAccount.safeParse({ upstream_id: "1", name: "a", platform: "openai", type: "oauth",
    status: "active", schedulable: true, eligibility: "available", reason: "RESOURCE_IDENTITY_UNVERIFIED" }).success, false);
  const failed = { revision: "2", source_id: id, page: 1, total: null, next_page: null, accounts: [],
    read: { id: "accounts.read", state: "denied", reason: "FORBIDDEN", http_status: 403, latency_ms: 1 } };
  assert.ok(discovery.safeParse(failed).success);
  assert.equal(discovery.safeParse({ ...failed, total: "0" }).success, false);
});

test("version qualification and readable mode have independent diagnostics", () => {
  const version = { id: "version.read", state: "available", reason: "VERSION_UNVERIFIED", http_status: 200, latency_ms: 0 };
  const other = ["health", "accounts.read", "usage.read", "wallet.read", "pricing.read", "management_auth", "quota.read",
    "wallet.write", "scheduling.write", "pricing.write"].map(id => ({
    id, state: "unverified", reason: id.endsWith(".write") ? "WRITE_NOT_PROBED" : "NOT_CONFIGURED", http_status: null, latency_ms: null,
  }));
  const jwtMode = { id: "run_mode", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 1 };
  const preview = { checked_at: "2026-11-22T00:00:00Z", revision: "1", source_id: null, saved: false,
    version: "0.2.24", run_mode: "standard", checks: [version, jwtMode, ...other] };
  assert.ok(diagnostic.safeParse(preview).success);
  assert.ok(diagnostic.safeParse({ ...preview, version: "0.2.23",
    checks: [{ ...version, reason: "VERIFIED_READ" }, jwtMode, ...other] }).success);
  assert.ok(diagnostic.safeParse({ ...preview, run_mode: null, checks: [version,
    { ...jwtMode, state: "unverified", reason: "MODE_REQUIRES_JWT", http_status: null, latency_ms: null }, ...other] }).success);
  assert.equal(diagnostic.safeParse({ ...preview, version: null }).success, false);
  for (const change of [
    { id: "run_mode" }, { id: "wallet.write" }, { state: "failed" },
    { evidence: { upstream_id: "1", verified_at: preview.checked_at } },
  ]) assert.equal(capabilityCheck.safeParse({ ...version, ...change }).success, false);
});

test("restored connection rejection retains its declared code and never retries", async () => {
  let calls = 0;
  const client = createConnectionClient("https://example.test", async () => {
    calls++;
    return Response.json({ defined: true, code: "CONNECTION_REVERIFICATION_REQUIRED", status: 409,
      message: "Connection verification required", data: { retry_at: null } }, { status: 409 });
  });
  await assert.rejects(client.discover({ revision: "1", source_id: id, page: 1, search: "" }),
    error => error instanceof ORPCError && error.defined && error.code === "CONNECTION_REVERIFICATION_REQUIRED"
      && error.status === 409 && error.data.retry_at === null);
  assert.equal(calls, 1);
});

test("connection client rejects invalid inputs and extra secrets and never replays writes", async () => {
  let calls = 0;
  const client = createConnectionClient("https://example.test", async () => { calls++; throw new TypeError("Connection lost"); });
  await assert.rejects(client.save({ revision: "1", base_url: "https://upstream.test", credential_action: "replace", credential: "test-key", confirmation: "" }));
  assert.equal(calls, 1);
  await assert.rejects(client.save({ revision: "1", base_url: "https://upstream.test", credential_action: "keep", credential: "unexpected", confirmation: "" }));
  assert.equal(calls, 1);
  const extra = createConnectionClient("https://example.test", async () => Response.json({ ...initial, secret: "never" }));
  await assert.rejects(extra.get({}));
});

test("connection impact keeps exact local counts, null-source zero and strict secret-free output", async () => {
  const impact = { revision: "1", source_id: null, checked_at: "2026-11-14T01:02:03.123456Z",
    managed_accounts: "0", participant_bindings: "0", dispatch_open: "0", pricing_open: "0", wallet_rejected: "0", wallet_unconfirmed: "0" };
  assert.ok(connectionImpact.safeParse(impact).success);
  assert.ok(connectionImpact.safeParse({ ...impact, source_id: id, managed_accounts: "9223372036854775807" }).success);
  for (const change of [{ managed_accounts: "1" }, { secret: "PRIVATE" }, { checked_at: "later" }, { pricing_open: 0 },
    ...["-1", "00", "1.0", "1e2", "bad", "9223372036854775808"].map(value => ({ source_id: id, wallet_unconfirmed: value }))]) {
    assert.equal(connectionImpact.safeParse({ ...impact, ...change }).success, false);
  }
  let calls = 0;
  const client = createConnectionClient("https://example.test", async (input, init) => {
    calls++;
    const request = new Request(input, init);
    assert.equal(request.url, "https://example.test/api/v1/connection/impact"); assert.equal(request.method, "GET");
    assert.equal(request.cache, "no-store");
    return Response.json(impact);
  });
  assert.deepEqual(await client.impact({}), impact); assert.equal(calls, 1);
  const malformed = createConnectionClient("https://example.test", async () => Response.json({ ...impact, secret: "PRIVATE" }));
  await assert.rejects(malformed.impact({}));
});

test("real Go PostgreSQL connection lifecycle and read-only diagnostics through oRPC", {
  skip: !process.env.SUB_POOL_TEST_CONNECTION_ORIGIN,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_CONNECTION_ORIGIN;
  const username = process.env.SUB_POOL_TEST_CONNECTION_USERNAME;
  const password = process.env.SUB_POOL_TEST_CONNECTION_PASSWORD;
  assert.ok(origin && username && password, "explicit isolated connection fixture required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  let cookie = "";
  const fetcher: typeof fetch = async (input, init) => {
    const request = new Request(input, init);
    request.headers.set("Origin", origin);
    if (cookie) request.headers.set("Cookie", cookie);
    const response = await fetch(request, { redirect: "error" });
    const issued = response.headers.get("set-cookie");
    if (issued) cookie = issued.split(";")[0] ?? "";
    return response;
  };
  const auth = createAuthClient(origin, fetcher);
  const client = createConnectionClient(origin, fetcher);
  await assert.rejects(client.get({}), error => error instanceof ORPCError && error.status === 401);
  await auth.login({ username, password });
  let upstreamCalls = 0;
  const upstream = createServer((request, response) => {
      const send = (status: number, body: unknown) => {
        response.writeHead(status, { "content-type": "application/json" });
        response.end(JSON.stringify(body));
      };
      upstreamCalls++;
      assert.equal(request.method, "GET");
      assert.equal(request.headers.cookie, undefined);
      const url = new URL(request.url ?? "/", "http://127.0.0.1");
      const path = url.pathname;
      if (path === "/health") {
        assert.equal(request.headers["x-api-key"], undefined);
        return send(200, { status: "ok" });
      }
      assert.equal(request.headers["x-api-key"], "adm_sc_live_contract_test");
      if (path === "/api/v1/admin/system/version") return send(200, { code: 0, data: { version: "controlled-test" } });
      const priceGroup = { id: 1, name: "Controlled group", platform: "openai" };
      if (path === "/api/v1/admin/groups") return send(200, { code: 0, data: { items: [priceGroup], total: 1, page: 1, page_size: 1 } });
      if (path === "/api/v1/admin/groups/1") return send(200, { code: 0, data: { ...priceGroup, rate_multiplier: 1,
        force_openai_fast: false, free_openai_fast: false, long_context_pricing_enabled: false, model_pricing: null } });
      if (path === "/api/v1/admin/accounts") {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(`{"code":0,"data":{"items":[{"id":9223372036854775807,"name":"Controlled account","platform":"openai","type":"oauth","status":"active","schedulable":true,"credentials":{"access_token":"must-never-leave-upstream"}}],"total":1,"page":1,"page_size":${url.searchParams.get("page_size")}}}`);
        return;
      }
      return send(403, { message: "private upstream failure" });
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  assert.ok(address && typeof address === "object");
  const upstreamOrigin = `http://127.0.0.1:${address.port}`;
  try {
    const before = await client.get({});
    assert.equal(before.credential_set, false);
    const saved = await client.save({ revision: before.revision, base_url: upstreamOrigin,
      credential_action: "replace", credential: "adm_sc_live_contract_test", confirmation: "" });
    const current = saved.configuration;
    assert.ok(current.source_id);
    assert.equal(current.credential_set, true);
    const checks = await client.test({ revision: current.revision, base_url: current.base_url, credential: "" });
    assert.equal(checks.saved, true);
    assert.equal(checks.version, "controlled-test");
    assert.equal(checks.run_mode, null);
    assert.equal(checks.checks.find(check => check.id === "wallet.write")?.reason, "ATOMIC_BALANCE_REQUIRED");
    assert.equal(checks.checks.find(check => check.id === "pricing.read")?.evidence?.upstream_id, "1");
    assert.equal(checks.checks.find(check => check.id === "usage.read")?.reason, "FORBIDDEN");
    const accounts = await client.discover({ revision: current.revision, source_id: current.source_id, page: 1, search: "" });
    assert.equal(accounts.total, "1");
    assert.equal(accounts.accounts[0]?.upstream_id, "9223372036854775807");
    assert.equal(accounts.accounts[0]?.eligibility, "unverified");
    assert.ok(!JSON.stringify(accounts).includes("must-never"));
    const restored = await client.get({});
    assert.equal(restored.diagnostic?.source_id, current.source_id);
    assert.ok(!JSON.stringify(restored).includes("adm_sc_live"));
    const callsBeforeImpact = upstreamCalls;
    const impact = await client.impact({});
    assert.equal(impact.source_id, current.source_id); assert.equal(impact.revision, current.revision);
    assert.equal(impact.managed_accounts, "0"); assert.equal(impact.wallet_unconfirmed, "0");
    assert.equal(upstreamCalls, callsBeforeImpact);
    const zone = await client.settings({});
    const updated = await client.updateSettings({ revision: zone.revision, timezone: "Asia/Taipei" });
    assert.equal(updated.timezone, "Asia/Taipei");
    await assert.rejects(client.updateSettings({ revision: zone.revision, timezone: "UTC" }), error => error instanceof ORPCError && error.status === 409);
    const cleared = await client.save({ revision: current.revision, base_url: current.base_url, credential_action: "clear", credential: "", confirmation: "" });
    assert.equal(cleared.configuration.credential_set, false);
    const previousCalls = upstreamCalls;
    await assert.rejects(client.discover({ revision: cleared.configuration.revision, source_id: current.source_id, page: 1, search: "" }),
      error => error instanceof ORPCError && error.code === "CONNECTION_NOT_CONFIGURED");
    assert.equal(upstreamCalls, previousCalls);
    await auth.logout({});
    await assert.rejects(client.settings({}), error => error instanceof ORPCError && error.status === 401);
  } finally {
    await new Promise<void>((resolve, reject) => upstream.close(error => error ? reject(error) : resolve()));
  }
});
test("capability state requires the matching scoped receipt", () => {
  const base = { http_status: null, latency_ms: null };
  const retained = { ...base, id: "pricing.write", state: "unverified", reason: "WRITE_NOT_PROBED" };
  const evidence = { upstream_id: "1", verified_at: "2026-11-18T00:00:00Z" };
  const write = { ...retained, state: "available", reason: "VERIFIED_WRITE", evidence };
  const quota = { ...base, id: "quota.read", state: "available", reason: "VERIFIED_PASSIVE_QUOTA", evidence: { ...evidence, account_revision: "1" } };
  for (const value of [retained, write, { ...write, id: "wallet.write" }, quota]) assert.equal(capabilityCheck.safeParse(value).success, true);
  for (const value of [
    { ...write, evidence: undefined }, { ...write, id: "wallet.write", evidence: undefined },
    { ...write, reason: "VERIFIED_READ" }, { ...quota, evidence }, { ...quota, id: "pricing.read" },
    { ...write, evidence: { ...evidence, upstream_id: "01" } },
  ]) assert.equal(capabilityCheck.safeParse(value).success, false);
});
