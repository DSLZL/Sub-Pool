import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer } from "node:http";
import { once } from "node:events";
import { ORPCError } from "@orpc/client";
import { poolContract, poolCreateInput, poolsOutputs, sharePercent } from "./pools";
import { usageNanos } from "./usage";
import { createAccountsClient, createAuthClient, createConnectionClient, createParticipantsClient, createPoolsClient, createUsageClient, createUsersClient } from "./client";

const id = (n: number) => Buffer.alloc(16, n).toString("base64url");
const stamp = "2026-11-01T00:00:00Z";
const input = { source_id: id(1), connection_revision: "1", name: "Local pool", enabled: true,
  accounts: [{ id: id(2), revision: "1" }], participants: [{ id: id(3), revision: "1", share_percent: "99.999999" }, { id: id(4), revision: "1", share_percent: "0.000001" }] };
const contract = { id: "1", pool_id: id(5), revision: "1", name: "Local pool", enabled: true, source_id: id(1), connection_revision: "1",
  effective_at: stamp, ended_at: null, current_revision: "1", is_current: true, source_current: true, visibility: "all_members",
  accounts: [{ id: id(2), revision: "1", name: "Root", source_id: id(1), upstream_id: "9007199254740993", identity_current: true }],
  participants: [{ id: id(3), revision: "1", name: "Member", binding_id: "1", share_percent: "25", enabled: true, binding_current: true }], unallocated_percent: "75" };

test("pool contracts preserve exact percentages, immutable intervals and scoped remainders", () => {
  assert.equal(poolCreateInput.safeParse(input).success, true);
  assert.equal(poolCreateInput.safeParse({ ...input, participants: input.participants.map(p => ({ ...p, share_percent: "50.000001" })) }).success, false);
  for (const value of ["-0", "01", ".5", "1.", "+1", "1e1", "NaN", "100.000001", "0.0000001", "bad"]) assert.equal(sharePercent.safeParse(value).success, false);
  assert.equal(sharePercent.safeParse("100.000000").success, true);
  assert.equal(poolCreateInput.safeParse({ ...input, accounts: [input.accounts[0], input.accounts[0]] }).success, false);
  assert.equal(poolCreateInput.safeParse({ ...input, accounts: [{ ...input.accounts[0], fingerprint: "private" }] }).success, false);
  assert.equal(poolCreateInput.safeParse({ ...input, participants: null }).success, false);
  assert.equal(poolContract.safeParse(contract).success, true);
  assert.equal(poolContract.safeParse({ ...contract, accounts: [{ ...contract.accounts[0], upstream_id: "bad" }] }).success, false);
  assert.equal(poolContract.safeParse({ ...contract, unallocated_percent: "100" }).success, false);
  assert.equal(poolContract.safeParse({ ...contract, ended_at: stamp }).success, false);
  assert.equal(poolContract.safeParse({ ...contract, visibility: "scoped_members", current_revision: null, unallocated_percent: null }).success, true);
  assert.equal(poolContract.safeParse({ ...contract, visibility: "scoped_members" }).success, false);
  assert.equal(poolContract.safeParse({ ...contract, visibility: "scoped_members", current_revision: null, unallocated_percent: null, participants: [] }).success, false);
  assert.equal(poolContract.safeParse({ ...contract, resource_fingerprint: "private" }).success, false);
  const evidence = { upstream_id: "9", participant_id: id(3), contract_id: "1", basis: "recorded_contract" };
  assert.equal(poolsOutputs.evidence.safeParse({ capture_id: "1", records: [evidence, { ...evidence, upstream_id: "10" }], next_after: null }).success, true);
  assert.equal(poolsOutputs.evidence.safeParse({ capture_id: "1", records: [evidence, { ...evidence, upstream_id: "8" }], next_after: null }).success, false);
  assert.equal(poolsOutputs.evidence.safeParse({ capture_id: "1", records: [{ ...evidence, participant_id: null }], next_after: null }).success, false);
});

