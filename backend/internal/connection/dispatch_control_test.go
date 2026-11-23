package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

type dispatchFixture struct {
	s                                *Service
	db                               *pgxpool.Pool
	token                            string
	c                                Configuration
	a                                ManagedAccount
	mu                               sync.Mutex
	enabled                          bool
	updated                          time.Time
	resource, mode, status, postMode string
	failRead                         bool
	writes                           []bool
	reads                            int
	onRead                           func(int)
	postEntered, postRelease         chan struct{}
}

func newDispatchFixture(t *testing.T, enabled bool) *dispatchFixture {
	t.Helper()
	s, db, token := setup(t)
	f := &dispatchFixture{s: s, db: db, token: token, enabled: enabled, updated: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), resource: "controlled-resource", mode: "standard", status: "active"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer controlled-test-jwt" || r.Header.Get("x-api-key") != "" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
			t.Error("unexpected native credential or query")
		}
		if r.Method == "GET" && r.URL.Path == "/api/v1/auth/me" {
			f.mu.Lock()
			mode := f.mode
			f.mu.Unlock()
			respond(w, map[string]any{"role": "admin", "run_mode": mode})
			return
		}
		f.mu.Lock()
		if r.Method == "POST" && r.URL.Path == "/api/v1/admin/accounts/1/schedulable" {
			var in struct {
				Schedulable *bool `json:"schedulable"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil || in.Schedulable == nil || r.Header.Get("Content-Type") != "application/json" {
				f.mu.Unlock()
				t.Error("invalid native payload")
				w.WriteHeader(400)
				return
			}
			f.writes = append(f.writes, *in.Schedulable)
			f.enabled = *in.Schedulable
			f.updated = f.updated.Add(time.Microsecond)
			mode, entered, release := f.postMode, f.postEntered, f.postRelease
			f.mu.Unlock()
			if entered != nil {
				close(entered)
				<-release
			}
			if mode == "timeout" {
				<-r.Context().Done()
				return
			}
			if mode == "bad_response" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				return
			}
			f.mu.Lock()
		} else if r.Method == "GET" && r.URL.Path == "/api/v1/admin/accounts/1" {
			f.reads++
			reads, hook, fail := f.reads, f.onRead, f.failRead
			f.mu.Unlock()
			if hook != nil {
				hook(reads)
			}
			if fail {
				w.WriteHeader(429)
				return
			}
			f.mu.Lock()
		} else {
			f.mu.Unlock()
			t.Error("unexpected native request", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		body := fullManagedAccount(1, f.resource)
		body["schedulable"], body["updated_at"], body["status"] = f.enabled, f.updated, f.status
		f.mu.Unlock()
		respond(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := s.Get(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Save(context.Background(), token, SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "Bearer controlled-test-jwt", Confirmation: "更换地址"})
	if err != nil {
		t.Fatal(err)
	}
	f.c = result.Configuration
	f.a = adoptManaged(t, s, token, f.c, 1, "Controlled account")
	setTestWriteGrant(t, s, token, writeScheduling, true)
	return f
}

func dispatchRequest(t *testing.T, f *dispatchFixture) DispatchPauseInput {
	t.Helper()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return DispatchPauseInput{AccountReadInput: readInput(f.a, f.c), RequestID: base64.RawURLEncoding.EncodeToString(id), RestoreAt: time.Now().UTC().Add(time.Hour), AcknowledgeAccountWide: true}
}

func pauseDispatch(t *testing.T, f *dispatchFixture) DispatchPause {
	t.Helper()
	p, err := f.s.PauseDispatch(context.Background(), f.token, dispatchRequest(t, f))
	if err != nil || p.State != "paused" {
		t.Fatal("pause not confirmed", p, err)
	}
	return p
}

func TestDispatchControlPausesReschedulesRestoresAndKeepsImmutableHistory(t *testing.T) {
	f := newDispatchFixture(t, true)
	ctx := context.Background()
	control, err := f.s.GetDispatchControl(ctx, f.token)
	if err != nil || !control.CanPause || !control.AutomaticRestoreAvailable {
		t.Fatal(control, err)
	}
	in := dispatchRequest(t, f)
	p, err := f.s.PauseDispatch(ctx, f.token, in)
	if err != nil || p.State != "paused" || p.Reason != "VERIFIED_PAUSE" || !p.PreviousSchedulable {
		t.Fatal(p, err)
	}
	duplicate, err := f.s.PauseDispatch(ctx, f.token, in)
	if err != nil || duplicate != p {
		t.Fatal("request replay changed result", duplicate, err)
	}
	in.RestoreAt = in.RestoreAt.Add(time.Hour)
	if _, err = f.s.PauseDispatch(ctx, f.token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("request ID accepted another body", err)
	}
	if _, err = f.s.PauseDispatch(ctx, f.token, dispatchRequest(t, f)); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("overlapping pause accepted", err)
	}
	p, err = f.s.RescheduleDispatch(ctx, f.token, DispatchRescheduleInput{DispatchChangeInput: DispatchChangeInput{p.ID, p.Revision}, RestoreAt: in.RestoreAt})
	if err != nil || p.Reason != "DEADLINE_CHANGED" {
		t.Fatal(p, err)
	}
	stale := DispatchChangeInput{p.ID, p.Revision}
	p, err = f.s.RestoreDispatch(ctx, f.token, stale)
	if err != nil || p.State != "restored" || p.Reason != "VERIFIED_RESTORE" {
		t.Fatal(p, err)
	}
	if _, err = f.s.RestoreDispatch(ctx, f.token, stale); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("restore replay accepted", err)
	}
	f.mu.Lock()
	enabled, writes := f.enabled, append([]bool{}, f.writes...)
	f.mu.Unlock()
	if !enabled || len(writes) != 2 || writes[0] || !writes[1] {
		t.Fatal("native write sequence", writes)
	}
	page, err := f.s.ListDispatchHistory(ctx, f.token, DispatchHistoryInput{AccountID: f.a.ID, Limit: 2})
	if err != nil || len(page.Events) != 2 || page.NextAfter == nil || page.Events[0].Pause.State != "pausing" || page.Events[1].Pause.State != "paused" {
		t.Fatal(page, err)
	}
	seen := len(page.Events)
	for page.NextAfter != nil {
		page, err = f.s.ListDispatchHistory(ctx, f.token, DispatchHistoryInput{AccountID: f.a.ID, After: *page.NextAfter, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page.Events)
	}
	if seen != 5 || page.Events[len(page.Events)-1].Pause != p {
		t.Fatal("history omitted or rewrote a transition", seen, page)
	}
	got, err := f.s.GetDispatchPause(ctx, f.token, p.ID)
	if err != nil || got != p {
		t.Fatal("persisted read differs", got, err)
	}
	encoded, _ := json.Marshal(page)
	for _, secret := range []string{"controlled-test-jwt", "private-account-token", "controlled-resource", "actor_id", "request_digest", "credentials"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("history leaked private data", secret)
		}
	}
}

func TestDispatchControlKeepsPreviouslyDisabledAccountDisabledAcrossServiceReplacement(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "already_disabled", true: "enabled"}[enabled], func(t *testing.T) {
			f := newDispatchFixture(t, enabled)
			ctx := context.Background()
			p := pauseDispatch(t, f)
			p, err := f.s.RescheduleDispatch(ctx, f.token, DispatchRescheduleInput{DispatchChangeInput: DispatchChangeInput{p.ID, p.Revision}, RestoreAt: time.Now().Add(150 * time.Millisecond)})
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := New(f.db, f.s.auth, bytes.Repeat([]byte{7}, 32), true)
			if err != nil {
				t.Fatal(err)
			}
			// The original browser session is gone; the accepted worker intent is independent of it.
			exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()-interval '1 millisecond'`)
			time.Sleep(170 * time.Millisecond)
			if err = replacement.recoverDispatchIntents(ctx, true); err != nil {
				t.Fatal(err)
			}
			processed, err := replacement.processDispatchDue(ctx)
			if err != nil || !processed {
				t.Fatal("due pause not processed", err)
			}
			var state, reason string
			if err = f.db.QueryRow(ctx, `SELECT state,reason FROM dispatch_pauses WHERE id=$1`, p.ID).Scan(&state, &reason); err != nil || state != "restored" {
				t.Fatal(state, reason, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.enabled != enabled || enabled && len(f.writes) != 2 || !enabled && len(f.writes) != 0 {
				t.Fatal("initial state not retained", f.enabled, f.writes)
			}
		})
	}
}

