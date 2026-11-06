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
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func fullManagedAccount(id int, resource string) map[string]any {
	a := accountData(id)
	a["credentials"] = map[string]any{"access_token": "private-account-token", "chatgpt_account_id": resource, "model_mapping": map[string]string{}}
	a["extra"] = map[string]any{}
	a["created_at"], a["updated_at"] = "2026-11-01T00:00:00Z", "2026-11-05T00:00:00Z"
	a["last_used_at"], a["error_message"] = "2026-11-05T22:00:00+08:00", ""
	return a
}

func adoptManaged(t *testing.T, s *Service, token string, c Configuration, id int, name string) ManagedAccount {
	t.Helper()
	result, err := s.Adopt(context.Background(), token, AdoptInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, UpstreamID: strconv.Itoa(id), Name: name, Monitoring: true, SourceMode: "active"})
	if err != nil || result.Account == nil {
		t.Fatalf("adoption failed: %v, check=%+v", err, result.Read)
	}
	return *result.Account
}

func readInput(a ManagedAccount, c Configuration) AccountReadInput {
	return AccountReadInput{ID: a.ID, Revision: a.Revision, SourceID: a.SourceID, ConnectionRevision: c.Revision}
}

func TestManagedProjectionDistinguishesPartialIdentityAndNeverExposesSecrets(t *testing.T) {
	full := fullManagedAccount(1, "private-resource-id")
	body, _ := json.Marshal(full)
	observation, ok := inspectAccount(body, "1", "management-secret")
	if !ok || observation.identity != "verified" || len(observation.fingerprint) != 32 || observation.Runtime.ErrorPresent == nil || *observation.Runtime.ErrorPresent || observation.Runtime.LastUsedAt == nil || observation.Runtime.LastUsedAt.Format(time.RFC3339) != "2026-11-05T14:00:00Z" {
		t.Fatal("full identity, real false or UTC time was lost")
	}
	safe, _ := json.Marshal(observation.AccountSnapshot)
	if strings.Contains(string(safe), "private-") || strings.Contains(string(safe), "credentials") {
		t.Fatal("private upstream fields escaped safe projection")
	}
	restricted := accountData(1)
	delete(restricted, "extra")
	restricted["credentials"] = map[string]any{"model_mapping": map[string]string{}}
	body, _ = json.Marshal(restricted)
	observation, ok = inspectAccount(body, "1", "management-secret")
	if !ok || observation.identity != "unverified" || observation.Runtime.ErrorPresent != nil || observation.Runtime.LastUsedAt != nil || observation.fingerprint != nil {
		t.Fatal("restricted detail invented missing identity/runtime facts")
	}
	full["parent_account_id"] = 2
	body, _ = json.Marshal(full)
	observation, ok = inspectAccount(body, "1", "management-secret")
	if !ok || observation.identity != "shadow" || observation.fingerprint != nil {
		t.Fatal("shadow counted as an independent resource")
	}
	for key, value := range map[string]any{"id": 2, "schedulable": nil, "last_used_at": "invalid", "name": "management-secret", "parent_account_id": 1} {
		t.Run(key, func(t *testing.T) {
			invalid := fullManagedAccount(1, "private-resource-id")
			invalid[key] = value
			body, _ := json.Marshal(invalid)
			if _, ok := inspectAccount(body, "1", "management-secret"); ok {
				t.Fatal("mismatched, missing, malformed or reflected fields accepted")
			}
		})
	}
}

func TestModelProjectionKeepsUnknownPassthroughAndAdvisoryCatalog(t *testing.T) {
	body := []byte(`{"credentials":{"model_mapping":{}},"access_token":"private-token"}`)
	mapping, passthrough, ok := modelConfiguration(body, "management-secret")
	if !ok || mapping == nil || len(mapping) != 0 || passthrough != nil {
		t.Fatal("empty mapping became all-disabled or missing passthrough became false")
	}
	for _, body := range []string{`{"credentials":{"model_mapping":null}}`, `{"credentials":{"model_mapping":{"a":"management-secret"}}}`, `{"extra":{"openai_passthrough":null}}`, `{"extra":[]}`} {
		if _, _, ok := modelConfiguration([]byte(body), "management-secret"); ok {
			t.Fatal("invalid/reflected model configuration accepted")
		}
	}
	for _, body := range []string{`null`, `{}`, `[{"id":"a"},{"id":"a"}]`, `[{"id":"management-secret"}]`} {
		if _, ok := projectModels([]byte(body), "management-secret"); ok {
			t.Fatal("malformed or unsafe catalog accepted")
		}
	}
	models, ok := projectModels([]byte(`[{"id":"candidate","display_name":"Advisory candidate","token":"private-token"}]`), "management-secret")
	if !ok || len(models) != 1 || models[0].Name != "Advisory candidate" {
		t.Fatal("safe native catalog projection failed")
	}
}

