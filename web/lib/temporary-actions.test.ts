import assert from "node:assert/strict";
import { test } from "node:test";
import { temporaryRound, temporaryPreflight, temporaryHistory, temporaryRights, temporaryRightsReceipt } from "@sub-pool/contracts/temporary";
import { readTemporaryHistory, temporarySame, buildTemporaryStart, checkTemporaryStart, checkTemporaryDetails, checkTemporaryStop, buildTemporaryRights, checkTemporaryRightsReceipt, checkTemporaryReminders, type TemporaryHistoryRow } from "./temporary-actions";
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

test("temporary start requires all frozen pairs and acknowledgements with exact UTC deadlines", () => {
  const input = buildTemporaryStart(preflight, { ...startDraft, deadline: "2026-01-01T00:05:00.000000001Z" }, now);
  assert.equal(input.deadline, "2026-01-01T00:05:00.000000001Z");
  for (const change of [{ caps: [] }, { caps: [{ account_id: id(8), participant_id: member, amount: "9" }] }, { acknowledged_participants: [] }, { deadline: "2026-01-01T00:06:00.000000001Z" }, { deadline: "2026-02-30T00:05:00Z" }, { deadline: round.started_at }, { mode: "no_carry" as const, informed: false }])
    assert.throws(() => buildTemporaryStart(preflight, { ...startDraft, ...change }, now));
  assert.throws(() => buildTemporaryStart(preflight, startDraft, now + 60000));
  assert.throws(() => buildTemporaryStart({ state: "unavailable", reason: "TEMPORARY_INPUT_CHANGED", scope_hash: "", scope: null }, startDraft, now));
});

test("temporary start receipt is bound to actor, complete scope and explicit input", () => {
  const input = buildTemporaryStart(preflight, startDraft, now);
  assert.doesNotThrow(() => checkTemporaryStart(input, scope, round.actor_id, round));
  assert.doesNotThrow(() => checkTemporaryStart({ ...input, deadline: "2026-01-01T00:05:00.000Z" }, scope, round.actor_id, round));
  for (const value of [{ ...round, actor_id: id(9) }, { ...round, informed: true }, { ...round, scope: { ...scope, simulation: { ...scope.simulation, epoch: id(9) } } }, { ...round, acknowledged_participants: [] }])
    assert.throws(() => checkTemporaryStart(input, scope, round.actor_id, value));
  assert.throws(() => checkTemporaryStop(round, round));
  assert.doesNotThrow(() => checkTemporaryStop(round, exited));
});

test("current rights changes bind frozen member, current period and revision and retain zero", () => {
  const rights = temporaryRights.parse({ round_id: round.id, account_id: account, revision: "9007199254740993", period, adjustments: { [member]: "-0.00000001" } });
  const input = buildTemporaryRights(exited, rights, member, "0", "  clear current debit  ", id(8));
  assert.equal(input.after, "0"); assert.equal(input.reason, "clear current debit"); assert.equal(input.revision, "9007199254740993");
  assert.equal(input.binding_id, "1"); assert.deepEqual(input.period, period);
  assert.throws(() => buildTemporaryRights(round, rights, member, "0", "reason", id(8)));
  assert.throws(() => buildTemporaryRights(exited, { ...rights, account_id: id(9) }, member, "0", "reason", id(8)));
  assert.throws(() => buildTemporaryRights(exited, { ...rights, period: null }, member, "0", "reason", id(8)));
  for (const [after, reason] of [["-0", "reason"], ["1.000000001", "reason"], ["0", " "]]) assert.throws(() => buildTemporaryRights(exited, rights, member, after ?? "", reason ?? "", id(8)));
});

test("rights receipt verifies prior signed q8 value without float rounding", () => {
  const rights = temporaryRights.parse({ round_id: round.id, account_id: account, revision: "100", period, adjustments: { [member]: "9007199254740993" } });
  const input = buildTemporaryRights(exited, rights, member, "9007199254740992.99999999", "one quantum", id(8));
  const receipt = temporaryRightsReceipt.parse({ ...input, before: "9007199254740993.00000000", delta: "-0.00000001", recorded_at: "2026-01-01T00:04:00Z" });
  assert.doesNotThrow(() => checkTemporaryRightsReceipt(input, rights, receipt));
  assert.throws(() => checkTemporaryRightsReceipt(input, rights, { ...receipt, before: "9007199254740992.99999999" }));
  assert.throws(() => checkTemporaryRightsReceipt(input, rights, { ...receipt, request_id: id(9) }));
});

test("round reminders require a later exact revision and matching saved preference", () => {
  const input = { round_id: round.id, revision: "9007199254740993", enabled: true, threshold: "90" };
  assert.doesNotThrow(() => checkTemporaryReminders(input, { revision: "9007199254740994", enabled: true, threshold: "90" }));
  for (const value of [{ revision: input.revision, enabled: true, threshold: "90" }, { revision: "9007199254740994", enabled: false, threshold: null }, { revision: "9007199254740994", enabled: true, threshold: "91" }])
    assert.throws(() => checkTemporaryReminders(input, value));
  assert.doesNotThrow(() => checkTemporaryReminders({ ...input, enabled: false, threshold: "" }, { revision: "9007199254740994", enabled: false, threshold: null }));
});

test("temporary detail consumption keeps missing evidence separate from actual zero", () => {
  const rows = temporaryDetailRows(details, "UTC"); assert.equal(rows[0]?.spent, "未记录"); assert.equal(rows[0]?.periodSpent, "未记录");
  const value = { ...details, round: exited, settlements: [{ round_id: round.id, account_id: account, state: "cancelled" as const, reason: "EARLY_TERMINATION", original_period: period as typeof scope.accounts[number]["settlement_period"], next_period: null,
    ledger_revision: "", closed_through: null, round_spent: { [member]: "0" }, period_spent: {}, adjustments: {}, residuals: {} }] };
  const row = temporaryDetailRows(value, "UTC")[0]; assert.equal(row?.spent, "0"); assert.equal(row?.periodSpent, "未记录"); assert.equal(row?.adjustment, "不新增结转");
  assert.equal(row?.participantID, member); assert.equal(row?.accountID, account); assert.equal(row?.closedThrough, "尚未确认完整尾段");
});
