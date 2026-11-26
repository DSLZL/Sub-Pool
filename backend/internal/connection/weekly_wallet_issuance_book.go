package connection

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type weeklyIssuanceOffset struct {
	Origin string            `json:"origin"`
	Cycle  weeklyWalletCycle `json:"cycle"`
	Amount string            `json:"amount"`
}

type weeklyIssuanceUsage struct {
	Observed     time.Time `json:"observed_at"`
	Rows         int       `json:"rows"`
	Unattributed int       `json:"unattributed_rows"`
	CostIssues   int       `json:"cost_issue_rows"`
}

type weeklyIssuanceIssue struct {
	weeklyWalletGroupIssue
	Usage *weeklyIssuanceUsage `json:"usage,omitempty"`
}

type weeklyIssuanceContribution struct {
	weeklyWalletCycle
	Name       string                 `json:"name"`
	Phase      string                 `json:"phase"`
	ContractID string                 `json:"contract_id"`
	EstimateID *string                `json:"estimate_id"`
	Share      string                 `json:"share_percent"`
	Gross      string                 `json:"gross"`
	Prior      string                 `json:"prior_issued"`
	Amount     string                 `json:"amount"`
	Debt       string                 `json:"debt"`
	Tail       string                 `json:"tail"`
	Offsets    []weeklyIssuanceOffset `json:"offsets"`
	Advance    string                 `json:"advance_origin,omitempty"`
	Usage      *weeklyIssuanceUsage   `json:"usage,omitempty"`
}

type weeklyIssuancePlan struct {
	Amount          string                       `json:"amount"`
	OutstandingDebt string                       `json:"outstanding_debt"`
	Balance         *string                      `json:"balance"`
	Frozen          *string                      `json:"frozen"`
	Contributions   []weeklyIssuanceContribution `json:"contributions"`
}

type weeklyIssuanceAuthority struct {
	PoolID          string `json:"pool_id"`
	PoolRevision    string `json:"pool_revision"`
	ContractID      string `json:"contract_id"`
	ControlRevision string `json:"control_revision"`
	AccountID       string `json:"account_id"`
	AccountRevision string `json:"account_revision"`
	ActorID         string `json:"actor_id"`
	ActorRevision   string `json:"actor_revision"`
}

type weeklyIssuanceRecord struct {
	*weeklyIssuancePlan
	Authority      []weeklyIssuanceAuthority `json:"authority,omitempty"`
	WalletGrant    string                    `json:"wallet_grant_id,omitempty"`
	PolicyRevision string                    `json:"policy_revision,omitempty"`
}

type weeklyIssuancePosition struct {
	weeklyIssuanceContribution
	origin    string
	ambiguous bool
	retry     bool
}

type weeklyIssuanceBook []weeklyIssuancePosition

func (book weeklyIssuanceBook) debtBalances() map[string]*big.Rat {
	balances := make(map[string]*big.Rat)
	for _, position := range book {
		if position.retry {
			continue
		}
		if debt := weeklyWalletAmount(position.Debt); debt != nil && debt.Sign() > 0 {
			balances[position.origin] = debt
		}
	}
	for _, position := range book {
		if position.retry {
			continue
		}
		for _, offset := range position.Offsets {
			if balance := balances[offset.Origin]; balance != nil {
				balance.Sub(balance, weeklyWalletAmount(offset.Amount))
			}
		}
	}
	return balances
}

