import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { createServer } from "node:http";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test } from "node:test";
import { ORPCError } from "@orpc/client";
import { createAuthClient, createConnectionClient, createParticipantsClient, createUsersClient, createWalletApplicationsClient, createWalletsClient } from "./client";

test("real Go PostgreSQL rejected wallet applications and disabled automation remain scoped through oRPC", { skip: !process.env.SUB_POOL_TEST_WALLET_APPLICATIONS_ORIGIN, timeout: 45000 }, async () => {
  const origin = process.env.SUB_POOL_TEST_WALLET_APPLICATIONS_ORIGIN, username = process.env.SUB_POOL_TEST_WALLET_APPLICATIONS_USERNAME, password = process.env.SUB_POOL_TEST_WALLET_APPLICATIONS_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable wallet application fixture required"); assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init); request.headers.set("Origin", origin ?? ""); if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" }); const issued = response.headers.get("set-cookie"); if (issued) cookie = issued.split(";")[0] ?? ""; return response;
    };
    return { auth: createAuthClient(origin ?? "", fetcher), connection: createConnectionClient(origin ?? "", fetcher), participants: createParticipantsClient(origin ?? "", fetcher), users: createUsersClient(origin ?? "", fetcher), wallets: createWalletsClient(origin ?? "", fetcher), applications: createWalletApplicationsClient(origin ?? "", fetcher) };
  }
  const admin = browser(), reader = browser(); await admin.auth.login({ username, password });
  const automation = { enabled: false, can_enable: false, state: "unavailable", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", execution_state: "not_implemented" };
  assert.deepEqual(await admin.applications.automation({}), automation);
  const secret = "wallet-application-native-local-only"; let calls = 0, unexpected = 0;
  const upstream = createServer((request, response) => {
    calls++; const url = new URL(request.url ?? "", "http://fixture.test");
    if (request.method !== "GET" || request.headers.authorization !== "Bearer " + secret || request.headers["x-api-key"] || request.headers.cookie) { unexpected++; response.writeHead(400).end(); return; }
    const send = (data: unknown) => response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data }));
    if (url.pathname === "/api/v1/admin/usage" && url.searchParams.size === 6
      && Object.entries({ user_id: "21", page: "1", page_size: "100", exact_total: "true", sort_by: "id", sort_order: "asc" })
        .every(([key, value]) => url.searchParams.get(key) === value)) {
      send({ items: [], total: 0, page: 1, page_size: 100 }); return;
    }
    if (url.pathname === "/api/v1/auth/me") { send({ role: "admin", run_mode: "simple" }); return; }
    if (/^\/api\/v1\/admin\/users\/(21|22)$/.test(url.pathname)) {
      const id = Number(url.pathname.split("/").at(-1)); send({ id, email: "fixture@example.invalid", username: "Native " + id, role: "user", status: "active", created_at: "2020-01-01T00:00:00Z", balance: 0, frozen_balance: 2.5, notes: "private native note" }); return;
    }
    unexpected++; response.writeHead(404).end();
  });
  upstream.listen(0, "127.0.0.1"); await once(upstream, "listening"); const address = upstream.address(); assert.ok(address && typeof address === "object");
  try {
    const initial = await admin.connection.get({});
    const saved = await admin.connection.save({ revision: initial.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: "Bearer " + secret, confirmation: "" }); assert.ok(saved.configuration.source_id);
    const binding = { source_id: saved.configuration.source_id, connection_revision: saved.configuration.revision };
    const participants = [];
    for (const upstream_id of ["21", "22"]) { const result = await admin.participants.create({ ...binding, upstream_id, name: "Member " + upstream_id, contact: "private contact", notes: "private local note", enabled: true }); assert.ok(result.participant); participants.push(result.participant); }
    const p = participants[0], q = participants[1]; assert.ok(p && q);
    const expected = { ...binding, participant_id: p.id, participant_revision: p.revision, binding_id: p.binding.id, latest_id: "0", recommendation_id: "0" };
    const key = () => randomBytes(16).toString("base64url");
    const hidden = await admin.applications.request({ ...expected, participant_id: q.id, participant_revision: q.revision, binding_id: q.binding.id, request_id: key() });
    const input = { ...expected, request_id: key() }, first = await admin.applications.request(input);
    assert.equal(first.state, "rejected"); assert.equal(first.dispatch_state, "not_dispatched"); assert.equal(first.target, null); assert.ok(first.details?.reasons.includes("RECOMMENDATION_NOT_GENERATED"));
    assert.deepEqual(await admin.applications.get({ id: first.id }), first);
    for (const receipt of await Promise.all([admin.applications.request(input), admin.applications.request(input)])) assert.deepEqual(receipt, first);
    await assert.rejects(admin.applications.request({ ...input, latest_id: "1" }), e => e instanceof ORPCError && e.status === 409);
    for (let i = 0; i < 10; i++) await admin.applications.request({ ...expected, request_id: key() });
    assert.equal((await admin.applications.list({ participant_id: p.id, start_at: "", end_at: "", after: "9", limit: 1 })).requests[0]?.id, "10");
    assert.equal(calls, 2);
    const read = await admin.wallets.refresh({ ...binding, participant_id: p.id, participant_revision: p.revision, binding_id: p.binding.id, latest_id: "0" });
    assert.ok(read.usage_sources); assert.equal(read.usage_sources.state, "exhausted"); assert.equal(read.usage_sources.complete, false); assert.deepEqual(read.usage_sources.accounts, []);
    const current = await admin.wallets.get({ participant_id: p.id }); assert.ok(current.latest_recommendation);
    const next = await admin.applications.request({ ...expected, request_id: key(), latest_id: read.id, recommendation_id: current.latest_recommendation.id });
    assert.equal(next.state, "rejected"); assert.ok(next.details?.recommendation_basis_hash); assert.equal(next.details?.reasons.includes("RECOMMENDATION_NOT_GENERATED"), false);
    assert.deepEqual(await admin.applications.request(input), first);
    await assert.rejects(admin.applications.request({ ...expected, request_id: key() }), e => e instanceof ORPCError && e.status === 409);
    const beforeReads = calls;
    const user = await admin.users.create({ username: "application-reader", password: "ReaderPassword123", enabled: true, features: ["balances"], account_ids: [], participant_ids: [p.id] }); await reader.auth.login({ username: user.username, password: "ReaderPassword123" });
    await assert.rejects(reader.applications.automation({}), e => e instanceof ORPCError && e.status === 403);
    const page = await reader.applications.list({ participant_id: "", start_at: "", end_at: "", after: "0", limit: 1 }); assert.equal(page.requests[0]?.id, first.id); assert.equal(page.next_after, first.id);
    const scoped = await reader.applications.get({ id: first.id }); assert.equal(scoped.details, null); assert.equal(scoped.visibility, "participant_summary");
    for (const privateValue of [q.id, binding.source_id, secret, first.details?.actor_id, first.details?.evaluated_basis_hash, input.request_id, "private contact"]) { assert.ok(privateValue); assert.equal(JSON.stringify(scoped).includes(privateValue), false); }
    for (const id of [hidden.id, "999999"]) await assert.rejects(reader.applications.get({ id }), e => e instanceof ORPCError && e.status === 404);
    await assert.rejects(reader.applications.request({ ...expected, request_id: key() }), e => e instanceof ORPCError && e.status === 403);
    const fraction = first.attempted_at.includes(".") ? first.attempted_at.slice(20, -1) : "";
    const oneNanosecondLater = first.attempted_at.slice(0, 19) + "." + (BigInt(fraction.padEnd(9, "0")) + 1n).toString().padStart(9, "0") + "Z";
    const exact = await reader.applications.list({ participant_id: p.id, start_at: first.attempted_at, end_at: oneNanosecondLater, after: "0", limit: 100 }); assert.equal(exact.requests.length, 1); assert.equal(exact.requests[0]?.id, first.id);
    const changed = await admin.users.update({ id: user.id, revision: user.revision, username: user.username, enabled: true, new_password: "", features: ["balances"], account_ids: [], participant_ids: [] });
    assert.equal((await reader.applications.list({ participant_id: "", start_at: "", end_at: "", after: "0", limit: 100 })).requests.length, 0);
    await assert.rejects(reader.applications.get({ id: first.id }), e => e instanceof ORPCError && e.status === 404);
    await admin.users.update({ id: user.id, revision: changed.revision, username: user.username, enabled: true, new_password: "", features: ["balances"], account_ids: [], participant_ids: [p.id] });
    assert.equal((await reader.applications.get({ id: first.id })).details, null);
    await reader.auth.logout({}); await assert.rejects(reader.applications.get({ id: first.id }), e => e instanceof ORPCError && e.status === 401);
    await admin.connection.save({ revision: saved.configuration.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "keep", credential: "", confirmation: "" });
    assert.deepEqual(await admin.applications.request(input), first);
    await assert.rejects(admin.applications.request({ ...expected, request_id: key(), latest_id: read.id, recommendation_id: current.latest_recommendation.id }), e => e instanceof ORPCError && e.status === 409);
    const history = await admin.applications.list({ participant_id: "", start_at: "", end_at: "", after: "0", limit: 100 }); assert.equal(history.requests.length, 13);
    assert.deepEqual(await admin.applications.automation({}), automation);
    assert.equal(calls, beforeReads); assert.equal(calls, 5); assert.equal(unexpected, 0);
    const evidence = process.env.SUB_POOL_TEST_WALLET_APPLICATIONS_EVIDENCE;
    if (evidence) await writeFile(join(evidence, "native-proof.json"), JSON.stringify({ participant_id: p.id, first_receipt: first, last_receipt_id: next.id, request_count: history.requests.length, native_gets: calls, native_unexpected: unexpected, state: "rejected", dispatch_state: "not_dispatched", target: null, automation }, null, 2) + "\n");
  } finally { upstream.closeAllConnections(); await new Promise<void>(resolve => upstream.close(() => resolve())); }
});
