package auth

import (
	"context"
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
	return nil
}
