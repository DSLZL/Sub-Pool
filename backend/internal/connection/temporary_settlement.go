package connection

import (
	"context"
	"encoding/json"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/estimation"
)

type TemporarySettlement struct {
	RoundID        string                        `json:"round_id"`
	AccountID      string                        `json:"account_id"`
	State          string                        `json:"state"`
	Reason         string                        `json:"reason"`
	OriginalPeriod ObservationPeriod             `json:"original_period"`
	NextPeriod     *ObservationPeriod            `json:"next_period"`
	LedgerRevision string                        `json:"ledger_revision"`
	ClosedThrough  *time.Time                    `json:"closed_through"`
	RoundSpent     map[string]string             `json:"round_spent"`
	PeriodSpent    map[string]string             `json:"period_spent"`
	Adjustments    map[string]string             `json:"adjustments"`
	Residuals      map[string]*estimation.Amount `json:"residuals"`
}

func temporarySettlement(ctx context.Context, tx pgx.Tx, round, account string) (*TemporarySettlement, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM temporary_events WHERE round_id=$1 AND account_id=$2 AND kind='settlement' ORDER BY id DESC LIMIT 1`, round, account).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out TemporarySettlement
	err = json.Unmarshal(raw, &out)
	return &out, err
}
func temporarySettlementDone(state string) bool {
	return state == "settled" || state == "no_carry" || state == "no_borrowing" || state == "cancelled"
}
func temporarySettlementsFinished(ctx context.Context, tx pgx.Tx, r TemporaryRound) (bool, error) {
	for _, a := range r.Scope.Accounts {
		row, err := temporarySettlement(ctx, tx, r.ID, a.AccountID)
		if err != nil {
			return false, err
		}
		if row == nil || !temporarySettlementDone(row.State) {
			return false, nil
		}
	}
	return true, nil
}
