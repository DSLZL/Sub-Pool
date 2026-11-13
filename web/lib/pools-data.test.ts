import { test } from "node:test";
import assert from "node:assert/strict";
import { participant, nativeUser, participantsOutputs } from "@sub-pool/contracts/participants";
import { poolContract } from "@sub-pool/contracts/pools";
import { checkParticipantExit, readParticipantPages, readPoolPages, readPoolHistoryPages, readPoolContracts, readCandidatePages, readRecordedUsage, RecordedSelectionError, shareDraft } from "./pools-data";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ShareBar } from "@/components/charts";
import { Val } from "@/components/ui";
import { proxyAPI } from "./api-proxy";

const id = (n: number) => { const value = Buffer.alloc(16); value.writeUInt32BE(n, 12); return value.toString("base64url"); };
const start = "2026-11-01T00:00:00Z", end = "2026-11-02T00:00:00Z";
const user = (n: number) => nativeUser.parse({ upstream_id: String(n), email: `u${n}@example.test`, username: `u${n}`, role: "user", status: "active", created_at: start });
const member = (n: number) => participant.parse({ id: id(n), revision: "1", name: `Member ${n}`, contact: "", notes: "", enabled: true, source_current: true,
  created_at: start, saved_at: start, binding: { id: String(n), participant_id: id(n), source_id: id(1), upstream_user_id: String(n), started_at: start, ended_at: null, identity: user(n) } });
const contract = (n: number) => poolContract.parse({ id: String(n), pool_id: id(800), revision: String(n), name: "Pool", enabled: true, source_id: id(1), connection_revision: "1",
  effective_at: start, ended_at: n === 10 ? null : end, current_revision: "10", is_current: n === 10, source_current: true, visibility: "all_members",
  accounts: [{ id: id(2), revision: "1", name: "Account", source_id: id(1), upstream_id: "1", identity_current: true }],
  participants: [{ id: id(3), revision: "1", name: "Member", binding_id: "3", share_percent: "25", enabled: true, binding_current: true }], unallocated_percent: "75" });
