import { test } from "node:test";
import assert from "node:assert/strict";
import { ORPCError } from "@orpc/client";
import { createParticipantsClient } from "./client";
import { attributedUsage, participantsOutputs } from "./participants";

const id = "a".repeat(21) + "A", start = "2028-02-28T23:59:59.999999999Z", split = "2028-03-01T00:00:00Z", end = "2028-04-01T00:00:00Z";
const metrics = { input_tokens: "9007199254740993", output_tokens: "0", cache_creation_tokens: null, cache_read_tokens: null,
  cache_creation_5m_tokens: null, cache_creation_1h_tokens: null, image_input_tokens: null, image_output_tokens: null,
  total_cost: "9007199254740993.0000000000000000001", actual_cost: "0", account_stats_cost: null };
const summary = { request_count: "1", metrics };
const selected = [{ id: "9", account_id: id, start_at: start, end_at: split, captured_at: "2028-04-01T01:00:00Z" },
  { id: "10", account_id: id, start_at: split, end_at: end, captured_at: "2028-04-01T02:00:00Z" }];
const history = { start_at: start, end_at: end, query_start_at: start, query_end_at: end,
  oldest_capture_at: "2028-04-01T01:00:00Z", newest_capture_at: "2028-04-01T02:00:00Z",
  history: { basis: "selected_captures", timezone: "UTC", authority_revision: "2", selections: selected },
  summary, assigned: summary, unassigned: null, unassigned_visible: false, groups: [{ account_id: id, participant_id: id, basis: "local_binding_at_log_time", ...summary }],
  grain: "month", buckets: [{ start_at: start, end_at: split, ...summary }], records: [], next_after: null,
  complete: false, settlement_eligible: false, currency: "USD", timestamp_basis: "native_created_at",
  limitations: ["RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"] };

test("history validates exact independent money, disjoint selections, calendar bounds and current authority", () => {
  const parsed = participantsOutputs.history.parse(history);
  assert.equal(parsed.summary.metrics.total_cost, metrics.total_cost);
  assert.equal(parsed.summary.metrics.actual_cost, "0");
  assert.equal(parsed.summary.metrics.account_stats_cost, null);
  assert.equal(attributedUsage.safeParse(history).success, false);
  for (const change of [
    { history: undefined }, { history: { ...history.history, authority_revision: "" } }, { history: { ...history.history, timezone: "Asia/Taipei" } },
    { history: { ...history.history, selections: [...selected].reverse() } },
    { history: { ...history.history, selections: [selected[0], { ...selected[1], start_at: start }] } },
    { history: { ...history.history, selections: [selected[0], { ...selected[1], observed_count: "99" }] } },
    { grain: "hour" }, { complete: true }, { settlement_eligible: true }, { query_end_at: undefined },
    { buckets: [{ ...history.buckets[0], end_at: "2028-03-02T00:00:00Z" }] },
    { end_at: "2029-04-01T00:00:00Z" }, { oldest_capture_at: "2028-03-01T01:00:00Z" },
  ]) assert.equal(participantsOutputs.history.safeParse({ ...history, ...change }).success, false);
});

test("history client validates bounded explicit selection and uses one fixed read without retry", async () => {
  let calls = 0;
  const input = { capture_ids: ["9", "10"], participant_id: "", model: "", api_key_id: "", grain: "month" as const, after: "0", limit: 100, start_at: start, end_at: end };
  const client = createParticipantsClient("http://127.0.0.1:3000", async (url, init) => {
    calls++;
    const request = new Request(url, init);
    assert.equal(request.url, "http://127.0.0.1:3000/api/v1/participants/usage/history");
    assert.deepEqual(await request.json(), input);
    return Response.json(history);
  });
  for (const bad of [{ capture_ids: [] }, { capture_ids: ["9", "9"] }, { grain: "hour" }, { start_at: null },
    { end_at: "2029-04-01T00:00:00Z" }, { end_at: start }, { after: "9.bad" }, { history: true },
    { start_at: "2028-02-28T23:59:59+00:00" }]) await assert.rejects(client.history({ ...input, ...bad } as never));
  assert.equal(calls, 0);
  assert.equal((await client.history(input)).summary.metrics.total_cost, metrics.total_cost);
  assert.equal(calls, 1);
  const denied = createParticipantsClient("http://127.0.0.1:3000", async () => {
    calls++;
    return Response.json({ defined: true, code: "NOT_FOUND", status: 404, message: "private", data: { retry_at: null } }, { status: 404 });
  });
  await assert.rejects(denied.history(input), error => error instanceof ORPCError && error.defined && error.status === 404);
  assert.equal(calls, 2);
});
