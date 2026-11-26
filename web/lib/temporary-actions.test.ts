import assert from "node:assert/strict";
import { test } from "node:test";
import { temporaryRound, temporaryPreflight, temporaryHistory, temporaryRights, temporaryRightsReceipt } from "@sub-pool/contracts/temporary";
import { readTemporaryHistory, temporarySame, checkTemporaryDetails, type TemporaryHistoryRow } from "./temporary-actions";
import { temporaryDetailRows } from "./temporary-data";

const id = (value: number) => Buffer.alloc(16, value).toString("base64url");
const account = id(1), member = id(2), source = id(3), roundID = id(4);
const period = { kind: "short", start_at: "2026-01-01T00:00:00Z", end_at: "2026-01-01T00:06:00Z", duration_seconds: "360", basis: "native_reset_and_duration" };
const amount = { numerator: "9", denominator: "1" };
const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id(5) };
const rawRound = {
  id: roundID, actor_id: id(6), started_at: "2026-01-01T00:03:00Z", deadline: "2026-01-01T00:05:00Z", mode: "carry", informed: false, exit: null,
  caps: [{ account_id: account, participant_id: member, amount: "9" }], acknowledged_participants: [member],
  scope: { simulation, source_id: source, connection_revision: "1", policy_revision: "1", expires_at: "2026-01-01T00:04:00Z", latest_deadline: period.end_at,
    accounts: [{ account_id: account, upstream_id: "7", account_revision: "1", contract_id: "1", contract_head: "1", member_head: "members", estimate_revision: "1", management_revision: "1", price_version: "1", wallet_multiplier: "0.5", periods: [period], settlement_period: period, remaining: amount, reserve: { numerator: "0", denominator: "1" }, members: [{ participant_id: member, binding_id: "1", user_id: "21", baseline: amount }] }],
    wallets: [{ participant_id: member, participant_revision: "1", binding_id: "1", connection_revision: "1", source_id: source, latest_id: "1", user_id: "21", recommendation_id: "1", basis_hash: "a".repeat(64) }],
  },
};

const round = temporaryRound.parse(rawRound), scope = round.scope;
const preflight = temporaryPreflight.parse({ state: "ready", reason: "CONDITIONAL_SIMULATION_ONLY", scope_hash: "a".repeat(64), scope });
const now = Date.parse(round.started_at);
const startDraft = { request_id: id(7), mode: round.mode, deadline: round.deadline, caps: round.caps, acknowledged_participants: round.acknowledged_participants, informed: false };
const summary = (value = round): TemporaryHistoryRow => temporaryHistory.parse({ rounds: [{ id: value.id, source_id: value.scope.source_id, simulation: value.scope.simulation,
  started_at: value.started_at, deadline: value.deadline, mode: value.mode, exit: value.exit, accounts: value.scope.accounts.length, rights_state: value.exit ? "processed" : "none" }], next_after: null }).rounds[0] as TemporaryHistoryRow;
const page = (rounds: TemporaryHistoryRow[], next_after: string | null = null) => ({ rounds, next_after });
const exited = temporaryRound.parse({ ...round, exit: { exited_at: "2026-01-01T00:04:00Z", recorded_at: "2026-01-01T00:04:00Z", reason: "terminated", actor_id: round.actor_id } });
const details = { round, settlements: [], reminders: { revision: "0", enabled: false, threshold: null } };

