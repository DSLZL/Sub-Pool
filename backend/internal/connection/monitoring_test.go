package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func TestPassiveQuotaPreservesRawWindowsAndActualSourceTime(t *testing.T) {
	body := []byte(`{"extra":{"codex_usage_updated_at":"2026-11-01T10:00:00+08:00","codex_5h_used_percent":0,"codex_5h_reset_after_seconds":60,"codex_7d_used_percent":99.123456789012345678,"private":"never expose"},"credentials":{"access_token":"never expose"}}`)
	q, ok := passiveQuota(body)
	if !ok || q.SourceAt == nil || q.SourceAt.Format(time.RFC3339) != "2026-11-01T02:00:00Z" || len(q.Windows) != 2 || *q.Windows[0].UsedPercent != "0" || *q.Windows[1].UsedPercent != "99.123456789012345678" || q.Windows[0].DurationSeconds != nil || q.Windows[0].ResetAt.Sub(*q.SourceAt) != time.Minute {
		t.Fatalf("source time/zero/precision lost: %+v", q)
	}
	encoded, _ := json.Marshal(q)
	if bytes.Contains(encoded, []byte("never expose")) || bytes.Contains(encoded, []byte("credentials")) {
		t.Fatal("raw private fields retained")
	}
	q, ok = passiveQuota([]byte(`{"extra":{"codex_5h_reset_after_seconds":60,"codex_5h_used_percent":10}}`))
	if !ok || q.SourceAt != nil || q.Windows[0].ResetAt != nil {
		t.Fatal("relative reset was anchored to retrieval time")
	}
	q, ok = passiveQuota([]byte(`{"extra":{"codex_5h_used_percent":70,"codex_5h_reset_at":"2000-01-01T00:00:00Z"}}`))
	if !ok || *q.Windows[0].UsedPercent != "70" {
		t.Fatal("old reset invented zero usage")
	}
	for _, body := range []string{`{"extra":{"codex_5h_used_percent":"0"}}`, `{"extra":{"codex_5h_used_percent":-1}}`, `{"extra":{"codex_usage_updated_at":"invalid"}}`, `{"extra":{"codex_5h_window_minutes":-1}}`, `{"extra":{"codex_5h_used_percent":1e999999}}`, `{"extra":[]}`} {
		if _, ok = passiveQuota([]byte(body)); ok {
			t.Fatalf("invalid snapshot accepted: %s", body)
		}
	}
	q, ok = passiveQuota([]byte(`{"extra":{}}`))
	if !ok || len(q.Windows) != 0 {
		t.Fatal("missing windows became zero")
	}
}

func TestPassiveQuotaZeroDurationPreservesOtherNativeFacts(t *testing.T) {
	q, ok := passiveQuota([]byte(`{"extra":{"codex_usage_updated_at":"2026-11-09T18:49:31+08:00","codex_5h_used_percent":0,"codex_5h_window_minutes":0,"codex_5h_reset_at":"2026-11-09T18:49:31+08:00","codex_7d_used_percent":56,"codex_7d_window_minutes":10080,"codex_7d_reset_at":"2026-11-11T14:36:29+08:00"}}`))
	if !ok || len(q.Windows) != 2 {
		t.Fatal("zero native duration rejected the valid adjacent window")
	}
	short, weekly := q.Windows[0], q.Windows[1]
	if short.DurationSeconds != nil || short.UsedPercent == nil || *short.UsedPercent != "0" || short.ResetAt == nil || short.ResetAt.Format(time.RFC3339) != "2026-11-09T10:49:31Z" {
		t.Fatalf("zero duration invented a period or discarded reported facts: %+v", short)
	}
	if weekly.DurationSeconds == nil || *weekly.DurationSeconds != "604800" || weekly.UsedPercent == nil || *weekly.UsedPercent != "56" {
		t.Fatalf("valid seven-day window lost: %+v", weekly)
	}
	for _, prefix := range []string{"codex_5h_", "codex_7d_"} {
		q, ok = passiveQuota([]byte(`{"extra":{"` + prefix + `window_minutes":0}}`))
		if !ok || len(q.Windows) != 1 || q.Windows[0].DurationSeconds != nil || q.Windows[0].UsedPercent != nil || q.Windows[0].ResetAt != nil {
			t.Fatal("zero duration alone invented quota facts")
		}
		for _, value := range []string{`-1`, `0.5`, `"0"`, `5256001`} {
			if _, valid := passiveQuota([]byte(`{"extra":{"` + prefix + `window_minutes":` + value + `}}`)); valid {
				t.Fatalf("invalid duration accepted: %s", value)
			}
		}
	}
}