test("weekly issuance validates positive split, optional balances and first-week capacity", () => {
  const end = "2026-11-08T00:00:00Z";
  const issuance = {
    amount: "80", balance: null, frozen: "10", outstanding_debt: "0",
    contributions: [{
      pool_id: id(5), account_id: id(2), period_start: stamp, period_end: end, name: "Account",
      phase: "settlement", contract_id: "1", estimate_id: "3", share_percent: "40",
      gross: "100", prior_issued: "20", advance_origin: "legacy:single:1", amount: "80", debt: "0", tail: "0", offsets: [],
    }],
  };
  const receipt = {
    id: "1", participant_id: id(3), name: "Member", phase: "settlement", period_end: end, recorded_at: stamp,
    application_id: "1", state: "applied", issuance,
  };
  assert.ok(poolsOutputs.weeklyWalletSettlement.safeParse(receipt).success);
  for (const bad of ["81", "-80", "wrong"]) {
    assert.equal(poolsOutputs.weeklyWalletSettlement.safeParse({ ...receipt, issuance: { ...issuance, amount: bad } }).success, false);
  }
  assert.equal(poolsOutputs.weeklyWalletSettlement.safeParse({ ...receipt, application_id: null }).success, false);
  const settings = {
    pool_id: id(5), pool_revision: "1", revision: "2", enabled: true, auto_allocate: false, state: "enabled",
    entries: [], issuances: [receipt], first_week_capacity: { [id(2)]: "100" },
  };
  assert.ok(poolsOutputs.weeklyWallet.safeParse(settings).success);
  assert.equal(poolsOutputs.weeklyWallet.safeParse({ ...settings, first_week_capacity: { [id(2)]: "-100" } }).success, false);
});

test("native weekly recommendations and controls preserve exact member settlement evidence", () => {
  const plan = { gross: "30", spent: "35", opening_debt: "0", debt: "5", balance_before: "15", target: "0", contract_id: "2", estimate_id: "9", share_percent: "40" };
  const proposal = { basis: "a".repeat(64), period_end: stamp, plan, delta: "-15" };
  const recommendation = { participant_id: id(3), pool: { id: id(5), name: "Local pool" }, state: "ready", proposal };
  assert.equal(poolsOutputs.weeklyWalletRecommendation.safeParse(recommendation).success, true);
  assert.deepEqual(poolsOutputs.weeklyWalletRecommendation.parse({ ...recommendation, proposal: { ...proposal, basis: null } }).proposal,
    { ...proposal, basis: null });
  assert.equal(poolsOutputs.weeklyWalletRecommendation.safeParse({ ...recommendation, pool: null }).success, false);
  assert.equal(poolsOutputs.weeklyWalletRecommendation.safeParse({ ...recommendation, state: "waiting_actual_cost" }).success, false);
  assert.equal(poolsOutputs.weeklyWalletRecommendation.safeParse({ ...recommendation, proposal: { ...proposal, basis: "unverified" } }).success, false);
  assert.equal(poolsOutputs.weeklyWallet.safeParse({ pool_id: id(5), revision: "2", pool_revision: "3", auto_allocate: true, enabled: false, state: "enabled", entries: [] }).success, true);
});

test("a weekly reference preserves exact amounts and cannot represent a settlement proposal", () => {
  const row = { pool_id: id(5), account_id: id(2), name: "Account", share_percent: "40", amount: "80",
    basis_at: stamp, used_percent: "56", samples: 2, reason: "collecting_samples" };
  const recommendation = { participant_id: id(3), pool: { id: id(5), name: "Local pool" },
    state: "waiting_estimate", proposal: null, reference: { amount: "80", accounts: [row] } };
  const parse = poolsOutputs.weeklyWalletRecommendation.safeParse;
  assert.equal(parse(recommendation).success, true);
  assert.equal(parse({ ...recommendation, reference: { amount: "80", accounts: [{ ...row, basis: "a".repeat(64) }] } }).success, false);
  assert.equal(parse({ ...recommendation, reference: { amount: "80", accounts: [{ ...row, samples: 4 }] } }).success, false);
  const missing = { ...row, account_id: id(6), amount: null, basis_at: null, used_percent: null, samples: 0, reason: "waiting_actual_cost" };
  assert.equal(parse({ ...recommendation, reference: { amount: "80", accounts: [row, missing] } }).success, false);
  assert.equal(parse({ ...recommendation, reference: { amount: null, accounts: [row, missing] } }).success, true);
  assert.equal(parse({ ...recommendation, reference: { amount: "80", accounts: [row], truncated: true } }).success, false);
  assert.equal(parse({ ...recommendation, reference: { amount: null, accounts: [row], truncated: true } }).success, true);
});

