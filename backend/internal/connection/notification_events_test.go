package connection

import (
	"context"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/notifications"
)

func TestFailedCollectionNotificationIsAtomicDeduplicatedAndNotAPermanentFailure(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 2)
	mail, e := notifications.New(s.pool, s.auth, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	nc, e := mail.GetConfiguration(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	in := notifications.SaveInput{Revision: nc.Revision, Enabled: true, Channel: "smtp", From: "sender@owned.test", Recipient: "admin@owned.test", SMTP: notifications.SMTP{Host: "127.0.0.1", Port: 1, TLS: "loopback_plain"}, Preferences: notifications.Preferences{CollectionFailed: true}, DedupeSeconds: 3600, CredentialAction: "keep"}
	nc, e = mail.SaveConfiguration(ctx, token, in)
	if e != nil {
		t.Fatal(e)
	}
	f.mode.Store(1)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	p, e := mail.List(ctx, token, notifications.Query{After: "0", Limit: 100})
	if e != nil || len(p.Notifications) != 1 {
		t.Fatal("missing committed failure event", e)
	}
	first := p.Notifications[0]
	if first.State != "queued" || first.Kind != "collection_failed" || first.SubjectID != a.ID || first.Details == nil || first.Details.EventReason != "UPSTREAM_FORBIDDEN" || strings.Contains(first.Details.Body, "private reflected") || !strings.Contains(first.Details.Body, "does not establish a permanent") {
		t.Fatal("incorrect collection event")
	}
	advanceCollectionFixture(t, s.pool)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	p, e = mail.List(ctx, token, notifications.Query{After: "0", Limit: 100})
	if e != nil || len(p.Notifications) != 2 || p.Notifications[1].Reason != "DUPLICATE_WINDOW" {
		t.Fatal("duplicate was dispatched", e)
	}
	var jobID string
	if e = s.pool.QueryRow(ctx, `SELECT id::text FROM collection_jobs ORDER BY collection_jobs.id DESC LIMIT 1`).Scan(&jobID); e != nil {
		t.Fatal(e)
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = notifications.RecordCollectionFailure(ctx, tx, jobID); e == nil {
		e = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if e != nil {
		t.Fatal(e)
	}
	p, e = mail.List(ctx, token, notifications.Query{After: "0", Limit: 100})
	if e != nil || len(p.Notifications) != 2 {
		t.Fatal("same source event was inserted twice")
	}
	in.Revision = nc.Revision
	in.Preferences.CollectionFailed = false
	nc, e = mail.SaveConfiguration(ctx, token, in)
	if e != nil {
		t.Fatal(e)
	}
	advanceCollectionFixture(t, s.pool)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	p, e = mail.List(ctx, token, notifications.Query{After: "0", Limit: 100})
	if e != nil || len(p.Notifications) != 3 || p.Notifications[2].Reason != "EVENT_DISABLED" {
		t.Fatal("disabled event did not retain skip")
	}
	advanceCollectionFixture(t, s.pool)
	queueAccount(t, s, token, c, a.ID)
	exec(t, s.pool, `CREATE FUNCTION reject_notify_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='notification.request' THEN RAISE EXCEPTION 'owned journal failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_notify_event BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_notify_event()`)
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	if _, e = s.processCollection(ctx, conn); e == nil {
		t.Fatal("failed notification audit did not roll back source result")
	}
	var state string
	if e = s.pool.QueryRow(ctx, `SELECT state FROM collection_jobs ORDER BY collection_jobs.id DESC LIMIT 1`).Scan(&state); e != nil || state != "running" {
		t.Fatal("source result committed without event", state, e)
	}
	if e = recoverCollections(ctx, conn); e != nil {
		t.Fatal(e)
	}
	p, e = mail.List(ctx, token, notifications.Query{After: "0", Limit: 100})
	if e != nil || len(p.Notifications) != 3 {
		t.Fatal("interrupted attempt became a failed event")
	}
}

func TestCollectionNotificationWaitCannotCommitAnExpiredManualActor(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 2)
	f.mode.Store(1)
	queueAccount(t, s, token, c, a.ID)
	block, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback(ctx)
	if _, e = block.Exec(ctx, `SELECT pg_advisory_xact_lock(1380991565)`); e != nil {
		t.Fatal(e)
	}
	expires := time.Now().Add(time.Second)
	exec(t, s.pool, `UPDATE auth_sessions SET idle_expires_at=$1`, expires)
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	var pid int
	if e = conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.processCollection(ctx, conn); done <- e }()
	observed := false
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var wait bool
		if e = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock%')`, pid).Scan(&wait); e != nil {
			t.Fatal(e)
		}
		if wait {
			observed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("actual event enqueue wait not reached")
	}
	if delay := time.Until(expires) + 25*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if e = block.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("expired collection actor committed after event wait")
	}
	var count int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired collection produced notification", e)
	}
	if e = recoverCollections(ctx, conn); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = s.pool.QueryRow(ctx, `SELECT state FROM collection_jobs`).Scan(&state); e != nil || state != "interrupted" {
		t.Fatal("rolled-back result was asserted successful", state, e)
	}
}