func (book weeklyIssuanceBook) apply(plan *weeklyIssuancePlan) []weeklyIssuanceIssue {
	slices.SortFunc(plan.Contributions, func(a, b weeklyIssuanceContribution) int {
		if weeklyWalletCycleOrder(a.weeklyWalletCycle, b.weeklyWalletCycle) {
			return -1
		}
		if weeklyWalletCycleOrder(b.weeklyWalletCycle, a.weeklyWalletCycle) {
			return 1
		}
		return 0
	})
	slices.SortFunc(book, func(a, b weeklyIssuancePosition) int {
		if weeklyWalletCycleOrder(a.weeklyWalletCycle, b.weeklyWalletCycle) {
			return -1
		}
		if weeklyWalletCycleOrder(b.weeklyWalletCycle, a.weeklyWalletCycle) {
			return 1
		}
		return 0
	})
	debts := book.debtBalances()
	var outstanding big.Rat
	for _, debt := range debts {
		if debt.Sign() > 0 {
			outstanding.Add(&outstanding, debt)
		}
	}
	plan.OutstandingDebt = usageDecimal(&outstanding)
	var issues []weeklyIssuanceIssue
	total := new(big.Rat)
	ready := plan.Contributions[:0]
	for _, item := range plan.Contributions {
		item.Prior, item.Debt, item.Tail = "0", "0", "0"
		item.Offsets = []weeklyIssuanceOffset{}
		net := weeklyWalletAmount(item.Gross)
		for _, position := range book {
			if position.retry || item.Phase != "settlement" || position.Phase != "advance" || position.weeklyWalletAccountKey != item.weeklyWalletAccountKey || !position.End.Equal(item.End) {
				continue
			}
			item.Advance, item.Prior = position.origin, position.Amount
			net.Sub(net, weeklyWalletAmount(position.Amount))
			for _, previous := range position.Offsets {
				net.Sub(net, weeklyWalletAmount(previous.Amount))
			}
		}
		if net.Sign() < 0 {
			item.Debt = usageDecimal(new(big.Rat).Neg(net))
			net.SetInt64(0)
		}
		for _, position := range book {
			debt := debts[position.origin]
			if net.Sign() == 0 || debt == nil || debt.Sign() <= 0 || position.End.After(item.Start) {
				continue
			}
			offset := net
			if net.Cmp(debt) > 0 {
				offset = debt
			}
			item.Offsets = append(item.Offsets, weeklyIssuanceOffset{position.origin, position.weeklyWalletCycle, usageDecimal(offset)})
			net.Sub(net, offset)
		}
		minor := weeklyWalletFloor(net)
		sum := new(big.Rat).Add(total, minor)
		item.Amount = usageDecimal(minor)
		if minor.Sign() > 0 && (!validNativeDelta(item.Amount) || !validNativeDelta(usageDecimal(sum))) {
			issues = append(issues, weeklyIssuanceIssue{weeklyWalletGroupIssue{item.weeklyWalletAccountKey, "amount_precision_unverified"}, item.Usage})
			continue
		}
		item.Tail = usageDecimal(net.Sub(net, minor))
		for _, offset := range item.Offsets {
			debts[offset.Origin].Sub(debts[offset.Origin], weeklyWalletAmount(offset.Amount))
		}
		total.Set(sum)
		ready = append(ready, item)
	}
	plan.Contributions, plan.Amount = ready, usageDecimal(total)
	return issues
}

