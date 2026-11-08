package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func (s *Service) ApplyMaintenancePlan(ctx context.Context, token, id, basis string, confirmation bool) (MaintenancePlan, error) {
	if !validRevision(id) || !maintenanceFingerprint.MatchString(basis) || !confirmation {
		return MaintenancePlan{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return MaintenancePlan{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1`, id))
	if err != nil {
		return p, err
	}
	policy, c, a, err := s.maintenanceEnvironment(ctx, tx, p.Scope.AccountID, true)
	if err != nil {
		return p, err
	}
	p, err = scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return p, err
	}
	if p.Basis.Fingerprint != basis || p.State == "blocked" || p.State == "expired" || p.State == "failed" {
		return p, auth.ErrConflict
	}
	if p.State != "ready" {
		return p, s.finish(ctx, tx, token, true)
	}
	if err = checkMaintenance(ctx, tx, &p, policy, c, a); err != nil {
		return p, err
	}
	if p.Validity != "valid" {
		reason := "BASIS_CHANGED"
		if p.Validity == "expired" {
			reason = "PLAN_EXPIRED"
		}
		if _, err = tx.Exec(ctx, `UPDATE maintenance_plans SET state='expired',reason=$2,finished_at=$3 WHERE id=$1`, id, reason, time.Now().UTC()); err == nil {
			err = maintenanceAudit(ctx, tx, p, &actor.ID, "expire", "conflict")
		}
		if err == nil {
			err = s.finish(ctx, tx, token, true)
		}
		if err != nil {
			return p, err
		}
		return p, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return p, err
	}
	digest := sha256.Sum256([]byte(token))
	p, err = scanMaintenance(tx.QueryRow(ctx, `UPDATE maintenance_plans SET state='queued',reason='QUEUED',queued_actor_id=$2,queued_actor_revision=$3,queued_digest=$4,queued_at=$5 WHERE id=$1 RETURNING `+maintenanceColumns, id, actor.ID, actor.Revision, digest[:], time.Now().UTC()))
	if err == nil {
		err = maintenanceAudit(ctx, tx, p, &actor.ID, "queue", "pending")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return p, err
}

func recoverMaintenance(ctx context.Context, conn *pgxpool.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE maintenance_plans SET state='failed',reason='WORKER_INTERRUPTED',finished_at=$1 WHERE state='running' RETURNING `+maintenanceColumns, time.Now().UTC())
	if err != nil {
		return err
	}
	plans := []MaintenancePlan{}
	for rows.Next() {
		p, e := scanMaintenance(rows)
		if e != nil {
			err = e
			break
		}
		plans = append(plans, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range plans {
		if err = maintenanceAudit(ctx, tx, p, nil, "apply", "failed"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func finishMaintenance(ctx context.Context, tx pgx.Tx, p MaintenancePlan, state, reason string, authorize func() error) error {
	if _, err := tx.Exec(ctx, `UPDATE maintenance_plans SET state=$2,reason=$3,finished_at=$4 WHERE id=$1 AND state='running'`, p.ID, state, reason, time.Now().UTC()); err != nil {
		return err
	}
	outcome := "failed"
	if state == "applied" {
		outcome = "succeeded"
	} else if state == "expired" {
		outcome = "conflict"
	}
	if err := maintenanceAudit(ctx, tx, p, nil, "apply", outcome); err != nil {
		return err
	}
	if authorize != nil {
		if err := authorize(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// The existing collector leader serializes local jobs; no maintenance path calls upstream.
func (s *Service) processMaintenance(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	p, err := scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE state='queued' ORDER BY maintenance_plans.id LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if errors.Is(err, auth.ErrNotFound) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE maintenance_plans SET state='running',reason='RUNNING',started_at=$2 WHERE id=$1`, p.ID, time.Now().UTC()); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	err = s.executeMaintenance(ctx, conn, p.ID)
	if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) {
		tx, err = conn.Begin(ctx)
		if err != nil {
			return true, err
		}
		defer tx.Rollback(ctx)
		p, err = scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1 AND state='running' FOR UPDATE`, p.ID))
		if err == nil {
			err = finishMaintenance(ctx, tx, p, "failed", "ACTOR_UNAVAILABLE", nil)
		}
	}
	return true, err
}

func (s *Service) executeMaintenance(ctx context.Context, conn *pgxpool.Conn, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var actor, revision string
	var digest []byte
	if err = tx.QueryRow(ctx, `SELECT queued_actor_id,queued_actor_revision::text,queued_digest FROM maintenance_plans WHERE id=$1`, id).Scan(&actor, &revision, &digest); err != nil {
		return err
	}
	authorize := func() error { return s.auth.AuthorizeQueuedAdmin(ctx, tx, digest, actor, revision) }
	if err = authorize(); err != nil {
		if !errors.Is(err, auth.ErrUnauthorized) && !errors.Is(err, auth.ErrForbidden) {
			return err
		}
		p, e := scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1 AND state='running' FOR UPDATE`, id))
		if e != nil {
			return e
		}
		return finishMaintenance(ctx, tx, p, "failed", "ACTOR_UNAVAILABLE", nil)
	}
	p, err := scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1`, id))
	if err != nil {
		return err
	}
	policy, c, a, err := s.maintenanceEnvironment(ctx, tx, p.Scope.AccountID, true)
	if err != nil {
		return err
	}
	p, err = scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if p.State != "running" {
		return tx.Commit(ctx)
	}
	if err = authorize(); err != nil {
		if !errors.Is(err, auth.ErrUnauthorized) && !errors.Is(err, auth.ErrForbidden) {
			return err
		}
		return finishMaintenance(ctx, tx, p, "failed", "ACTOR_UNAVAILABLE", nil)
	}
	if !time.Now().UTC().Before(p.ExpiresAt) {
		return finishMaintenance(ctx, tx, p, "expired", "PLAN_EXPIRED", authorize)
	}
	current, err := buildMaintenance(ctx, tx, MaintenanceInput{MaintenanceScope: p.Scope, Kind: p.Kind, RulesVersion: p.RulesVersion}, policy, c, a)
	if err != nil {
		return err
	}
	if maintenanceBlocked(current) || current.Basis.Fingerprint != p.Basis.Fingerprint {
		return finishMaintenance(ctx, tx, p, "expired", "BASIS_CHANGED", authorize)
	}
	if err = authorize(); err != nil {
		if !errors.Is(err, auth.ErrUnauthorized) && !errors.Is(err, auth.ErrForbidden) {
			return err
		}
		return finishMaintenance(ctx, tx, p, "failed", "ACTOR_UNAVAILABLE", nil)
	}
	return finishMaintenance(ctx, tx, p, "applied", "APPLIED", authorize)
}