func TestDispatchControlNeverOverwritesObservedExternalChanges(t *testing.T) {
	for _, scenario := range []string{"same_false_new_version", "enabled_external", "identity", "missing_version", "inactive", "local_revision", "source_revision", "simple_mode", "disabled_policy"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDispatchFixture(t, true)
			p := pauseDispatch(t, f)
			ctx := context.Background()
			f.mu.Lock()
			switch scenario {
			case "same_false_new_version":
				f.updated = f.updated.Add(time.Microsecond)
			case "enabled_external":
				f.enabled = true
				f.updated = f.updated.Add(time.Microsecond)
			case "identity":
				f.resource = "replacement-resource"
			case "missing_version":
				f.updated = time.Time{}
			case "inactive":
				f.status = "disabled"
			case "simple_mode":
				f.mode = "simple"
			}
			f.mu.Unlock()
			switch scenario {
			case "local_revision":
				exec(t, f.db, `UPDATE managed_accounts SET revision=revision+1 WHERE id=$1`, f.a.ID)
			case "source_revision":
				exec(t, f.db, `UPDATE connection_config SET revision=revision+1`)
			case "disabled_policy":
				setTestWriteGrant(t, f.s, f.token, writeScheduling, false)
			}
			got, err := f.s.RestoreDispatch(ctx, f.token, DispatchChangeInput{p.ID, p.Revision})
			if err != nil || got.State != "needs_review" {
				t.Fatal("unqualified restore not stopped", got, err)
			}
			f.mu.Lock()
			writes := len(f.writes)
			f.mu.Unlock()
			if writes != 1 {
				t.Fatal("external intent overwritten", writes)
			}
			if _, err = f.s.RestoreDispatch(ctx, f.token, DispatchChangeInput{got.ID, got.Revision}); !errors.Is(err, auth.ErrConflict) {
				t.Fatal("review state retried native write", err)
			}
			closed, err := f.s.ReviewDispatch(ctx, f.token, DispatchReviewInput{DispatchChangeInput: DispatchChangeInput{got.ID, got.Revision}, AcknowledgeResultUnknown: true})
			if err != nil || closed.State != "reviewed" || closed.Reason != "MANUALLY_REVIEWED" {
				t.Fatal("manual review invented restored state", closed, err)
			}
		})
	}
}