func TestManagedLifecyclePreservesHistoryAndIsolatesSourcesAndResources(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var mode, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Authorization") != "" {
			t.Error("upstream request exceeded read-only credential boundary")
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			respond(w, []any{map[string]any{"id": "candidate", "display_name": "Advisory candidate", "secret": "private-model-secret"}})
			return
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
		resource := "same-resource"
		if mode.Load() == 1 {
			http.Error(w, "private-error", 403)
			return
		}
		if mode.Load() == 2 && id == 1 {
			resource = "changed-resource"
		}
		a := fullManagedAccount(id, resource)
		if id == 2 {
			a["parent_account_id"] = 1
		}
		if id == 4 {
			delete(a, "created_at")
			delete(a, "updated_at")
			delete(a, "extra")
			a["credentials"] = map[string]any{"model_mapping": map[string]string{}}
		}
		if id == 5 {
			a["type"] = "apikey"
		}
		respond(w, a)
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Local name")
	if a.IdentityState != "verified" || a.CollectionState != "awaiting_collector" || a.ID == a.UpstreamID {
		t.Fatal("local identity or collection prerequisite was inferred incorrectly")
	}
	for _, id := range []int{1, 3} {
		if _, err := s.Adopt(ctx, token, AdoptInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, UpstreamID: strconv.Itoa(id), Name: "Duplicate", SourceMode: "active"}); !errors.Is(err, auth.ErrConflict) {
			t.Fatal("duplicate upstream/resource identity was allowed")
		}
	}
	for id, state := range map[int]string{2: "shadow", 4: "unverified", 5: "unsupported"} {
		other := adoptManaged(t, s, token, c, id, fmt.Sprintf("Other %d", id))
		if other.IdentityState != state || other.CollectionState != state {
			t.Fatal("unverified/shadow/unsupported account became independently collectable")
		}
	}
	before := calls.Load()
	paused, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Renamed", SourceMode: "passive"})
	if err != nil || paused.Account.CollectionState != "paused" || !paused.Account.ObservedAt.Equal(a.ObservedAt) || calls.Load() != before || paused.Account.Snapshot.Account.Schedulable != a.Snapshot.Account.Schedulable {
		t.Fatal("pause changed remote scheduling/history or dispatched a request")
	}
	if _, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Stale", SourceMode: "active"}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale edit succeeded")
	}
	restarted, _ := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	restored, err := restarted.GetManaged(ctx, token, a.ID)
	if err != nil || restored.Account.Name != "Renamed" || restored.Account.Monitoring || restored.Account.SourceID != a.SourceID {
		t.Fatal("restart lost local state")
	}
	resumed, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: restored.Account.Revision, Name: "Renamed", Monitoring: true, SourceMode: "active"})
	if err != nil || resumed.Account.CollectionState != "awaiting_collector" || calls.Load() != before {
		t.Fatal("resume claimed a running collector or changed upstream")
	}
	a = resumed.Account
	models, err := s.ManagedModels(ctx, token, readInput(a, c))
	if err != nil || len(models.Models) != 1 || models.MappingMode != "empty" || models.AccessProven || models.Passthrough == nil || *models.Passthrough {
		t.Fatal("model catalog became an access/blocking guarantee")
	}
	mode.Store(1)
	failed, err := s.RefreshManaged(ctx, token, readInput(a, c))
	if err != nil || failed.Account.Read.Reason != "FORBIDDEN" || failed.Account.CollectionState != "read_unavailable" || !failed.Account.ObservedAt.Equal(a.ObservedAt) || !failed.Account.LastAttemptAt.After(a.LastAttemptAt) {
		t.Fatal("failed refresh became fresh/healthy or erased prior data")
	}
	mode.Store(2)
	changed, err := s.RefreshManaged(ctx, token, readInput(failed.Account, c))
	if err != nil || changed.Account.IdentityState != "conflict" || changed.Account.CollectionState != "conflict" {
		t.Fatal("resource identity silently rebound historical account")
	}
	var fingerprint []byte
	if err := pool.QueryRow(ctx, `SELECT resource_fingerprint FROM managed_accounts WHERE id=$1`, a.ID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	original := sha256.Sum256([]byte("sub-pool:openai-resource:same-resource"))
	if !bytes.Equal(fingerprint, original[:]) {
		t.Fatal("resource quarantine rewrote original identity")
	}
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1000)
		respond(w, fullManagedAccount(1, "same-resource"))
	}))
	defer second.Close()
	_ = save(t, s, token, second.URL)
	before = calls.Load()
	if _, err := s.RefreshManaged(ctx, token, readInput(changed.Account, c)); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
		t.Fatal("old identity dispatched to changed target")
	}
	old, err := s.GetManaged(ctx, token, a.ID)
	if err != nil || old.Account.SourceCurrent || old.Account.CollectionState != "source_changed" {
		t.Fatal("target change did not block old monitoring")
	}
	mode.Store(0)
	back := save(t, s, token, server.URL)
	fresh := adoptManaged(t, s, token, back, 1, "Fresh source")
	if fresh.ID == a.ID || fresh.SourceID == a.SourceID {
		t.Fatal("returning to an old address reused historical identity")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM managed_accounts`).Scan(&count); err != nil || count != 5 {
		t.Fatal("source changes or pause removed account history")
	}
	encoded, _ := json.Marshal(old)
	if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "same-resource") || strings.Contains(string(encoded), "adm_sc_test_only_secret") {
		t.Fatal("managed response exposed private upstream fields")
	}
}

func TestManagedObservationsRemainBoundToConnectionRevision(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	var denied atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if denied.Load() {
			http.Error(w, "private-error", 403)
			return
		}
		respond(w, fullManagedAccount(1, "same-resource"))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Local")
	if a.ObservedConnectionRevision != c.Revision || !a.ConnectionAvailable {
		t.Fatal("initial observation lost its connection revision")
	}
	withoutKey, _ := New(pool, s.auth, nil, true)
	locked, err := withoutKey.GetManaged(ctx, token, a.ID)
	if err != nil || locked.Account.ConnectionAvailable || locked.Account.CollectionState != "connection_unavailable" {
		t.Fatal("missing storage key reported a usable connection")
	}
	cleared, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "clear"})
	if err != nil || cleared.SourceChanged {
		t.Fatal("clearing credentials changed source identity")
	}
	before := calls.Load()
	view, err := s.GetManaged(ctx, token, a.ID)
	if err != nil || !view.Account.SourceCurrent || view.Account.ConnectionAvailable || view.Account.CollectionState != "connection_unavailable" || view.Account.ObservedConnectionRevision != c.Revision || !view.Account.ObservedAt.Equal(a.ObservedAt) {
		t.Fatal("cleared credentials left an apparently usable or newly observed account")
	}
	if _, err := s.RefreshManaged(ctx, token, readInput(a, cleared.Configuration)); !errors.Is(err, ErrNotConfigured) || calls.Load() != before {
		t.Fatal("cleared credentials dispatched an upstream request")
	}
	replaced, err := s.Save(ctx, token, SaveInput{Revision: cleared.Configuration.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "replacement-management-secret"})
	if err != nil || replaced.SourceChanged {
		t.Fatal("same-address credential replacement changed source identity")
	}
	view, err = s.GetManaged(ctx, token, a.ID)
	if err != nil || view.Account.CollectionState != "connection_changed" || view.Account.ObservedConnectionRevision != c.Revision || calls.Load() != before {
		t.Fatal("credential replacement silently confirmed the old observation")
	}
	if _, err := s.RefreshManaged(ctx, token, readInput(a, c)); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
		t.Fatal("stale connection revision dispatched an upstream request")
	}
	denied.Store(true)
	failed, err := s.RefreshManaged(ctx, token, readInput(a, replaced.Configuration))
	if err != nil || failed.Account.CollectionState != "read_unavailable" || failed.Account.ObservedConnectionRevision != c.Revision || !failed.Account.ObservedAt.Equal(a.ObservedAt) {
		t.Fatal("failed refresh advanced the last successful observation")
	}
	denied.Store(false)
	fresh, err := s.RefreshManaged(ctx, token, readInput(failed.Account, replaced.Configuration))
	if err != nil || fresh.Account.CollectionState != "awaiting_collector" || fresh.Account.ObservedConnectionRevision != replaced.Configuration.Revision {
		t.Fatal("successful refresh did not bind the new connection revision")
	}
}

func TestManagedReadsApplyCurrentScopeBeforeSearchPaginationAndUpstream(t *testing.T) {
	s, _, admin := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
		respond(w, fullManagedAccount(id, fmt.Sprintf("resource-%d", id)))
	}))
	defer server.Close()
	c := save(t, s, admin, server.URL)
	a := adoptManaged(t, s, admin, c, 1, "Visible")
	b := adoptManaged(t, s, admin, c, 2, "Hidden")
	u, err := s.auth.CreateUser(ctx, admin, auth.UserCreate{Username: "reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"accounts"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, member, err := s.auth.Login(ctx, "reader", "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListManaged(ctx, member, "", "", 1)
	if err != nil || len(page.Accounts) != 1 || page.Accounts[0].ID != a.ID || page.NextAfter != nil {
		t.Fatal("scope was applied after pagination or leaked a cursor")
	}
	for _, search := range []string{"Hidden", "%", "_"} {
		page, err = s.ListManaged(ctx, member, "", search, 100)
		if err != nil || len(page.Accounts) != 0 {
			t.Fatal("search broadened current account scope")
		}
	}
	before := calls.Load()
	if _, err := s.GetManaged(ctx, member, b.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("out-of-scope details exposed an identity")
	}
	if _, err := s.RefreshManaged(ctx, member, readInput(b, c)); !errors.Is(err, auth.ErrNotFound) || calls.Load() != before {
		t.Fatal("out-of-scope refresh dispatched upstream")
	}
	if _, err := s.ManagedModels(ctx, member, readInput(a, c)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("member received administrator model configuration")
	}
	if _, err := s.UpdateManaged(ctx, member, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Denied", SourceMode: "active"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("member changed monitoring")
	}
	if _, err := s.RefreshManaged(ctx, member, readInput(a, c)); err != nil {
		t.Fatal("authorized scoped status refresh failed", err)
	}
	u, err = s.auth.UpdateUser(ctx, admin, auth.UserUpdate{ID: u.ID, Revision: u.Revision, Username: u.Username, Enabled: true, Grants: auth.Grants{Features: []string{"accounts"}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetManaged(ctx, member, a.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("current scope contraction retained access")
	}
	u, err = s.auth.UpdateUser(ctx, admin, auth.UserUpdate{ID: u.ID, Revision: u.Revision, Username: u.Username, Enabled: true, Grants: auth.Grants{Features: []string{}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListManaged(ctx, member, "", "", 100); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("account ID bypassed removed feature grant")
	}
	if err := s.auth.Logout(ctx, member); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetManaged(ctx, member, a.ID); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("logout retained account access")
	}
}

func TestManagedOptimisticConcurrencyAndAuditRollback(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
		respond(w, fullManagedAccount(id, fmt.Sprintf("resource-%d", id)))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Original")
	done := make(chan error, 2)
	for _, name := range []string{"First", "Second"} {
		go func(name string) {
			_, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: name, SourceMode: "passive"})
			done <- err
		}(name)
	}
	passed, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-done
		if err == nil {
			passed++
		} else if errors.Is(err, auth.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || conflicts != 1 {
		t.Fatal("concurrent local edit lost optimistic revision")
	}
	before, _ := s.GetManaged(ctx, token, a.ID)
	exec(t, pool, `CREATE FUNCTION reject_account_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`)
	exec(t, pool, `CREATE TRIGGER reject_account_event BEFORE INSERT ON account_events FOR EACH ROW EXECUTE FUNCTION reject_account_event()`)
	if _, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: before.Account.Revision, Name: "Rollback", SourceMode: "active"}); err == nil {
		t.Fatal("audit failure reported successful edit")
	}
	if _, err := s.RefreshManaged(ctx, token, readInput(before.Account, c)); err == nil {
		t.Fatal("audit failure reported successful refresh")
	}
	if _, err := s.Adopt(ctx, token, AdoptInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, UpstreamID: "2", Name: "Rollback", SourceMode: "active"}); err == nil {
		t.Fatal("audit failure reported successful adoption")
	}
	after, _ := s.GetManaged(ctx, token, a.ID)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM managed_accounts`).Scan(&count); err != nil || count != 1 || after.Account.Revision != before.Account.Revision || after.Account.Name != before.Account.Name || !after.Account.LastAttemptAt.Equal(before.Account.LastAttemptAt) {
		t.Fatal("audit failure partially persisted local account state")
	}
}