type monitorUpstream struct {
	calls, mode      atomic.Int32
	block            atomic.Bool
	arrived, release chan struct{}
}

func monitoringServer(t *testing.T) (*httptest.Server, *monitorUpstream) {
	t.Helper()
	f := &monitorUpstream{arrived: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") || strings.Contains(r.URL.Path, "usage") || strings.Contains(r.URL.Path, "quota") || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("collector exceeded fixed passive GET boundary")
			http.Error(w, "bad", 400)
			return
		}
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
		if err != nil {
			t.Error("unexpected endpoint")
			http.Error(w, "bad", 400)
			return
		}
		if f.block.CompareAndSwap(true, false) {
			close(f.arrived)
			select {
			case <-f.release:
			case <-r.Context().Done():
				return
			}
		}
		mode := f.mode.Load()
		if mode == 1 && id == 2 {
			http.Error(w, "private reflected upstream error", 403)
			return
		}
		if mode == 5 {
			w.Header().Set("Retry-After", "120")
			http.Error(w, "private rate-limit body", 429)
			return
		}
		resource := fmt.Sprintf("private-resource-%d", id)
		if mode == 4 {
			resource = "private-changed-resource"
		}
		a := fullManagedAccount(id, resource)
		extra := map[string]any{"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339Nano), "codex_5h_used_percent": 0, "codex_5h_window_minutes": 180, "codex_5h_reset_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "codex_7d_used_percent": 42}
		if mode == 2 {
			extra = map[string]any{}
		}
		if mode == 3 {
			extra["codex_usage_updated_at"] = "2000-01-01T00:00:00Z"
			extra["codex_5h_reset_at"] = "2000-01-02T00:00:00Z"
			extra["codex_5h_used_percent"] = 70
		}
		if mode == 6 {
			delete(extra, "codex_usage_updated_at")
		}
		a["extra"] = extra
		respond(w, a)
	}))
	t.Cleanup(server.Close)
	return server, f
}

func passiveManaged(t *testing.T, s *Service, token string, c Configuration, id int) ManagedAccount {
	t.Helper()
	a := adoptManaged(t, s, token, c, id, fmt.Sprintf("Account %d", id))
	v, err := s.UpdateManaged(context.Background(), token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: a.Name, Monitoring: true, SourceMode: "passive"})
	if err != nil {
		t.Fatal(err)
	}
	return v.Account
}

