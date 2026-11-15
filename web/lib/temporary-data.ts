import type { TemporaryStatus } from "@sub-pool/contracts/temporary";
import { displayTime } from "./connection-data";
import type { TemporaryDetails, TemporaryRound } from "./temporary-actions";

const exitNames = { deadline: "到期", period_changed: "官方周期变化", terminated: "提前终止" };
const adviceNames = { temporary_active: "临时建议生效", recalculation_required: "建议待重新核算", ordinary_restored: "普通建议已恢复" };
const remoteNames = { temporary_active: "临时轮次进行中", remote_restore_pending: "远端恢复待核对", not_required: "本轮无远端应用需恢复", remote_restored: "远端恢复已有确认" };
const settlementNames = { pending_evidence: "待核对", settled: "已结算", no_carry: "不结转", no_borrowing: "无借用", cancelled: "已取消" };
const reasonNames: Record<string, string> = { EARLY_TERMINATION: "提前终止，取消本轮未处理权益", NO_NEW_CARRY_RIGHTS: "本轮不新增结转权益", ORIGINAL_PERIOD_TAIL_UNVERIFIED: "原周期尾段消费尚未完整确认", IMMEDIATE_NEXT_PERIOD_UNVERIFIED: "紧邻的下一周期尚未确认", FROZEN_SOURCE_CHANGED: "来源已变化，待核对", FROZEN_IDENTITY_OR_CONTRACT_CHANGED: "绑定或合同已变化，待核对", SETTLEMENT_LEDGER_CHANGED: "账本版本已变化，待核对", FROZEN_ATTRIBUTION_OR_PRICE_CHANGED: "消费归属或价格依据已变化，待核对", scenario: "完整原周期消费与下一周期权益已核验", no_borrowing: "本轮没有待结转借用" };

export const temporaryStateNames = { unavailable: "临时参与不可用", idle: "隔离模拟环境暂无轮次", active: "隔离模拟轮次进行中", exited: "隔离模拟轮次已退出" };
export function temporaryOverview(status: TemporaryStatus) {
  if (status.state === "unavailable") return "余额应用与安全收回尚未通过原生核验；不能确认历史为空或钱包已恢复。";
  return `${temporaryStateNames[status.state]}；${status.rights_state === "processed" ? "逐账号权益已处理" : status.rights_state === "pending_evidence" ? "逐账号权益待证据核对" : "尚无已处理权益"}。建议恢复与远端恢复请在详情中分别核对。`;
}

export function temporaryMessage(status: TemporaryStatus, timezone: string | null) {
  if (status.state === "unavailable") return "临时参与不可用：余额应用与安全收回尚未通过核验。原生轮次与权益记录尚未接入，不能确认历史为空或远端余额已收回。";
  if (!status.round) return "隔离模拟环境尚无已记录轮次。新轮需完整范围预检、明确上限和截止时间。";
  const round = status.round;
  const reminders = round.exit ? "随轮次结束，不再发送" : status.reminders?.enabled ? `已启用，阈值 ${status.reminders.threshold}%` : "关闭";
  const restoration = (status.restorations ?? []).map(row => `成员 #${round.scope.wallets.find(wallet => wallet.participant_id === row.participant_id)?.user_id ?? "未确认"}：${adviceNames[row.advice_state]}，${remoteNames[row.remote_state]}`).join("；");
  return `隔离模拟环境 · 最近轮次 ${round.id} · ${round.mode === "carry" ? "结转" : "不结转"}。开始 ${displayTime(round.started_at, timezone)}；截止 ${displayTime(round.deadline, timezone)}。${round.exit ? `${exitNames[round.exit.reason]}：${displayTime(round.exit.exited_at, timezone)}` : "本轮进行中"}。逐账号权益${status.rights_state === "processed" ? "已处理" : status.rights_state === "pending_evidence" ? "仍待证据核对" : "尚未处理"}。本轮提醒${reminders}。${restoration}`;
}

export function temporaryRows(status: TemporaryStatus | null, timezone: string | null) {
  if (!status || status.state === "unavailable" || !status.round) return [];
  return temporarySettlementRows(status.round, status.settlements ?? [], timezone);
}

export function temporaryDetailRows(details: TemporaryDetails | null, timezone: string | null) {
  return details ? temporarySettlementRows(details.round, details.settlements, timezone) : [];
}

function temporarySettlementRows(round: TemporaryRound, settlements: TemporaryDetails["settlements"], timezone: string | null) {
  return round.scope.accounts.flatMap(account => {
    const settlement = settlements.find(row => row.account_id === account.account_id);
    return account.members.map(member => ({
      key: `${round.id}:${account.account_id}:${member.participant_id}`, round: round.id, accountID: account.account_id, participantID: member.participant_id,
      account: `#${account.upstream_id} · ${displayTime(account.settlement_period.start_at, timezone)} — ${displayTime(account.settlement_period.end_at, timezone)}`,
      member: `#${member.user_id}`,
      spent: settlement?.round_spent[member.participant_id] ?? "未记录",
      periodSpent: settlement?.period_spent[member.participant_id] ?? "未记录",
      closedThrough: settlement?.closed_through ? displayTime(settlement.closed_through, timezone) : "尚未确认完整尾段",
      nextPeriod: settlement?.next_period ? `${displayTime(settlement.next_period.start_at, timezone)} — ${displayTime(settlement.next_period.end_at, timezone)}` : "尚未确认",
      adjustment: settlement?.state === "settled" ? `${settlement.adjustments[member.participant_id] ?? "未记录"} 钱包 USD` : settlement && settlement.state !== "pending_evidence" ? "不新增结转" : "待核对",
      state: settlement ? settlementNames[settlement.state] : round.exit ? "待核对" : "本轮进行中",
      reason: settlement ? reasonNames[settlement.reason] ?? "结算依据仍需核对" : round.exit ? "尚无本账号处理记录" : "退出后按各账号原周期独立核对",
    }));
  });
}
