package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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

func assertDispatchClosed(t *testing.T, capability DispatchCapability) {
	t.Helper()
	if capability.CanPause || capability.CanRestore || capability.AutomaticRestoreAvailable || capability.HistoryState != "unavailable" || capability.Reason != "SCHEDULING_CONTROL_UNQUALIFIED" {
		t.Fatal("read granted unqualified control or invented history", capability)
	}
}

func TestDispatchScopePrecedesLiteralSearchAndPaginationAndUsesCurrentGrants(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
		respond(w, fullManagedAccount(id, "resource-"+strconv.Itoa(id)))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, `Visible %_\ account`)
	b := adoptManaged(t, s, token, c, 2, "Hidden account")
	d := adoptManaged(t, s, token, c, 3, "Visible other account")
	member, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "dispatch-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"dispatch"}, AccountIDs: []string{a.ID, d.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	seen := map[string]bool{}
	input := DispatchListInput{Limit: 1}
	for pageNumber := 0; ; pageNumber++ {
		page, err := s.ListDispatchAccounts(ctx, reader, input)
		if err != nil || len(page.Accounts) != 1 || page.ConnectionRevision != c.Revision || pageNumber > 1 {
			t.Fatal("scoped continuation lost a visible account", page, err)
		}
		assertDispatchClosed(t, page.DispatchCapability)
		item := page.Accounts[0]
		if item.ID == b.ID || seen[item.ID] || !item.SourceCurrent || item.SourceID != *c.SourceID || item.Revision != "1" {
			t.Fatal("scope was applied after pagination", page)
		}
		seen[item.ID] = true
		if page.NextAfter == nil {
			break
		}
		if *page.NextAfter != item.ID {
			t.Fatal("invalid continuation", page)
		}
		input.After = *page.NextAfter
	}
	if !seen[a.ID] || !seen[d.ID] || calls.Load() != before {
		t.Fatal("local selector called upstream or omitted visible records")
	}
	page, err := s.ListDispatchAccounts(ctx, reader, DispatchListInput{Search: `%_\`, Limit: 100})
	if err != nil || len(page.Accounts) != 1 || page.Accounts[0].ID != a.ID || page.NextAfter != nil {
		t.Fatal("literal wildcard search broadened scope", page, err)
	}
	for _, search := range []string{"hidden", "no match"} {
		page, err := s.ListDispatchAccounts(ctx, reader, DispatchListInput{Search: search, Limit: 100})
		if err != nil || page.Accounts == nil || len(page.Accounts) != 0 || page.NextAfter != nil {
			t.Fatal("hidden or absent search returned data", page, err)
		}
	}
	for _, target := range []ManagedAccount{b, {ID: strings.Repeat("A", 22), Revision: "1", SourceID: *c.SourceID}} {
		if _, err := s.ReadDispatchAccount(ctx, reader, readInput(target, c)); !errors.Is(err, auth.ErrNotFound) || calls.Load() != before {
			t.Fatal("hidden and absent detail differ or contact native target", err)
		}
	}
	if got, err := s.ReadDispatchAccount(ctx, reader, readInput(a, c)); err != nil || got.Read.State != "available" {
		t.Fatal("dispatch grant required unrelated account/global grants", got, err)
	}
	before = calls.Load()
	for _, field := range []string{"account-revision", "source", "connection-revision"} {
		stale := readInput(a, c)
		switch field {
		case "account-revision":
			stale.Revision = "2"
		case "source":
			stale.SourceID = strings.Repeat("A", 22)
		case "connection-revision":
			stale.ConnectionRevision = "1"
		}
		if _, err := s.ReadDispatchAccount(ctx, reader, stale); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
			t.Fatal("stale binding dispatched", field, err)
		}
	}
	exec(t, db, "UPDATE auth_users SET account_ids=ARRAY[$2]::text[] WHERE id=$1", member.ID, d.ID)
	if _, err := s.ReadDispatchAccount(ctx, reader, readInput(a, c)); !errors.Is(err, auth.ErrNotFound) || calls.Load() != before {
		t.Fatal("current scope contraction did not protect native detail", err)
	}
	exec(t, db, "UPDATE auth_users SET read_features='{}' WHERE id=$1", member.ID)
	if _, err := s.ListDispatchAccounts(ctx, reader, DispatchListInput{Limit: 100}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked feature retained selector access", err)
	}
	if _, err := s.ReadDispatchAccount(ctx, reader, readInput(d, c)); !errors.Is(err, auth.ErrForbidden) || calls.Load() != before {
		t.Fatal("revoked feature dispatched native read", err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("old binding reached replacement source") }))
	defer other.Close()
	changed := save(t, s, token, other.URL)
	page, err = s.ListDispatchAccounts(ctx, token, DispatchListInput{Limit: 100})
	if err != nil || len(page.Accounts) != 3 || page.ConnectionRevision != changed.Revision {
		t.Fatal("source replacement erased local selectors", page, err)
	}
	for _, item := range page.Accounts {
		if item.SourceCurrent {
			t.Fatal("old source became current", item)
		}
	}
	if _, err := s.ReadDispatchAccount(ctx, token, readInput(a, c)); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
		t.Fatal("old source read survived replacement", err)
	}
}

func TestDispatchNativeObservationRejectsChangedAndUnknownIdentityWithoutChangingStorage(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int64
	var scenario atomic.Value
	scenario.Store("enabled")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/accounts/1" || r.URL.RawQuery != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("dispatch forwarded unexpected target, credentials or mutation", r.Method, r.URL.Path)
		}
		body := fullManagedAccount(1, "private-resource-1")
		switch scenario.Load().(string) {
		case "disabled":
			body["schedulable"], body["status"] = false, "disabled"
		case "missing":
			delete(body, "schedulable")
		case "null":
			body["schedulable"] = nil
		case "id":
			body["id"] = 2
		case "platform":
			body["platform"] = "anthropic"
		case "type":
			body["type"] = "apikey"
		case "shadow":
			body["parent_account_id"] = 2
		case "resource":
			body["credentials"] = map[string]any{"chatgpt_account_id": "other-resource"}
		case "restricted":
			delete(body, "credentials")
		case "secret":
			body["status"] = "adm_sc_test_only_secret"
		case "denied":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"private":"private-account-token"}`))
			return
		}
		respond(w, body)
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Root")
	var before string
	const storage = `SELECT json_build_array((SELECT row_to_json(a) FROM managed_accounts a WHERE id=$1),(SELECT count(*) FROM account_events),(SELECT count(*) FROM management_events),(SELECT count(*) FROM wallet_reads),(SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM notifications))::text`
	if err := db.QueryRow(ctx, storage, a.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, state, reason string }{
		{"enabled", "available", "VERIFIED_READ"}, {"disabled", "available", "VERIFIED_READ"},
		{"missing", "failed", "INVALID_RESPONSE"}, {"null", "failed", "INVALID_RESPONSE"}, {"id", "failed", "INVALID_RESPONSE"},
		{"platform", "failed", "ACCOUNT_IDENTITY_CHANGED"}, {"type", "failed", "ACCOUNT_IDENTITY_CHANGED"}, {"shadow", "failed", "ACCOUNT_IDENTITY_CHANGED"}, {"resource", "failed", "ACCOUNT_IDENTITY_CHANGED"},
		{"restricted", "unverified", "RESOURCE_IDENTITY_UNVERIFIED"}, {"secret", "failed", "INVALID_RESPONSE"}, {"denied", "denied", "FORBIDDEN"},
	} {
		t.Run(item.name, func(t *testing.T) {
			scenario.Store(item.name)
			got, err := s.ReadDispatchAccount(ctx, token, readInput(a, c))
			if err != nil || got.Read.ID != "dispatch.account.read" || got.Read.State != item.state || got.Read.Reason != item.reason || got.CheckedAt.IsZero() || got.AccountReadInput != readInput(a, c) {
				t.Fatal("incorrect native observation", got, err)
			}
			assertDispatchClosed(t, got.DispatchCapability)
			if item.state == "available" {
				if got.ConfiguredSchedulable == nil || *got.ConfiguredSchedulable != (item.name == "enabled") || got.NativeStatus == nil || *got.NativeStatus != map[string]string{"enabled": "active", "disabled": "disabled"}[item.name] {
					t.Fatal("explicit native false became unknown or changed meaning", got)
				}
			} else if got.ConfiguredSchedulable != nil || got.NativeStatus != nil {
				t.Fatal("unverified read exposed current config or reused old values", got)
			}
			encoded, _ := json.Marshal(got)
			for _, private := range []string{"private-account-token", "private-resource-1", "adm_sc_test_only_secret", "credentials", "last_used_at", server.URL} {
				if bytes.Contains(encoded, []byte(private)) {
					t.Fatal("protected projection leaked native data", private)
				}
			}
		})
	}
	scenario.Store("enabled")
	restarted, err := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.ReadDispatchAccount(ctx, token, readInput(a, c)); err != nil || got.ConfiguredSchedulable == nil || !*got.ConfiguredSchedulable {
		t.Fatal("replacement lost saved identity or encrypted credential", got, err)
	}
	var after string
	if err := db.QueryRow(ctx, storage, a.ID).Scan(&after); err != nil || after != before {
		t.Fatal("dispatch read changed snapshots, revisions, audit or business records", err)
	}
	beforeCalls := calls.Load()
	wrongKey, err := New(db, s.auth, bytes.Repeat([]byte{8}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.ReadDispatchAccount(ctx, token, readInput(a, c)); !errors.Is(err, ErrSecretStorage) || calls.Load() != beforeCalls {
		t.Fatal("unavailable secret storage dispatched", err)
	}
	exec(t, db, "UPDATE managed_accounts SET identity_state='conflict' WHERE id=$1", a.ID)
	if got, err := s.ReadDispatchAccount(ctx, token, readInput(a, c)); err != nil || got.Read.Reason != "ACCOUNT_IDENTITY_CHANGED" || got.ConfiguredSchedulable != nil {
		t.Fatal("quarantined identity recovered through a read", got, err)
	}
}

func TestDispatchHTTPRejectsWriteIntentsMalformedInputAndForeignAuthority(t *testing.T) {
	s, _, token := setup(t)
	boundary, err := auth.NewHandler(s.auth, "https://pool.example", false)
	if err != nil {
		t.Fatal(err)
	}
	h := &DispatchHandler{Service: s, Boundary: boundary}
	const body = `{"after":"","search":"","limit":100}`
	for _, item := range []struct {
		name, method, path, body, origin, cookie string
		status                                   int
	}{
		{"valid local selector", "POST", "/api/v1/dispatch/list", body, "https://pool.example", token, 200},
		{"method", "GET", "/api/v1/dispatch/list", "", "https://pool.example", token, 405},
		{"query", "POST", "/api/v1/dispatch/list?limit=1", body, "https://pool.example", token, 400},
		{"foreign origin", "POST", "/api/v1/dispatch/list", body, "https://foreign.example", token, 403},
		{"anonymous", "POST", "/api/v1/dispatch/list", body, "https://pool.example", "", 401},
		{"duplicate", "POST", "/api/v1/dispatch/list", `{"after":"","search":"","limit":1,"limit":100}`, "https://pool.example", token, 400},
		{"null", "POST", "/api/v1/dispatch/list", `{"after":"","search":null,"limit":1}`, "https://pool.example", token, 400},
		{"wrong type", "POST", "/api/v1/dispatch/list", `{"after":"","search":"","limit":"1"}`, "https://pool.example", token, 400},
		{"missing", "POST", "/api/v1/dispatch/list", `{"limit":1}`, "https://pool.example", token, 400},
		{"extra", "POST", "/api/v1/dispatch/list", `{"after":"","search":"","limit":1,"can_pause":true}`, "https://pool.example", token, 400},
		{"bad cursor", "POST", "/api/v1/dispatch/list", `{"after":"foreign","search":"","limit":1}`, "https://pool.example", token, 400},
		{"bad limit", "POST", "/api/v1/dispatch/list", `{"after":"","search":"","limit":0}`, "https://pool.example", token, 400},
		{"control", "POST", "/api/v1/dispatch/list", `{"after":"","search":"bad\nname","limit":1}`, "https://pool.example", token, 400},
		{"malformed binding", "POST", "/api/v1/dispatch/account", `{"id":"bad","revision":"1","source_id":"bad","connection_revision":"1"}`, "https://pool.example", token, 400},
		{"pause", "POST", "/api/v1/dispatch/pause", `{}`, "https://pool.example", token, 404},
		{"restore", "POST", "/api/v1/dispatch/restore", `{}`, "https://pool.example", token, 404},
		{"history", "POST", "/api/v1/dispatch/history", `{}`, "https://pool.example", token, 404},
	} {
		t.Run(item.name, func(t *testing.T) {
			r := httptest.NewRequest(item.method, "https://pool.example"+item.path, strings.NewReader(item.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", item.origin)
			if item.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "__Host-sub_pool_session", Value: item.cookie})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != item.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("boundary mismatch", w.Code, w.Body.String())
			}
		})
	}
}