func TestManagedRemoteWaitRechecksExpiryBeforePersistenceAndModelDispatch(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var blocked atomic.Bool
	var modelCalls atomic.Int32
	entered, release := make(chan struct{}, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			modelCalls.Add(1)
			respond(w, []any{})
			return
		}
		if blocked.Load() {
			entered <- struct{}{}
			<-release
		}
		respond(w, fullManagedAccount(1, "resource-1"))
	}))
	defer server.Close()
	defer unblock()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Original")
	blocked.Store(true)
	deadline := time.Now().Add(time.Second)
	digest := sha256.Sum256([]byte(token))
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() { _, err := s.ManagedModels(ctx, token, readInput(a, c)); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("ended before upstream barrier: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("upstream barrier not reached")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	unblock()
	if err := <-done; !errors.Is(err, auth.ErrUnauthorized) || modelCalls.Load() != 0 {
		t.Fatal("expired read dispatched a second request or returned model data")
	}
	var revision string
	var count int
	if err := pool.QueryRow(ctx, `SELECT revision::text FROM managed_accounts WHERE id=$1`, a.ID).Scan(&revision); err != nil || revision != a.Revision {
		t.Fatal("expired model read changed account")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_events WHERE action='account.models.read'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired read committed an audit success")
	}
}

func TestManagedRefreshAndEditRecheckExpiryAfterRealWaits(t *testing.T) {
	for _, wait := range []string{"network", "account-lock"} {
		t.Run(wait, func(t *testing.T) {
			s, pool, token := setup(t)
			ctx := context.Background()
			var blocked atomic.Bool
			entered, release := make(chan struct{}, 1), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if blocked.Load() {
					entered <- struct{}{}
					<-release
				}
				respond(w, fullManagedAccount(1, "resource-1"))
			}))
			defer server.Close()
			defer unblock()
			c := save(t, s, token, server.URL)
			a := adoptManaged(t, s, token, c, 1, "Original")
			lock, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(ctx)
			if wait == "account-lock" {
				if _, err := lock.Exec(ctx, `SELECT id FROM managed_accounts WHERE id=$1 FOR UPDATE`, a.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				blocked.Store(true)
			}
			deadline := time.Now().Add(time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				var err error
				if wait == "account-lock" {
					_, err = s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Expired", SourceMode: "active"})
				} else {
					_, err = s.RefreshManaged(ctx, token, readInput(a, c))
				}
				done <- err
			}()
			if wait == "network" {
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("ended before network barrier: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("network barrier not reached")
				}
			} else {
				observed := false
				for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
					select {
					case err := <-done:
						t.Fatalf("ended before account lock: %v", err)
					default:
					}
					if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT id,revision,source_id,upstream_id%')`).Scan(&observed); err != nil {
						t.Fatal(err)
					}
					if observed {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !observed {
					t.Fatal("actual account lock wait not observed")
				}
			}
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			unblock()
			_ = lock.Rollback(ctx)
			if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("expired wait returned protected data or a successful edit", err)
			}
			var revision string
			var observed time.Time
			if err := pool.QueryRow(ctx, `SELECT revision::text,observed_at FROM managed_accounts WHERE id=$1`, a.ID).Scan(&revision, &observed); err != nil || revision != a.Revision || !observed.Equal(a.ObservedAt) {
				t.Fatal("expired wait partially persisted account changes")
			}
		})
	}
}
