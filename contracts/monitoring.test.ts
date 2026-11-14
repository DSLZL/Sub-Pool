import assert from "node:assert/strict";
import { test } from "node:test";
import { once } from "node:events";
import { createServer } from "node:http";
import { setTimeout } from "node:timers/promises";
import { ORPCError } from "@orpc/client";
import { accountObservation, collectionJob, collectionRun, monitoringConfig, monitoringOutputs, quotaSnapshot } from "./monitoring";
import { createAccountsClient, createAuthClient, createConnectionClient, createMonitoringClient, createUsersClient } from "./client";

const id = "a".repeat(21) + "A";
const timestamp = "2026-11-06T01:00:00Z";
const snapshot = { source_id: id, connection_revision: "1", account_revision: "1", source_mode: "passive", source_at: null,
  received_at: timestamp, windows: [{ kind: "short", duration_seconds: null, used_percent: "0", reset_at: null, reset_expired: false }] };
const counts = { queued: "0", running: "0", succeeded: "0", failed: "0", skipped: "0", conflict: "0", interrupted: "0" };

test("monitoring schemas preserve missing time, exact percentages and partial persisted outcomes", () => {
  assert.ok(quotaSnapshot.safeParse(snapshot).success);
  assert.equal(quotaSnapshot.safeParse({ ...snapshot, source_mode: "active" }).success, false);
  assert.equal(quotaSnapshot.safeParse({ ...snapshot, windows: [{ ...snapshot.windows[0], used_percent: 0 }] }).success, false);
  assert.equal(quotaSnapshot.safeParse({ ...snapshot, windows: [{ ...snapshot.windows[0], used_percent: "1e999999" }] }).success, false);
  assert.equal(quotaSnapshot.safeParse({ ...snapshot, credentials: "secret" }).success, false);
  assert.ok(quotaSnapshot.safeParse({ ...snapshot, windows: [{ ...snapshot.windows[0], used_percent: "99.123456789012345678" }] }).success);
  assert.ok(collectionRun.safeParse({ id: "1", reason: "manual", actor_id: id, created_at: timestamp, state: "completed",
    counts: { ...counts, succeeded: "1", failed: "1", skipped: "1" } }).success);
  assert.equal(collectionRun.safeParse({ id: "1", reason: "manual", actor_id: id, created_at: timestamp, state: "completed", counts: { ...counts, queued: "1" } }).success, false);
  assert.equal(collectionJob.safeParse({ id: "1", run_id: "1", account_id: id, source_id: id, connection_revision: "1", account_revision: "1",
    policy_revision: "1", source_mode: "passive", state: "succeeded", reason: "VERIFIED_PASSIVE_SNAPSHOT", queued_at: timestamp,
    started_at: timestamp, finished_at: null, retry_after: null, read: null }).success, false);
  const observation = { account_id: id, requested_mode: "passive", snapshot, latest_attempt: null, freshness: "unknown", stale_warning: true, next_scheduled_at: null };
  assert.ok(accountObservation.safeParse(observation).success);
  assert.equal(accountObservation.safeParse({ ...observation, freshness: "missing" }).success, false);
  assert.equal(monitoringConfig.safeParse({ revision: "1", enabled: false, interval_seconds: 300, stale_after_seconds: 60, refresh_on_change: true,
    near_reset_enabled: false, near_reset_lead_seconds: 600, near_reset_interval_seconds: 60, stale_warnings_enabled: true, saved_at: null }).success, false);
});

test("monitoring client rejects forged inputs/results and never replays a collection request", async () => {
  let calls = 0;
  const client = createMonitoringClient("https://example.test", async () => { calls++; throw new TypeError("connection lost after dispatch"); });
  await assert.rejects(client.collect({ source_id: id, connection_revision: "1", all: false, account_ids: [id] }));
  assert.equal(calls, 1);
  await assert.rejects(client.collect({ source_id: id, connection_revision: "1", all: true, account_ids: [id] }));
  assert.equal(calls, 1);
  const invalid = createMonitoringClient("https://example.test", async () => Response.json({ status: "ok", scope: "all_business" }));
  await assert.rejects(invalid.health({}));
  const blocked = createMonitoringClient("https://example.test", async () => Response.json({ defined: true, code: "SOURCE_BLOCKED", status: 403,
    message: "Request could not be completed.", data: { retry_at: null } }, { status: 403 }));
  await assert.rejects(blocked.account({ id }), error => error instanceof ORPCError && error.defined && error.status === 403);
});