const summary = (n: number) => { const { accounts, participants, unallocated_percent, ...value } = contract(n); return value; };
const read = { id: "users.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 } as const;

test("exit recovery binds the immutable participant, revision, identity and acknowledged pool contracts", () => {
  const target = member(2), receipt = { participant_id: target.id, previous_revision: "1", revision: "2", binding_id: "2", name: target.name, effective_at: end, pool_contract_ids: ["9", "10"] };
  checkParticipantExit(receipt, target);
  checkParticipantExit(receipt, target, receipt);
  for (const changed of [{ ...receipt, participant_id: id(3) }, { ...receipt, previous_revision: "2" }, { ...receipt, binding_id: "3" }, { ...receipt, name: "Other" }, { ...receipt, effective_at: start }, { ...receipt, pool_contract_ids: ["10"] }]) {
    assert.throws(() => checkParticipantExit(changed, target, receipt));
  }
});

test("participant catalogs require every page without partial or duplicate identities", async () => {
  const values = await readParticipantPages(async after => after ? { participants: [member(3)], next_after: null } : { participants: [member(2)], next_after: id(2) });
  assert.deepEqual(values.map(p => p.id), [id(2), id(3)]);
  await assert.rejects(readParticipantPages(async after => { if (after) throw new Error("late denied page"); return { participants: [member(2)], next_after: id(2) }; }));
  await assert.rejects(readParticipantPages(async after => ({ participants: [member(2)], next_after: after ? null : id(2) })), /Repeated participant/);
});

test("native candidate coverage exceeds 200 and rejects changed source or a failed later page", async () => {
  const result = await readCandidatePages(async page => ({ source_id: id(1), connection_revision: "1", page, next_page: page < 3 ? page + 1 : null, total: "205",
    users: Array.from({ length: page === 3 ? 5 : 100 }, (_, index) => user((page - 1) * 100 + index + 1)), read }), id(1), "1");
  assert.equal(result.users.length, 205);
  assert.equal(result.users.at(-1)?.upstream_id, "205");
  const denied = await readCandidatePages(async page => page === 1
    ? { source_id: id(1), connection_revision: "1", page, next_page: 2, total: "2", users: [user(1)], read }
    : { source_id: id(1), connection_revision: "1", page, next_page: null, total: null, users: [], read: { ...read, state: "denied", reason: "FORBIDDEN" } }, id(1), "1");
  assert.deepEqual(denied.users, []);
  await assert.rejects(readCandidatePages(async page => ({ source_id: id(2), connection_revision: "1", page, next_page: null, total: "1", users: [user(1)], read }), id(1), "1"), /source changed/);
  await assert.rejects(readCandidatePages(async page => ({ source_id: id(1), connection_revision: "1", page, next_page: null, total: "2", users: [user(1)], read }), id(1), "1"), /Incomplete candidate/);
});

test("global history has numeric continuation independent of current-pool visibility", async () => {
  const history = await readPoolHistoryPages(async after => after === "0" ? { contracts: [summary(9)], next_after: "9" } : { contracts: [summary(10)], next_after: null });
  assert.deepEqual(history.map(c => c.id), ["9", "10"]);
  const details = await readPoolContracts(history, async item => contract(Number(item.id)));
  assert.equal(details[0].participants[0].share_percent, "25");
  await assert.rejects(readPoolHistoryPages(async () => ({ contracts: [summary(9)], next_after: "9" })), /continuation/);
  await assert.rejects(readPoolContracts(history, async () => contract(10)), /Pool changed/);
  await assert.rejects(readPoolPages(async () => ({ pools: [summary(9)], next_after: null })), /Invalid current pool/);
  await assert.rejects(readPoolPages(async after => { if (after) throw new Error("denied"); return { pools: [summary(10)], next_after: id(800) }; }));
});

test("share draft validation preserves six-place percentages and an explicit zero", () => {
  assert.deepEqual(shareDraft({ a: "99.999999", b: "0.000001", c: "0", d: "" }), { valid: true, total: "100", unallocated: "0" });
  assert.deepEqual(shareDraft({ a: "33.333333", b: "33.333333" }), { valid: true, total: "66.666666", unallocated: "33.333334" });
  assert.deepEqual(shareDraft({ a: "" }), { valid: true, total: "0", unallocated: "100" });
  assert.equal(shareDraft({ a: "100", b: "0.000001" }).valid, false);
  for (const value of ["-1", "1e1", "1.0000001", " 1", "01", "Infinity"]) assert.equal(shareDraft({ a: value }).valid, false, value);
});

const metrics = { input_tokens: null, output_tokens: null, cache_creation_tokens: null, cache_read_tokens: null, cache_creation_5m_tokens: null, cache_creation_1h_tokens: null,
  image_input_tokens: null, image_output_tokens: null, total_cost: "9007199254740993.000000000000000001", actual_cost: "0", account_stats_cost: null };
const totals = { request_count: "1", metrics };
const usage = () => participantsOutputs.usage.parse({ start_at: start, end_at: end, oldest_capture_at: end, newest_capture_at: end, summary: totals, assigned: totals, unassigned: null, unassigned_visible: false,
  groups: [{ account_id: id(2), participant_id: id(3), basis: "local_binding_at_log_time", ...totals }], grain: "day", buckets: [{ start_at: start, end_at: end, ...totals }], records: [], next_after: null,
  complete: false, settlement_eligible: false, currency: "USD", timestamp_basis: "native_created_at",
  limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"] });
const capture = (account: string, n: number, captured: string) => ({ id: String(n), account_id: account, start_at: start, end_at: end, captured_at: captured });

test("recorded usage reads all capture pages, preserves nanoseconds and uses server participant totals", async () => {
  const queries: { ids: string[]; participant: string }[] = [];
  const result = await readRecordedUsage([{ id: id(2) }, { id: id(4) }], [{ id: id(3) }], async (account, after) => {
    if (account === id(4)) return { captures: [capture(account, 11, "2026-11-02T00:00:00.000000003Z")], next_after: null };
    return after === "0" ? { captures: [capture(account, 9, "2026-11-02T00:00:00.000000002Z")], next_after: "9" }
      : { captures: [capture(account, 10, "2026-11-02T00:00:00.000000001Z")], next_after: null };
  }, async (ids, participant) => { queries.push({ ids, participant }); return usage(); });
  assert.deepEqual(queries, [{ ids: ["9", "11"], participant: "" }, { ids: ["9", "11"], participant: id(3) }]);
  assert.equal(result.byParticipant[id(3)].summary.metrics.total_cost, metrics.total_cost);
  assert.equal(result.byParticipant[id(3)].summary.metrics.actual_cost, "0");
  assert.deepEqual(result.missingAccounts, []);
});

test("missing, incompatible or failed capture selections never become a partial or zero total", async () => {
  let reads = 0;
  const noUsage = async () => { reads++; return usage(); };
  const empty = await readRecordedUsage([{ id: id(2) }], [], async () => ({ captures: [], next_after: null }), noUsage);
  assert.equal(empty.view, null); assert.deepEqual(empty.missingAccounts, [id(2)]);
  await assert.rejects(readRecordedUsage(Array.from({ length: 21 }, (_, n) => ({ id: id(n + 1) })), [], async () => { throw new Error("must not read"); }, noUsage), RecordedSelectionError);
  await assert.rejects(readRecordedUsage([{ id: id(2) }, { id: id(4) }], [], async account => ({ captures: [{ ...capture(account, account === id(2) ? 9 : 10, end), end_at: account === id(2) ? end : "2026-11-03T00:00:00Z" }], next_after: null }), noUsage), RecordedSelectionError);
  await assert.rejects(readRecordedUsage([{ id: id(2) }], [], async (_, after) => { if (after !== "0") throw new Error("late failure"); return { captures: [capture(id(2), 9, end)], next_after: "9" }; }, noUsage));
  assert.equal(reads, 0);
  await assert.rejects(readRecordedUsage([{ id: id(2) }], [{ id: id(3) }], async () => ({ captures: [capture(id(2), 9, end)], next_after: null }), async (_, participant) => participant ? { ...usage(), groups: [{ ...usage().groups[0], participant_id: id(5) }] } : usage()), /binding changed/);
});

test("existing value and share visuals retain exact amounts and conceal a scoped remainder", () => {
  for (const value of ["0", "0.000000000000000001", "9007199254740993.000000000000000001"]) {
    assert.ok(renderToStaticMarkup(createElement(Val, { v: value, prefix: "$" })).replace(/<wbr\s*\/?\s*>/g, "").includes("$" + value));
  }
  assert.ok(renderToStaticMarkup(createElement(Val, { v: null })).includes("未知"));
  const parts = [{ id: id(3), label: "Same name", pct: "25", color: "var(--chart-1)" }, { id: id(4), label: "Same name", pct: "0.000001", color: "var(--chart-2)" }];
  const scoped = renderToStaticMarkup(createElement(ShareBar, { parts, unallocated: null }));
  assert.equal(scoped.includes("未分配"), false);
  assert.ok(scoped.includes("width:25%")); assert.ok(scoped.includes("0.000001%"));
  assert.ok(renderToStaticMarkup(createElement(ShareBar, { parts, unallocated: "74.999999" })).includes("74.999999%"));
});

const config = { publicOrigin: "https://pool.example", backendOrigin: "http://127.0.0.1:18765", allowLoopbackHTTP: true };
const request = (path: string, value: unknown = {}, cookie = "__Host-sub_pool_session=" + "c".repeat(43)) => new Request(config.publicOrigin + path, {
  method: "POST", headers: { "Content-Type": "application/json", Origin: config.publicOrigin, Cookie: cookie, "X-Unsafe-Header": "do-not-forward" }, body: JSON.stringify(value),
});

test("participant and pool proxies keep fixed destinations, strict shapes and bounded full catalogs", async () => {
  const members = Array.from({ length: 100 }, (_, index) => {
    const value = member(index + 1);
    return { ...value, name: "周".repeat(128), contact: "c".repeat(512), notes: "n".repeat(2048), binding: { ...value.binding, identity: { ...value.binding.identity!, email: "e".repeat(512), username: "u".repeat(512) } } };
  });
  const full = contract(10);
  full.participants = Array.from({ length: 500 }, (_, index) => ({ ...full.participants[0], id: id(index + 1000), name: "林".repeat(128), share_percent: "0" }));
  full.unallocated_percent = "100";
  poolContract.parse(full);
  const exited = { participant_id: id(1), previous_revision: "1", revision: "2", binding_id: "1", name: "Member", effective_at: end, pool_contract_ids: ["11"] };
  const outputs: Record<string, unknown> = { "/api/v1/participants/exit": exited, "/api/v1/participants/exit-result": exited, "/api/v1/participants/list": { participants: members, next_after: null }, "/api/v1/pools/get": full, "/api/v1/pools/history": { contracts: [summary(9), summary(10)], next_after: null } };
  for (const [path, body] of Object.entries(outputs)) {
    let calls = 0;
    const response = await proxyAPI(request(path), config, async (target, options) => {
      calls++; assert.equal(target, config.backendOrigin + path);
      const headers = new Headers(options?.headers);
      assert.equal(headers.get("Origin"), config.publicOrigin); assert.equal(headers.get("X-Unsafe-Header"), null);
      assert.equal(headers.get("Cookie"), "__Host-sub_pool_session=" + "c".repeat(43));
      return Response.json(body);
    });
    assert.equal(response.status, 200, path); assert.equal(calls, 1); assert.deepEqual(await response.json(), body);
  }
  let calls = 0;
  const fetcher = async () => { calls++; return Response.json({ participants: [], next_after: null }); };
  assert.equal((await proxyAPI(request("/api/v1/participants/list?x=1"), config, fetcher)).status, 400);
  assert.equal((await proxyAPI(request("/api/v1/pools/unknown"), config, fetcher)).status, 404);
  assert.equal((await proxyAPI(request("/api/v1/participants/list", {}, "__Host-sub_pool_session=" + "c".repeat(43) + "; __Host-sub_pool_session=" + "d".repeat(43)), config, fetcher)).status, 401);
  assert.equal((await proxyAPI(new Request(config.publicOrigin + "/api/v1/pools/list"), config, fetcher)).status, 405);
  assert.equal(calls, 0);
  assert.equal((await proxyAPI(request("/api/v1/participants/list"), config, async () => Response.json({ participants: [], next_after: null, secret: "private" }))).status, 502);
});

test("lost pool and participant acknowledgements are never replayed by the proxy", async () => {
  for (const path of ["/api/v1/pools/update", "/api/v1/participants/create", "/api/v1/participants/exit", "/api/v1/participants/exit-result"]) {
    let calls = 0;
    const response = await proxyAPI(request(path), config, async () => { calls++; throw new Error("accepted before disconnect"); });
    assert.equal(response.status, 502); assert.equal(calls, 1);
  }
});
