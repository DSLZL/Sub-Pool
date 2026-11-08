package connection

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestMonitoringStatusUsesDurableLatestRunSuccessAndEarliestSchedule(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	status, err := s.GetMonitoringStatus(ctx, token)
	if err != nil || status.Enabled || status.LatestRun != nil || status.LastSuccessAt != nil || status.NextScheduledAt != nil || status.WorkerRecent {
		t.Fatalf("empty status invented collection facts: %+v, %v", status, err)
	}
	server, upstream := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a, b := passiveManaged(t, s, token, c, 1), passiveManaged(t, s, token, c, 2)
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	policy, err = s.UpdateMonitoringConfig(ctx, token, policy)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	status, err = s.GetMonitoringStatus(ctx, token)
	if err != nil || status.NextScheduledAt == nil || status.NextScheduledAt.Before(before) || status.NextScheduledAt.After(time.Now()) {
		t.Fatalf("new eligible accounts were not due: %+v, %v", status, err)
	}
	first := queueAccount(t, s, token, c, a.ID, b.ID)
	status, err = s.GetMonitoringStatus(ctx, token)
	if err != nil || status.NextScheduledAt != nil || status.LastSuccessAt != nil || status.LatestRun == nil || status.LatestRun.ID != first.ID || status.LatestRun.Counts.Queued != "2" {
		t.Fatalf("queued work was scheduled again or lost: %+v, %v", status, err)
	}
	processQueued(t, s)
	var success time.Time
	if err = pool.QueryRow(ctx, `SELECT max(finished_at) FROM collection_jobs WHERE state='succeeded'`).Scan(&success); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	exec(t, pool, `UPDATE collection_jobs SET started_at=$1,retry_after=$2 WHERE account_id=$3`, base.Add(-2*time.Minute), base.Add(10*time.Minute), a.ID)
	exec(t, pool, `UPDATE collection_jobs SET started_at=$1,retry_after=$2 WHERE account_id=$3`, base.Add(-time.Minute), base.Add(time.Minute), b.ID)
	status, err = s.GetMonitoringStatus(ctx, token)
	if err != nil || status.NextScheduledAt == nil || !status.NextScheduledAt.Equal(base.Add(4*time.Minute)) || status.LastSuccessAt == nil || !status.LastSuccessAt.Equal(success) || status.LatestRun.Counts.Succeeded != "2" {
		t.Fatalf("earliest schedule bypassed cooldown or lost success: %+v, %v", status, err)
	}
	observation, err := s.GetAccountObservation(ctx, token, b.ID)
	if err != nil || observation.NextScheduledAt == nil || !observation.NextScheduledAt.Equal(*status.NextScheduledAt) {
		t.Fatal("global and account planners disagree")
	}
	// Cross 9/10 without dispatching skipped cooldown work.
	for range 9 {
		queueAccount(t, s, token, c, b.ID)
	}
	advanceCollectionFixture(t, pool)
	upstream.mode.Store(1)
	latest := queueAccount(t, s, token, c, b.ID)
	processQueued(t, s)
	exec(t, pool, `UPDATE collection_runs SET created_at=$1 WHERE id=$2`, base.Add(-time.Hour), latest.ID)
	calls := upstream.calls.Load()
	restarted, err := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	status, err = restarted.GetMonitoringStatus(ctx, token)
	if err != nil || status.LatestRun == nil || status.LatestRun.ID != latest.ID || status.LatestRun.Counts.Failed != "1" || status.LastSuccessAt == nil || !status.LastSuccessAt.Equal(success) {
		t.Fatalf("latest failure replaced success or numeric latest run was lost: %+v, %v", status, err)
	}
	history, err := s.ListCollectionRuns(ctx, token, "0", 1)
	if err != nil || len(history.Runs) != 1 || history.Runs[0].ID != first.ID || history.NextAfter == nil {
		t.Fatal("status change altered ascending history pagination")
	}
	policy.Enabled = false
	policy, err = s.UpdateMonitoringConfig(ctx, token, policy)
	if err != nil {
		t.Fatal(err)
	}
	status, err = s.GetMonitoringStatus(ctx, token)
	if err != nil || status.NextScheduledAt != nil || status.LastSuccessAt == nil || !status.LastSuccessAt.Equal(success) || status.LatestRun.ID != latest.ID || upstream.calls.Load() != calls {
		t.Fatalf("pause/status read changed historical facts or contacted upstream: %+v, %v", status, err)
	}
}

func TestMonitoringStatusHasNoScheduleWithoutCurrentCredentialsOrAuthority(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, upstream := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	_, err = s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "clear"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.GetMonitoringStatus(ctx, token)
	if err != nil || !status.Enabled || status.NextScheduledAt != nil {
		t.Fatalf("cleared credential still had a schedule: %+v, %v", status, err)
	}
	observation, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || observation.NextScheduledAt != nil {
		t.Fatal("account advertised a schedule without credentials")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.scheduleCollections(ctx, conn)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	status, err = s.GetMonitoringStatus(ctx, token)
	if err != nil || status.LatestRun != nil || upstream.calls.Load() != 1 {
		t.Fatal("worker queued unavailable work or status dispatched upstream")
	}
	save(t, s, token, "https://other.example")
	status, err = s.GetMonitoringStatus(ctx, token)
	if err != nil || status.NextScheduledAt != nil {
		t.Fatal("old-source account acquired a new schedule")
	}
	_, err = s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "status-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"accounts"}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, "status-reader", "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetMonitoringStatus(ctx, reader); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary reader received global monitoring history")
	}
	if err = s.auth.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetMonitoringStatus(ctx, token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("revoked administrator retained status access")
	}
}

func TestMonitoringStatusRechecksSessionAfterObservedPolicyWait(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(ctx)
	if _, err = locker.Exec(ctx, `SELECT revision FROM monitoring_config FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(1200 * time.Millisecond)
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$1`, expires)
	done := make(chan error, 1)
	go func() { _, err := s.GetMonitoringStatus(ctx, token); done <- err }()
	waiting := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT revision::text,enabled,interval_seconds%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("status did not reach the actual policy lock barrier")
	}
	time.Sleep(time.Until(expires) + 30*time.Millisecond)
	if err = locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired wait released global status: %v", err)
	}
}