test("monitoring status keeps historical success distinct from latest failure and planned time", async () => {
  const status = { enabled: true, worker_seen_at: null, worker_recent: false, next_scheduled_at: timestamp,
    last_success_at: timestamp, latest_run: { id: "10", reason: "scheduled", actor_id: null,
      created_at: timestamp, state: "completed", counts: { ...counts, failed: "1" } },
    active_available: false, active_reason: "ACTIVE_QUERY_SIDE_EFFECT" };
  const client = createMonitoringClient("https://example.test", async () => Response.json(status));
  assert.deepEqual(await client.status({}), status);
  assert.ok(monitoringOutputs.status.safeParse({ ...status, enabled: false, next_scheduled_at: null }).success);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, enabled: false }).success, false);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, worker_recent: true }).success, false);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, last_success_at: "yesterday" }).success, false);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, latest_run: { ...status.latest_run, id: "9", state: "running" } }).success, false);
  const oldStatus = { ...status, next_scheduled_at: undefined };
  assert.equal(monitoringOutputs.status.safeParse(oldStatus).success, false);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, secret: "private" }).success, false);
  const malformed = createMonitoringClient("https://example.test", async () => Response.json(oldStatus));
  await assert.rejects(malformed.status({}));
});

test("real Go PostgreSQL background collection and scoped observations through oRPC", {
  skip: !process.env.SUB_POOL_TEST_MONITORING_ORIGIN, timeout: 30000,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_MONITORING_ORIGIN;
  const username = process.env.SUB_POOL_TEST_MONITORING_USERNAME;
  const password = process.env.SUB_POOL_TEST_MONITORING_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable monitoring fixture required");
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  const publicOrigin = origin;
  function browser() {
    let cookie = "";
    const fetcher: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      request.headers.set("Origin", publicOrigin);
      if (cookie) request.headers.set("Cookie", cookie);
      const response = await fetch(request, { redirect: "error" });
      const issued = response.headers.get("set-cookie");
      if (issued) cookie = issued.split(";")[0] ?? "";
      return response;
    };
    return { auth: createAuthClient(publicOrigin, fetcher), users: createUsersClient(publicOrigin, fetcher),
      connection: createConnectionClient(publicOrigin, fetcher), accounts: createAccountsClient(publicOrigin, fetcher), monitoring: createMonitoringClient(publicOrigin, fetcher) };
  }
  const admin = browser(), reader = browser();
  assert.deepEqual(await admin.monitoring.health({}), { status: "ok", scope: "process" });
  await assert.rejects(admin.monitoring.config({}), error => error instanceof ORPCError && error.status === 401);
  await admin.auth.login({ username, password });
  const policy = await admin.monitoring.config({});
  assert.equal(policy.enabled, false);
  const initial = await admin.monitoring.status({});
  assert.equal(initial.active_available, false);
  assert.equal(initial.active_reason, "ACTIVE_QUERY_SIDE_EFFECT");
  let failSecond = false, calls = 0, unexpected = 0;
  const secret = "isolated-monitoring-management-key";
  const upstream = createServer((request, response) => {
    calls++;
    const url = new URL(request.url ?? "/", "http://127.0.0.1");
    if (request.method !== "GET" || request.headers["x-api-key"] !== secret || request.headers.cookie || !/^\/api\/v1\/admin\/accounts\/[123]$/.test(url.pathname)) {
      unexpected++; response.writeHead(400).end("private unexpected endpoint"); return;
    }
    const accountID = Number(url.pathname.split("/").at(-1));
    if (failSecond && accountID === 2) { response.writeHead(403).end("private reflected error and " + secret); return; }
    response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data: { id: accountID, name: "Fixture " + accountID, platform: "openai", type: "oauth", status: "active", schedulable: true,
      created_at: timestamp, updated_at: timestamp, error_message: "", credentials: { chatgpt_account_id: "private-resource-" + accountID, access_token: "private-account-token" },
      extra: { codex_usage_updated_at: new Date().toISOString(), codex_5h_used_percent: 0, codex_5h_window_minutes: 180,
        codex_5h_reset_at: new Date(Date.now() + 3600000).toISOString(), codex_7d_used_percent: 42 } } }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  assert.ok(address && typeof address === "object");
  const upstreamOrigin = "http://127.0.0.1:" + address.port;
  try {
    const before = await admin.connection.get({});
    const saved = await admin.connection.save({ revision: before.revision, base_url: upstreamOrigin, credential_action: "replace", credential: secret, confirmation: "" });
    assert.ok(saved.configuration.source_id);
    const binding = { source_id: saved.configuration.source_id, connection_revision: saved.configuration.revision };
    const managed = [];
    for (const upstreamID of [1, 2, 3]) {
      const adopted = await admin.accounts.adopt({ ...binding, upstream_id: String(upstreamID), name: "Local " + upstreamID,
        monitoring: true, source_mode: upstreamID === 3 ? "active" : "passive" });
      assert.ok(adopted.account); managed.push(adopted.account);
    }
    const [first, second, third] = managed;
    assert.ok(first && second && third);
    assert.equal(calls, 3);
    failSecond = true;
    const queued = await admin.monitoring.collect({ ...binding, all: true, account_ids: [] });
    assert.equal(queued.counts.queued, "2");
    assert.equal(queued.counts.skipped, "1");
    let run = await admin.monitoring.run({ id: queued.id, after: "0", limit: 1 });
    const deadline = Date.now() + 10000;
    while (run.run.state !== "completed") {
      assert.ok(Date.now() < deadline, "durable background collection stalled");
      await setTimeout(25); run = await admin.monitoring.run({ id: queued.id, after: "0", limit: 1 });
    }
    assert.equal(run.run.counts.succeeded, "1"); assert.equal(run.run.counts.failed, "1"); assert.equal(run.run.counts.skipped, "1");
    assert.ok(run.next_after);
    const rest = await admin.monitoring.run({ id: queued.id, after: run.next_after, limit: 100 });
    assert.equal(rest.jobs.length, 2);
    assert.equal(calls, 5); assert.equal(unexpected, 0);
    const good = await admin.monitoring.account({ id: first.id });
    assert.equal(good.freshness, "fresh"); assert.equal(good.snapshot?.windows[0]?.used_percent, "0"); assert.equal(good.snapshot?.windows[0]?.duration_seconds, "10800");
    const bad = await admin.monitoring.account({ id: second.id });
    assert.equal(bad.freshness, "missing"); assert.equal(bad.latest_attempt?.read?.reason, "FORBIDDEN");
    const active = await admin.monitoring.account({ id: third.id });
    assert.equal(active.latest_attempt?.reason, "ACTIVE_QUERY_SIDE_EFFECT"); assert.equal(active.snapshot, null);
    const repeated = await admin.monitoring.collect({ ...binding, all: false, account_ids: [first.id] });
    assert.equal(repeated.counts.skipped, "1"); assert.equal(calls, 5);
    const lists = await admin.monitoring.runs({ after: "0", limit: 1 }); assert.equal(lists.runs.length, 1); assert.ok(lists.next_after);
    let user = await admin.users.create({ username: "monitor." + crypto.randomUUID().slice(0, 10), password: "ReaderPassword123", enabled: true,
      features: ["accounts"], account_ids: [first.id], participant_ids: [] });
    await reader.auth.login({ username: user.username, password: "ReaderPassword123" });
    assert.equal((await reader.monitoring.account({ id: first.id })).freshness, "fresh");
    await assert.rejects(reader.monitoring.account({ id: second.id }), error => error instanceof ORPCError && error.status === 404);
    await assert.rejects(reader.monitoring.run({ id: queued.id, after: "0", limit: 10 }), error => error instanceof ORPCError && error.status === 403);
    await assert.rejects(reader.monitoring.collect({ ...binding, all: false, account_ids: [first.id] }), error => error instanceof ORPCError && error.status === 403);
    user = await admin.users.update({ id: user.id, revision: user.revision, username: user.username, enabled: true, features: [],
      account_ids: user.account_ids, participant_ids: [], new_password: "" });
    await assert.rejects(reader.monitoring.account({ id: first.id }), error => error instanceof ORPCError && error.status === 403);
    const safe = JSON.stringify([good, bad, active, run, rest, lists]);
    assert.doesNotMatch(safe, /private-|isolated-monitoring-management-key|actor_digest|access_token|password/);
    assert.equal((await admin.monitoring.status({})).worker_recent, true);
    await admin.auth.logout({});
  } finally { upstream.close(); upstream.closeAllConnections(); }
});

test("active snapshots require strict simulated provenance and cannot qualify native active mode", () => {
  const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id };
  const active = { ...snapshot, source_mode: "active", simulation };
  assert.ok(quotaSnapshot.safeParse(active).success);
  for (const change of [undefined, null, { ...simulation, environment: "native" }, { ...simulation, epoch: "invalid" },
    { ...simulation, protocol: "future" }, { ...simulation, secret: "private" }]) {
    assert.equal(quotaSnapshot.safeParse({ ...active, simulation: change }).success, false);
  }
  assert.equal(quotaSnapshot.safeParse({ ...snapshot, simulation }).success, false);
  const status = { enabled: false, worker_seen_at: null, worker_recent: false, next_scheduled_at: null,
    last_success_at: null, latest_run: null, active_available: true, active_reason: "SIMULATED_PROTOCOL_ONLY" };
  assert.ok(monitoringOutputs.status.safeParse(status).success);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, active_reason: "ACTIVE_QUERY_SIDE_EFFECT" }).success, false);
  assert.equal(monitoringOutputs.status.safeParse({ ...status, active_available: false }).success, false);
});
