package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// AuthorizeQueuedAdmin validates a private persisted session digest, never browser-supplied proof.
func (s *Service) AuthorizeQueuedAdmin(ctx context.Context, tx pgx.Tx, digest []byte, actor, revision string) error {
	if len(digest) != 32 || !validID(actor) || !validRevision(revision) {
		return ErrUnauthorized
	}
	session, enabled, revoked, err := lockedSession(ctx, tx, digest, false)
	if err != nil {
		return err
	}
	if !live(session, enabled, revoked) || session.User.ID != actor || session.User.Revision != revision {
		return ErrUnauthorized
	}
	if session.User.Role != "admin" {
		return ErrForbidden
	}
	if session.MustChangePassword {
		return ErrPasswordChangeRequired
	}
	return nil
}

// AuthorizeAutomationAdmin checks a persisted explicit control, without retaining a browser credential.
func (s *Service) AuthorizeAutomationAdmin(ctx context.Context, tx pgx.Tx, actor, revision string) (Identity, error) {
	var out Identity
	if !validID(actor) || !validRevision(revision) {
		return out, ErrUnauthorized
	}
	var enabled, deleted, mustChange bool
	err := tx.QueryRow(ctx, `SELECT id,username,role,revision::text,enabled,deleted_at IS NOT NULL,must_change_password FROM auth_users WHERE id=$1 FOR SHARE`, actor).
		Scan(&out.ID, &out.Username, &out.Role, &out.Revision, &enabled, &deleted, &mustChange)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!enabled || deleted || out.Revision != revision) {
		err = ErrUnauthorized
	}
	if err == nil && out.Role != "admin" {
		err = ErrForbidden
	}
	if err == nil && mustChange {
		err = ErrPasswordChangeRequired
	}
	return out, err
}
