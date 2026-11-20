package help

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/testdb"
)

const testPassword = "OwnedHelpPassword123"

type fixture struct {
	pool     *pgxpool.Pool
	identity *auth.Service
	s        *Service
	admin    auth.Identity
	token    string
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Open(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	identity := auth.New(pool)
	admin, err := identity.Bootstrap(ctx, "help-admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, admin.Username, testPassword, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{pool, identity, New(pool, identity), admin, token}
}
func exec(t *testing.T, f fixture, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func member(t *testing.T, f fixture, name string, features []string) (auth.ManagedUser, string) {
	t.Helper()
	u, err := f.identity.CreateUser(context.Background(), f.token, auth.UserCreate{Username: name, Password: testPassword, Enabled: true, Grants: auth.Grants{Features: features, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.identity.Login(context.Background(), name, testPassword, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	return u, token
}
func TestHelpCatalogProjectsCurrentGrantsBeforeReturningContent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	catalog, err := f.s.Catalog(ctx, f.token)
	if err != nil || len(catalog.Guides) != 7 || len(catalog.Announcements) != 3 {
		t.Fatal("admin catalogue unavailable", err)
	}
	if catalog.Announcements[0].ID != "runtime-readiness-v1" {
		t.Fatal("latest operational announcement unavailable")
	}
	for _, a := range catalog.Announcements {
		if a.ReadAt != nil || len(a.ContentSHA256) != 64 {
			t.Fatal("new announcement read state invented")
		}
	}
	u, token := member(t, f, "scoped-reader", []string{"pools", "estimates"})
	c, err := f.s.Catalog(ctx, token)
	if err != nil || len(c.Guides) != 3 || len(c.Announcements) != 0 {
		t.Fatal("scoped help leaked or lost topics", err)
	}
	exec(t, f, "UPDATE auth_users SET read_features='{}',revision=revision+1 WHERE id=$1", u.ID)
	c, err = f.s.Catalog(ctx, token)
	if err != nil || len(c.Guides) != 1 || c.Guides[0].ID != "authority" {
		t.Fatal("old grants reused", err)
	}
	a := catalog.Announcements[0]
	if _, err = f.s.Read(ctx, token, a.ID, a.ContentSHA256); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary user acknowledged admin news", err)
	}
	if err = f.identity.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Catalog(ctx, token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("revoked session read help", err)
	}
}
func TestAnnouncementAcknowledgementIsContentBoundPersonalConcurrentAndDurable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := announcements()[0]
	exec(t, f, "INSERT INTO announcement_reads(user_id,announcement_id,content_sha256) VALUES($1,$2,$3)", f.admin.ID, a.ID, strings.Repeat("0", 64))
	c, err := f.s.Catalog(ctx, f.token)
	if err != nil || c.Announcements[0].ReadAt != nil {
		t.Fatal("old content marked new content read", err)
	}
	if _, err = f.s.Read(ctx, f.token, a.ID, strings.Repeat("1", 64)); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale content accepted", err)
	}
	var wg sync.WaitGroup
	results := make(chan Announcement, 6)
	failures := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			out, err := f.s.Read(ctx, f.token, a.ID, a.ContentSHA256)
			if err != nil {
				failures <- err
			} else {
				results <- out
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first *time.Time
	for out := range results {
		if out.ReadAt == nil {
			t.Fatal("confirmed read has no timestamp")
		}
		if first == nil {
			first = out.ReadAt
		} else if !first.Equal(*out.ReadAt) {
			t.Fatal("first-read time overwritten")
		}
	}
	var n int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM management_events WHERE action='announcement.read' AND actor_id=$1", f.admin.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("duplicate audit", n, err)
	}
	c, err = New(f.pool, auth.New(f.pool)).Catalog(ctx, f.token)
	if err != nil || c.Announcements[0].ReadAt == nil || !first.Equal(*c.Announcements[0].ReadAt) {
		t.Fatal("persisted read lost", err)
	}
	other, token := member(t, f, "other-admin", []string{})
	exec(t, f, "UPDATE auth_users SET role='admin',revision=revision+1 WHERE id=$1", other.ID)
	c, err = f.s.Catalog(ctx, token)
	if err != nil || c.Announcements[0].ReadAt != nil {
		t.Fatal("another admin inherited read state", err)
	}
	if _, err = f.s.Read(ctx, token, a.ID, a.ContentSHA256); err != nil {
		t.Fatal(err)
	}
}
func TestAnnouncementAuditFailureRollsBackThePersonalReceipt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := announcements()[0]
	exec(t, f, "ALTER TABLE management_events ADD CONSTRAINT owned_help_audit_denial CHECK(action<>'announcement.read')")
	if _, err := f.s.Read(ctx, f.token, a.ID, a.ContentSHA256); err == nil {
		t.Fatal("failed audit accepted")
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM announcement_reads").Scan(&n); err != nil || n != 0 {
		t.Fatal("read receipt survived failed transaction", n, err)
	}
	exec(t, f, "ALTER TABLE management_events DROP CONSTRAINT owned_help_audit_denial")
	if out, err := f.s.Read(ctx, f.token, a.ID, a.ContentSHA256); err != nil || out.ReadAt == nil {
		t.Fatal("read failed after actual repair", err)
	}
}
func TestAnnouncementReadRechecksExpiryAfterUniqueRowWait(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := announcements()[0]
	hold, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(ctx)
	if _, err = hold.Exec(ctx, "INSERT INTO announcement_reads(user_id,announcement_id,content_sha256) VALUES($1,$2,$3)", f.admin.ID, a.ID, a.ContentSHA256); err != nil {
		t.Fatal(err)
	}
	exec(t, f, "UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '2 seconds'")
	done := make(chan error, 1)
	go func() { _, err := f.s.Read(ctx, f.token, a.ID, a.ContentSHA256); done <- err }()
	var application string
	if err = f.pool.QueryRow(ctx, "SELECT current_setting('application_name')").Scan(&application); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE 'INSERT INTO announcement_reads%')", application).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("read never reached actual unique row wait")
	}
	time.Sleep(2100 * time.Millisecond)
	if err = hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, auth.ErrUnauthorized) {
			t.Fatal("expired session committed after wait", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read did not finish")
	}
	var n int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM announcement_reads").Scan(&n); err != nil || n != 0 {
		t.Fatal("expired read persisted", n, err)
	}
}
