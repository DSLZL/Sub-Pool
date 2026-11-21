package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
)

var (
	ErrLastAdministrator = &Failure{Status: 409, Code: "LAST_ADMIN_REQUIRED"}
	ErrCurrentPassword   = &Failure{Status: 403, Code: "CURRENT_PASSWORD_INVALID"}
)

func (s *Service) beginUserMutation(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	// Take the shared lifecycle lock before any actor, target or credential row lock.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7210461102)`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func requireAnotherAdministrator(ctx context.Context, tx pgx.Tx, target string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users WHERE role='admin' AND enabled AND deleted_at IS NULL AND id<>$1)`, target).Scan(&exists)
	if err == nil && !exists {
		return ErrLastAdministrator
	}
	return err
}

func confirmCurrentPassword(ctx context.Context, tx pgx.Tx, actor, password string) error {
	if password == "" || len(password) > 128 {
		return ErrCurrentPassword
	}
	var hash string
	if err := tx.QueryRow(ctx, `SELECT password_hash FROM auth_users WHERE id=$1`, actor).Scan(&hash); err != nil {
		return err
	}
	if !verifyPassword(password, hash) {
		return ErrCurrentPassword
	}
	return nil
}

func revokeUserCredentials(ctx context.Context, tx pgx.Tx, userID string, at time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, at); err != nil {
		return err
	}
	return revokeAPIKeys(ctx, tx, userID, at)
}

// ResetAdministratorPassword is called by the local recovery command with a deployment database connection.
func (s *Service) ResetAdministratorPassword(ctx context.Context, username, next string) error {
	name, err := NormalizeUsername(username)
	if err != nil || !ValidPassword(next) {
		return ErrInput
	}
	if err = s.acquireKDF(ctx); err != nil {
		return err
	}
	defer func() { <-s.kdf }()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, current string
	err = tx.QueryRow(ctx, `SELECT id,password_hash FROM auth_users WHERE username=$1 AND role='admin' AND enabled AND deleted_at IS NULL FOR UPDATE`, name).Scan(&id, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if verifyPassword(next, current) {
		return ErrInput
	}
	hash, err := hashPassword(next)
	if err != nil {
		return err
	}
	var revision string
	if err = tx.QueryRow(ctx, `UPDATE auth_users SET password_hash=$2,revision=revision+1 WHERE id=$1 RETURNING revision::text`, id, hash).Scan(&revision); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err = revokeUserCredentials(ctx, tx, id, now); err != nil {
		return err
	}
	if err = recordLocalCredentialEvent(ctx, tx, now, id, revision, "user.reset_password_local", "local:reset-password"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recordLocalCredentialEvent(ctx context.Context, tx pgx.Tx, at time.Time, id, revision, action, task string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO auth_events(occurred_at,action,outcome,target_user_id,source_ip,source_kind) VALUES ($1,$2,'success',$3,'local','local')`, at, action, id); err != nil {
		return err
	}
	_, err := audit.Record(ctx, tx, audit.Entry{TaskID: &task, Action: action, Outcome: "succeeded", TargetType: "user", TargetID: id, Revision: &revision})
	return err
}
