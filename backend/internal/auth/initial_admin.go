package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type InitialAdminSetup struct {
	GeneratedPassword string
	LegacyRevoked     int
}

func administratorExists(ctx context.Context, tx pgx.Tx) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users WHERE role='admin')`).Scan(&exists)
	return exists, err
}

func insertFirstAdministrator(ctx context.Context, tx pgx.Tx, name, password string, mustChange bool) (Identity, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return Identity{}, err
	}
	id, err := randomValue(16)
	if err != nil {
		return Identity{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_users(id,username,role,password_hash,created_at,must_change_password)
		VALUES ($1,$2,'admin',$3,$4,$5)`, id, name, hash, time.Now().UTC(), mustChange); err != nil {
		return Identity{}, err
	}
	if err = event(ctx, tx, "bootstrap", "success", "local", &id); err != nil {
		return Identity{}, err
	}
	return Identity{ID: id, Username: name, Role: "admin", Revision: "1"}, nil
}

func generateInitialPassword() (string, error) {
	for {
		value, err := randomValue(15)
		if err != nil {
			return "", err
		}
		if ValidPassword(value) {
			return value, nil
		}
	}
}

// EnsureInitialAdmin returns a generated credential only to the process that committed its creation.
func (s *Service) EnsureInitialAdmin(ctx context.Context, configured *string) (InitialAdminSetup, error) {
	if err := s.acquireKDF(ctx); err != nil {
		return InitialAdminSetup{}, err
	}
	defer func() { <-s.kdf }()
	tx, err := s.beginUserMutation(ctx)
	if err != nil {
		return InitialAdminSetup{}, err
	}
	defer tx.Rollback(ctx)
	exists, err := administratorExists(ctx, tx)
	if err != nil {
		return InitialAdminSetup{}, err
	}
	var result InitialAdminSetup
	if exists {
		result.LegacyRevoked, err = revokeLegacyInitialPasswords(ctx, tx)
	} else {
		var password string
		if configured == nil {
			password, err = generateInitialPassword()
			result.GeneratedPassword = password
		} else if ValidPassword(*configured) {
			password = *configured
		} else {
			return InitialAdminSetup{}, ErrInput
		}
		if err == nil {
			_, err = insertFirstAdministrator(ctx, tx, "admin", password, true)
		}
	}
	if err != nil {
		return InitialAdminSetup{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return InitialAdminSetup{}, err
	}
	return result, nil
}

func revokeLegacyInitialPasswords(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `SELECT u.id,u.password_hash FROM auth_users u
		WHERE u.deleted_at IS NULL AND u.password_hash<>''
		AND (u.role='admin' OR EXISTS (SELECT 1 FROM auth_events e
			WHERE e.user_id=u.id AND e.action='bootstrap' AND e.outcome='success'))
		ORDER BY u.id FOR UPDATE OF u`)
	if err != nil {
		return 0, err
	}
	var affected []string
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return 0, err
		}
		if verifyPassword("admin", hash) {
			affected = append(affected, id)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, id := range affected {
		var revision string
		if err = tx.QueryRow(ctx, `UPDATE auth_users SET password_hash='',must_change_password=true,revision=revision+1
			WHERE id=$1 RETURNING revision::text`, id).Scan(&revision); err != nil {
			return 0, err
		}
		if err = revokeUserCredentials(ctx, tx, id, now); err != nil {
			return 0, err
		}
		if err = recordLocalCredentialEvent(ctx, tx, now, id, revision, "user.revoke_legacy_initial_password", "local:legacy-initial-password"); err != nil {
			return 0, err
		}
	}
	return len(affected), nil
}
