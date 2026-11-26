package connection

import (
	"context"
	"encoding/json"

	"time"

	"sub-pool/backend/internal/auth"
)

func (s *Service) GetTemporaryRound(ctx context.Context, token, id string) (TemporaryRoundDetails, error) {
	if !managedID(id) {
		return TemporaryRoundDetails{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	defer tx.Rollback(ctx)
	r, err := temporaryRound(ctx, tx, id)
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	r.Exit, err = temporaryEffectiveExit(ctx, tx, r, time.Now().UTC())
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	out, err := temporaryRoundDetails(ctx, tx, r)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	return out, nil
}
func (s *Service) GetTemporaryRights(ctx context.Context, token, round, account string) (TemporaryRights, error) {
	if !managedID(round) || !managedID(account) {
		return TemporaryRights{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRights{}, err
	}
	defer tx.Rollback(ctx)
	r, err := temporaryRound(ctx, tx, round)
	if err != nil {
		return TemporaryRights{}, err
	}
	found := false
	for _, archived := range r.Scope.Accounts {
		if archived.AccountID == account {
			found = len(archived.Members) != 0
			break
		}
	}
	if !found {
		return TemporaryRights{}, auth.ErrNotFound
	}
	out := TemporaryRights{RoundID: round, AccountID: account, Revision: "0", Adjustments: map[string]string{}}
	settlement, err := temporarySettlement(ctx, tx, round, account)
	if err != nil {
		return TemporaryRights{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,snapshot FROM temporary_events
	 WHERE round_id=$1 AND account_id=$2 AND kind='rights' ORDER BY id DESC`, round, account)
	if err != nil {
		return TemporaryRights{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		var change TemporaryRightsReceipt
		if err = rows.Scan(&id, &raw); err != nil {
			return TemporaryRights{}, err
		}
		if err = json.Unmarshal(raw, &change); err != nil {
			return TemporaryRights{}, err
		}
		if out.Period == nil {
			out.Period, out.Revision = &change.Period, id
		}
		if _, exists := out.Adjustments[change.ParticipantID]; !exists && temporarySamePeriod(*out.Period, change.Period) {
			out.Adjustments[change.ParticipantID] = change.After
		}
	}
	if err = rows.Err(); err != nil {
		return TemporaryRights{}, err
	}
	if settlement != nil {
		if out.Period == nil {
			out.Period = settlement.NextPeriod
		}
		if out.Period != nil && settlement.NextPeriod != nil && temporarySamePeriod(*out.Period, *settlement.NextPeriod) {
			for member, amount := range settlement.Adjustments {
				if _, exists := out.Adjustments[member]; !exists {
					out.Adjustments[member] = amount
				}
			}
		}
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return TemporaryRights{}, err
	}
	return out, nil
}
