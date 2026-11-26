package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
)

type weeklyLegacyPhase struct {
	weeklyWalletAccountKey
	end   time.Time
	phase string
}

type weeklyLegacyDebt struct {
	origin   string
	recorded time.Time
	debt     string
}

func readLegacyWeeklyIssuanceBook(ctx context.Context, tx pgx.Tx, member Participant) (weeklyIssuanceBook, error) {
	rows, err := tx.Query(ctx, `SELECT w.origin,w.pool_id,w.account_id,w.period_end,w.phase,w.recorded_at,w.snapshot,r.snapshot,`+walletApplicationSnapshotSQL+`,
		w.application_id IS NOT NULL AND count(*) OVER (PARTITION BY w.application_id)>1
	 FROM (
	  SELECT 'legacy:single:'||id::text AS origin,pool_id,account_id,period_end,phase,recorded_at,snapshot,binding_id,application_id
	   FROM weekly_wallet_allocations
	  UNION ALL
	  SELECT 'legacy:group:'||id::text,NULL,NULL,period_end,phase,recorded_at,snapshot,binding_id,application_id
	   FROM weekly_wallet_groups
	 ) w JOIN participant_bindings b ON b.id=w.binding_id
	 LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE b.source_id=$1 AND b.upstream_user_id=$2::text::bigint
	  AND (w.application_id IS NULL OR `+walletApplicationSnapshotSQL+`->>'state'='applied')
	 ORDER BY w.recorded_at,w.origin`, member.Binding.SourceID, member.Binding.UpstreamUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var book weeklyIssuanceBook
	phases := make(map[weeklyLegacyPhase]int)
	debts := make(map[weeklyWalletAccountKey]weeklyLegacyDebt)
	bestDebt := func(item weeklyIssuancePosition, recorded time.Time, debt string) {
		key := item.weeklyWalletAccountKey
		if previous := debts[key]; !recorded.Before(previous.recorded) {
			debts[key] = weeklyLegacyDebt{item.origin, recorded, debt}
		}
	}
	for rows.Next() {
		var origin, phase string
		var pool, account *string
		var end, recorded time.Time
		var raw, request, result []byte
		var sharedReceipt bool
		if err = rows.Scan(&origin, &pool, &account, &end, &phase, &recorded, &raw, &request, &result, &sharedReceipt); err != nil {
			return nil, err
		}
		credited, proven := legacyWeeklyCredit(request, result, member)
		proven = proven && !sharedReceipt
		if pool != nil && account != nil {
			var old weeklyWalletPlan
			if err = json.Unmarshal(raw, &old); err != nil {
				return nil, err
			}
			cycle := weeklyWalletCycle{weeklyWalletAccountKey{*pool, *account}, end.UTC().Add(-7 * 24 * time.Hour), end.UTC()}
			position := weeklyIssuancePosition{weeklyIssuanceContribution: weeklyIssuanceContribution{
				weeklyWalletCycle: cycle, Phase: phase, Gross: old.Gross, Amount: credited,
			}, origin: origin, ambiguous: !proven}
			key := weeklyLegacyPhase{cycle.weeklyWalletAccountKey, cycle.End, phase}
			used := weeklyWalletAmount(old.OpeningDebt)
			if phase == "advance" && used != nil && used.Sign() > 0 {
				used = minWeeklyAmount(used, weeklyWalletAmount(old.Gross))
				position.Offsets = []weeklyIssuanceOffset{{Origin: origin + ":opening-debt", Cycle: cycle, Amount: usageDecimal(used)}}
			}
			if before, found := phases[key]; found {
				position.ambiguous = true
				book[before].ambiguous = true
				continue
			}
			phases[key] = len(book)
			book = append(book, position)
			bestDebt(position, recorded, old.Debt)
			continue
		}

		var group weeklyWalletGroupPlan
		if err = json.Unmarshal(raw, &group); err != nil {
			return nil, err
		}
		// Closed debt was carried into the successor in the same legacy snapshot.
		for ordinal, item := range append(group.Closed, group.Contributions...) {
			if item.Phase == "unfunded" {
				continue
			}
			cycle := item.weeklyWalletCycle
			cycle.Start, cycle.End = cycle.Start.UTC(), cycle.End.UTC()
			key := weeklyLegacyPhase{cycle.weeklyWalletAccountKey, cycle.End, item.Phase}
			index, found := phases[key]
			if !found {
				position := weeklyIssuancePosition{weeklyIssuanceContribution: weeklyIssuanceContribution{
					weeklyWalletCycle: cycle, Phase: item.Phase, Gross: item.Gross, Amount: "0",
				}, origin: fmt.Sprintf("%s:%d", origin, ordinal)}
				if proven && len(group.Contributions) == 1 && len(group.Closed) == 0 {
					position.Amount = credited
				}
				index = len(book)
				phases[key] = index
				book = append(book, position)
			}
			position := &book[index]
			// Confirmed group history lacks per-account credit when several positions shared the write.
			if !proven || credited != "0" && (len(group.Contributions) != 1 || len(group.Closed) != 0) {
				position.ambiguous = true
			}
			if used := weeklyWalletAmount(item.UsedToCover); item.Phase == "advance" && used != nil && used.Sign() > 0 {
				position.Offsets = []weeklyIssuanceOffset{{Origin: position.origin + ":group-offset", Cycle: cycle, Amount: usageDecimal(used)}}
			}
			bestDebt(*position, recorded, item.Debt)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i, item := range book {
		if debt := debts[item.weeklyWalletAccountKey]; debt.origin == item.origin {
			book[i].Debt = debt.debt
		}
	}
	return book, nil
}

func minWeeklyAmount(a, b *big.Rat) *big.Rat {
	if b != nil && b.Cmp(a) < 0 {
		return b
	}
	return a
}

func legacyWeeklyCredit(request, result []byte, member Participant) (string, bool) {
	if request == nil && result == nil {
		return "0", true
	}
	var original, confirmed WalletApplicationSnapshot
	if json.Unmarshal(request, &original) != nil || json.Unmarshal(result, &confirmed) != nil ||
		original.Unit != "native_wallet" || original.State != "pending" || confirmed.Unit != "native_wallet" ||
		confirmed.State != "applied" || confirmed.DispatchState != "confirmed" ||
		original.Details == nil || original.Details.NativeAdjustment == nil || confirmed.Details == nil || confirmed.Details.NativeAdjustment == nil ||
		confirmed.Details.NativeAdjustment.BalanceAfter == nil ||
		!validRevision(confirmed.Details.NativeAdjustment.LedgerID) ||
		original.Details.Input != confirmed.Details.Input || original.Details.UpstreamUserID != member.Binding.UpstreamUserID ||
		confirmed.Details.UpstreamUserID != member.Binding.UpstreamUserID || original.Details.Input.SourceID != member.Binding.SourceID {
		return "0", false
	}
	amount := weeklyWalletAmount(confirmed.Details.Input.NativeDelta)
	if amount == nil || !validNativeDelta(confirmed.Details.Input.NativeDelta) {
		return "0", false
	}
	if amount.Sign() <= 0 {
		return "0", true
	}
	return usageDecimal(amount), true
}
