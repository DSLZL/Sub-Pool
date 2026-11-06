import assert from "node:assert/strict";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { createAccountsClient, createAuthClient, createConnectionClient, createSecurityClient, createUsersClient } from "./client";
import { loginPage, managementEvent, networkComparison, requestSource } from "./security";

const id = "A".repeat(22);
const stamp = "2026-11-06T00:00:00Z";
const source = { ip: "203.0.113.1", peer_ip: "127.0.0.1", kind: "trusted_forwarded" };

test("security schemas distinguish untrusted reports, legacy sources and uncertain operations", () => {
  assert.ok(requestSource.safeParse(source).success);
  assert.ok(requestSource.safeParse({ ip: null, peer_ip: null, kind: "legacy" }).success);
  assert.equal(requestSource.safeParse({ ...source, kind: "peer" }).success, false);
  assert.equal(requestSource.safeParse({ ...source, ip: "not-an-ip" }).success, false);
  assert.ok(networkComparison.safeParse({ reported_ip: "203.0.113.2", trusted: false, server_source: source }).success);
  assert.equal(networkComparison.safeParse({ reported_ip: "203.0.113.2", trusted: true, server_source: source }).success, false);
  const entry = { id: "1", operation_id: id, occurred_at: stamp, actor_id: id, task_id: null, action: "fixture.apply", outcome: "unknown",
    target_type: "account", target_id: id, target_revision: null, source_id: null };
  assert.ok(managementEvent.safeParse(entry).success);
  assert.equal(managementEvent.safeParse({ ...entry, task_id: "worker" }).success, false);
  assert.equal(managementEvent.safeParse({ ...entry, raw_error: "private" }).success, false);
  assert.ok(loginPage.safeParse({ events: [], next_after: null, counts: { success: "0", failure: "0", limited: "0", blocked: "0" } }).success);
});

test("security client keeps nanosecond ranges, rejects unsafe reports and never replays mutations", async () => {
  let calls = 0;
  const client = createSecurityClient("https://pool.test", async () => { calls++; throw new TypeError("Connection lost"); });
  await assert.rejects(client.updateSource({ ip: "203.0.113.1", revision: "0", blocked: true }));
  assert.equal(calls, 1);
  await assert.rejects(client.compareNetwork({ reported_ip: "invalid", consent: true }));
  assert.equal(calls, 1);
  await assert.rejects(client.loginAudit({ from: "2026-11-06T00:00:00.000000001Z", until: "2026-11-06T00:00:00.000000002Z", after: "0", limit: 1, outcome: "all" }));
  assert.equal(calls, 2);
  await assert.rejects(client.loginAudit({ from: "2026-11-06T00:00:00.000000002Z", until: "2026-11-06T00:00:00.000000001Z", after: "0", limit: 1, outcome: "all" }));
  assert.equal(calls, 2);
});

