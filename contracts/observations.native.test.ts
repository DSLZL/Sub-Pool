import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { setTimeout } from "node:timers/promises";
import { ORPCError } from "@orpc/client";
import { createAccountsClient, createAuthClient, createConnectionClient, createMonitoringClient, createObservationsClient, createUsersClient } from "./client";

test("real Go PostgreSQL observation decisions and verified native range through oRPC", {
  skip: !process.env.SUB_POOL_TEST_OBSERVATIONS_ORIGIN, timeout: 120000,
}, async () => {
  const origin = process.env.SUB_POOL_TEST_OBSERVATIONS_ORIGIN;
  const username = process.env.SUB_POOL_TEST_OBSERVATIONS_USERNAME;
  const password = process.env.SUB_POOL_TEST_OBSERVATIONS_PASSWORD;
  assert.ok(origin && username && password, "explicit disposable observation fixture required");
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
    return { auth: createAuthClient(publicOrigin, fetcher), connection: createConnectionClient(publicOrigin, fetcher), accounts: createAccountsClient(publicOrigin, fetcher),
      monitoring: createMonitoringClient(publicOrigin, fetcher), observations: createObservationsClient(publicOrigin, fetcher), users: createUsersClient(publicOrigin, fetcher) };
  }
  const admin = browser(), reader = browser();
  await admin.auth.login({ username, password });
  let calls = 0, unexpected = 0;
  const secret = "isolated-observations-management-key", reset = new Date(Date.now() + 3600000).toISOString();
  const upstream = createServer((request, response) => {
    calls++;
    if (request.method !== "GET" || request.headers["x-api-key"] !== secret || request.headers.cookie || request.url !== "/api/v1/admin/accounts/1") {
      unexpected++; response.writeHead(400).end("unexpected fixture request"); return;
    }
    response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ code: 0, data: {
      id: 1, name: "Native observation account", platform: "openai", type: "oauth", status: "active", schedulable: true,
      created_at: "2020-01-01T00:00:00Z", updated_at: "2020-01-01T00:00:00Z", error_message: "",
      credentials: { chatgpt_account_id: "private-observed-resource", access_token: "never expose" },
      // The cached source predates receipt across independent process clocks.
      extra: { codex_usage_updated_at: new Date(Date.now() - 1000).toISOString(), codex_5h_used_percent: 0, codex_5h_window_minutes: 180, codex_5h_reset_at: reset, codex_7d_used_percent: 42 },
    } }));
  });
  upstream.listen(0, "127.0.0.1");
  await once(upstream, "listening");
  const address = upstream.address();
  assert.ok(address && typeof address === "object");
  try {
    const initial = await admin.connection.get({});
    const saved = await admin.connection.save({ revision: initial.revision, base_url: "http://127.0.0.1:" + address.port, credential_action: "replace", credential: secret, confirmation: "" });
    assert.ok(saved.configuration.source_id);
    const binding = { source_id: saved.configuration.source_id, connection_revision: saved.configuration.revision };
    const adopted = await admin.accounts.adopt({ ...binding, upstream_id: "1", name: "Observed locally", monitoring: true, source_mode: "passive" });
    assert.ok(adopted.account);
    const accountID = adopted.account.id;
    async function collect() {
      const run = await admin.monitoring.collect({ ...binding, all: false, account_ids: [accountID] });
      for (const until = Date.now() + 15000; Date.now() < until;) {
        const page = await admin.monitoring.run({ id: run.id, after: "0", limit: 100 });
        if (page.run.state === "completed") {
          const job = page.jobs[0];
          assert.ok(job);
          assert.equal(job.state, "succeeded");
          assert.equal(page.jobs.length, 1);
          return job;
        }
        await setTimeout(100);
      }
      throw new Error("owned native collector did not complete");
    }
    const firstJob = await collect();
    assert.ok(firstJob.retry_after);
    const retryAt = Date.parse(firstJob.retry_after);
    assert.ok(Number.isFinite(retryAt) && retryAt - Date.now() < 65000, "bounded real collector cooldown required");
    await setTimeout(Math.max(0, retryAt - Date.now()) + 50);
    await collect();
    const query = { account_id: accountID, source_id: binding.source_id, source_mode: "passive" as const, validity: "all" as const,
      start_at: new Date(Date.now() - 86400000).toISOString(), end_at: new Date(Date.now() + 86400000).toISOString(), after: "0", limit: 100 };
    const facts = await admin.observations.list(query);
    const [first, last] = facts.records;
    assert.ok(first && last && facts.records.length === 2);
    assert.notEqual(first.id, last.id);
    assert.equal(first.snapshot.windows[0]?.duration_seconds, "10800");
    assert.equal(first.snapshot.windows[0]?.used_percent, "0");
    const recordedSnapshots = facts.records.map(item => item.snapshot);
    const before = calls;
    const observer = await admin.users.create({ username: "observation-reader", password: "ReaderPassword123", enabled: true, features: ["observations"], account_ids: [accountID], participant_ids: [] });
    await reader.auth.login({ username: observer.username, password: "ReaderPassword123" });
    const initialState = await reader.observations.state({ account_id: accountID });
    assert.equal(initialState.revision, "1");
    const review = { account_id: accountID, revision: "1", observation_id: first.id, excluded: true, reason: "异常读数保留原始事实" };
    const excluded = await admin.observations.review(review);
    assert.equal(excluded.excluded, true);
    assert.equal(excluded.management_revision, "2");
    assert.deepEqual(excluded.snapshot, first.snapshot);
    assert.equal((await reader.observations.list({ ...query, validity: "excluded" })).records.length, 1);
    await assert.rejects(reader.observations.review({ ...review, revision: "2" }), error => error instanceof ORPCError && error.status === 403);
    const start = { account_id: accountID, revision: "2", start_observation_id: first.id, end_observation_id: last.id, window_kind: "short" as const, reason: "Two actual native observations" };
    await assert.rejects(admin.observations.start(start), error => error instanceof ORPCError && error.code === "OBSERVATION_RANGE_UNVERIFIED");
    const restored = await admin.observations.review({ ...review, revision: "2", excluded: false, reason: "核实后恢复" });
    assert.equal(restored.management_revision, "3");
    const range = await admin.observations.start({ ...start, revision: "3" });
    assert.equal(range.revision, "4");
    assert.equal(range.manual_start?.usable, true);
    assert.equal(range.manual_start?.period?.duration_seconds, "10800");
    assert.equal(range.recalculation_state, "not_configured");
    await assert.rejects(admin.observations.review({ ...review, revision: "3" }), error => error instanceof ORPCError && error.status === 409);
    const revoked = await admin.observations.revoke({ account_id: accountID, revision: "4", reason: "撤销本地人工起点" });
    assert.equal(revoked.revision, "5");
    assert.equal(revoked.manual_start, null);
    const history = await reader.observations.events({ account_id: accountID, after: "0", limit: 100 });
    assert.deepEqual(history.events.map(event => event.action), ["exclude", "restore", "start", "revoke"]);
    assert.ok(history.events.every(event => event.actor_id === null));
    const detail = await reader.observations.get({ id: first.id });
    assert.equal(detail.basis.usage_state, "unavailable");
    assert.equal(detail.basis.estimate_state, "unavailable");
    assert.deepEqual((await admin.observations.list(query)).records.map(item => item.snapshot), recordedSnapshots);
    await admin.users.update({ id: observer.id, revision: observer.revision, username: observer.username, enabled: true, new_password: "", features: ["observations"], account_ids: [], participant_ids: [] });
    for (const id of [first.id, "9223372036854775807"]) {
      await assert.rejects(reader.observations.get({ id }), error => error instanceof ORPCError && error.status === 404);
    }
    await reader.auth.logout({});
    await assert.rejects(reader.observations.state({ account_id: accountID }), error => error instanceof ORPCError && error.status === 401);
    assert.equal(calls, before);
    assert.equal(calls, 3);
    assert.equal(unexpected, 0);
  } finally {
    upstream.closeAllConnections();
    await new Promise<void>(resolve => upstream.close(() => resolve()));
  }
});
