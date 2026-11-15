import assert from "node:assert/strict";
import { test } from "node:test";
import { temporaryStatus } from "@sub-pool/contracts/temporary";
import { temporaryMessage, temporaryRows, temporaryOverview } from "./temporary-data";

const id = (value: number) => Buffer.alloc(16, value).toString("base64url");
const account = id(1), member = id(2), source = id(3), roundID = id(4);
const period = { kind: "short", start_at: "2026-01-01T00:00:00Z", end_at: "2026-01-01T00:06:00Z", duration_seconds: "360", basis: "native_reset_and_duration" };
const amount = { numerator: "9", denominator: "1" };
const simulation = { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: id(5) };
const round = {
  id: roundID, actor_id: id(6), started_at: "2026-01-01T00:03:00Z", deadline: "2026-01-01T00:05:00Z", mode: "carry", informed: false, exit: null,
  caps: [{ account_id: account, participant_id: member, amount: "9" }], acknowledged_participants: [member],
  scope: { simulation, source_id: source, connection_revision: "1", policy_revision: "1", expires_at: "2026-01-01T00:04:00Z", latest_deadline: period.end_at,
    accounts: [{ account_id: account, upstream_id: "7", account_revision: "1", contract_id: "1", contract_head: "1", member_head: "members", estimate_revision: "1", management_revision: "1", price_version: "1", wallet_multiplier: "0.5", periods: [period], settlement_period: period, remaining: amount, reserve: { numerator: "0", denominator: "1" }, members: [{ participant_id: member, binding_id: "1", user_id: "21", baseline: amount }] }],
    wallets: [{ participant_id: member, participant_revision: "1", binding_id: "1", connection_revision: "1", source_id: source, latest_id: "1", user_id: "21", recommendation_id: "1", basis_hash: "a".repeat(64) }],
  },
};
const active = { can_start: false, state: "active", round_state: "active", reason: "CONDITIONAL_SIMULATION_ONLY", rights_state: "none", reminders_available: true, simulation, round, settlements: [], reminders: { revision: "0", enabled: false, threshold: null } };

test("unavailable and unprocessed temporary rights never become empty success or zero adjustments", () => {
  const native = temporaryStatus.parse({ can_start: false, state: "unavailable", reason: "FINANCIAL_APPLICATION_UNAVAILABLE", round_state: "not_implemented", rights_state: "not_implemented", reminders_available: false });
  assert.match(temporaryMessage(native, "UTC"), /不能确认历史为空/);
  assert.deepEqual(temporaryRows(native, "UTC"), []);
  const status = temporaryStatus.parse(active), rows = temporaryRows(status, "UTC");
  assert.equal(rows.length, 1); assert.equal(rows[0]?.member, "#21"); assert.equal(rows[0]?.adjustment, "待核对");
  assert.equal(rows[0]?.state, "本轮进行中"); assert.match(rows[0]?.account ?? "", /#7/);
  assert.match(temporaryMessage(status, "UTC"), /隔离模拟环境/); assert.match(temporaryMessage(status, "UTC"), /本轮提醒关闭/);
});

test("recorded cancellation and ordinary advice do not claim remote restoration", () => {
  const status = temporaryStatus.parse({ ...active, state: "exited", round_state: "exited", rights_state: "processed", reminders_available: false,
    round: { ...round, exit: { exited_at: "2026-01-01T00:04:00Z", recorded_at: "2026-01-01T00:04:00Z", reason: "terminated", actor_id: id(6) } },
    settlements: [{ round_id: roundID, account_id: account, state: "cancelled", reason: "EARLY_TERMINATION", original_period: period, next_period: null, ledger_revision: "", closed_through: null, round_spent: {}, period_spent: {}, adjustments: {}, residuals: {} }],
    restorations: [{ participant_id: member, advice_state: "ordinary_restored", remote_state: "remote_restore_pending", application_id: null }],
  });
  const message = temporaryMessage(status, "UTC"), rows = temporaryRows(status, "UTC");
  assert.match(message, /普通建议已恢复，远端恢复待核对/); assert.doesNotMatch(message, /远端恢复已有确认/);
  assert.match(message, /本轮提醒随轮次结束，不再发送/);
  assert.match(temporaryOverview(status), /隔离模拟轮次已退出；逐账号权益已处理/);
  assert.doesNotMatch(temporaryOverview(status), /钱包已恢复/);
  assert.equal(rows[0]?.state, "已取消"); assert.equal(rows[0]?.adjustment, "不新增结转");
  assert.equal(rows[0]?.reason, "提前终止，取消本轮未处理权益");
});
