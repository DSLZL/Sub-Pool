package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func seedConnectionImpact(t *testing.T, db *pgxpool.Pool, source, prefix string) {
	t.Helper()
	participant := strings.Repeat(prefix, 21) + "A"
	exec(t, db, `INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at) VALUES($1,'Impact','','',false,now(),now())`, participant)
	exec(t, db, `INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at,ended_at)
		VALUES($1,$2,1,'{}',now()-interval '1 day',now()),($1,$2,2,'{}',now(),NULL)`, participant, source)
	for i, state := range []string{"pausing", "paused", "restoring", "needs_review", "restored", "failed", "reviewed"} {
		id := fmt.Sprintf("%s%020dA", prefix, i)
		exec(t, db, `INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at)
			VALUES($1,$2,$3,'Impact',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`, id, source, i+1)
		exec(t, db, `INSERT INTO dispatch_pauses(account_id,actor_id,request_id,request_digest,source_id,connection_revision,account_revision,state,reason,previous_schedulable,native_updated_at,restore_at,created_at,updated_at)
			VALUES($1,(SELECT id FROM auth_users WHERE username='admin'),$1,repeat('a',64),$2,1,1,$3,'fixture',true,now(),now(),now(),now())`, id, source, state)
	}
	for i, state := range []string{"applying", "applied", "reverting", "needs_review", "reverted", "rejected", "conflict", "reviewed"} {
		exec(t, db, `INSERT INTO pricing_changes(actor_id,request_id,request_digest,source_id,connection_revision,group_id,group_name,changes,native_before,native_target,state,reason,created_at,updated_at)
			VALUES((SELECT id FROM auth_users WHERE username='admin'),$1,repeat('a',64),$2,1,$3,'Impact','[]','{}','{}',$4,'fixture',now(),now())`, fmt.Sprintf("%s%020dA", prefix, i), source, i+1, state)
	}
	for i, snapshot := range []string{`{"state":"rejected","dispatch_state":"not_dispatched"}`, `{"state":"rejected","dispatch_state":"not_dispatched"}`, `{"state":"rejected","dispatch_state":"unknown"}`, `{}`, `{"state":"applied","dispatch_state":"completed"}`} {
		exec(t, db, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot)
			VALUES((SELECT id FROM auth_users WHERE username='admin'),$1,repeat('a',64),$2,(SELECT min(id) FROM participant_bindings WHERE participant_id=$2),now(),$3)`, fmt.Sprintf("%s%020dA", prefix, i), participant, snapshot)
	}
}

func TestConnectionImpactIsReadOnlySourceBoundAndRetainsHistoricalRequests(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	check := func(c Configuration, expected []string) ConnectionImpact {
		t.Helper()
		out, err := s.GetConnectionImpact(ctx, token)
		if err != nil || out.Revision != c.Revision || !reflect.DeepEqual(out.SourceID, c.SourceID) || out.CheckedAt.IsZero() || out.CheckedAt.Location() != time.UTC {
			t.Fatal("impact lost current source, revision or time", out, err)
		}
		if got := []string{out.ManagedAccounts, out.ParticipantBindings, out.DispatchOpen, out.PricingOpen, out.WalletRejected, out.WalletUnconfirmed}; !reflect.DeepEqual(got, expected) {
			t.Fatal("incorrect impact counts", got, expected)
		}
		return out
	}
	zero := []string{"0", "0", "0", "0", "0", "0"}
	c, err := s.Get(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	check(c, zero)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	c = save(t, s, token, server.URL)
	seedConnectionImpact(t, db, *c.SourceID, "A")
	exec(t, db, `INSERT INTO connection_sources(id,base_url,created_at) VALUES($1,'https://other.example',now())`, strings.Repeat("B", 21)+"A")
	seedConnectionImpact(t, db, strings.Repeat("B", 21)+"A", "B")
	snapshot := func() string {
		var value string
		if err := db.QueryRow(ctx, `SELECT jsonb_build_array(
			(SELECT to_jsonb(t) FROM connection_config t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM managed_accounts t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM participant_bindings t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM dispatch_pauses t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM pricing_changes t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM wallet_application_requests t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM management_events t),
			(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM system_events t))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	out := check(c, []string{"7", "1", "4", "4", "2", "3"})
	restarted, err := New(db, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	s = restarted
	check(c, []string{"7", "1", "4", "4", "2", "3"})
	if before != snapshot() || calls.Load() != 0 {
		t.Fatal("impact changed business history or contacted native target")
	}
	body, _ := json.Marshal(out)
	if strings.Contains(string(body), "fixture") || strings.Contains(string(body), "test_only_secret") {
		t.Fatal("impact exposed a record or credential")
	}
	old := c
	if _, err = s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: "https://replacement.example", CredentialAction: "clear", Confirmation: "更换地址"}); !errors.Is(err, auth.ErrConflict) || before != snapshot() || calls.Load() != 0 {
		t.Fatal("unconfirmed financial history allowed source replacement or changed retained facts", err)
	}
	var retained int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM dispatch_pauses WHERE source_id=$1`, old.SourceID).Scan(&retained); err != nil || retained != 7 {
		t.Fatal("source change rewrote old operations", retained, err)
	}
}

func TestConnectionImpactRechecksExpiryAfterConfigurationLock(t *testing.T) {
	s, db, token := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	save(t, s, token, "https://impact.example")
	digest := sha256.Sum256([]byte(token))
	deadline := time.Now().Add(250 * time.Millisecond)
	exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT singleton FROM connection_config FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.GetConnectionImpact(ctx, token); done <- err }()
	var waiting bool
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT c.revision,c.source_id%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("impact did not reach actual configuration lock")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	_ = tx.Rollback(ctx)
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired session received impact after waiting", err)
	}
}

func TestConnectionImpactHTTPKeepsStrictCurrentAdminSessionBoundary(t *testing.T) {
	s, db, token := setup(t)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Service: s, Boundary: boundary}
	key, err := s.auth.IssueAPIKey(context.Background(), token, "impact exclusion", "management")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, suffix, cookie, bearer string
		status                         int
	}{
		{"GET", "", token, "", 200}, {"GET", "", "", "", 401},
		{"POST", "", token, "", 405}, {"GET", "?extra=1", token, "", 400},
		{"GET", "", token + "; sub_pool_dev_session=" + token, "", 401},
		{"GET", "", "", key.Secret, 403},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/connection/impact"+tc.suffix, nil)
		if tc.cookie != "" {
			r.Header.Set("Cookie", "sub_pool_dev_session="+tc.cookie)
		}
		if tc.bearer != "" {
			r.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("impact boundary changed", w.Code, tc.status, w.Body.String())
		}
	}
	exec(t, db, `UPDATE auth_users SET role='user',revision=revision+1 WHERE username='admin'`)
	if _, err = s.GetConnectionImpact(context.Background(), token); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("role contraction exposed global impact", err)
	}
}
