import assert from "node:assert/strict";
import { test } from "node:test";
import { notification, notificationConfiguration } from "@sub-pool/contracts/notifications";
import { channelDraft, channelSave, checkNotificationSave, checkTestReceipt, notificationFilter, notificationReason, notificationSaveBase, policySave, readNotificationDetail, readNotificationPages, recoverNotificationTest } from "./notifications-data";
import { proxyAPI } from "./api-proxy";

const id = (n: number) => { const bytes = Buffer.alloc(16); bytes.writeUInt32BE(n, 12); return bytes.toString("base64url"); };
function configuration() {
  return notificationConfiguration.parse({ revision: "8", enabled: false, financial_enabled: false, native_source_id: null, native_financial_enabled: false, channel: "smtp", from: "sender@owned.invalid", recipient: "admin@owned.invalid",
    smtp: { host: "smtp.owned.invalid", port: 465, tls: "tls", username: "mail" }, secret_set: true, secret_storage_available: true,
    preferences: { balance_low: true, recommendation_changed: false, estimate_changed: true, collection_failed: true }, dedupe_seconds: 61, saved_at: "2026-11-07T12:00:00Z",
    capabilities: ["balance_low", "recommendation_changed", "estimate_changed"].map(kind => ({ kind, available: false, effective: false, reason: "FINANCIAL_BASIS_UNAVAILABLE" })).concat([{ kind: "collection_failed", available: true, effective: false, reason: "CONFIRMED_COLLECTION_FAILURES_ONLY" }]),
  });
}

test("native policy explicitly activates the source and channel save carries no activation", () => {
  const saved = notificationConfiguration.parse({ ...configuration(), financial_enabled: true, native_source_id: id(12),
    capabilities: configuration().capabilities.map(item => item.kind === "collection_failed" ? item
      : { ...item, available: true, reason: "NATIVE_CONFIRMED_BASIS_ONLY" }) });
  assert.equal(channelSave(saved, channelDraft(saved)).activate_native_source_id, undefined);
  const input = policySave(saved, saved.preferences, "61", true);
  assert.equal(input.activate_native_source_id, id(12));
  const receipt = { ...saved, revision: "9", enabled: true, native_financial_enabled: true };
  assert.doesNotThrow(() => checkNotificationSave(receipt, input));
  assert.throws(() => checkNotificationSave({ ...receipt, native_source_id: id(13) }, input));
  assert.throws(() => checkNotificationSave({ ...receipt, native_financial_enabled: false }, input));
});
function row(n: number, admin = true) {
  return notification.parse({ id: String(n), kind: admin ? "test" : "collection_failed", subject_type: admin ? "system" : "account", subject_id: admin ? "test" : id(50), participant_id: null,
    created_at: "2026-11-07T12:00:01.000001Z", started_at: "2026-11-07T12:00:01.000002Z", finished_at: "2026-11-07T12:00:01.000003Z", state: "accepted", reason: admin ? "TRANSPORT_ACCEPTED" : "ACCEPTED", visibility: admin ? "administrator" : "subject_summary",
    details: admin ? { config_revision: "8", channel: "smtp", from: "sender@owned.invalid", recipient: "admin@owned.invalid", subject: "Owned test", body: "Owned test body", event_reason: "EXPLICIT_ADMINISTRATOR_TEST", event_key: "test:" + id(n), actor_id: id(90), provider_id: null } : null,
  });
}
const page = (notifications: ReturnType<typeof row>[], next_after: string | null = null) => ({ notifications, next_after });

test("notification reads consume numeric pages including 9/10 and enforce server filters without catalogs", async () => {
  const seen: string[] = [];
  const result = await readNotificationPages({ list: async input => {
    seen.push(input.after); assert.equal(input.kind, "test"); assert.equal(input.state, "accepted"); assert.equal(input.participant_id, "");
    return input.after === "0" ? page([row(9)], "9") : page([row(10)]);
  } }, true, { kind: "test", state: "accepted" });
  assert.deepEqual(seen, ["0", "9"]); assert.deepEqual(result.map(item => item.id), ["9", "10"]);
  const ordinary = await readNotificationPages({ list: async () => page([row(1, false)]) }, false);
  assert.equal(ordinary[0]?.details, null); assert.ok(!JSON.stringify(ordinary).includes("admin@owned.invalid"));
});

