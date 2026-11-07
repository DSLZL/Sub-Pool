import assert from "node:assert/strict";
import { test } from "node:test";
import { walletsOutputs } from "@sub-pool/contracts/wallets";
import { walletApplication, walletApplicationsOutputs } from "@sub-pool/contracts/wallet-applications";
import { readWalletData, walletRow, walletOperation, WalletReadError } from "./wallets-data";
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

test("wallet reads consume every scoped page and preserve exact native amounts, retained failures and rejected history", async () => {
  const firstValue = view(1), zeroValue = view(2), values = [firstValue, zeroValue]; let lists = 0, histories = 0, auto = 0;
  const data = await readWalletData({
    list: async ({ after }) => { lists++; return { participants: [summary(after ? zeroValue : firstValue)], next_after: after ? null : id(1) }; },
    get: async ({ participant_id }) => { const found = values.find(row => row.participant_id === participant_id); assert.ok(found); return found; },
  }, {
    list: async ({ after }) => { histories++; return after === "0" ? history([operation(9)], "9") : history([operation(10)]); },
    automation: async () => { auto++; return automation; },
  }, true);
  assert.equal(lists, 2); assert.equal(histories, 2); assert.equal(auto, 1);
  const [firstWallet, zeroWallet] = data.wallets; assert.ok(firstWallet && zeroWallet);
  const first = walletRow(firstWallet, data.operations), zero = walletRow(zeroWallet, data.operations);
  assert.equal(first.balance, "9007199254740993.123456789012345678"); assert.equal(first.frozen, "2.5"); assert.equal(zero.balance, "0");
  assert.equal(first.balanceReadAt, "2026-11-07T12:00:00.000002Z"); assert.match(first.readNote, /完整确认读数 #1/); assert.match(first.readNote, /最近尝试 #2/);
  assert.equal(first.recommendation, null); assert.equal(first.view.difference, null); assert.equal(first.lastApplied?.id, "10"); assert.equal(first.lastApplied?.outcome, "rejected");
  assert.equal(walletOperation(operation(10)).after, null); assert.match(walletOperation(operation(10)).note, /未向上游派发/);
});

test("a duplicate cursor, changed identity, repeated numeric history or later failure invalidates the complete wallet read", async () => {
  const value = view(), base = { list: async () => ({ participants: [summary(value)], next_after: null }), get: async () => value };
  const applications = { list: async () => history([]), automation: async () => automation };
  await assert.rejects(readWalletData({ ...base, list: async () => ({ participants: [summary(value)], next_after: id(1) }) }, applications, true), WalletReadError);
  await assert.rejects(readWalletData({ ...base, get: async () => ({ ...value, participant_revision: "2" }) }, applications, true), WalletReadError);
  await assert.rejects(readWalletData(base, { ...applications, list: async () => history([operation(9)], "9") }, true), WalletReadError);
  await assert.rejects(readWalletData({ ...base, get: async () => { throw new TypeError("later wallet unavailable"); } }, applications, true), /later wallet unavailable/);
  await assert.rejects(readWalletData(base, { ...applications, list: async () => ({ ...history([]), participant_id: id(2) }) }, true), WalletReadError);
});

test("balances-only ordinary reads request no account catalog or administrator automation and keep stripped history", async () => {
  const value = view(2, false); let auto = 0;
  const data = await readWalletData({ list: async () => ({ participants: [summary(value)], next_after: null }), get: async () => value },
    { list: async () => history([operation(9, 2, false)]), automation: async () => { auto++; throw new Error("administrator-only"); } }, false);
  assert.equal(auto, 0); assert.equal(data.automation, null); assert.equal(walletRow(value, data.operations).balance, "0");
  const [request] = data.operations; assert.ok(request);
  assert.equal(walletOperation(request).actor, "—"); assert.equal(walletOperation(request).rec, "—");
  assert.equal(JSON.stringify(data).includes(id(800)), false); assert.equal(data.wallets[0]?.latest_recommendation, null);
});

test("wallet read budgets reject a 501st participant and a 2001st operation without silently truncating", async () => {
  const value = view(), applications = { list: async () => history([]), automation: async () => automation };
  let offset = 0;
  await assert.rejects(readWalletData({ list: async () => { const rows = Array.from({ length: Math.min(100, 501 - offset) }, (_, index) => summary(view(offset + index + 1))); offset += rows.length; const last = rows.at(-1); assert.ok(last); return { participants: rows, next_after: offset === 501 ? null : last.participant_id }; }, get: async () => value }, applications, true), /500/);
  await assert.rejects(readWalletData({ list: async () => ({ participants: [], next_after: null }), get: async () => value },
    { ...applications, list: async ({ after }) => { const begin = Number(after), requests = Array.from({ length: Math.min(100, 2001 - begin) }, (_, index) => operation(begin + index + 1)); const last = requests.at(-1); assert.ok(last); return history(requests, begin + requests.length === 2001 ? null : last.id); } }, true), /2000/);
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
  for (const path of ["/api/v1/wallets/refresh", "/api/v1/wallets/recompute", "/api/v1/wallet-applications/request", "/api/v1/wallet-applications/automation/update"]) assert.equal((await proxyAPI(request(path), config, async () => { throw new Error("mutation must not dispatch"); })).status, 404);
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