test("real Go PostgreSQL security controls, current grants and audit through oRPC", {
  skip: !process.env.SUB_POOL_TEST_SECURITY_ORIGIN,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_SECURITY_ORIGIN;
  const username = process.env.SUB_POOL_TEST_SECURITY_USERNAME;
  const password = process.env.SUB_POOL_TEST_SECURITY_PASSWORD;
  assert.ok(origin && username && password);
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  const transport = (source: string) => {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      request.headers.set("Origin", origin);
      request.headers.set("X-Forwarded-For", source);
      if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" });
      const issued = response.headers.get("set-cookie");
      if (issued) cookie = issued.split(";")[0] ?? "";
      return response;
    };
    return fetcher;
  };
  const fetcher = transport("198.51.100.10");
  const auth = createAuthClient(origin, fetcher), security = createSecurityClient(origin, fetcher), users = createUsersClient(origin, fetcher);
  const is = (status: number, code: string) => (error: unknown) => error instanceof ORPCError && error.defined && error.status === status && error.code === code;
  assert.equal((await security.publicConfig({})).network_diagnostics_enabled, false);
  await assert.rejects(security.config({}), is(401, "UNAUTHORIZED"));
  await auth.login({ username, password });
  const range = { from: new Date(Date.now() - 3_600_000).toISOString(), until: new Date(Date.now() + 3_600_000).toISOString(), after: "0", limit: 100 };
  for (const ip of ["198.51.100.10", "127.0.0.1"]) {
    await assert.rejects(security.updateSource({ ip, revision: "0", blocked: true }), is(409, "SELF_SOURCE_PROTECTED"));
  }
  const blockedIP = "203.0.113.77";
  const blockedFetch = transport(blockedIP), blockedAuth = createAuthClient(origin, blockedFetch);
  await blockedAuth.login({ username, password });
  const rule = await security.updateSource({ ip: blockedIP, revision: "0", blocked: true });
  assert.equal(rule.blocked, true);
  await assert.rejects(blockedAuth.current({}), is(403, "SOURCE_BLOCKED"));
  await assert.rejects(createAccountsClient(origin, blockedFetch).list({ after: "", search: "", limit: 100 }), is(403, "SOURCE_BLOCKED"));
  await assert.rejects(createConnectionClient(origin, blockedFetch).get({}), is(403, "SOURCE_BLOCKED"));
  await assert.rejects(createAuthClient(origin, transport(blockedIP)).login({ username, password }), is(403, "SOURCE_BLOCKED"));
  const member = await users.create({ username: "security.reader", password: "ReaderPassword123", enabled: true, features: ["security"], account_ids: [], participant_ids: [] });
  const memberFetch = transport("198.51.100.20"), memberAuth = createAuthClient(origin, memberFetch), memberSecurity = createSecurityClient(origin, memberFetch);
  await memberAuth.login({ username: member.username, password: "ReaderPassword123" });
  assert.equal((await memberSecurity.sources({ after: "", state: "blocked", limit: 100 })).rules[0]?.ip, blockedIP);
  await assert.rejects(memberSecurity.config({}), is(403, "FORBIDDEN"));
  await assert.rejects(memberSecurity.managementAudit(range), is(403, "FORBIDDEN"));
  await assert.rejects(memberSecurity.updateSource({ ip: "203.0.113.78", revision: "0", blocked: true }), is(403, "FORBIDDEN"));
  await assert.rejects(memberSecurity.compareNetwork({ reported_ip: "203.0.113.250", consent: true }), is(409, "NETWORK_DIAGNOSTICS_DISABLED"));
  const config = await security.config({});
  const enabled = await security.updateConfig({ revision: config.revision, network_diagnostics_enabled: true });
  const comparison = await memberSecurity.compareNetwork({ reported_ip: "203.0.113.250", consent: true });
  assert.equal(comparison.trusted, false);
  assert.equal(comparison.server_source.ip, "198.51.100.20");
  assert.equal(comparison.server_source.peer_ip, "127.0.0.1");
  assert.equal(comparison.server_source.kind, "trusted_forwarded");
  assert.equal((await security.sources({ after: "", state: "all", limit: 100 })).rules.length, 1);
  await security.updateConfig({ revision: enabled.revision, network_diagnostics_enabled: false });
  await assert.rejects(createAuthClient(origin, transport("198.51.100.30")).login({ username: "unknown.user", password: "NeverLoggedSecret" }), is(401, "UNAUTHORIZED"));
  const page = await memberSecurity.loginAudit({ ...range, outcome: "all" });
  assert.equal(page.counts.blocked, "1");
  assert.equal(page.counts.failure, "1");
  assert.equal(page.events.find(event => event.outcome === "blocked")?.source.ip, blockedIP);
  assert.ok(!JSON.stringify(page).includes("NeverLoggedSecret"));
  const management = await security.managementAudit(range);
  assert.ok(management.events.some(event => event.action === "security.source.block" && event.outcome === "denied"));
  assert.ok(management.events.some(event => event.action === "user.create"));
  assert.ok(!JSON.stringify(management).includes("ReaderPassword123"));
  await users.update({ id: member.id, revision: member.revision, username: member.username, enabled: true, features: [], account_ids: [], participant_ids: [], new_password: "" });
  await assert.rejects(memberSecurity.loginAudit({ ...range, outcome: "all" }), is(403, "FORBIDDEN"));
  await assert.rejects(security.updateSource({ ip: blockedIP, revision: "0", blocked: false }), is(409, "CONFLICT"));
  const unblocked = await security.updateSource({ ip: blockedIP, revision: rule.revision, blocked: false });
  assert.equal(unblocked.revision, "2");
  assert.equal((await security.sources({ after: "", state: "blocked", limit: 100 })).rules.length, 0);
  assert.ok((await blockedAuth.current({})).user.id);
  const ipv6 = await security.updateSource({ ip: "::c000:201", revision: "0", blocked: true });
  assert.equal(ipv6.ip, "::c000:201");
  const first = await security.sources({ after: "", state: "all", limit: 1 });
  assert.ok(first.next_after);
  const second = await security.sources({ after: first.next_after, state: "all", limit: 1 });
  assert.equal(second.rules[0]?.ip, ipv6.ip);
  assert.equal(second.next_after, null);
  await security.updateSource({ ip: ipv6.ip, revision: ipv6.revision, blocked: false });
  await memberAuth.logout({});
  await auth.logout({});
  await assert.rejects(security.managementAudit(range), is(401, "UNAUTHORIZED"));
});