test("temporary immutable matching ignores object key order but distinguishes arrays and foreign scope", () => {
  assert.equal(temporarySame({ a: ["0"], b: { x: 1 } }, { b: { x: 1 }, a: ["0"] }), true);
  assert.equal(temporarySame([], {}), false);
  assert.equal(temporarySame({ a: 1 }, { b: 1 }), false);
  assert.throws(() => checkTemporaryDetails({ ...details, round: { ...round, scope: { ...scope, source_id: id(8) } } }, round));
  assert.throws(() => checkTemporaryDetails({ ...details, round: { ...round, caps: [{ ...round.caps[0], account_id: account, participant_id: member, amount: "8" }] } }, round));
  assert.doesNotThrow(() => checkTemporaryDetails({ ...details, round: exited }, round));
  assert.throws(() => checkTemporaryDetails(details, exited));
  assert.doesNotThrow(() => checkTemporaryDetails({ ...details, round: { ...exited, exit: { ...exited.exit, exited_at: "2026-01-01T00:04:00Z", recorded_at: "2026-01-01T00:04:01Z", reason: "terminated", actor_id: round.actor_id } } }, exited));
  assert.throws(() => checkTemporaryDetails({ ...details, round: { ...exited, exit: { exited_at: "2026-01-01T00:04:00.000000001Z", recorded_at: "2026-01-01T00:04:01Z", reason: "terminated", actor_id: round.actor_id } } }, exited));
});

test("temporary history follows opaque cursor positions and preserves exact cross-page order", async () => {
  const older = summary(temporaryRound.parse({ ...round, id: id(8), started_at: "2026-01-01T00:02:59.999999999Z" }));
  const calls: string[] = [];
  const client = { history: async ({ after }: { after: string }) => {
    calls.push(after);
    return after === "" ? page([summary()], round.id) : page([older]);
  } };
  const first = await readTemporaryHistory(client);
  const second = await readTemporaryHistory(client, undefined, first.rounds[0]);
  assert.deepEqual([...first.rounds, ...second.rounds].map(row => row.id), [round.id, older.id]);
  assert.deepEqual(calls, ["", round.id]);
  const newer = summary(temporaryRound.parse({ ...round, id: id(9), started_at: "2026-01-01T00:03:00.000000001Z" }));
  await assert.rejects(readTemporaryHistory({ history: async () => page([newer]) }, undefined, summary()));
  await assert.rejects(readTemporaryHistory({ history: async () => { throw Error("second page unavailable"); } }, undefined, summary()), /second page unavailable/);
});

test("temporary history rejects duplicate, empty, mismatched and nonadvancing cursors", async () => {
  for (const next of [page([summary()], id(9)), page([], round.id)]) await assert.rejects(readTemporaryHistory({ history: async () => next }));
  await assert.rejects(readTemporaryHistory({ history: async () => page([summary()], round.id) }, undefined, summary()));
});

test("temporary history reaches round 2001 with one page at a time", async () => {
  let count = 0;
  const client = { history: async () => {
    const rows = Array.from({ length: Math.min(100, 2001 - count) }, () => {
      const index = ++count, identity = Buffer.alloc(16); identity.writeUInt32BE(index);
      return { ...summary(), id: identity.toString("base64url"), started_at: new Date(now - index * 1000).toISOString() };
    });
    return page(rows, count < 2001 ? rows.at(-1)?.id ?? null : null);
  } };
  let found = await readTemporaryHistory(client);
  while (found.next_after) found = await readTemporaryHistory(client, undefined, found.rounds.at(-1));
  assert.equal(count, 2001);
  assert.equal(found.rounds.length, 1);
});






test("temporary detail consumption keeps missing evidence separate from actual zero", () => {
  const rows = temporaryDetailRows(details); assert.equal(rows[0]?.spent, "未记录"); assert.equal(rows[0]?.periodSpent, "未记录");
  const value = { ...details, round: exited, settlements: [{ round_id: round.id, account_id: account, state: "cancelled" as const, reason: "EARLY_TERMINATION", original_period: period as typeof scope.accounts[number]["settlement_period"], next_period: null,
    ledger_revision: "", closed_through: null, round_spent: { [member]: "0" }, period_spent: {}, adjustments: {}, residuals: {} }] };
  const row = temporaryDetailRows(value)[0]; assert.equal(row?.spent, "0"); assert.equal(row?.periodSpent, "未记录"); assert.equal(row?.adjustment, "不新增结转");
  assert.equal(row?.participantID, member); assert.equal(row?.accountID, account); assert.equal(row?.closedThrough, null);
  assert.equal(row?.period.start_at, period.start_at);
});
