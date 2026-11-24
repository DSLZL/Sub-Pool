import assert from "node:assert/strict";
import { test } from "node:test";
import { walletsOutputs } from "@sub-pool/contracts/wallets";
import { walletApplication, walletApplicationsOutputs } from "@sub-pool/contracts/wallet-applications";
import { readWalletData, readWalletHistory, readWalletOperationPage, readWalletRequests, walletTimeRange, walletTimeMatches, walletRow, walletOperation, WalletReadError } from "./wallets-data";
import { proxyAPI } from "./api-proxy";

const id = (n: number) => { const bytes = Buffer.alloc(16); bytes.writeUInt32BE(n, 12); return bytes.toString("base64url"); };
const automation = walletApplicationsOutputs.automation.parse({ enabled: false, can_enable: false, state: "unavailable", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", execution_state: "not_implemented" });
function view(n = 1, admin = true) {
  const field = (value: string | null) => ({ value, state: value === null ? "unverified" : "reported", reason: value === null ? "FIELD_NOT_RETURNED" : "NATIVE_FIELD_REPORTED" });
  const receipt = { id: "1", participant_id: id(n), binding_id: String(n), source_id: id(900), connection_revision: "1", attempted_at: "2026-11-07T12:00:00.000001Z", participant_revision: "1", upstream_user_id: String(n + 20), read_at: "2026-11-07T12:00:00.000002Z", balance: field(n === 1 ? "9007199254740993.123456789012345678" : "0"), frozen_balance: field("2.5"), read: { id: "users.read", state: "available", reason: "READ_OK", http_status: 200, latency_ms: 1 }, field_authority: { id: "users.balance.read", state: "available", reason: "READ_OK", http_status: 200, latency_ms: 1 }, unit: "native_wallet" };
  return walletsOutputs.get.parse({
    participant_id: id(n), name: "Member " + n, participant_revision: "1", binding_id: String(n), source_id: id(900), upstream_user_id: String(n + 20), enabled: true, source_current: true,
    connection_revision: "1", checked_at: "2026-11-07T12:00:01Z",
    latest_read: { ...receipt, id: "2", read_at: null, balance: field(null), frozen_balance: field(null), read: { ...receipt.read, state: "failed", reason: "UPSTREAM_FAILED", http_status: 502 }, field_authority: { ...receipt.field_authority, state: "unverified", reason: "FIELD_PERMISSION_UNVERIFIED", http_status: null } },
    last_confirmed_read: receipt, read_references_current: true, latest_recommendation: null, contributions: [], visibility: admin ? "all_recorded_sources" : "authorized_sources", current_state: admin ? "not_generated" : "unavailable", needs_recalculation: admin ? false : null, current_target: null, applicable: false, difference: null, last_application: null, application_state: "not_implemented",
  });
}
function summary(value: ReturnType<typeof view>) {
  const { participant_id, name, participant_revision, binding_id, source_id, upstream_user_id, enabled, source_current } = value;
  return { participant_id, name, participant_revision, binding_id, source_id, upstream_user_id, enabled, source_current };
}
function operation(n: number, participant = 1, admin = true) {
  return walletApplication.parse({
    id: String(n), participant_id: id(participant), participant_name: "Member " + participant, attempted_at: "2026-11-07T12:00:01Z",
    state: "rejected", dispatch_state: "not_dispatched", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", target: null, unit: "native_wallet", visibility: admin ? "administrator" : "participant_summary",
    details: admin ? { actor_id: id(800), input: { request_id: id(n), participant_id: id(participant), participant_revision: "1", binding_id: String(participant), connection_revision: "1", source_id: id(900), latest_id: "0", recommendation_id: "0" }, upstream_user_id: String(participant + 20), evaluated_basis_hash: "a".repeat(64), recommendation_basis_hash: null, reasons: ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED", "WALLET_WRITE_CAPABILITY_UNVERIFIED", "TARGET_UNAVAILABLE", "RECOMMENDATION_NOT_GENERATED"] } : null,
  });
}
const history = (requests: ReturnType<typeof operation>[], next_after: string | null = null) => ({ participant_id: "", start_at: "", end_at: "", requests, next_after });

test("simulated operation display distinguishes independently confirmed proof, uncertainty and private summaries", () => {
  const marker = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id(900) };
  const before = { ...marker, user_id: "21", balance: "95", frozen_balance: "0", wallet_version: "7", ledger_revision: "17", closed_through: "2026-11-07T12:00:00Z",
    pending_requests: "0", source_account_ids: ["1"], scope_complete: true, billing_type: "standard_wallet", ledger_closed: true };
  const request = { ...marker, operation_id: "b".repeat(64), user_id: "21", expected_wallet_version: "7", expected_ledger_revision: "17", expected_closed_through: before.closed_through, source_account_ids: ["1"], target: "1.92500000", quantum: "0.00000001" };
  const old = operation(1); assert.ok(old.details);
  const proof = { ...marker, operation_id: request.operation_id, user_id: "21", request_hash: "c".repeat(64), request, state: "applied", reason: "APPLIED", before,
    after: { ...before, balance: request.target, wallet_version: "8", ledger_revision: "18", closed_through: "2026-11-07T12:00:02Z" }, occurred_at: "2026-11-07T12:00:02Z" };
  const raw = { ...old, simulation: marker, updated_at: "2026-11-07T12:00:03Z", state: "applied", dispatch_state: "confirmed", reason: "SIMULATED_APPLIED", unit: "simulated_wallet_usd", target: { numerator: "77", denominator: "40" },
    details: { ...old.details, input: { ...old.details.input, simulation: marker, recommendation_id: "1" }, recommendation_basis_hash: "a".repeat(64), reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_APPLIED"],
      simulation_write: { request, before, original_target: { numerator: "77", denominator: "40" }, remainder: { numerator: "0", denominator: "1" } }, simulation_proof: proof } };
  const applied = walletOperation(walletApplication.parse(raw));
  assert.equal(applied.outcome, "confirmed"); assert.equal(applied.before, "95"); assert.equal(applied.target, "1.92500000"); assert.equal(applied.after, "1.92500000"); assert.match(applied.note, /仅模拟协议/);
  const unknown = walletOperation(walletApplication.parse({ ...raw, state: "unknown", dispatch_state: "unknown", reason: "SIMULATED_WRITE_RESULT_UNKNOWN", details: { ...raw.details, reasons: ["CONDITIONAL_SIMULATION_ONLY", "SIMULATED_WRITE_RESULT_UNKNOWN"] } }));
  assert.equal(unknown.target, "1.92500000"); assert.equal(unknown.after, null); assert.equal(unknown.outcome, "unknown"); assert.match(unknown.note, /禁止重复派发/);
  const publicRow = walletOperation(walletApplication.parse({ ...raw, visibility: "participant_summary", details: null, target: null }));
  assert.equal(publicRow.target, null); assert.equal(publicRow.before, null); assert.equal(publicRow.after, null); assert.equal(publicRow.actor, "—");
});

test("wallet reads consume every scoped page and preserve exact native amounts, retained failures and rejected history", async () => {
  const firstValue = view(1), zeroValue = view(2), values = [firstValue, zeroValue]; let lists = 0, histories = 0, auto = 0;
  const data = await readWalletData({
    list: async ({ after }) => { lists++; return { participants: [summary(after ? zeroValue : firstValue)], next_after: after ? null : id(1) }; },
    get: async ({ participant_id }) => { const found = values.find(row => row.participant_id === participant_id); assert.ok(found); return found; },
  }, {
    list: async ({ after, participant_id }) => {
      histories++;
      const page = participant_id === id(1) ? after === "0" ? history([operation(9)], "9") : history([operation(10)]) : history([]);
      return { ...page, participant_id };
    },
    automation: async () => { auto++; return automation; },
  }, true);
  assert.equal(lists, 2); assert.equal(histories, 3); assert.equal(auto, 1);
  const [firstWallet, zeroWallet] = data.wallets; assert.ok(firstWallet && zeroWallet);
  const first = walletRow(firstWallet, data.operations), zero = walletRow(zeroWallet, data.operations);
  assert.equal(first.balance, "9007199254740993.123456789012345678"); assert.equal(first.frozen, "2.5"); assert.equal(zero.balance, "0");
  assert.equal(first.balanceReadAt, "2026-11-07T12:00:00.000002Z"); assert.match(first.readNote, /完整确认读数 #1/); assert.match(first.readNote, /最近尝试 #2/);
  assert.equal(first.recommendation, null); assert.equal(first.view.difference, null); assert.equal(first.lastApplied?.id, "10"); assert.equal(first.lastApplied?.outcome, "rejected");
  assert.equal(walletOperation(operation(10)).after, null); assert.match(walletOperation(operation(10)).note, /未向上游派发/);
});

test("a duplicate cursor, changed identity, repeated numeric history or later failure invalidates the complete wallet read", async () => {
  const value = view(), base = { list: async () => ({ participants: [summary(value)], next_after: null }), get: async () => value };
  const applications = { list: async (input: { participant_id: string }) => ({ ...history([]), participant_id: input.participant_id }), automation: async () => automation };
  await assert.rejects(readWalletData({ ...base, list: async () => ({ participants: [summary(value)], next_after: id(1) }) }, applications, true), WalletReadError);
  await assert.rejects(readWalletData({ ...base, get: async () => ({ ...value, participant_revision: "2" }) }, applications, true), WalletReadError);
  await assert.rejects(readWalletData(base, { ...applications, list: async input => ({ ...history([operation(9)], "9"), participant_id: input.participant_id }) }, true), WalletReadError);
  await assert.rejects(readWalletData({ ...base, get: async () => { throw new TypeError("later wallet unavailable"); } }, applications, true), /later wallet unavailable/);
  await assert.rejects(readWalletData(base, { ...applications, list: async () => ({ ...history([]), participant_id: id(2) }) }, true), WalletReadError);
});

test("balances-only ordinary reads request no account catalog or administrator automation and keep stripped history", async () => {
  const value = view(2, false); let auto = 0;
  const data = await readWalletData({ list: async () => ({ participants: [summary(value)], next_after: null }), get: async () => value },
    { list: async input => ({ ...history([operation(9, 2, false)]), participant_id: input.participant_id }), automation: async () => { auto++; throw new Error("administrator-only"); } }, false);
  assert.equal(auto, 0); assert.equal(data.automation, null); assert.equal(walletRow(value, data.operations).balance, "0");
  const [request] = data.operations; assert.ok(request);
  assert.equal(walletOperation(request).actor, "—"); assert.equal(walletOperation(request).rec, "—");
  assert.equal(JSON.stringify(data).includes(id(800)), false); assert.equal(data.wallets[0]?.latest_recommendation, null);
});

test("wallet catalog reaches 1000 participants and fetches details only for the selected page", async () => {
  const rows = Array.from({ length: 1000 }, (_, index) => view(index + 1));
  const detail: string[] = [];
  const data = await readWalletData({
    list: async ({ after }) => {
      const start = after ? rows.findIndex(row => row.participant_id === after) + 1 : 0;
      const participants = rows.slice(start, start + 100).map(summary);
      return { participants, next_after: start + participants.length < rows.length ? participants.at(-1)?.participant_id ?? null : null };
    },
    get: async ({ participant_id }) => {
      detail.push(participant_id);
      const row = rows.find(item => item.participant_id === participant_id);
      assert.ok(row);
      return row;
    },
  }, { list: async input => ({ ...history([]), participant_id: input.participant_id }), automation: async () => automation }, true, undefined, id(975));
  assert.equal(data.catalog.length, 1000);
  assert.equal(data.wallets.at(-1)?.participant_id, id(1000));
  assert.equal(data.nextAfter, null);
  assert.deepEqual(detail, rows.slice(975).map(row => row.participant_id));
});

test("numeric operation pages reach record 2001 and keep the participant scope", async () => {
  const filter = { participant_id: id(1), start_at: "", end_at: "" };
  const client = { list: async ({ after }: { after: string }) => {
    const begin = Number(after), requests = Array.from({ length: Math.min(100, 2001 - begin) }, (_, index) => operation(begin + index + 1));
    return { ...history(requests, begin + requests.length === 2001 ? null : requests.at(-1)?.id ?? null), ...filter };
  } };
  assert.equal((await readWalletRequests(client, id(1), true)).length, 2001);
  const tail = await readWalletOperationPage(client, filter, "2000", true);
  assert.equal(tail.requests[0]?.id, "2001");
  assert.equal(tail.next_after, null);
});

const origin = "https://pool.test", backend = "https://go.test", config = { publicOrigin: origin, backendOrigin: backend, allowLoopbackHTTP: false };
function request(path: string, method = "POST", extra: HeadersInit = {}) {
  return new Request(origin + path, { method, headers: { Origin: origin, "Content-Type": "application/json", Cookie: "other=unrelated; __Host-sub_pool_session=" + "a".repeat(43), Authorization: "do-not-forward", "X-Forwarded-For": "198.51.100.1", ...extra }, body: method === "POST" ? "{}" : undefined });
}

test("wallet proxy exposes only the five fixed reads with validated native units, current cookie and safe defined errors", async () => {
  const outputs = { "/api/v1/wallets/list": { participants: [summary(view())], next_after: null }, "/api/v1/wallets/get": view(),
    "/api/v1/wallet-applications/list": history([operation(9)]), "/api/v1/wallet-applications/get": operation(9), "/api/v1/wallet-applications/automation": automation };
  let calls = 0;
  for (const [path, output] of Object.entries(outputs)) {
    const result = await proxyAPI(request(path), config, async (input, init) => {
      calls++; assert.equal(String(input), backend + path); assert.equal(init?.method, "POST"); assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error");
      const headers = new Headers(init?.headers); assert.equal(headers.get("Cookie"), "__Host-sub_pool_session=" + "a".repeat(43)); assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("X-Forwarded-For"), null); assert.equal(headers.get("Origin"), origin);
      return Response.json(output);
    }); assert.equal(result.status, 200); assert.deepEqual(await result.json(), output); assert.equal(result.headers.get("Cache-Control"), "no-store");
  }
  assert.equal(calls, 5);
  const error = await proxyAPI(request("/api/v1/wallets/get"), config, async () => Response.json({ defined: true, code: "CONNECTION_NOT_CONFIGURED", status: 409, message: "private", data: { retry_at: null } }, { status: 409 }));
  assert.equal(error.status, 409); assert.equal((await error.json()).message.includes("private"), false);
  for (const path of ["/api/v1/wallets/update", "/api/v1/wallet-applications/request", "/api/v1/wallet-applications/automation/update"]) assert.equal((await proxyAPI(request(path), config, async () => { throw new Error("mutation must not dispatch"); })).status, 404);
});

test("wallet proxy refuses forged financial/ordinary details, enabling, cookie issuance and oversized replies without replay", async () => {
  const cases: [string, Response][] = [
    ["/api/v1/wallets/get", Response.json({ ...view(), current_target: "0" })],
    ["/api/v1/wallet-applications/get", Response.json({ ...operation(9), state: "confirmed" })],
    ["/api/v1/wallet-applications/get", Response.json({ ...operation(9), visibility: "participant_summary" })],
    ["/api/v1/wallet-applications/automation", Response.json({ ...automation, enabled: true })],
    ["/api/v1/wallet-applications/automation", Response.json(automation, { headers: { "Set-Cookie": "other=secret" } })],
    ["/api/v1/wallet-applications/list", new Response(JSON.stringify(history([])) + " ".repeat(1024 * 1024))],
  ]; let calls = 0;
  for (const [path, response] of cases) { const result = await proxyAPI(request(path), config, async () => { calls++; return response; }); assert.equal(result.status, 502); }
  assert.equal(calls, cases.length);
  const fetcher: typeof fetch = async () => { throw new Error("invalid request must not dispatch"); };
  assert.equal((await proxyAPI(request("/api/v1/wallets/get", "GET"), config, fetcher)).status, 405);
  assert.equal((await proxyAPI(request("/api/v1/wallets/get?target=0"), config, fetcher)).status, 400);
  assert.equal((await proxyAPI(request("/api/v1/wallets/get", "POST", { Origin: "https://attacker.test" }), config, fetcher)).status, 403);
});

test("automatic wallet explanations pass the existing read proxy without becoming an amount or ordinary history", async () => {
  const value = view();
  const recommendation = { id: "10", participant_id: value.participant_id, binding_id: value.binding_id, generated_at: value.checked_at,
    rules_version: "gate-003-rules-v1", trigger: "input_change", participant_revision: value.participant_revision, basis_hash: "a".repeat(64), expires_at: null,
    global: { state: "no_recorded_sources", source_count: 0, limitations: ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] },
    contributions: [], target: null, complete: false, applicable: false };
  const automatic = walletsOutputs.get.parse({ ...value, latest_recommendation: recommendation, current_state: "unavailable" });
  const response = await proxyAPI(request("/api/v1/wallets/get"), config, async () => Response.json(automatic));
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), automatic);
  assert.equal(walletRow(automatic, []).recommendation, null);
  const leaked = await proxyAPI(request("/api/v1/wallets/get"), config, async () => Response.json({ ...automatic, visibility: "authorized_sources", needs_recalculation: null }));
  assert.equal(leaked.status, 502);
});

