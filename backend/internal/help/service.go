package help

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

var contentID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var contentDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Service struct {
	pool *pgxpool.Pool
	auth *auth.Service
}

func New(pool *pgxpool.Pool, identity *auth.Service) *Service {
	return &Service{pool: pool, auth: identity}
}

func (s *Service) Catalog(ctx context.Context, token string) (Catalog, error) {
	out := Catalog{Version: Version, Guides: []Guide{}, Announcements: []Announcement{}}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.AuthorizeSession(ctx, tx, token)
	if err != nil {
		return out, err
	}
	var features []string
	if err = tx.QueryRow(ctx, "SELECT read_features FROM auth_users WHERE id=$1", actor.ID).Scan(&features); err != nil {
		return out, err
	}
	for _, g := range guides() {
		if actor.Role == "admin" || g.Audience == "all" || g.Audience == "scoped" && slices.Contains(features, g.Feature) {
			out.Guides = append(out.Guides, g)
		}
	}
	if actor.Role == "admin" {
		out.Announcements = announcements()
		for i := range out.Announcements {
			a := &out.Announcements[i]
			err = tx.QueryRow(ctx, "SELECT read_at FROM announcement_reads WHERE user_id=$1 AND announcement_id=$2 AND content_sha256=$3", actor.ID, a.ID, a.ContentSHA256).Scan(&a.ReadAt)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return Catalog{}, err
			}
			if a.ReadAt != nil {
				utc := a.ReadAt.UTC()
				a.ReadAt = &utc
			}
		}
		_, err = s.auth.AuthorizeAdminSession(ctx, tx, token)
	} else {
		_, err = s.auth.AuthorizeSession(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}

func (s *Service) Read(ctx context.Context, token, id, contentSHA256 string) (Announcement, error) {
	var out Announcement
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Announcement{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return Announcement{}, err
	}
	if !contentID.MatchString(id) || !contentDigest.MatchString(contentSHA256) {
		return out, auth.ErrInput
	}
	for _, a := range announcements() {
		if a.ID == id {
			out = a
			break
		}
	}
	if out.ID == "" {
		return out, auth.ErrNotFound
	}
	if contentSHA256 != out.ContentSHA256 {
		return Announcement{}, auth.ErrConflict
	}
	inserted, err := tx.Exec(ctx, "INSERT INTO announcement_reads(user_id,announcement_id,content_sha256) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", actor.ID, id, contentSHA256)
	if err != nil {
		return Announcement{}, err
	}
	if inserted.RowsAffected() == 1 {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "announcement.read", Outcome: "succeeded", TargetType: "announcement", TargetID: id})
		if err != nil {
			return Announcement{}, err
		}
	}
	var at time.Time
	if err = tx.QueryRow(ctx, "SELECT read_at FROM announcement_reads WHERE user_id=$1 AND announcement_id=$2 AND content_sha256=$3", actor.ID, id, contentSHA256).Scan(&at); err != nil {
		return Announcement{}, err
	}
	at = at.UTC()
	out.ReadAt = &at
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}
