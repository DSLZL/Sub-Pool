package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) processWeeklyWalletUsage(ctx context.Context, after string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return after, err
	}
	defer release()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return after, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil || !policy.Enabled {
		return after, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil || c.SourceID == nil || !c.CredentialSet || c.reverificationRequired || s.simulationEpoch(c) != "" {
		return after, err
	}
	var member string
	err = tx.QueryRow(ctx, `SELECT x.id FROM participants x WHERE x.enabled AND x.deleted_at IS NULL
		AND EXISTS (SELECT 1 FROM pool_contract_participants m JOIN pool_contracts pc ON pc.id=m.contract_id
		 JOIN pools p ON p.id=pc.pool_id AND p.enabled AND p.revision=pc.revision
		 JOIN participant_bindings bind ON bind.id=m.binding_id AND bind.ended_at IS NULL AND bind.source_id=pc.source_id
		 WHERE m.participant_id=x.id AND pc.enabled AND pc.source_id=$1)
		ORDER BY (x.id <= $2),x.id LIMIT 1`, c.SourceID, after).Scan(&member)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return after, err
	}
	grouped, err := weeklyWalletGroupRequired(ctx, tx, member)
	if err != nil || !grouped {
		return member, err
	}
	env, ok, err := s.weeklyWalletGroupEnvironment(ctx, tx, member, nil)
	if err != nil || !ok {
		return member, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	requests, err := s.discoverWeeklyWalletUsage(ctx, tx, env, env.pools[0].ID, now)
	if err != nil {
		return member, err
	}
	for _, request := range requests {
		view, _, err := readNativeWeeklyPlan(ctx, tx, request.Account, c, policy, now)
		if err != nil {
			return member, err
		}
		if view == nil || view.Observation == nil || view.stale(policy, now) {
			continue
		}
		expires := view.Observation.At.Add(time.Duration(policy.StaleAfterSeconds) * time.Second)
		if err = queueWeeklyUsage(ctx, tx, c, request.Account, request.Key.Range, now, expires, false); err != nil {
			return member, err
		}
	}
	return member, tx.Commit(ctx)
}