function sourceScan(state: "exhausted" | "partial" | "failed" = "exhausted") {
  return {
    started_at: "2026-11-07T12:00:00.000001Z", captured_at: "2026-11-07T12:00:00.000002Z", state,
    read: { id: "usage.read", state: state === "exhausted" ? "available" : "denied", reason: state === "exhausted" ? "READ_OK" : "PERMISSION_DENIED", http_status: state === "exhausted" ? 200 : 403, latency_ms: 1 },
    pages_read: state === "failed" ? 0 : 1, upstream_total: state === "failed" ? null : state === "partial" ? "101" : "6", scanned_count: state === "failed" ? "0" : state === "partial" ? "100" : "6",
    accounts: state === "failed" ? [] : [{ upstream_account_id: "7", managed_account_id: id(7) }, { upstream_account_id: "8", managed_account_id: null }],
    complete: false, limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "WALLET_DEDUCTION_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", ...(state === "exhausted" ? [] : ["PERMISSION_DENIED"])],
  };
}

test("native source notes expose observation membership only to administrators without changing exact wallet or financial state", async () => {
  const value = view(); assert.ok(value.latest_read);
  const scanned = walletsOutputs.get.parse({ ...value, latest_read: { ...value.latest_read, usage_sources: sourceScan() } });
  const result = await proxyAPI(request("/api/v1/wallets/get"), config, async () => Response.json(scanned));
  assert.equal(result.status, 200); assert.deepEqual(await result.json(), scanned);
  const row = walletRow(scanned, []);
  assert.match(row.readNote, /原生用量来源扫描已结束/);
  assert.match(row.readNote, /记录 2 个来源（读取时已纳管 1 个、未纳管 1 个）/);
  assert.match(row.readNote, /不代表完整消费或钱包扣款/);
  assert.match(row.readNote, /完整确认读数 #1/); assert.match(row.readNote, /最近尝试 #2/);
  assert.equal(row.balance, "9007199254740993.123456789012345678"); assert.equal(row.frozen, "2.5");
  assert.equal(row.recommendation, null); assert.equal(row.view.applicable, false); assert.deepEqual(row.contributions, []);
});

test("partial, failed and exhausted-empty source scans keep independent evidence and never claim complete wallet coverage", () => {
  const value = view(); assert.ok(value.latest_read);
  const cases = [
    { scan: sourceScan("partial"), expected: /原生用量来源扫描未完成，仅保留已核验部分/, counts: /记录 2 个来源/ },
    { scan: sourceScan("failed"), expected: /原生用量来源扫描失败，未确认任何来源/, counts: null },
    { scan: { ...sourceScan(), accounts: [], upstream_total: "0", scanned_count: "0" }, expected: /原生用量来源扫描已结束/, counts: /记录 0 个来源（读取时已纳管 0 个、未纳管 0 个）/ },
  ];
  for (const { scan, expected, counts } of cases) {
    const scanned = walletsOutputs.get.parse({ ...value, latest_read: { ...value.latest_read, usage_sources: scan } });
    const row = walletRow(scanned, []);
    assert.match(row.readNote, expected); assert.match(row.readNote, /不代表完整消费或钱包扣款/);
    if (counts) assert.match(row.readNote, counts); else assert.doesNotMatch(row.readNote, /记录 0 个来源/);
    assert.equal(row.balance, value.last_confirmed_read?.balance.value); assert.equal(row.recommendation, null); assert.equal(row.view.applicable, false);
  }
});

test("latest source evidence never falls back to an older confirmed scan and older receipts remain readable", () => {
  const value = view(); assert.ok(value.latest_read && value.last_confirmed_read);
  const oldScan = { ...value.last_confirmed_read, usage_sources: sourceScan() };
  const legacy = walletsOutputs.get.parse({ ...value, last_confirmed_read: oldScan });
  const missing = walletsOutputs.get.parse({ ...value, latest_read: null, last_confirmed_read: null, read_references_current: false });
  for (const row of [walletRow(value, []), walletRow(legacy, []), walletRow(missing, [])]) {
    assert.match(row.readNote, /尚未记录原生用量来源扫描/); assert.doesNotMatch(row.readNote, /记录 2 个来源/);
  }
  const failed = walletsOutputs.get.parse({ ...legacy, latest_read: { ...value.latest_read, usage_sources: sourceScan("failed") } });
  assert.match(walletRow(failed, []).readNote, /扫描失败，未确认任何来源/);
  assert.doesNotMatch(walletRow(failed, []).readNote, /记录 2 个来源/);
});

test("ordinary source inventories are rejected in either receipt head and never projected as source metadata", async () => {
  const value = view(1, false); assert.ok(value.latest_read && value.last_confirmed_read);
  assert.doesNotMatch(walletRow(value, []).readNote, /来源扫描|已纳管|未纳管/);
  for (const key of ["latest_read", "last_confirmed_read"] as const) {
    const leaked = { ...value, [key]: { ...value[key], usage_sources: sourceScan() } };
    assert.equal(walletsOutputs.get.safeParse(leaked).success, false);
    const response = await proxyAPI(request("/api/v1/wallets/get"), config, async () => Response.json(leaked));
    assert.equal(response.status, 502);
    assert.doesNotMatch(walletRow(leaked, []).readNote, /来源扫描|已纳管|未纳管/);
  }
});


test("conditional simulated wallet amounts preserve exact zero and stay labelled independently from financial application", () => {
  const base = view(), simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id(700) };
  const amount = { numerator: "77", denominator: "40" }, zero = { numerator: "0", denominator: "1" };
  const proof = { input_hash: "a".repeat(64), start_at: "2026-11-07T10:00:00Z", end_at: "2026-11-07T11:59:00Z", closed_through: "2026-11-07T11:59:01Z", expires_at: "2026-11-07T12:10:00Z", ledger_revision: "17", consumed: amount, spent: zero, remaining: amount, reserve: zero };
  const contribution = { account_id: id(7), account_revision: "1", current_contract_id: "1", historical_contract_head: "2", share_percent: "25", estimate_id: "3", estimate_state: "scenario", observation_head: "3", capture_head: "0", state: "scenario", amount, simulation, simulation_proof: proof };
  const recommendation = { id: "4", participant_id: base.participant_id, binding_id: base.binding_id, generated_at: "2026-11-07T12:00:00Z", rules_version: "gate-003-rules-v1", trigger: "wallet_refresh", participant_revision: "1", basis_hash: "a".repeat(64), expires_at: proof.expires_at, simulation, global: { state: "scenario", source_count: 1, limitations: ["CONDITIONAL_SIMULATION_ONLY", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] }, contributions: [contribution], target: amount, complete: true, applicable: false };
  assert.ok(base.last_confirmed_read);
  const receipt = { ...base.last_confirmed_read, unit: "simulated_wallet_usd", simulation, balance: { value: "95", state: "reported", reason: "SIMULATED_FIELD_REPORTED" }, frozen_balance: { value: "0", state: "reported", reason: "SIMULATED_FIELD_REPORTED" } };
  const current = walletsOutputs.get.parse({ ...base, current_state: "scenario", current_target: amount, latest_recommendation: recommendation, latest_read: receipt, last_confirmed_read: receipt, contributions: [contribution] });
  const row = walletRow(current, []);
  assert.equal(row.recState, "scenario"); assert.equal(row.recommendation, "77/40"); assert.equal(row.balance, "95"); assert.equal(row.frozen, "0");
  assert.match(row.recReason, /模拟协议条件化建议/); assert.match(row.recReason, /提交时仍需服务端核对当前绑定与全部前提/); assert.match(row.readNote, /模拟钱包 USD/);
  assert.equal(row.contributions[0]?.state, "模拟协议条件贡献 · 临时影响未记录"); assert.equal(row.view.applicable, false);
  const exhausted = walletsOutputs.get.parse({ ...current, current_target: zero, latest_recommendation: { ...recommendation, target: zero, contributions: [{ ...contribution, amount: zero }] }, contributions: [{ ...contribution, amount: zero }] });
  assert.equal(walletRow(exhausted, []).recommendation, "0");
  const stale = walletsOutputs.get.parse({ ...current, checked_at: proof.expires_at, current_state: "recalculation_required", current_target: null, needs_recalculation: true });
  assert.equal(walletRow(stale, []).recommendation, null); assert.equal(walletRow(stale, []).recState, "stale");
});