func TestDispatchControlUnknownWriteAndInterruptedIntentAreNeverReplayed(t *testing.T) {
	for _, scenario := range []string{"bad_response", "timeout", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDispatchFixture(t, true)
			ctx := context.Background()
			f.mu.Lock()
			f.postMode = scenario
			f.mu.Unlock()
			if scenario == "timeout" {
				f.s.upstream.http.Timeout = 80 * time.Millisecond
			}
			p, err := f.s.PauseDispatch(ctx, f.token, dispatchRequest(t, f))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "interrupted" {
				exec(t, f.db, `UPDATE dispatch_pauses SET state='restoring',reason='RESTORE_PENDING',revision=revision+1 WHERE id=$1`, p.ID)
				if err = f.s.recoverDispatchIntents(ctx, true); err != nil {
					t.Fatal(err)
				}
				p, err = f.s.GetDispatchPause(ctx, f.token, p.ID)
			}
			if err != nil || p.State != "needs_review" {
				t.Fatal(p, err)
			}
			for i := 0; i < 2; i++ {
				if _, err = f.s.processDispatchDue(ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 1 || f.enabled {
				t.Fatal("unknown native operation replayed", f.writes)
			}
		})
	}
}

func TestDispatchControlRetriesOnlyReadFailuresAndSerializesConcurrentOperations(t *testing.T) {
	f := newDispatchFixture(t, true)
	ctx := context.Background()
	p := pauseDispatch(t, f)
	f.mu.Lock()
	f.failRead = true
	f.mu.Unlock()
	p, err := f.s.RestoreDispatch(ctx, f.token, DispatchChangeInput{p.ID, p.Revision})
	if err != nil || p.State != "paused" || p.Reason != "READ_RETRY_PENDING" {
		t.Fatal("known no-write read failure became success/unknown", p, err)
	}
	f.mu.Lock()
	f.failRead = false
	f.postEntered = make(chan struct{})
	f.postRelease = make(chan struct{})
	entered, release := f.postEntered, f.postRelease
	f.mu.Unlock()
	result := make(chan error, 1)
	go func() { _, e := f.s.RestoreDispatch(ctx, f.token, DispatchChangeInput{p.ID, p.Revision}); result <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native restore did not start")
	}
	if _, err = f.s.RescheduleDispatch(ctx, f.token, DispatchRescheduleInput{DispatchChangeInput: DispatchChangeInput{p.ID, p.Revision}, RestoreAt: time.Now().Add(time.Hour)}); !errors.Is(err, auth.ErrConflict) {
		close(release)
		t.Fatal("concurrent operation entered account lock", err)
	}
	if err = f.s.recoverDispatchIntents(ctx, true); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 2 || !f.enabled {
		t.Fatal("serialized restore duplicated", f.writes)
	}
}