func queueAccount(t *testing.T, s *Service, token string, c Configuration, ids ...string) CollectionRun {
	t.Helper()
	r, err := s.QueueCollection(context.Background(), token, CollectInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, AccountIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func processQueued(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for i := 0; i < 20; i++ {
		processed, err := s.processCollection(ctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			return
		}
	}
	t.Fatal("fixture did not drain")
}

func advanceCollectionFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	exec(t, pool, `UPDATE collection_jobs SET started_at=started_at-interval '2 minutes',retry_after=clock_timestamp()-interval '1 second'`)
}

func TestMonitoringDurablePartialResultsAndCurrentAccountScope(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	b := passiveManaged(t, s, token, c, 2)
	_ = adoptManaged(t, s, token, c, 3, "Active blocked")
	paused := passiveManaged(t, s, token, c, 4)
	if _, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: paused.ID, Revision: paused.Revision, Name: paused.Name, SourceMode: "passive"}); err != nil {
		t.Fatal(err)
	}
	f.mode.Store(1)
	r, err := s.QueueCollection(ctx, token, CollectInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, All: true, AccountIDs: []string{}})
	if err != nil || r.Counts.Queued != "2" || r.Counts.Skipped != "2" {
		t.Fatalf("queue acknowledgement invented completion: %+v %v", r, err)
	}
	duplicate := queueAccount(t, s, token, c, a.ID)
	if duplicate.Counts.Conflict != "1" {
		t.Fatal("overlapping run dispatched twice")
	}
	processQueued(t, s)
	v, err := s.GetCollectionRun(ctx, token, r.ID, "0", 1)
	if err != nil || v.Run.State != "completed" || v.Run.Counts.Succeeded != "1" || v.Run.Counts.Failed != "1" || v.Run.Counts.Skipped != "2" || v.NextAfter == nil {
		t.Fatalf("partial results lost: %+v %v", v, err)
	}
	page, err := s.GetCollectionRun(ctx, token, r.ID, *v.NextAfter, 100)
	if err != nil || len(page.Jobs) != 3 {
		t.Fatal("job cursor lost per-account results")
	}
	view, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || view.Freshness != "fresh" || view.Snapshot == nil || *view.Snapshot.Windows[0].UsedPercent != "0" || *view.Snapshot.Windows[0].DurationSeconds != "10800" {
		t.Fatalf("actual quota missing: %+v %v", view, err)
	}
	bad, _ := s.GetAccountObservation(ctx, token, b.ID)
	if bad.Snapshot != nil || bad.LatestAttempt == nil || bad.LatestAttempt.Read.Reason != "FORBIDDEN" {
		t.Fatal("failed account invented quota")
	}
	encoded, _ := json.Marshal([]any{v, page, view, bad})
	for _, secret := range []string{"private-", "test_only_secret", "actor_digest", "password"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("private data escaped")
		}
	}
	member, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "monitor-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"accounts"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("198.51.100.7"))
	if err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	if _, err = s.GetAccountObservation(ctx, memberToken, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetAccountObservation(ctx, memberToken, b.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("hidden account exposed")
	}
	if _, err = s.GetCollectionRun(ctx, memberToken, r.ID, "0", 100); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("member received global run results")
	}
	if _, err = s.QueueCollection(ctx, memberToken, CollectInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, AccountIDs: []string{a.ID}}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("read grant authorized collection write")
	}
	member.Grants.Features = []string{}
	if _, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: member.ID, Revision: member.Revision, Username: member.Username, Enabled: true, Grants: member.Grants}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetAccountObservation(ctx, memberToken, a.ID); !errors.Is(err, auth.ErrForbidden) || f.calls.Load() != before {
		t.Fatal("current grants not enforced before protected read")
	}
}