function retainedRecommendation(number: string) {
  return walletsOutputs.recompute.parse({ id: number, participant_id: id(1), binding_id: "1", generated_at: "2026-11-07T12:00:00.123456789Z",
    rules_version: "gate-003-rules-v1", trigger: "manual_recompute", participant_revision: "1", basis_hash: "a".repeat(64), expires_at: null,
    global: { state: "no_recorded_sources", source_count: 0, limitations: ["WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"] },
    contributions: [], target: null, complete: false, applicable: false });
}
test("retained history consumes both immutable kinds with numeric cursors above Number precision", async () => {
  const read = view().last_confirmed_read!; const first = "9007199254740992", second = "9007199254740993"; const calls: string[] = [];
  const client: Parameters<typeof readWalletHistory>[0] = { history: async input => {
    assert.equal(input.participant_id, id(1)); assert.equal(input.limit, 100); calls.push(input.kind + ":" + input.after);
    return input.kind === "wallet" ? { participant_id: id(1), kind: "wallet", wallet_reads: [{ ...read, binding_id: input.after === "0" ? "1" : "99", id: input.after === "0" ? first : second }], recommendations: [], next_after: input.after === "0" ? first : null }
      : { participant_id: id(1), kind: "recommendation", wallet_reads: [], recommendations: [retainedRecommendation(second)], next_after: null };
  } };
  const rows = await readWalletHistory(client, id(1));
  const next = await readWalletHistory(client, id(1), undefined, { wallet: rows.nextAfter.wallet ?? "0", recommendation: "0" });
  assert.deepEqual(calls, ["wallet:0", "recommendation:0", "wallet:" + first, "recommendation:0"]);
  assert.equal(rows.walletReads[0]?.id, first); assert.equal(next.walletReads[0]?.id, second);
  assert.equal(rows.walletReads[0]?.binding_id, "1"); assert.equal(next.walletReads[0]?.binding_id, "99"); assert.equal(rows.recommendations[0]?.id, second);
  assert.equal(rows.walletReads[0]?.balance.value, "9007199254740993.123456789012345678");
});
test("history identity, wrong kind, backwards cursor and later failure fail the page", async () => {
  const read = view().last_confirmed_read!, page = { participant_id: id(1), kind: "wallet" as const, wallet_reads: [read], recommendations: [], next_after: null };
  for (const bad of [{ ...page, participant_id: id(2) }, { ...page, kind: "recommendation" as const }, { ...page, wallet_reads: [{ ...read, participant_id: id(2) }] },
    { ...page, next_after: "2" }, { ...page, wallet_reads: [], next_after: "1" }, { ...page, recommendations: [retainedRecommendation("1")] }]) {
    await assert.rejects(readWalletHistory({ history: async () => bad }, id(1)), WalletReadError);
  }
  await assert.rejects(readWalletHistory({ history: async () => ({ ...page, next_after: "1" }) }, id(1), undefined, { wallet: "1", recommendation: "0" }), WalletReadError);
  await assert.rejects(readWalletHistory({ history: async ({ kind }) => { if (kind === "recommendation") throw new Error("second kind failed"); return page; } }, id(1)), /second kind failed/);
  const tail = await readWalletHistory({ history: async ({ kind, after }) => kind === "wallet"
    ? { ...page, wallet_reads: [{ ...read, id: String(BigInt(after) + 1n) }] }
    : { ...page, kind, wallet_reads: [], recommendations: [] } }, id(1), undefined, { wallet: "2000", recommendation: "0" });
  assert.equal(tail.walletReads[0]?.id, "2001");
});
test("display filters retain exact UTC nanoseconds, half-open endpoints and valid calendars without mutating application inputs", () => {
  const start = "2026-11-07T12:00:00.123456788Z", middle = "2026-11-07T12:00:00.123456789Z", end = "2026-11-07T12:00:00.123456790Z";
  const range = walletTimeRange(start, end); assert.equal(range.error, null);
  assert.equal(walletTimeMatches(start, range), true); assert.equal(walletTimeMatches(middle, range), true); assert.equal(walletTimeMatches(end, range), false);
  assert.equal(walletTimeMatches(middle, walletTimeRange("", "")), true);
  assert.equal(walletTimeMatches(middle, walletTimeRange(start, "")), true); assert.equal(walletTimeMatches(middle, walletTimeRange("", end)), true);
  for (const [from, to] of [[end, start], [start, start], ["2026-02-30T00:00:00Z", ""], ["2026-11-07T12:00:00+00:00", ""], ["bad", ""]]) {
    const invalid = walletTimeRange(from!, to!); assert.ok(invalid.error); assert.equal(walletTimeMatches(middle, invalid), false);
  }
  const original = [operation(1), operation(2)], before = JSON.stringify(original);
  assert.deepEqual(original.filter(row => walletTimeMatches(row.attempted_at, range)), []); assert.equal(JSON.stringify(original), before);
});
test("refresh recompute and history use fixed POST proxies with strict replies, no identity forwarding and no automatic retry", async () => {
  const outputs = { "/api/v1/wallets/refresh": view().last_confirmed_read!, "/api/v1/wallets/recompute": retainedRecommendation("1"),
    "/api/v1/wallets/history": { participant_id: id(1), kind: "wallet", wallet_reads: [view().last_confirmed_read!], recommendations: [], next_after: null } };
  for (const [path, output] of Object.entries(outputs)) {
    let calls = 0;
    const result = await proxyAPI(request(path), config, async (input, init) => {
      calls++; assert.equal(String(input), backend + path); assert.equal(init?.method, "POST");
      const headers = new Headers(init?.headers); assert.equal(headers.get("Authorization"), null); assert.equal(headers.get("X-Forwarded-For"), null);
      assert.equal(headers.get("Cookie"), "__Host-sub_pool_session=" + "a".repeat(43)); return Response.json(output);
    });
    assert.equal(result.status, 200); assert.deepEqual(await result.json(), output); assert.equal(calls, 1);
    const malformed = await proxyAPI(request(path), config, async () => { calls++; return Response.json({ ...output, secret: "PRIVATE_SENTINEL" }); });
    assert.equal(malformed.status, 502); assert.equal((await malformed.text()).includes("PRIVATE_SENTINEL"), false); assert.equal(calls, 2);
    const lost = await proxyAPI(request(path), config, async () => { calls++; throw new Error("PRIVATE_SENTINEL"); });
    assert.equal(lost.status, 502); assert.equal(calls, 3);
    for (const [suffix, method, extra, expected] of [["", "GET", {}, 405], ["?target=0", "POST", {}, 400], ["", "POST", { Origin: "https://attacker.test" }, 403]] as const) {
      assert.equal((await proxyAPI(request(path + suffix, method, extra), config, async () => { throw new Error("must not dispatch"); })).status, expected);
    }
    const denied = await proxyAPI(request(path), config, async () => Response.json({ defined: true, code: "FORBIDDEN", status: 403, message: "private", data: { retry_at: null } }, { status: 403 }));
    assert.equal(denied.status, 403); assert.equal((await denied.text()).includes("private"), false);
  }
});