test("pool writes validate locally and never replay a lost acknowledgement", async () => {
  let calls = 0;
  const client = createPoolsClient("http://127.0.0.1:3000", (async () => { calls++; throw new TypeError("lost reply"); }) as typeof fetch);
  await assert.rejects(client.create({ ...input, participants: [{ id: id(3), revision: "1", share_percent: "101" }] }));
  assert.equal(calls, 0);
  await assert.rejects(client.create(input));
  assert.equal(calls, 1);
});

test("real Go PostgreSQL pools, immutable shares and scoped contract evidence through oRPC", {
  skip: !process.env.SUB_POOL_TEST_POOLS_ORIGIN,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_POOLS_ORIGIN, username = process.env.SUB_POOL_TEST_POOLS_USERNAME, password = process.env.SUB_POOL_TEST_POOLS_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable pool fixture required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init); request.headers.set("Origin", origin!);
      if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" });
      const issued = response.headers.get("set-cookie"); if (issued) cookie = issued.split(";")[0] ?? "";
      return response;
    };
    return { auth: createAuthClient(origin!, fetcher), users: createUsersClient(origin!, fetcher), connection: createConnectionClient(origin!, fetcher),
      accounts: createAccountsClient(origin!, fetcher), participants: createParticipantsClient(origin!, fetcher),
      usage: createUsageClient(origin!, fetcher), pools: createPoolsClient(origin!, fetcher) };
  }
  const admin = browser(), reader = browser();
  const denied = (status: number) => (e: unknown) => e instanceof ORPCError && e.defined && e.status === status;
  await assert.rejects(admin.pools.list({ after: "", search: "", limit: 100 }), denied(401));
  await admin.auth.login({ username, password });
  let unexpected = 0, calls = 0, rows: string[] = [], otherRows: string[] = [];
  const secret = "adm_sc_native_pool_fixture";
  const row = (n: number, user: number | null, time: string, account = 1) => JSON.stringify({
    id: n, user_id: user, account_id: account, api_key_id: 31, model: "requested", created_at: time,
    input_tokens: 10, output_tokens: 0, total_cost: 0.1, actual_cost: 0.2, billing_type: 0,
  });
  const nanosISO = (n: bigint) => new Date(Number(n / 1000000n)).toISOString().slice(0, 19) + "." + (n % 1000000000n).toString().padStart(9, "0") + "Z";
  const upstream = createServer((request, response) => {
    calls++;
    if (request.method !== "GET" || request.headers["x-api-key"] !== secret || request.headers.cookie) { unexpected++; response.writeHead(403).end("private"); return; }
    const url = new URL(request.url ?? "/", "http://upstream.invalid");
    const send = (data: unknown) => response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data }));
    const user = /^\/api\/v1\/admin\/users\/(\d+)$/.exec(url.pathname);
    if (user) {
      const n = Number(user[1]);
      send({ id: n, username: "same-name", email: `member${n}@example.test`, role: "user", status: "active", created_at: stamp, deleted_at: null,
        balance: 999, api_keys: [{ key: "private-key" }], notes: "private-note" }); return;
    }
    const account = /^\/api\/v1\/admin\/accounts\/([12])$/.exec(url.pathname);
    if (account) {
      const n = Number(account[1]);
      send({ id: n, name: "Native " + n, platform: "openai", type: "oauth", status: "active", schedulable: true,
        credentials: { chatgpt_account_id: "private-resource-" + n, access_token: "private-token" }, extra: {}, created_at: stamp, updated_at: stamp }); return;
    }
    if (url.pathname === "/api/v1/admin/usage") {
      const data = url.searchParams.get("account_id") === "2" ? otherRows : rows;
      response.writeHead(200, { "Content-Type": "application/json" }).end(`{"code":0,"data":{"items":[${data.join(",")}],"total":${data.length},"page":1,"page_size":100}}`); return;
    }
    unexpected++; response.writeHead(403).end("private unexpected");
  });
  upstream.listen(0, "127.0.0.1"); await once(upstream, "listening");
  const address = upstream.address(); assert.ok(address && typeof address === "object");
  try {
    const config = await admin.connection.get({});
    const saved = await admin.connection.save({ revision: config.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: secret, confirmation: "" });
    const c = saved.configuration; assert.ok(c.source_id);
    const adopted = [];
    for (const n of [1, 2]) {
      const result = await admin.accounts.adopt({ source_id: c.source_id, connection_revision: c.revision, upstream_id: String(n), name: "Account " + n, monitoring: false, source_mode: "passive" });
      assert.ok(result.account); adopted.push(result.account);
    }
    const a = adopted[0]!, b = adopted[1]!;
    const local = { source_id: c.source_id, connection_revision: c.revision, contact: "private-contact", notes: "private-note", enabled: true };
    const madeP = await admin.participants.create({ ...local, upstream_id: "21", name: "P" });
    const madeQ = await admin.participants.create({ ...local, upstream_id: "22", name: "Q" });
    assert.ok(madeP.participant && madeQ.participant);
    const p = madeP.participant, q = madeQ.participant;
    const range = { start_at: new Date(Date.now() - 60000).toISOString(), end_at: new Date(Date.now() + 3600000).toISOString() };
    const capture = (account: typeof a) => admin.usage.capture({ id: account.id, revision: account.revision, source_id: account.source_id, connection_revision: c.revision, ...range });
    rows = [row(1, 21, p.binding.started_at)];
    const before = await capture(a);
    const accounts = [a, b].map(account => ({ id: account.id, revision: account.revision }));
    const members = [{ id: p.id, revision: p.revision, share_percent: "25" }, { id: q.id, revision: q.revision, share_percent: "25" }];
    const base = { source_id: c.source_id, connection_revision: c.revision, name: "Native local pool", enabled: true };
    const callsBefore = calls;
    const first = await admin.pools.create({ ...base, accounts: [accounts[0]!], participants: members });
    assert.equal(first.unallocated_percent, "50");
    assert.deepEqual((await admin.pools.list({ after: "", search: "", limit: 100 })).pools.map(p => p.pool_id), [first.pool_id]);
    assert.equal((await admin.pools.get({ id: first.pool_id })).id, first.id);
    await assert.rejects(admin.pools.create({ ...base, accounts: [accounts[0]!], participants: [] }), denied(409));
    const second = await admin.pools.update({ id: first.pool_id, revision: first.revision, ...base, accounts, participants: members.map(m => ({ ...m, share_percent: m.id === p.id ? "75" : "25" })) });
    assert.equal(second.unallocated_percent, "0"); assert.equal(calls, callsBefore);
    const old = await admin.pools.contract({ id: first.id });
    assert.equal(old.is_current, false); assert.equal(old.unallocated_percent, "50");
    assert.equal(old.participants.find(m => m.id === p.id)?.share_percent, "25");
    const t1 = usageNanos(first.effective_at)!, t2 = usageNanos(second.effective_at)!;
    rows = [row(9, 21, nanosISO(t1 - 1n)), row(10, 21, first.effective_at), row(11, 22, first.effective_at), row(12, 21, second.effective_at)];
    otherRows = [row(1, 21, second.effective_at, 2)];
    assert.ok(t2 >= t1);
    const observed = await capture(a), hidden = await capture(b);
    const all = await admin.pools.evidence({ capture_id: observed.id, after: "0", limit: 100 });
    assert.deepEqual(all.records.map(r => r.contract_id), [null, first.id, first.id, second.id]);
    const prior = await admin.pools.evidence({ capture_id: before.id, after: "0", limit: 100 });
    assert.equal(prior.records[0]?.basis, "no_recorded_contract");
    let user = await admin.users.create({ username: "pool.reader", password: "ReaderPassword123", enabled: true, features: ["pools", "usage"], account_ids: [a.id], participant_ids: [p.id] });
    await reader.auth.login({ username: user.username, password: "ReaderPassword123" });
    const visible = await reader.pools.get({ id: first.pool_id });
    assert.equal(visible.visibility, "scoped_members"); assert.equal(visible.unallocated_percent, null); assert.equal(visible.current_revision, null);
    assert.deepEqual(visible.accounts.map(a => a.id), [a.id]); assert.deepEqual(visible.participants.map(p => p.share_percent), ["75"]);
    const page1 = await reader.pools.evidence({ capture_id: observed.id, after: "0", limit: 2 });
    assert.deepEqual(page1.records.map(r => r.upstream_id), ["9", "10"]); assert.equal(page1.next_after, "10");
    const page2 = await reader.pools.evidence({ capture_id: observed.id, after: page1.next_after!, limit: 2 });
    assert.deepEqual(page2.records.map(r => r.upstream_id), ["12"]); assert.equal(page2.next_after, null);
    await assert.rejects(reader.pools.evidence({ capture_id: hidden.id, after: "0", limit: 100 }), denied(404));
    await assert.rejects(reader.pools.disable({ id: first.pool_id, revision: second.revision }), denied(403));
    const third = await admin.pools.update({ id: first.pool_id, revision: second.revision, ...base, accounts, participants: [{ id: q.id, revision: q.revision, share_percent: "100" }] });
    await assert.rejects(reader.pools.get({ id: first.pool_id }), denied(404));
    const history = await reader.pools.history({ id: first.pool_id, after: "0", limit: 100 });
    assert.deepEqual(history.contracts.map(c => c.id), [first.id, second.id]);
    const globalHistory = await reader.pools.history({ id: "", after: "0", limit: 100 });
    assert.deepEqual(globalHistory.contracts.filter(c => c.pool_id === first.pool_id).map(c => c.id), [first.id, second.id]);
    assert.ok(globalHistory.contracts.every(c => c.current_revision === null));
    await assert.rejects(reader.pools.contract({ id: third.id }), denied(404));
    const userChange = { id: user.id, revision: user.revision, username: user.username, enabled: true, new_password: "", features: user.features, account_ids: user.account_ids, participant_ids: [] };
    user = await admin.users.update(userChange);
    assert.deepEqual((await reader.pools.history({ id: "", after: "0", limit: 100 })).contracts, []);
    await assert.rejects(reader.pools.contract({ id: first.id }), denied(404));
    await assert.rejects(reader.pools.evidence({ capture_id: observed.id, after: "0", limit: 100 }), denied(404));
    await admin.connection.save({ revision: c.revision, base_url: "https://changed.example", credential_action: "clear", credential: "", confirmation: "更换地址" });
    const disabled = await admin.pools.disable({ id: first.pool_id, revision: third.revision });
    assert.equal(disabled.enabled, false); assert.equal(disabled.source_current, false); assert.equal(disabled.source_id, c.source_id);
    assert.equal(JSON.stringify(disabled).includes("private-"), false); assert.equal(unexpected, 0);
  } finally {
    await Promise.allSettled([admin.auth.logout({}), reader.auth.logout({})]);
    upstream.closeAllConnections();
    await new Promise<void>((resolve, reject) => upstream.close(error => !error || (error as NodeJS.ErrnoException).code === "ERR_SERVER_NOT_RUNNING" ? resolve() : reject(error)));
  }
});