test("notification partial failures, repeated cursors, role/filter mismatches and read bounds expose no partial result", async () => {
  await assert.rejects(readNotificationPages({ list: async input => { if (input.after !== "0") throw new Error("later denied"); return page([row(1)], "1"); } }, true));
  for (const value of [page([row(1)], "2"), page([row(1)], "1"), page([row(1, false)])]) {
    await assert.rejects(readNotificationPages({ list: async () => value }, true));
  }
  await assert.rejects(readNotificationPages({ list: async () => page([row(1)]) }, true, { kind: "collection_failed", state: "" }));
  await assert.rejects(readNotificationPages({ list: async () => page([row(1)]) }, true, { kind: "", state: "unknown" }));
  await assert.rejects(readNotificationPages({ list: async input => {
    const n = Number(input.after); return page(Array.from({ length: 100 }, (_, i) => row(n + i + 1)), String(n + 100));
  } }, true), /2000/);
});

test("channel saves select TLS, explicitly clear credentials and retain hidden Resend SMTP fields", () => {
  const saved = configuration(), draft = channelDraft(saved);
  assert.equal(draft.credential, "");
  const input = channelSave(saved, { ...draft, recipient: "changed@owned.invalid" });
  assert.equal(input.smtp.tls, "tls"); assert.equal(input.enabled, false); assert.equal(input.dedupe_seconds, 61);
  assert.deepEqual(input.preferences, saved.preferences); assert.equal(input.credential_action, "keep"); assert.equal(input.credential, "");
  const replacement = channelSave(saved, { ...draft, credential: "new owned secret" }); assert.equal(replacement.credential_action, "replace");
  const clear = channelSave(saved, { ...draft, tls: "starttls", clear_credential: true, credential: "must not send" });
  assert.equal(clear.smtp.tls, "starttls"); assert.equal(clear.credential_action, "clear"); assert.equal(clear.credential, "");
  assert.equal(channelDraft(saved).clear_credential, false);
  const resend = channelSave(saved, { ...draft, channel: "resend", port: "", host: "unsaved", tls: "loopback_plain", credential: "re_owned" }); assert.deepEqual(resend.smtp, saved.smtp);
  const policy = policySave(saved, { ...saved.preferences, collection_failed: false }, "61", false);
  assert.deepEqual(policy.smtp, saved.smtp); assert.equal(policy.channel, "smtp"); assert.equal(policy.credential, ""); assert.equal(policy.dedupe_seconds, 61);
  for (const port of ["", "0", "-1", "65536", "465.5", "4e2"]) assert.throws(() => channelSave(saved, { ...draft, port }));
  for (const seconds of ["", "0", "59", "86401", "61.5", "1e3"]) assert.throws(() => policySave(saved, saved.preferences, seconds, false));
});

test("configuration acknowledgements require public input equality and exact next revision", () => {
  const saved = configuration(), input = notificationSaveBase(saved);
  const receipt = { ...saved, revision: "9" };
  checkNotificationSave(receipt, input);
  for (const value of [saved, { ...receipt, recipient: "other@owned.invalid" }, { ...receipt, smtp: { ...saved.smtp, tls: "starttls" as const } }, { ...receipt, dedupe_seconds: 60 }]) assert.throws(() => checkNotificationSave(value, input));
  assert.throws(() => checkNotificationSave({ ...receipt, secret_set: false }, { ...input, credential_action: "replace", credential: "new secret" }));
  assert.throws(() => checkNotificationSave(receipt, { ...input, credential_action: "clear" }));
  checkNotificationSave({ ...receipt, secret_set: false }, { ...input, credential_action: "clear" });
});