func TestDispatchControlEnforcesCurrentAuthorityAndScopedHistory(t *testing.T) {
	f := newDispatchFixture(t, true)
	ctx := context.Background()
	p := pauseDispatch(t, f)
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "dispatch-life-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"dispatch"}, AccountIDs: []string{f.a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.GetDispatchPause(ctx, reader, p.ID); err != nil || got.ID != p.ID {
		t.Fatal(got, err)
	}
	if got, err := f.s.GetDispatchControl(ctx, reader); err != nil || got.CanPause || got.Reason != "ADMIN_REQUIRED" {
		t.Fatal(got, err)
	}
	if _, err = f.s.RestoreDispatch(ctx, reader, DispatchChangeInput{p.ID, p.Revision}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary user could restore", err)
	}
	exec(t, f.db, `INSERT INTO dispatch_events(pause_id,account_id,actor_id,actor,action,occurred_at,snapshot)
		SELECT e.pause_id,e.account_id,e.actor_id,e.actor,e.action,e.occurred_at,e.snapshot
		FROM dispatch_events e CROSS JOIN generate_series(1,12) WHERE e.id=(SELECT min(id) FROM dispatch_events WHERE pause_id=$1)`, p.ID)
	after, count, previous := "", 0, int64(0)
	for {
		page, err := f.s.ListDispatchHistory(ctx, reader, DispatchHistoryInput{After: after, Limit: 2})
		if err != nil || page.AuthorityRevision != member.Revision {
			t.Fatal("global scoped history unavailable", page, err)
		}
		for _, event := range page.Events {
			id, err := strconv.ParseInt(event.ID, 10, 64)
			if err != nil || id <= previous || event.Pause.AccountID != f.a.ID {
				t.Fatal("history lost numeric order across digit boundary", event, previous, err)
			}
			previous, count = id, count+1
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if count != 14 {
		t.Fatal("history skipped records across numeric pages", count)
	}
	exec(t, f.db, `UPDATE auth_users SET account_ids='{}',revision=revision+1 WHERE id=$1`, member.ID)
	if _, err = f.s.GetDispatchPause(ctx, reader, p.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("stale account scope read pause", err)
	}
	if _, err = f.s.ListDispatchHistory(ctx, reader, DispatchHistoryInput{AccountID: f.a.ID, Limit: 1}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("stale account scope read history", err)
	}
	if hidden, err := f.s.ListDispatchHistory(ctx, reader, DispatchHistoryInput{Limit: 1}); err != nil || len(hidden.Events) != 0 || hidden.NextAfter != nil {
		t.Fatal("global history leaked a removed account scope", hidden, err)
	}
}

func TestDispatchControlRechecksExpiredSessionAfterNativeWait(t *testing.T) {
	f := newDispatchFixture(t, true)
	ctx := context.Background()
	f.mu.Lock()
	f.onRead = func(reads int) {
		if reads == 3 {
			time.Sleep(700 * time.Millisecond)
		}
	}
	f.mu.Unlock()
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '500 milliseconds'`)
	_, err := f.s.PauseDispatch(ctx, f.token, dispatchRequest(t, f))
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired session crossed native write boundary", err)
	}
	if err = f.s.recoverDispatchIntents(ctx, true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 || !f.enabled {
		t.Fatal("expired browser wrote native state", f.writes)
	}
}

func TestDispatchControlRequiresGrantAndAccountWideAcknowledgement(t *testing.T) {
	f := newDispatchFixture(t, true)
	ctx := context.Background()
	in := dispatchRequest(t, f)
	in.AcknowledgeAccountWide = false
	if _, err := f.s.PauseDispatch(ctx, f.token, in); !errors.Is(err, auth.ErrInput) {
		t.Fatal("missing account-wide acknowledgement accepted", err)
	}
	in = dispatchRequest(t, f)
	in.RestoreAt = time.Now().Add(31 * 24 * time.Hour)
	if _, err := f.s.PauseDispatch(ctx, f.token, in); !errors.Is(err, auth.ErrInput) {
		t.Fatal("unbounded restore deadline", err)
	}
	setTestWriteGrant(t, f.s, f.token, writeScheduling, false)
	if _, err := f.s.PauseDispatch(ctx, f.token, dispatchRequest(t, f)); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("default policy allowed writes", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 {
		t.Fatal("invalid request made a write", f.writes)
	}
}

func TestDispatchControlProjectsDatabaseAndRetainedSnapshotTimesInUTC(t *testing.T) {
	f := newDispatchFixture(t, true)
	ctx := context.Background()
	p := pauseDispatch(t, f)
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL TIME ZONE 'Asia/Taipei'`); err != nil {
		t.Fatal(err)
	}
	got, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM dispatch_pauses WHERE id=$1`, p.ID))
	if err != nil || got.CreatedAt.Location() != time.UTC || got.NativeUpdatedAt.Location() != time.UTC || got.RestoreAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC {
		t.Fatal("database timezone escaped DTO", got, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	old := p
	zone := time.FixedZone("old offset", 8*3600)
	old.CreatedAt, old.UpdatedAt = old.CreatedAt.In(zone), old.UpdatedAt.In(zone)
	old.NativeUpdatedAt, old.RestoreAt = old.NativeUpdatedAt.In(zone), old.RestoreAt.In(zone)
	body, _ := json.Marshal(old)
	exec(t, f.db, `UPDATE dispatch_events SET snapshot=$2 WHERE pause_id=$1`, p.ID, body)
	history, err := f.s.ListDispatchHistory(ctx, f.token, DispatchHistoryInput{AccountID: f.a.ID, Limit: 100})
	if err != nil || len(history.Events) == 0 {
		t.Fatal(err)
	}
	for _, event := range history.Events {
		if event.OccurredAt.Location() != time.UTC || event.Pause.CreatedAt.Location() != time.UTC || event.Pause.RestoreAt.Location() != time.UTC {
			t.Fatal("retained offset escaped history projection", event)
		}
	}
}
