import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { ORPCError } from "@orpc/client";
import { createAccountsClient, createAuthClient, createConnectionClient, createParticipantsClient, createPoolsClient, createUsersClient, createWalletsClient } from "./client";

test("real Go PostgreSQL wallets preserve native receipts, full-source provenance and current privacy through oRPC", { skip: !process.env.SUB_POOL_TEST_WALLETS_ORIGIN, timeout: 45000 }, async () => {
  const origin = process.env.SUB_POOL_TEST_WALLETS_ORIGIN, username = process.env.SUB_POOL_TEST_WALLETS_USERNAME, password = process.env.SUB_POOL_TEST_WALLETS_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable wallets fixture required"); assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init); request.headers.set("Origin", origin ?? ""); if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" }); const issued = response.headers.get("set-cookie"); if (issued) cookie = issued.split(";")[0] ?? ""; return response;
    };
    return { auth: createAuthClient(origin ?? "", fetcher), connection: createConnectionClient(origin ?? "", fetcher), accounts: createAccountsClient(origin ?? "", fetcher), participants: createParticipantsClient(origin ?? "", fetcher), pools: createPoolsClient(origin ?? "", fetcher), users: createUsersClient(origin ?? "", fetcher), wallets: createWalletsClient(origin ?? "", fetcher) };
  }
  const admin = browser(), reader = browser(); await admin.auth.login({ username, password });
  const secret = "wallet-native-local-only"; let calls = 0, unexpected = 0, denied = false;
  const upstream = createServer((request, response) => {
    calls++; const url = new URL(request.url ?? "", "http://fixture.test");
    if (request.method !== "GET" || request.headers.authorization !== "Bearer " + secret || request.headers["x-api-key"] || request.headers.cookie) { unexpected++; response.writeHead(400).end(); return; }
    const send = (data: unknown) => response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data }));
    if (url.pathname === "/api/v1/admin/usage" && url.searchParams.size === 6
      && Object.entries({ user_id: "21", page: "1", page_size: "100", exact_total: "true", sort_by: "id", sort_order: "asc" })
        .every(([key, value]) => url.searchParams.get(key) === value)) {
      send({ items: [{ id: 1, user_id: 21, account_id: 1 }, { id: 2, user_id: 21, account_id: 2 }], total: 2, page: 1, page_size: 100 }); return;
    }
    if (url.pathname === "/api/v1/auth/me") { send({ role: "admin", run_mode: "simple" }); return; }
    if (/^\/api\/v1\/admin\/users\/(21|22)$/.test(url.pathname)) {
      if (denied) { response.writeHead(403).end("private native failure"); return; }
      const id = Number(url.pathname.split("/").at(-1));
      send({ id, email: "fixture@example.invalid", username: "Native " + id, role: "user", status: "active", created_at: "2020-01-01T00:00:00Z", balance: 0, frozen_balance: 2.5, notes: "private native note", api_keys: [{ key: "never return native keys" }] }); return;
    }
    if (/^\/api\/v1\/admin\/accounts\/(1|2)$/.test(url.pathname)) {
      const id = Number(url.pathname.split("/").at(-1));
      send({ id, name: "Native root " + id, platform: "openai", type: "oauth", status: "active", schedulable: true, created_at: "2020-01-01T00:00:00Z", updated_at: "2020-01-01T00:00:00Z", error_message: "", credentials: { chatgpt_account_id: "private-wallet-resource-" + id, access_token: "never expose" }, extra: { codex_usage_updated_at: new Date(Math.floor(Date.now() / 1000) * 1000).toISOString(), codex_5h_used_percent: 10, codex_5h_window_minutes: 180, codex_5h_reset_at: new Date(Date.now() + 3600000).toISOString(), codex_7d_used_percent: 10 } }); return;
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
    const accounts = [];
    for (const upstream_id of ["1", "2"]) { const result = await admin.accounts.adopt({ ...binding, upstream_id, name: "Root " + upstream_id, monitoring: false, source_mode: "passive" }); assert.ok(result.account); accounts.push(result.account); }
    const a = accounts[0], b = accounts[1]; assert.ok(a && b);
    const pool = await admin.pools.create({ ...binding, name: "Full-source wallet fixture", enabled: true, accounts: accounts.map(row => ({ id: row.id, revision: row.revision })), participants: [{ id: p.id, revision: p.revision, share_percent: "25" }, { id: q.id, revision: q.revision, share_percent: "50" }] });
    const expected = { ...binding, participant_id: p.id, participant_revision: p.revision, binding_id: p.binding.id, latest_id: "0" };
    const unread = await admin.wallets.get({ participant_id: p.id }); assert.equal(unread.latest_read, null); assert.equal(unread.current_target, null); assert.equal(unread.current_state, "not_generated");
    const first = await admin.wallets.refresh(expected); assert.equal(first.balance.value, "0"); assert.equal(first.frozen_balance.value, "2.5"); assert.equal(first.unit, "native_wallet");
    assert.ok(first.usage_sources); assert.equal(first.usage_sources.state, "exhausted"); assert.equal(first.usage_sources.complete, false);
    assert.equal(first.usage_sources.scanned_count, "2"); assert.deepEqual(first.usage_sources.accounts, [
      { upstream_account_id: "1", managed_account_id: a.id }, { upstream_account_id: "2", managed_account_id: b.id },
    ]);
    let current = await admin.wallets.get({ participant_id: p.id }); assert.deepEqual(current.latest_read, first); assert.deepEqual(current.last_confirmed_read, first);
    assert.equal(current.latest_recommendation?.global.source_count, 2); assert.equal(current.current_target, null); assert.equal(current.applicable, false); assert.equal(current.last_application, null); assert.equal(current.difference, null);
    const beforeConflict = calls; await assert.rejects(admin.wallets.refresh(expected), e => e instanceof ORPCError && e.status === 409); assert.equal(calls, beforeConflict);
    denied = true; const failed = await admin.wallets.refresh({ ...expected, latest_id: first.id }); denied = false;
    assert.equal(failed.read.state, "denied"); assert.equal(failed.balance.value, null);
    current = await admin.wallets.get({ participant_id: p.id }); assert.deepEqual(current.last_confirmed_read, first); assert.deepEqual(current.latest_read, failed);
    let head = failed.id;
    for (let i = 0; i < 9; i++) head = (await admin.wallets.refresh({ ...expected, latest_id: head })).id;
    const ninth = await admin.wallets.history({ participant_id: p.id, kind: "wallet", after: "8", limit: 1 }); assert.equal(ninth.wallet_reads[0]?.id, "9"); assert.equal(ninth.next_after, "9");
    const tenth = await admin.wallets.history({ participant_id: p.id, kind: "wallet", after: "9", limit: 100 }); assert.equal(tenth.wallet_reads[0]?.id, "10");
    const user = await admin.users.create({ username: "wallet-reader", password: "ReaderPassword123", enabled: true, features: ["balances"], account_ids: [a.id], participant_ids: [p.id] }); await reader.auth.login({ username: user.username, password: "ReaderPassword123" });
    const beforeReads = calls;
    const page = await reader.wallets.list({ after: "", limit: 1 }); assert.equal(page.participants.length, 1); assert.equal(page.participants[0]?.participant_id, p.id); assert.equal(page.next_after, null);
    const scoped = await reader.wallets.get({ participant_id: p.id }); assert.equal(scoped.latest_read?.usage_sources, undefined); assert.equal(scoped.last_confirmed_read?.usage_sources, undefined); assert.equal(scoped.contributions.length, 1); assert.equal(scoped.contributions[0]?.account_id, a.id); assert.equal(scoped.latest_recommendation, null); assert.equal(scoped.needs_recalculation, null);
    for (const hidden of [b.id, q.id, "source_count", "basis_hash", "private contact", secret, "never return native keys"]) assert.equal(JSON.stringify(scoped).includes(hidden), false);
    await assert.rejects(reader.wallets.get({ participant_id: q.id }), e => e instanceof ORPCError && e.status === 404);
    await assert.rejects(reader.wallets.history({ participant_id: p.id, kind: "recommendation", after: "0", limit: 100 }), e => e instanceof ORPCError && e.status === 403);
    await assert.rejects(reader.wallets.refresh({ ...expected, latest_id: head }), e => e instanceof ORPCError && e.status === 403);
    current = await admin.wallets.get({ participant_id: p.id }); assert.ok(current.latest_recommendation); const previous = current.latest_recommendation;
    await admin.pools.update({ id: pool.pool_id, revision: pool.revision, ...binding, name: pool.name, enabled: true, accounts: accounts.map(row => ({ id: row.id, revision: row.revision })), participants: [{ id: q.id, revision: q.revision, share_percent: "50" }] });
    current = await admin.wallets.get({ participant_id: p.id }); assert.equal(current.current_state, "recalculation_required"); assert.equal(current.contributions.length, 2);
    const next = await admin.wallets.recompute({ ...expected, latest_id: previous.id }); assert.equal(next.global.source_count, 2); assert.equal(next.target, null);
    const history = await admin.wallets.history({ participant_id: p.id, kind: "recommendation", after: "0", limit: 100 }); assert.deepEqual(history.recommendations.find(row => row.id === previous.id), previous);
    await admin.users.update({ id: user.id, revision: user.revision, username: user.username, enabled: true, new_password: "", features: ["balances"], account_ids: [], participant_ids: [] });
    assert.equal((await reader.wallets.list({ after: "", limit: 100 })).participants.length, 0);
    for (const participant_id of [p.id, "c".repeat(21) + "A"]) await assert.rejects(reader.wallets.get({ participant_id }), e => e instanceof ORPCError && e.status === 404);
    await reader.auth.logout({}); await assert.rejects(reader.wallets.get({ participant_id: p.id }), e => e instanceof ORPCError && e.status === 401);
    assert.equal(calls, beforeReads); assert.equal(unexpected, 0);
    const evidence = process.env.SUB_POOL_TEST_WALLETS_EVIDENCE;
    if (evidence) await writeFile(join(evidence, "native-proof.json"), JSON.stringify({ participant_id: p.id, first_receipt: first, latest_receipt_id: head, final_recommendation_id: next.id, native_gets: calls, native_unexpected: unexpected, source_count: next.global.source_count, applicable: false, target: null }, null, 2) + "\n");
  } finally { upstream.closeAllConnections(); await new Promise<void>(resolve => upstream.close(() => resolve())); }
});