test("a changed credential target requires deliberate replacement or clearing before dispatch", () => {
  const saved = configuration(), draft = channelDraft(saved);
  for (const change of [{ tls: "starttls" as const }, { host: "other.owned.invalid" }, { port: "587" }, { username: "other" }, { channel: "resend" as const }]) {
    assert.throws(() => channelSave(saved, { ...draft, ...change }), /重新输入凭据或明确选择清除/);
    assert.equal(channelSave(saved, { ...draft, ...change, credential: "owned-replacement" }).credential_action, "replace");
    assert.equal(channelSave(saved, { ...draft, ...change, clear_credential: true }).credential_action, "clear");
  }
  assert.equal(channelSave({ ...saved, secret_set: false }, { ...draft, tls: "starttls" }).smtp.tls, "starttls");
  assert.equal(channelSave(saved, { ...draft, from: "changed@owned.invalid" }).credential_action, "keep");
});

test("policy save uses the explicit global switch independently of selected event preferences", () => {
  const saved = configuration();
  const enabled = policySave(saved, saved.preferences, "61", true);
  assert.equal(enabled.enabled, true);
  assert.deepEqual(enabled, { ...notificationSaveBase(saved), enabled: true });
  assert.equal(saved.enabled, false);
  assert.equal(channelSave(saved, channelDraft(saved)).enabled, false);
  const unavailableOnly = { ...saved.preferences, collection_failed: false };
  assert.equal(policySave(saved, unavailableOnly, "61", false).enabled, false);
  assert.equal(policySave({ ...saved, enabled: true }, saved.preferences, "61", false).enabled, false);
  assert.equal(policySave(saved, saved.preferences, "61", false).preferences.collection_failed, true);
  const alreadyEnabled = policySave({ ...saved, enabled: true }, unavailableOnly, "61", true);
  assert.equal(alreadyEnabled.enabled, true);
  assert.equal(alreadyEnabled.preferences.collection_failed, false);
  assert.throws(() => checkNotificationSave({ ...saved, revision: "9" }, enabled));
});

test("only explicit supported policy save activates saved financial preferences and binds its receipt", () => {
  const prior = configuration();
  const saved = notificationConfiguration.parse({ ...prior, capabilities: prior.capabilities.map(item => item.kind === "collection_failed" ? item : { ...item, available: true, reason: "SIMULATED_COMPLETE_BASIS_ONLY" }) });
  assert.equal(channelSave(saved, channelDraft(saved)).financial_enabled, false);
  assert.equal(policySave(saved, saved.preferences, "61", false).financial_enabled, false);
  assert.equal(policySave(prior, prior.preferences, "61", true).financial_enabled, false);
  const activated = policySave(saved, saved.preferences, "61", true);
  assert.equal(activated.financial_enabled, true);
  const receipt = { ...saved, revision: "9", enabled: true, financial_enabled: true };
  checkNotificationSave(receipt, activated);
  assert.throws(() => checkNotificationSave({ ...receipt, financial_enabled: false }, activated));
  assert.equal(channelSave(receipt, channelDraft(receipt)).financial_enabled, true);
});

test("notification filters preserve nanoseconds, exact 31-day half-open bounds and all server selectors", async () => {
  const item = { ...row(9), participant_id: id(51) };
  const filter = notificationFilter({ start_at: "2026-11-07T12:00:01.000001000Z", end_at: "2026-11-07T12:00:01.000001001Z", participant_id: id(51), subject_type: "system", subject_id: "test", kind: "test", state: "accepted" });
  const calls: string[] = [];
  const result = await readNotificationPages({ list: async input => { calls.push(input.after); assert.deepEqual(input, { ...filter, after: input.after, limit: 100 }); return input.after === "0" ? page([item], "9") : page([{ ...item, id: "10" }]); } }, true, filter);
  assert.deepEqual(calls, ["0", "9"]); assert.equal(result.length, 2);
  assert.doesNotThrow(() => notificationFilter({ start_at: "2026-11-01T00:00:00.000000001Z", end_at: "2026-12-02T00:00:00.000000001Z" }));
  await assert.rejects(readNotificationPages({ list: async () => page([row(9)]) }, true, { start_at: "2026-11-07T12:00:00Z", end_at: row(9).created_at }));
});

