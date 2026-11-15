package connection

import (
	"context"
	"errors"
	"reflect"
	"time"

	"sub-pool/backend/internal/auth"
)

const walletScanSize = 25

// shortcut: scans coalesce intermediate changes; use a transactional queue if every transient basis must be retained.
func (s *Service) processWalletRecommendations(ctx context.Context, after string) (string, error) {
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return after, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT p.id FROM participants p WHERE p.deleted_at IS NULL AND p.id>$1
	 AND EXISTS(SELECT 1 FROM participant_bindings b WHERE b.participant_id=p.id AND b.ended_at IS NULL)
	 ORDER BY p.id LIMIT $2`, after, walletScanSize)
	if err != nil {
		return after, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return after, err
	}
	for _, id := range ids {
		if err = s.refreshWalletRecommendation(ctx, id); err != nil && !errors.Is(err, auth.ErrNotFound) {
			return id, err
		}
	}
	if len(ids) == walletScanSize {
		return ids[len(ids)-1], nil
	}
	return "", nil
}

func (s *Service) refreshWalletRecommendation(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockWallet(ctx, tx, id); err != nil {
		return err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	p, err := readParticipant(ctx, tx, id, c, false, false)
	if err != nil {
		return err
	}
	basis, err := s.walletBasis(ctx, tx, id)
	if err != nil {
		return err
	}
	latest, err := latestWalletRecommendation(ctx, tx, id, p.Binding.ID)
	if err != nil {
		return err
	}
	current := s.simulationWalletCurrent(c, basis, id, time.Now().UTC(), latest)
	if latest != nil {
		current.Trigger = latest.Trigger
		if reflect.DeepEqual(current, latest.WalletRecommendationSnapshot) {
			return tx.Commit(ctx)
		}
	}
	task := "wallet-inputs"
	if _, err = s.appendWalletRecommendation(ctx, tx, nil, &task, id, "input_change"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
