package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/telemetry"
)

const SourceBlockActive = `blocked AND (expires_at IS NULL OR expires_at > statement_timestamp())`

func recordSourceExpiry(ctx context.Context, tx pgx.Tx, address, revision string) error {
	task := "source-expiry"
	_, err := audit.Record(ctx, tx, audit.Entry{
		TaskID: &task, Action: "security.source.expire", Outcome: "succeeded",
		TargetType: "source_ip", TargetID: address, Revision: &revision,
	})
	return err
}

func (s *Service) expireSourceRules(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = securityConfig(ctx, tx, true); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `WITH due AS (
		SELECT ip,revision FROM source_blocks
		WHERE blocked AND expires_at<=statement_timestamp()
		ORDER BY expires_at,ip LIMIT 100 FOR UPDATE
	) UPDATE source_blocks AS rule SET blocked=false,revision=rule.revision+1
		FROM due WHERE rule.ip=due.ip RETURNING host(due.ip),due.revision::text`)
	if err != nil {
		return err
	}
	type expiredSource struct{ IP, Revision string }
	rules, err := pgx.CollectRows(rows, pgx.RowToStructByPos[expiredSource])
	if err != nil {
		return err
	}
	for _, rule := range rules {
		ip, _ := parseIP(rule.IP)
		if err = recordSourceExpiry(ctx, tx, ip.String(), rule.Revision); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) RunSourceExpiry(ctx context.Context, runtime *database.Runtime) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		if release, ok := runtime.TryEnter(); ok {
			batch, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := s.expireSourceRules(batch)
			cancel()
			release()
			if err != nil && ctx.Err() == nil {
				telemetry.Error(ctx, "source_expiry", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