test("invalid notification calendar, range and object filters dispatch zero requests", async () => {
  let calls = 0;
  const list = async () => { calls++; return page([]); };
  for (const filter of [
    { start_at: "2026-11-01T00:00:00Z" }, { end_at: "2026-11-02T00:00:00Z" },
    { start_at: "2026-02-30T00:00:00Z", end_at: "2026-03-03T00:00:00Z" },
    { start_at: "2026-11-01T00:00:00Z", end_at: "2026-12-02T00:00:00.000000001Z" },
    { start_at: "2026-11-01T00:00:00Z", end_at: "2026-11-01T00:00:00Z" },
    { start_at: "2026-11-01T00:00:00+00:00", end_at: "2026-11-02T00:00:00Z" },
    { subject_id: id(5) }, { subject_type: "account" as const, subject_id: "test" }, { participant_id: "5" },
  ]) await assert.rejects(readNotificationPages({ list }, true, filter), /尚未发出读取请求/);
  assert.equal(calls, 0);
});

test("valid but mismatched participant, subject and time rows reject the entire notification traversal", async () => {
  for (const filter of [{ participant_id: id(51) }, { subject_type: "account" as const }, { subject_type: "system" as const, subject_id: id(51) },
    { start_at: "2026-11-07T12:00:01.000001001Z", end_at: "2026-11-07T12:01:00Z" }]) {
    await assert.rejects(readNotificationPages({ list: async () => page([row(9)]) }, true, filter));
  }
  await assert.rejects(readNotificationPages({ list: async input => input.after === "0" ? page([{ ...row(9), participant_id: id(51) }], "9") : page([row(10)]) }, true, { participant_id: id(51) }));
});

test("notification detail binds immutable identity and mail snapshot while allowing terminal progress", async () => {
  const selected = row(9), ordinary = row(9, false);
  const queued = notification.parse({ ...selected, state: "queued", reason: "QUEUED", started_at: null, finished_at: null });
  assert.equal((await readNotificationDetail(queued, true, async () => selected)).state, "accepted");
  assert.equal((await readNotificationDetail(ordinary, false, async () => ordinary)).details, null);
  for (const value of [row(10), { ...selected, participant_id: id(51) }, { ...selected, created_at: "2026-11-07T12:00:01.000000Z" },
    { ...selected, details: { ...selected.details, body: "FOREIGN_SECRET_SENTINEL" } }, ordinary]) {
    await assert.rejects(readNotificationDetail(selected, true, async () => notification.parse(value)), /未显示内容/);
  }
  await assert.rejects(readNotificationDetail(ordinary, false, async () => selected), /未显示内容/);
});

test("uncertain test recovery is read-only and bound to original actor/key/revision/id", async () => {
  const intent = { request_id: id(10), revision: "8", actor_id: id(90) }, receipt = row(10), cursors: string[] = [];
  const recovered = await recoverNotificationTest({ list: async input => { cursors.push(input.after); return input.after === "0" ? page([row(9)], "9") : page([receipt]); }, get: async () => { throw new Error("ID not known yet"); } }, intent);
  assert.equal(recovered?.id, "10"); assert.deepEqual(cursors, ["0", "9"]);
  assert.equal(await recoverNotificationTest({ list: async () => page([]), get: async () => receipt }, intent), null);
  const known = await recoverNotificationTest({ list: async () => { throw new Error("known ID must get"); }, get: async input => { assert.equal(input.id, "10"); return receipt; } }, { ...intent, id: "10" });
  assert.equal(known?.state, "accepted"); assert.match(notificationReason(receipt), /不代表已送达或已读/);
  for (const change of [{ actor_id: id(91) }, { revision: "9" }, { request_id: id(11) }, { id: "9" }]) assert.throws(() => checkTestReceipt(receipt, { ...intent, ...change }));
});

