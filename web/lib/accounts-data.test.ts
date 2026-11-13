import assert from "node:assert/strict";
import { test } from "node:test";
import type { ManagedAccount, AccountModelConfiguration } from "@sub-pool/contracts/accounts";
import type { AccountObservation, CollectionJob, CollectionRun } from "@sub-pool/contracts/monitoring";
import { accountReadInput, bindAccountRefresh, bindObservation, compactTime, jobNote, modelRows, quotaWindow, readRunHistory, readRunJobs, windowTitle } from "./accounts-data";

const id = "a".repeat(21) + "A", other = "b".repeat(21) + "A", stamp = "2026-11-06T01:02:03Z";
const read = { id: "accounts.read", state: "available", reason: "VERIFIED_READ", http_status: 200, latency_ms: 0 } as const;
const account: ManagedAccount = {
  id, revision: "2", source_id: id, upstream_id: "9223372036854775807", name: "Local", monitoring: true, source_mode: "passive", source_current: true,
  identity_state: "verified", connection_available: true, observed_connection_revision: "3", collection_state: "awaiting_collector", read,
  snapshot: { account: { upstream_id: "9223372036854775807", name: "Remote", platform: "openai", type: "oauth", status: "active", schedulable: false,
    eligibility: "unverified", reason: "RESOURCE_IDENTITY_UNVERIFIED" }, runtime: { last_used_at: null, rate_limited_at: null, rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, error_present: null } },
  last_attempt_at: stamp, observed_at: stamp, created_at: stamp, saved_at: stamp,
};
const counts = { queued: "0", running: "0", succeeded: "1", failed: "0", skipped: "0", conflict: "0", interrupted: "0" };
const run: CollectionRun = { id: "9223372036854775807", reason: "manual", actor_id: id, created_at: stamp, state: "completed", counts };
const job: CollectionJob = { id: "1", run_id: run.id, account_id: id, source_id: id, connection_revision: "3", account_revision: "2", policy_revision: "1", source_mode: "passive", state: "succeeded", reason: "VERIFIED_PASSIVE", queued_at: stamp, started_at: stamp, finished_at: stamp, retry_after: null, read: null };
const snapshot: NonNullable<AccountObservation["snapshot"]> = { source_id: id, connection_revision: "3", account_revision: "2", source_mode: "passive", source_at: null, received_at: stamp,
  windows: [{ kind: "short", duration_seconds: "18000", used_percent: "0.123456789123456789", reset_at: stamp, reset_expired: true }] };
const observation: AccountObservation = { account_id: id, requested_mode: "passive", snapshot, latest_attempt: job, freshness: "unknown", stale_warning: true, next_scheduled_at: null };

test("durable history uses exact IDs, every page and no partial result after failure", async () => {
  const cursors: string[] = [];
  const result = await readRunHistory(async after => { cursors.push(after); return after === "0" ? { runs: [{ ...run, id: "1" }], next_after: "1" } : { runs: [run], next_after: null }; });
  assert.deepEqual(cursors, ["0", "1"]); assert.equal(result[0].id, "9223372036854775807");
  await assert.rejects(readRunHistory(async () => ({ runs: [{ ...run, id: "1" }], next_after: "1" })));
  await assert.rejects(readRunHistory(async after => { if (after !== "0") throw new Error("lost second page"); return { runs: [{ ...run, id: "1" }], next_after: "1" }; }));
  await assert.rejects(readRunHistory(async () => ({ runs: [], next_after: "1" })));
});

test("all job pages must match the acknowledged run and its seven durable counts", async () => {
  const two: CollectionRun = { ...run, counts: { ...counts, succeeded: "0", skipped: "1", conflict: "1" } };
  const first = { ...job, state: "skipped" as const }, second = { ...job, id: "2", account_id: other, state: "conflict" as const };
  const result = await readRunJobs(async after => after === "0" ? { run: two, jobs: [first], next_after: "1" } : { run: two, jobs: [second], next_after: null }, run.id);
  assert.deepEqual(result.jobs.map(value => value.state), ["skipped", "conflict"]);
  for (const bad of [{ ...second, run_id: "1" }, { ...second, id: "1" }, { ...second, account_id: id }]) {
    await assert.rejects(readRunJobs(async after => after === "0" ? { run: two, jobs: [first], next_after: "1" } : { run: two, jobs: [bad], next_after: null }, run.id));
  }
  await assert.rejects(readRunJobs(async () => ({ run, jobs: [], next_after: null }), run.id));
  await assert.rejects(readRunJobs(async () => ({ run: { ...run, id: "1" }, jobs: [job], next_after: null }), run.id));
  await assert.rejects(readRunJobs(async () => ({ run: two, jobs: [first], next_after: "2" }), run.id));
});