func TestDispatchRechecksSessionExpiryAfterObservedLocksAndNativeWait(t *testing.T) {
	for _, wait := range []string{"network", "configuration", "account", "list-configuration"} {
		t.Run(wait, func(t *testing.T) {
			s, db, token := setup(t)
			ctx := context.Background()
			var block atomic.Bool
			var calls atomic.Int64
			entered, release := make(chan struct{}, 1), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if block.Load() {
					entered <- struct{}{}
					<-release
				}
				respond(w, fullManagedAccount(1, "resource-1"))
			}))
			defer server.Close()
			defer unblock()
			c := save(t, s, token, server.URL)
			a := adoptManaged(t, s, token, c, 1, "Root")
			before := calls.Load()
			deadline := time.Now().Add(time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest[:], deadline)
			lock, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(ctx)
			pattern := "SELECT c.revision,c.source_id%"
			if wait == "network" {
				block.Store(true)
			} else if wait == "account" {
				_, err = lock.Exec(ctx, "SELECT id FROM managed_accounts WHERE id=$1 FOR UPDATE", a.ID)
				pattern = "%FROM managed_accounts WHERE id=$1%"
			} else {
				_, err = lock.Exec(ctx, "SELECT singleton FROM connection_config FOR UPDATE")
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if wait == "list-configuration" {
					out, err := s.ListDispatchAccounts(ctx, token, DispatchListInput{Limit: 100})
					if len(out.Accounts) != 0 {
						err = errors.New("expired selector returned protected accounts")
					}
					done <- err
					return
				}
				out, err := s.ReadDispatchAccount(ctx, token, readInput(a, c))
				if out.ID != "" || out.ConfiguredSchedulable != nil || out.NativeStatus != nil {
					err = errors.New("expired read returned protected native config")
				}
				done <- err
			}()
			if wait == "network" {
				select {
				case <-entered:
				case err := <-done:
					t.Fatal("read ended before native barrier", err)
				case <-time.After(3 * time.Second):
					t.Fatal("native wait not observed")
				}
			} else {
				observePoolWait(t, db, pattern)
			}
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			unblock()
			_ = lock.Rollback(ctx)
			select {
			case err := <-done:
				if !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatal("wait bypassed current session expiry", err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("read did not finish after release")
			}
			expected := before
			if wait == "network" {
				expected++
			}
			if calls.Load() != expected {
				t.Fatal("expired read dispatched after a lock wait or replayed native call")
			}
		})
	}
}