func readWeeklyIssuanceBook(ctx context.Context, tx pgx.Tx, member Participant) (weeklyIssuanceBook, error) {
	book, err := readLegacyWeeklyIssuanceBook(ctx, tx, member)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT w.id::text,w.snapshot,COALESCE(
	  CASE WHEN w.application_id IS NULL THEN w.snapshot->>'amount'='0' ELSE
	   (`+walletApplicationResolvedSQL+`) AND r.snapshot #> '{details,native_adjustment,weekly_issuance}'='true'
	   AND r.snapshot #>> '{details,input,source_id}'=b.source_id
	   AND r.snapshot #>> '{details,upstream_user_id}'=b.upstream_user_id::text
	   AND r.snapshot #>> '{details,input,native_delta}'=w.snapshot->>'amount'
	  END,false), COALESCE(`+walletApplicationSnapshotSQL+`->>'dispatch_state'='not_dispatched',false)
	 FROM weekly_wallet_issuances w
	 JOIN participant_bindings b ON b.id=w.binding_id LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE b.source_id=$1 AND b.upstream_user_id=$2::text::bigint AND
	  (w.application_id IS NULL OR `+walletApplicationSnapshotSQL+`->>'state'='applied'
	   OR ((`+walletApplicationResolvedSQL+`) AND `+walletApplicationSnapshotSQL+`->>'dispatch_state'='not_dispatched'))
	 ORDER BY w.id`, member.Binding.SourceID, member.Binding.UpstreamUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var plan weeklyIssuancePlan
		var proven, retry bool
		if err = rows.Scan(&id, &plan, &proven, &retry); err != nil {
			return nil, err
		}
		for i, item := range plan.Contributions {
			if !proven {
				item.Amount, item.Debt, item.Offsets = "0", "0", nil
			}
			book = append(book, weeklyIssuancePosition{
				weeklyIssuanceContribution: item, origin: fmt.Sprintf("issuance:%s:%d", id, i), ambiguous: !proven, retry: retry,
			})
		}
	}
	return book, rows.Err()
}

func listWeeklyIssuances(ctx context.Context, tx pgx.Tx, pool string) ([]weeklyWalletEntry, error) {
	rows, err := tx.Query(ctx, `SELECT w.id::text,b.participant_id,p.name,w.recorded_at,w.snapshot,w.application_id::text,
	  COALESCE(`+walletApplicationSnapshotSQL+`->>'state','applied') FROM weekly_wallet_issuances w
	 JOIN participant_bindings b ON b.id=w.binding_id JOIN participants p ON p.id=b.participant_id
	 LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE EXISTS(SELECT 1 FROM weekly_wallet_issuance_phases k WHERE k.issuance_id=w.id AND k.pool_id=$1)
	 ORDER BY w.id DESC LIMIT 100`, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []weeklyWalletEntry{}
	for rows.Next() {
		var item weeklyWalletEntry
		if err = rows.Scan(&item.ID, &item.ParticipantID, &item.Name, &item.RecordedAt, &item.Issuance, &item.ApplicationID, &item.State); err != nil {
			return nil, err
		}
		item.RecordedAt = item.RecordedAt.UTC()
		summarizeWeeklyIssuance(&item)
		out = append(out, item)
	}
	return out, rows.Err()
}

func summarizeWeeklyIssuance(entry *weeklyWalletEntry) {
	entry.Phase = "advance"
	for _, item := range entry.Issuance.Contributions {
		entry.PeriodEnd = maxTime(entry.PeriodEnd, item.End.UTC())
		if item.Phase == "settlement" {
			entry.Phase = "settlement"
		}
	}
}

func saveWeeklyIssuance(ctx context.Context, tx pgx.Tx, member Participant, entry *weeklyWalletEntry) error {
	entry.issuanceRecord.weeklyIssuancePlan = entry.Issuance
	err := tx.QueryRow(ctx, `INSERT INTO weekly_wallet_issuances(binding_id,application_id,recorded_at,basis,snapshot)
	 VALUES($1,$2,$3,$4,$5) RETURNING id::text`, member.Binding.ID, entry.ApplicationID, entry.RecordedAt, entry.basis, entry.issuanceRecord).Scan(&entry.ID)
	if err != nil {
		return err
	}
	for _, item := range entry.Issuance.Contributions {
		count, err := tx.Exec(ctx, `INSERT INTO weekly_wallet_issuance_phases(source_id,upstream_user_id,pool_id,account_id,period_end,phase,issuance_id)
		 VALUES($1,$2::text::bigint,$3,$4,$5,$6,$7)
		 ON CONFLICT(source_id,upstream_user_id,pool_id,account_id,period_end,phase) DO UPDATE SET issuance_id=EXCLUDED.issuance_id
		 WHERE EXISTS(SELECT 1 FROM weekly_wallet_issuances w JOIN wallet_application_requests r ON r.id=w.application_id
		  WHERE w.id=weekly_wallet_issuance_phases.issuance_id AND `+walletApplicationResolvedSQL+`
		  AND `+walletApplicationSnapshotSQL+`->>'dispatch_state'='not_dispatched')`,
			member.Binding.SourceID, member.Binding.UpstreamUserID, item.PoolID, item.AccountID, item.End, item.Phase, entry.ID)
		if err != nil {
			return err
		}
		if count.RowsAffected() != 1 {
			return errors.New("weekly wallet phase already reserved")
		}
	}
	return nil
}
