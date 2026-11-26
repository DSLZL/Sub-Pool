import { uiCopy } from "@/lib/glossary";
import type { TemporaryStatus } from "@sub-pool/contracts/temporary";
import { displayTime } from "./connection-data";
import type { TemporaryDetails, TemporaryRound } from "./temporary-actions";

const exitNames = { deadline: "到期", period_changed: "官方周期变化", terminated: "提前终止" };
const adviceNames = { temporary_active: "临时建议生效", recalculation_required: "建议待重算", ordinary_restored: "常规建议已恢复" };
const remoteNames = { temporary_active: "临时轮次进行中", remote_restore_pending: "远端恢复待核对", not_required: "无需恢复", remote_restored: "远端恢复已确认" };
const settlementNames = { pending_evidence: uiCopy.pendingReview, settled: "已结算", no_carry: "不结转", no_borrowing: "无借用", cancelled: "已取消" };
const reasonNames: Record<string, string> = { EARLY_TERMINATION: "提前结束", NO_NEW_CARRY_RIGHTS: "无新增权益", ORIGINAL_PERIOD_TAIL_UNVERIFIED: "期末消费未知", IMMEDIATE_NEXT_PERIOD_UNVERIFIED: "下一周期未知", FROZEN_SOURCE_CHANGED: "来源变更待核对", FROZEN_IDENTITY_OR_CONTRACT_CHANGED: "绑定或合同变更", SETTLEMENT_LEDGER_CHANGED: "台账已变", FROZEN_ATTRIBUTION_OR_PRICE_CHANGED: "归属或计价待核对", scenario: "消费与权益已核对", no_borrowing: "无需结转" };

export const temporaryStateNames = { unavailable: "临时参与不可用", idle: "暂无历史轮次", active: "旧轮次进行中", exited: "旧轮次已退出" };
export function temporaryOverview(status: TemporaryStatus) {
  if (status.state === "unavailable") return "历史状态未知。";
  return `${temporaryStateNames[status.state]}；${status.rights_state === "processed" ? "权益已处理" : status.rights_state === "pending_evidence" ? "权益待核对" : "暂无处理权益"}。详见详情。`;
}

export function temporaryMessage(status: TemporaryStatus, timezone: string | null) {
  if (status.state === "unavailable") return "历史不可读。";
  if (!status.round) return "暂无轮次。";
  const round = status.round;
  const reminders = round.exit ? "已结束" : status.reminders?.enabled ? `阈值 ${status.reminders.threshold}%` : "关闭";
  const restoration = (status.restorations ?? []).map(row => `成员 #${round.scope.wallets.find(wallet => wallet.participant_id === row.participant_id)?.user_id ?? "未确认"}：${adviceNames[row.advice_state]}，${remoteNames[row.remote_state]}`).join("；");
  return `旧轮次 ${round.id} · ${round.mode === "carry" ? "结转" : "不结转"}。${displayTime(round.started_at, timezone, false)} → ${displayTime(round.deadline, timezone, false)}。${round.exit ? `${exitNames[round.exit.reason]} ${displayTime(round.exit.exited_at, timezone, false)}` : "进行中"}。权益${status.rights_state === "processed" ? "已处理" : status.rights_state === "pending_evidence" ? "待核对" : "未处理"}。提醒${reminders}。${restoration}`;
}

export function temporaryRows(status: TemporaryStatus | null) {
  if (!status || status.state === "unavailable" || !status.round) return [];
  return temporarySettlementRows(status.round, status.settlements ?? []);
}

export function temporaryDetailRows(details: TemporaryDetails | null) {
  return details ? temporarySettlementRows(details.round, details.settlements) : [];
}

function temporarySettlementRows(round: TemporaryRound, settlements: TemporaryDetails["settlements"]) {
  return round.scope.accounts.flatMap(account => {
    const settlement = settlements.find(row => row.account_id === account.account_id);
    return account.members.map(member => ({
      key: `${round.id}:${account.account_id}:${member.participant_id}`, round: round.id, accountID: account.account_id, participantID: member.participant_id,
      account: `#${account.upstream_id}`, period: account.settlement_period,
      member: `#${member.user_id}`,
      spent: settlement?.round_spent[member.participant_id] ?? "未记录",
      periodSpent: settlement?.period_spent[member.participant_id] ?? "未记录",
      closedThrough: settlement?.closed_through ?? null,
      nextPeriod: settlement?.next_period ?? null,
      adjustment: settlement?.state === "settled" ? `${settlement.adjustments[member.participant_id] ?? "未记录"} 钱包 USD` : settlement && settlement.state !== "pending_evidence" ? "不新增结转" : uiCopy.pendingReview,
      state: settlement ? settlementNames[settlement.state] : round.exit ? uiCopy.pendingReview : "本轮进行中",
      reason: settlement ? reasonNames[settlement.reason] ?? "结算待核对" : round.exit ? "暂无处理记录" : "退出后逐项核对",
    }));
  });
}