const config = { publicOrigin: "http://127.0.0.1:3000", backendOrigin: "http://127.0.0.1:8080", allowLoopbackHTTP: true };
const request = (path: string, method = "POST", body = "{}", origin = config.publicOrigin) => new Request(config.publicOrigin + "/api/v1/notifications/" + path, { method, body: method === "GET" ? undefined : body, headers: { Origin: origin, "Content-Type": "application/json", Cookie: "sub_pool_dev_session=" + "A".repeat(43) + "; unrelated=private", Authorization: "private", "x-user-role": "admin", "x-forwarded-for": "198.51.100.1" } });

test("all five notification proxy paths retain fixed destinations, cookie isolation, strict outputs and no-store", async () => {
  for (const [path, value] of [["config", configuration()], ["config/update", configuration()], ["test", row(1)], ["get", row(1)], ["list", page([row(1)])]] as const) {
    let calls = 0;
    const result = await proxyAPI(request(path), config, async (url, init) => {
      calls++; assert.equal(String(url), config.backendOrigin + "/api/v1/notifications/" + path); assert.equal(init?.method, "POST");
      const headers = new Headers(init?.headers); assert.equal(headers.get("cookie"), "sub_pool_dev_session=" + "A".repeat(43));
      for (const key of ["authorization", "x-user-role", "x-forwarded-for"]) assert.equal(headers.get(key), null);
      assert.equal(init?.cache, "no-store"); assert.equal(init?.redirect, "error"); return Response.json(value);
    });
    assert.equal(result.status, 200, path); assert.equal(calls, 1); assert.equal(result.headers.get("cache-control"), "no-store");
    assert.equal((await proxyAPI(request(path), config, async () => Response.json({ ...value, secret: "PRIVATE_SENTINEL" }))).status, 502);
  }
});

test("notification proxy preserves defined errors and never retries uncertain mutations", async () => {
  for (const [code, status] of [["SOURCE_BLOCKED", 403], ["FORBIDDEN", 403], ["NOT_FOUND", 404], ["CONFLICT", 409], ["SECRET_STORAGE_UNAVAILABLE", 503]] as const) {
    let calls = 0;
    const result = await proxyAPI(request("config/update"), config, async () => { calls++; return Response.json({ defined: true, code, status, message: "PRIVATE_SENTINEL", data: { retry_at: null } }, { status }); });
    assert.equal(result.status, status); assert.equal(calls, 1); assert.ok(!(await result.text()).includes("PRIVATE_SENTINEL"));
  }
  for (const path of ["config/update", "test"]) {
    let calls = 0; const result = await proxyAPI(request(path), config, async () => { calls++; throw new Error("PRIVATE_SENTINEL"); });
    assert.equal(result.status, 502); assert.equal(calls, 1); assert.ok(!(await result.text()).includes("PRIVATE_SENTINEL"));
  }
});

test("notification proxy rejects undeclared paths, method/origin/query/body and response violations", async () => {
  let calls = 0; const fetcher: typeof fetch = async () => { calls++; return Response.json(page([])); };
  for (const [path, method, body, origin, status] of [["retry", "POST", "{}", config.publicOrigin, 404], ["list", "GET", "", config.publicOrigin, 405], ["config", "POST", "{}", "https://other.invalid", 403], ["list?secret=private", "POST", "{}", config.publicOrigin, 400], ["config/update", "POST", " ".repeat(65537), config.publicOrigin, 400]] as const) {
    assert.equal((await proxyAPI(request(path, method, body, origin), config, fetcher)).status, status);
  }
  assert.equal(calls, 0);
  const oversized = await proxyAPI(request("list"), config, async () => new Response(" ".repeat(1024 * 1024 + 1))); assert.equal(oversized.status, 502);
  const cookie = await proxyAPI(request("get"), config, async () => Response.json(row(1), { headers: { "Set-Cookie": "sub_pool_dev_session=" + "A".repeat(43) + "; Path=/; HttpOnly; SameSite=Lax" } })); assert.equal(cookie.status, 502);
});
