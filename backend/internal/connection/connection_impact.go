package connection

import (
	"context"
	"time"
)

type ConnectionImpact struct {
	Revision            string    `json:"revision"`
	SourceID            *string   `json:"source_id"`
	CheckedAt           time.Time `json:"checked_at"`
	ManagedAccounts     string    `json:"managed_accounts"`
	ParticipantBindings string    `json:"participant_bindings"`
	DispatchOpen        string    `json:"dispatch_open"`
	PricingOpen         string    `json:"pricing_open"`
	WalletRejected      string    `json:"wallet_rejected"`
	WalletUnconfirmed   string    `json:"wallet_unconfirmed"`
}

func (s *Service) GetConnectionImpact(ctx context.Context, token string) (ConnectionImpact, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return ConnectionImpact{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ConnectionImpact{}, err
	}
	out := ConnectionImpact{Revision: c.Revision, SourceID: c.SourceID}
	// One statement keeps every local count on the same snapshot; this does not reserve later writes.
	err = tx.QueryRow(ctx, `SELECT statement_timestamp(),
		(SELECT count(*)::text FROM managed_accounts WHERE source_id=$1),
		(SELECT count(*)::text FROM participant_bindings WHERE source_id=$1 AND ended_at IS NULL),
		(SELECT count(*)::text FROM dispatch_pauses WHERE source_id=$1 AND state IN ('pausing','paused','restoring','needs_review')),
		(SELECT count(*)::text FROM pricing_changes WHERE source_id=$1 AND state IN ('applying','applied','reverting','needs_review')),
		(SELECT count(*)::text FROM wallet_application_requests r JOIN participant_bindings b ON b.id=r.binding_id
			WHERE b.source_id=$1 AND (`+walletApplicationSnapshotSQL+`) @> '{"state":"rejected","dispatch_state":"not_dispatched"}'),
		(SELECT count(*)::text FROM wallet_application_requests r JOIN participant_bindings b ON b.id=r.binding_id
			WHERE b.source_id=$1 AND NOT (`+walletApplicationResolvedSQL+`))`, c.SourceID).
		Scan(&out.CheckedAt, &out.ManagedAccounts, &out.ParticipantBindings, &out.DispatchOpen, &out.PricingOpen, &out.WalletRejected, &out.WalletUnconfirmed)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return ConnectionImpact{}, err
	}
	out.CheckedAt = out.CheckedAt.UTC()
	return out, nil
}