func TestMonitoringCollectorClaimsNumericJobOrder(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, _ := monitoringServer(t)
	c := save(t, s, token, server.URL)
	for i := 1; i <= 12; i++ {
		passiveManaged(t, s, token, c, i)
	}
	run, err := s.QueueCollection(ctx, token, CollectInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, All: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for i := 0; i < 2; i++ {
		processed, err := s.processCollection(ctx, conn)
		if err != nil || !processed {
			t.Fatalf("queued collection did not run: %v", err)
		}
	}
	page, err := s.GetCollectionRun(ctx, token, run.ID, "0", 2)
	if err != nil || len(page.Jobs) != 2 || page.Jobs[1].ID != "2" || page.Jobs[1].State != "succeeded" {
		t.Fatalf("collector did not claim oldest numeric job: %+v %v", page, err)
	}
	after, seen := "0", 0
	for {
		page, err := s.GetCollectionRun(ctx, token, run.ID, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range page.Jobs {
			seen++
			if job.ID != strconv.Itoa(seen) {
				t.Fatalf("numeric job cursor reordered or omitted results: %s after %s", job.ID, after)
			}
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if seen != 12 {
		t.Fatalf("job cursor lost results: %d", seen)
	}
}

func TestMonitoringNumericCursorsAndLatestAttempt(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 2)
	for i := 1; i <= 12; i++ {
		if i == 12 {
			advanceCollectionFixture(t, pool)
			f.mode.Store(1)
		}
		queueAccount(t, s, token, c, a.ID)
		processQueued(t, s)
	}
	observation, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || observation.LatestAttempt == nil || observation.LatestAttempt.ID != "12" || observation.LatestAttempt.State != "failed" || observation.Snapshot == nil {
		t.Fatalf("latest numeric attempt or previous facts lost: %+v %v", observation, err)
	}
	after, seen := "0", 0
	for {
		page, err := s.ListCollectionRuns(ctx, token, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Runs {
			seen++
			if run.ID != strconv.Itoa(seen) {
				t.Fatalf("numeric run cursor reordered or omitted results: %s after %s", run.ID, after)
			}
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if seen != 12 {
		t.Fatalf("run cursor lost results: %d", seen)
	}
}

func TestMonitoringCooldownFailureRetentionAndRawSourceFreshness(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	first, _ := s.GetAccountObservation(ctx, token, a.ID)
	advanceCollectionFixture(t, pool)
	f.mode.Store(5)
	r := queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	v, _ := s.GetCollectionRun(ctx, token, r.ID, "0", 10)
	if v.Jobs[0].Read.Reason != "RATE_LIMITED" || v.Jobs[0].RetryAfter == nil || time.Until(*v.Jobs[0].RetryAfter) < 110*time.Second {
		t.Fatal("Retry-After was not persisted")
	}
	before := f.calls.Load()
	again := queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	if again.Counts.Skipped != "1" || f.calls.Load() != before {
		t.Fatal("manual dispatch bypassed cooldown")
	}
	failed, _ := s.GetAccountObservation(ctx, token, a.ID)
	if failed.Snapshot == nil || !failed.Snapshot.ReceivedAt.Equal(first.Snapshot.ReceivedAt) {
		t.Fatal("failure erased or refreshed prior success")
	}
	policy, _ := s.GetMonitoringConfig(ctx, token)
	policy.Enabled = true
	if _, err := s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.scheduleCollections(ctx, conn); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	processQueued(t, s)
	if f.calls.Load() != before {
		t.Fatal("configuration refresh bypassed persisted cooldown")
	}
	advanceCollectionFixture(t, pool)
	f.mode.Store(3)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	stale, _ := s.GetAccountObservation(ctx, token, a.ID)
	if stale.Freshness != "stale" || !stale.StaleWarning || !stale.Snapshot.Windows[0].ResetExpired || *stale.Snapshot.Windows[0].UsedPercent != "70" {
		t.Fatal("old source became current or reset invented zero")
	}
	advanceCollectionFixture(t, pool)
	f.mode.Store(6)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	unknown, _ := s.GetAccountObservation(ctx, token, a.ID)
	if unknown.Freshness != "unknown" || unknown.Snapshot.SourceAt != nil {
		t.Fatal("retrieval time substituted for missing source time")
	}
	advanceCollectionFixture(t, pool)
	f.mode.Store(2)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	missing, _ := s.GetAccountObservation(ctx, token, a.ID)
	if missing.Snapshot == nil || !missing.Snapshot.ReceivedAt.Equal(unknown.Snapshot.ReceivedAt) || missing.LatestAttempt.Reason != "SNAPSHOT_MISSING" {
		t.Fatal("missing windows replaced prior facts")
	}
}

func TestMonitoringQueuedBindingsAndSessionRevocationPreventDispatch(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	for _, change := range []string{"connection", "account", "policy", "logout"} {
		advanceCollectionFixture(t, pool)
		r := queueAccount(t, s, token, c, a.ID)
		before := f.calls.Load()
		want := ""
		switch change {
		case "connection":
			c = save(t, s, token, server.URL)
			want = "CONNECTION_CHANGED"
		case "account":
			v, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Changed", Monitoring: true, SourceMode: "passive"})
			if err != nil {
				t.Fatal(err)
			}
			a = v.Account
			want = "ACCOUNT_CHANGED"
		case "policy":
			p, _ := s.GetMonitoringConfig(ctx, token)
			p.StaleWarningsEnabled = false
			if _, err := s.UpdateMonitoringConfig(ctx, token, p); err != nil {
				t.Fatal(err)
			}
			want = "POLICY_CHANGED"
		case "logout":
			if err := s.auth.Logout(ctx, token); err != nil {
				t.Fatal(err)
			}
			want = "ACTOR_UNAVAILABLE"
		}
		processQueued(t, s)
		if change == "logout" {
			_, fresh, err := s.auth.Login(ctx, "admin", "TestPassword123", netip.MustParseAddr("127.0.0.1"))
			if err != nil {
				t.Fatal(err)
			}
			token = fresh
		}
		v, err := s.GetCollectionRun(ctx, token, r.ID, "0", 100)
		if err != nil || v.Jobs[0].Reason != want || f.calls.Load() != before {
			t.Fatalf("%s queued work retargeted or outlived authority: %+v %v", change, v, err)
		}
	}
}

func TestMonitoringQuarantinesChangedResourcesWithoutRebindingHistory(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	first, _ := s.GetAccountObservation(ctx, token, a.ID)
	advanceCollectionFixture(t, pool)
	f.mode.Store(4)
	r := queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	v, _ := s.GetCollectionRun(ctx, token, r.ID, "0", 10)
	view, _ := s.GetAccountObservation(ctx, token, a.ID)
	if v.Jobs[0].State != "conflict" || v.Jobs[0].Reason != "RESOURCE_IDENTITY_CHANGED" || view.Freshness != "identity_unverified" || !view.Snapshot.ReceivedAt.Equal(first.Snapshot.ReceivedAt) {
		t.Fatal("quota history silently rebound to different resource")
	}
	managed, _ := s.GetManaged(ctx, token, a.ID)
	if managed.Account.IdentityState != "conflict" {
		t.Fatal("resource conflict not durable")
	}
}

func TestMonitoringIntentAndResultAuditFailuresRollBackAndRecoverUnknown(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, _ := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	exec(t, pool, `CREATE FUNCTION reject_monitor_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject fixture audit'; END $$; CREATE TRIGGER reject_monitor_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_monitor_audit()`)
	p, _ := s.GetMonitoringConfig(ctx, token)
	p.Enabled = true
	if _, err := s.UpdateMonitoringConfig(ctx, token, p); err == nil {
		t.Fatal("policy committed without audit")
	}
	if _, err := s.QueueCollection(ctx, token, CollectInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, AccountIDs: []string{a.ID}}); err == nil {
		t.Fatal("job committed without intent")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM collection_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed queue left durable intent")
	}
	p, _ = s.GetMonitoringConfig(ctx, token)
	if p.Enabled || p.Revision != "1" {
		t.Fatal("failed config save persisted")
	}
	exec(t, pool, `DROP TRIGGER reject_monitor_audit ON management_events`)
	r := queueAccount(t, s, token, c, a.ID)
	exec(t, pool, `CREATE TRIGGER reject_monitor_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_monitor_audit()`)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = s.processCollection(ctx, conn); err == nil {
		t.Fatal("result succeeded without journal")
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM quota_observations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("snapshot committed without result journal")
	}
	exec(t, pool, `DROP TRIGGER reject_monitor_audit ON management_events`)
	if err = recoverCollections(ctx, conn); err != nil {
		t.Fatal(err)
	}
	v, _ := s.GetCollectionRun(ctx, token, r.ID, "0", 10)
	if v.Jobs[0].State != "interrupted" {
		t.Fatal("abandoned running job not distinguished from failure/success")
	}
	var unknown int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE operation_id=$1 AND outcome='unknown'`, "collection:job:"+v.Jobs[0].ID).Scan(&unknown); err != nil || unknown != 1 {
		t.Fatal("correlated unknown result missing")
	}
}

func TestMonitoringLeaderReplacementPreservesSuccessAndInterruptsAbandonedReads(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	ids := []string{}
	for i := 1; i <= 3; i++ {
		a := passiveManaged(t, s, token, c, i)
		ids = append(ids, a.ID)
	}
	r := queueAccount(t, s, token, c, ids...)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := s.processCollection(ctx, conn); err != nil || !processed {
		t.Fatal(err)
	}
	conn.Release()
	v, _ := s.GetCollectionRun(ctx, token, r.ID, "0", 100)
	exec(t, pool, `UPDATE collection_jobs SET state='running',reason='RUNNING',started_at=clock_timestamp()-interval '2 minutes' WHERE id=`+v.Jobs[1].ID)
	f.block.Store(true)
	firstCtx, firstCancel := context.WithCancel(ctx)
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); s.RunCollector(firstCtx) }()
	defer func() { firstCancel(); <-firstDone }()
	select {
	case <-f.arrived:
	case <-time.After(4 * time.Second):
		t.Fatal("first leader never reached actual upstream read")
	}
	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var acquired bool
	if err = probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, collectorLock).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if acquired {
		_, _ = probe.Exec(ctx, `SELECT pg_advisory_unlock($1)`, collectorLock)
	}
	probe.Release()
	if acquired {
		t.Fatal("another worker could acquire the live leader lock")
	}
	replacement, _ := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	secondCtx, secondCancel := context.WithCancel(ctx)
	secondDone := make(chan struct{})
	go func() { defer close(secondDone); replacement.RunCollector(secondCtx) }()
	defer func() { secondCancel(); <-secondDone }()
	firstCancel()
	<-firstDone
	deadline := time.Now().Add(6 * time.Second)
	for {
		v, err = s.GetCollectionRun(ctx, token, r.ID, "0", 100)
		if err != nil {
			t.Fatal(err)
		}
		if v.Run.State == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement did not recover durable running jobs")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v.Run.Counts.Succeeded != "1" || v.Run.Counts.Interrupted != "2" {
		t.Fatalf("restart fabricated success or lost partial result: %+v", v.Run)
	}
	var snapshots int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM quota_observations`).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatal("restart lost committed fact or committed abandoned read")
	}
	status, err := replacement.GetMonitoringStatus(ctx, token)
	if err != nil || !status.WorkerRecent || status.Enabled || status.ActiveAvailable {
		t.Fatal("worker liveness confused with enabled/active capability")
	}
	advanceCollectionFixture(t, pool)
	fresh := queueAccount(t, s, token, c, v.Jobs[2].AccountID)
	deadline = time.Now().Add(6 * time.Second)
	for {
		page, err := replacement.GetCollectionRun(ctx, token, fresh.ID, "0", 10)
		if err != nil {
			t.Fatal(err)
		}
		if page.Run.State == "completed" {
			if page.Run.Counts.Succeeded != "1" {
				t.Fatal("replacement did not execute explicit recovery read")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement worker stalled")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMonitoringEnabledWorkerSchedulesWithoutBrowserAndPausePreservesFacts(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := monitoringServer(t)
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 1)
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	policy, err = s.UpdateMonitoringConfig(ctx, token, policy)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.RunCollector(workerCtx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(4 * time.Second)
	for {
		var succeeded int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM collection_jobs j JOIN collection_runs r ON r.id=j.run_id WHERE r.reason='scheduled' AND r.actor_id IS NULL AND r.actor_digest IS NULL AND j.state='succeeded'`).Scan(&succeeded); err != nil {
			t.Fatal(err)
		}
		if succeeded == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("enabled worker did not collect independently of a browser request")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	policy.Enabled = false
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	advanceCollectionFixture(t, pool)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.scheduleCollections(ctx, conn)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM collection_jobs`).Scan(&jobs); err != nil || jobs != 1 || f.calls.Load() != 2 {
		t.Fatal("pause created another job or upstream request")
	}
	observation, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || observation.Snapshot == nil || observation.NextScheduledAt != nil {
		t.Fatal("pause removed a committed observation or retained normal scheduling")
	}
}

func TestMonitoringQueuedSessionExpiryAfterObservedNetworkAndConfigWait(t *testing.T) {
	for _, where := range []string{"network", "configuration"} {
		t.Run(where, func(t *testing.T) {
			s, pool, token := setup(t)
			ctx := context.Background()
			server, f := monitoringServer(t)
			c := save(t, s, token, server.URL)
			a := passiveManaged(t, s, token, c, 1)
			if where == "network" {
				r := queueAccount(t, s, token, c, a.ID)
				expires := time.Now().UTC().Add(1200 * time.Millisecond)
				if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET idle_expires_at=$1`, expires); err != nil {
					t.Fatal(err)
				}
				f.block.Store(true)
				done := make(chan error, 1)
				go func() {
					conn, err := pool.Acquire(ctx)
					if err != nil {
						done <- err
						return
					}
					defer conn.Release()
					_, err = s.processCollection(ctx, conn)
					done <- err
				}()
				select {
				case <-f.arrived:
				case <-time.After(time.Second):
					close(f.release)
					t.Fatal("expiry fixture did not reach actual upstream barrier")
				}
				time.Sleep(time.Until(expires) + 30*time.Millisecond)
				close(f.release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				var state string
				var facts int
				if err := pool.QueryRow(ctx, `SELECT reason FROM collection_jobs WHERE run_id=$1`, r.ID).Scan(&state); err != nil || state != "ACTOR_UNAVAILABLE" {
					t.Fatal("expired queued actor persisted protected result")
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM quota_observations`).Scan(&facts); err != nil || facts != 0 {
					t.Fatal("late response became a protected snapshot")
				}
			} else {
				locker, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer locker.Rollback(ctx)
				if _, err = locker.Exec(ctx, `SELECT revision FROM monitoring_config FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				expires := time.Now().UTC().Add(1200 * time.Millisecond)
				if _, err = pool.Exec(ctx, `UPDATE auth_sessions SET idle_expires_at=$1`, expires); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, err := s.QueueCollection(ctx, token, CollectInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, AccountIDs: []string{a.ID}})
					done <- err
				}()
				deadline := time.Now().Add(time.Second)
				waiting := false
				for time.Now().Before(deadline) {
					var count int
					err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT revision::text,enabled,interval_seconds%'`).Scan(&count)
					if err != nil {
						t.Fatal(err)
					}
					if count > 0 {
						waiting = true
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("did not observe actual configuration lock wait")
				}
				time.Sleep(time.Until(expires) + 30*time.Millisecond)
				if err = locker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatalf("expired wait persisted a job: %v", err)
				}
				var jobs int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM collection_jobs`).Scan(&jobs); err != nil || jobs != 0 {
					t.Fatal("expired queue left a job")
				}
			}
		})
	}
}

func TestMonitoringScheduleUsesActualResetAndNeverOverridesCooldown(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	policy := MonitoringConfig{Revision: "1", Enabled: true, IntervalSeconds: 300, StaleAfterSeconds: 1800, NearResetLeadSeconds: 600, NearResetIntervalSeconds: 60}
	a := storedAccount{ManagedAccount: ManagedAccount{Monitoring: true, SourceCurrent: true, ConnectionAvailable: true, SourceMode: "passive", IdentityState: "verified", SourceID: "source", Revision: "1"}}
	c := storedConfig{Configuration: Configuration{Revision: "1"}}
	base := now.Add(-120 * time.Second)
	reset := now.Add(time.Minute)
	h := collectionHistory{Job: &CollectionJob{QueuedAt: base, StartedAt: &base, State: "succeeded", AccountRevision: "1", ConnectionRevision: "1", PolicyRevision: "1"}, Snapshot: &QuotaSnapshot{SourceID: "source", ConnectionRevision: "1", Windows: []QuotaWindow{{ResetAt: &reset}}}}
	normal := nextCollection(now, policy, a, c, h)
	if normal == nil || !normal.Equal(base.Add(300*time.Second)) {
		t.Fatal("normal interval not based on actual attempt")
	}
	policy.NearResetEnabled = true
	near := nextCollection(now, policy, a, c, h)
	if near == nil || !near.Equal(base.Add(time.Minute)) {
		t.Fatal("near reset preference ignored")
	}
	cooldown := now.Add(time.Hour)
	h.Cooldown = &cooldown
	if next := nextCollection(now, policy, a, c, h); next == nil || !next.Equal(cooldown) {
		t.Fatal("reset refresh bypassed rate limit")
	}
	a.SourceMode = "active"
	if nextCollection(now, policy, a, c, h) != nil {
		t.Fatal("scheduler enabled unproven active query")
	}
	a.SourceMode = "passive"
	a.ConnectionAvailable = false
	if nextCollection(now, policy, a, c, h) != nil {
		t.Fatal("unavailable connection kept automatic schedule")
	}
	a.ConnectionAvailable = true
	policy.Enabled = false
	if nextCollection(now, policy, a, c, h) != nil {
		t.Fatal("paused policy kept automatic schedule")
	}
}