test("own contribution explains exact temporary inputs without inventing missing historical effects", () => {
  const base = view(1, false);
  const source = { account_id: id(100), account_revision: "1", current_contract_id: "1", historical_contract_head: "1", share_percent: "50", estimate_id: "1", estimate_state: "scenario", observation_head: "1", capture_head: "1",
    state: "scenario", amount: { numerator: "1", denominator: "3" }, simulation: { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id(900) } };
  const state = (effect?: unknown) => {
    const result = walletRow(walletsOutputs.get.parse({ ...base, contributions: [{ ...source, ...(effect === undefined ? {} : { temporary_effect: effect }) }] }), []).contributions[0];
    assert.ok(result); return result;
  };
  assert.match(state().state, /临时影响未记录/);
  assert.match(state({ state: "ordinary" }).state, /普通分配（无临时调整）/);
  for (const [mode, label] of [["carry", "结转"], ["no_carry", "不结转"]]) {
    const active = state({ state: "active", mode, cap: "9007199254740993.00000001", deadline: "2026-11-07T12:10:00.000000001Z" });
    assert.equal(active.amount, "1/3");
    assert.ok(active.state.includes("本人临时上限 9007199254740993.00000001（" + label + "）"));
    assert.ok(active.state.includes("2026-11-07T12:10:00.000000001Z（UTC）"));
  }
  assert.match(state({ state: "active", mode: "carry", cap: "0", deadline: "2026-11-07T12:10:00Z" }).state, /本人临时上限 0/);
  const rights = state({ state: "rights", adjustment: "-0.00000001" });
  assert.equal(rights.amount, "1/3"); assert.match(rights.state, /本人当前周期结转调整 -0.00000001（分配输入）/);
  const unavailable = walletRow(walletsOutputs.get.parse({ ...base, contributions: [{ ...source, state: "evidence_incomplete", amount: null, simulation: undefined }] }), []).contributions[0];
  assert.ok(unavailable); assert.equal(unavailable.state, "费用与钱包扣费依据不足");
});