test("queued and interrupted outcomes retain their actual state without predicting success", async () => {
  for (const state of ["queued", "running", "failed", "interrupted"] as const) {
    const actualRun: CollectionRun = { ...run, state: state === "queued" || state === "running" ? state : "completed", counts: { ...counts, succeeded: "0", [state]: "1" } };
    const result = await readRunJobs(async () => ({ run: actualRun, jobs: [{ ...job, state, reason: state === "interrupted" ? "WORKER_INTERRUPTED" : job.reason, started_at: state === "queued" ? null : stamp, finished_at: state === "queued" || state === "running" ? null : stamp }], next_after: null }), run.id);
    assert.equal(result.run.state, actualRun.state); assert.equal(result.jobs[0].state, state);
    if (state === "interrupted") assert.match(jobNote(result.jobs[0]), /后台工作中断/);
  }
});

test("observation binding rejects a changed account, source, mode or later account revision", () => {
  assert.equal(bindObservation(account, observation), observation);
  for (const change of [{ account_id: other }, { requested_mode: "active" as const }, { snapshot: { ...snapshot, source_id: other } }, { snapshot: { ...snapshot, account_revision: "3" } }, { latest_attempt: { ...job, source_id: other } }]) {
    assert.throws(() => bindObservation(account, { ...observation, ...change }));
  }
  const historical = { ...observation, freshness: "connection_changed" as const, snapshot: { ...snapshot, connection_revision: "1", account_revision: "1" } };
  assert.equal(bindObservation(account, historical).freshness, "connection_changed");
  assert.deepEqual(accountReadInput(account, "3"), { id, revision: "2", source_id: id, connection_revision: "3" });
});

test("window presentation preserves exact percentages, expired facts, actual duration and missing windows", () => {
  const row = { account, observation, error: null };
  const short = quotaWindow(row, "short");
  assert.ok(short);
  assert.equal(short?.used_percent, "0.123456789123456789"); assert.equal(short?.reset_expired, true);
  assert.match(windowTitle(short), /5 小时/); assert.equal(quotaWindow(row, "seven_day"), null);
  const zero = { ...observation, snapshot: { ...snapshot, windows: [{ ...short, used_percent: "0", duration_seconds: null }] } };
  assert.equal(quotaWindow({ ...row, observation: zero }, "short")?.used_percent, "0");
  assert.match(windowTitle(zero.snapshot.windows[0]), /时长未知/);
  assert.equal(compactTime(null, "UTC"), "暂无记录"); assert.match(compactTime(stamp, "Asia/Tokyo"), /10:02/);
});

test("advisory catalog and absent/empty/passthrough mapping never invent model access", () => {
  const value: AccountModelConfiguration = { account_id: id, source_id: id, connection_revision: "3", checked_at: stamp, models: [{ id: "native", name: "Native" }], mapping: null, passthrough: null, mapping_mode: "unknown", access_proven: false, account_read: read, catalog_read: read };
  for (const change of [{}, { mapping: [], mapping_mode: "empty" as const }, { passthrough: true, mapping_mode: "passthrough" as const }]) assert.deepEqual(modelRows({ ...value, ...change }), [{ requested: "native", mapped: null }]);
  assert.deepEqual(modelRows({ ...value, mapping: [{ from: "alias", to: "native" }], mapping_mode: "configured" }), [{ requested: "native", mapped: null }, { requested: "alias", mapped: "native" }]);
});

test("upstream quota denial is not reported as a change to the local reader role", () => {
  assert.match(jobNote({ ...job, state: "failed", reason: "FORBIDDEN" }), /上游凭据/);
  assert.match(jobNote({ ...job, state: "skipped", reason: "SNAPSHOT_MISSING" }), /未用零补齐/);
  assert.match(jobNote({ ...job, reason: "VERIFIED_PASSIVE_SNAPSHOT" }), /已保存被动窗口/);
});

test("status refresh acknowledges the selected resource and exact next revision without changing local configuration", () => {
  const before = { ...account, revision: "9007199254740992" };
  const next = { ...before, revision: "9007199254740993", observed_at: "2026-11-13T00:00:00Z" };
  const value = { account: next, connection_revision: "3" };
  assert.equal(bindAccountRefresh(before, "3", value), next);
  for (const change of [{ id: other }, { revision: before.revision }, { revision: "9007199254740994" }, { monitoring: false }, { source_id: other }]) {
    assert.throws(() => bindAccountRefresh(before, "3", { ...value, account: { ...next, ...change } }));
  }
  assert.throws(() => bindAccountRefresh(before, "4", value));
});

test("failed status reads retain the saved snapshot and observation time while recording a new attempt", () => {
  const failed = { ...account, revision: "3", collection_state: "read_unavailable" as const, last_attempt_at: "2026-11-13T00:00:00Z", read: { ...read, state: "denied" as const, reason: "FORBIDDEN" as const, http_status: 403 } };
  assert.equal(bindAccountRefresh(account, "3", { account: failed, connection_revision: "3" }), failed);
  assert.throws(() => bindAccountRefresh(account, "3", { account: { ...failed, observed_at: failed.last_attempt_at }, connection_revision: "3" }));
  assert.throws(() => bindAccountRefresh(account, "3", { account: { ...failed, snapshot: { ...failed.snapshot, runtime: { ...failed.snapshot.runtime, error_present: false } } }, connection_revision: "3" }));
});
