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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/testdb"
)

func setup(t *testing.T) (*Service, *pgxpool.Pool, string) {
	t.Helper()
	pool := testdb.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	identity := auth.New(pool)
	if _, err := identity.Bootstrap(ctx, "admin", "TestPassword123"); err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, "admin", "TestPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(pool, identity, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	return s, pool, token
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func save(t *testing.T, s *Service, token, base string) Configuration {
	t.Helper()
	c, err := s.Get(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Save(context.Background(), token, SaveInput{Revision: c.Revision, BaseURL: base, CredentialAction: "replace", Credential: "adm_sc_test_only_secret", Confirmation: "更换地址"})
	if err != nil {
		t.Fatal(err)
	}
	return result.Configuration
}

func TestConfigurationEncryptionRestartClearAndTargetIsolation(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(validUpstream))
	defer server.Close()
	c := save(t, s, token, server.URL)
	firstSource := *c.SourceID
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT secret FROM connection_config`).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("adm_sc_test_only_secret")) {
		t.Fatal("credential persisted as plaintext")
	}
	encoded, _ := json.Marshal(c)
	if strings.Contains(string(encoded), "test_only_secret") {
		t.Fatal("secret returned in configuration")
	}
	restarted, _ := New(pool, auth.New(pool), bytes.Repeat([]byte{7}, 32), true)
	d, err := restarted.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: server.URL})
	if err != nil || !d.Saved || d.SourceID == nil || *d.SourceID != firstSource {
		t.Fatalf("restart lost encrypted credential: %v", err)
	}
	c, err = s.Get(ctx, token)
	if err != nil || c.Diagnostic == nil {
		t.Fatal("saved diagnostics not persisted")
	}
	if _, err = s.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: "https://new.example"}); !errors.Is(err, auth.ErrInput) {
		t.Fatal("stored credential could be sent to new draft target")
	}
	preview, err := s.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: server.URL, Credential: "Bearer preview.jwt"})
	if err != nil || preview.Saved || preview.RunMode == nil {
		t.Fatal("preview test failed or was recorded as saved configuration")
	}
	read, _ := s.Get(ctx, token)
	if read.Diagnostic == nil || read.Diagnostic.RunMode != nil || read.Revision != c.Revision {
		t.Fatal("preview changed saved config/capabilities")
	}
	for _, input := range []SaveInput{
		{Revision: c.Revision, BaseURL: "https://new.example", CredentialAction: "keep", Confirmation: "更换地址"},
		{Revision: c.Revision, BaseURL: "https://new.example", CredentialAction: "replace", Credential: "new-secret"},
	} {
		if _, err := s.Save(ctx, token, input); !errors.Is(err, auth.ErrInput) {
			t.Fatal("unsafe target change accepted")
		}
	}
	changed := save(t, s, token, "https://new.example")
	if *changed.SourceID == firstSource || changed.Diagnostic != nil {
		t.Fatal("target reused source identity or capabilities")
	}
	returned := save(t, s, token, server.URL)
	if *returned.SourceID == firstSource || *returned.SourceID == *changed.SourceID {
		t.Fatal("return to old address resurrected old source identity")
	}
	if _, err := s.Discover(ctx, token, DiscoverInput{Revision: returned.Revision, SourceID: firstSource, Page: 1}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("old source could discover current resources")
	}
	wrongKey, _ := New(pool, s.auth, bytes.Repeat([]byte{8}, 32), true)
	if _, err := wrongKey.Test(ctx, token, TestInput{Revision: returned.Revision, BaseURL: server.URL}); !errors.Is(err, ErrSecretStorage) {
		t.Fatal("wrong key accepted")
	}
	noKey, _ := New(pool, s.auth, nil, true)
	if _, err := noKey.Save(ctx, token, SaveInput{Revision: returned.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "new-secret"}); !errors.Is(err, ErrSecretStorage) {
		t.Fatal("credential accepted without storage protection")
	}
	cleared, err := noKey.Save(ctx, token, SaveInput{Revision: returned.Revision, BaseURL: server.URL, CredentialAction: "clear"})
	if err != nil || cleared.Configuration.CredentialSet || cleared.Configuration.CredentialKind != nil {
		t.Fatal("secret clearing did not persist")
	}
	if _, err := s.Discover(ctx, token, DiscoverInput{Revision: cleared.Configuration.Revision, SourceID: *returned.SourceID, Page: 1}); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("cleared credential remained usable")
	}
}

func TestSettingsAndConnectionUseCurrentRolesAndPreserveFactTimes(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	user, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, member, err := s.auth.Login(ctx, "reader", "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.GetSettings(ctx, member)
	if err != nil || settings.Timezone != "UTC" {
		t.Fatal("member cannot read non-sensitive timezone")
	}
	if _, err = s.Get(ctx, member); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("member read connection metadata")
	}
	if _, err = s.UpdateSettings(ctx, member, "1", "Asia/Taipei"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("member changed global settings")
	}
	for _, zone := range []string{"Local", "", "Not/AZone", "../UTC"} {
		if _, err = s.UpdateSettings(ctx, token, "1", zone); !errors.Is(err, auth.ErrInput) {
			t.Fatal("invalid timezone accepted")
		}
	}
	updated, err := s.UpdateSettings(ctx, token, "1", "Asia/Taipei")
	if err != nil || updated.Timezone != "Asia/Taipei" || updated.Revision != "2" || updated.SavedAt == nil || updated.SavedAt.Location() != time.UTC {
		t.Fatal("timezone did not persist with UTC save time")
	}
	if _, err = s.UpdateSettings(ctx, token, "1", "UTC"); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale settings silently overwritten")
	}
	var created time.Time
	if err = pool.QueryRow(ctx, `SELECT created_at FROM auth_users WHERE id=$1`, user.ID).Scan(&created); err != nil || !created.Equal(user.CreatedAt) {
		t.Fatal("timezone rewrote facts")
	}
	exec(t, pool, `UPDATE auth_users SET enabled=false WHERE id=$1`, user.ID)
	if _, err = s.GetSettings(ctx, member); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("disabled member retained settings access")
	}
	if err = s.auth.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("revoked session retained connection access")
	}
}

func TestConfigurationConcurrencyAndAuditFailureAreAtomic(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	c := save(t, s, token, "https://first.example")
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, base := range []string{"https://second.example", "https://third.example"} {
		wg.Add(1)
		go func(base string) {
			defer wg.Done()
			_, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: base, CredentialAction: "replace", Credential: "new-secret", Confirmation: "更换地址"})
			results <- err
		}(base)
	}
	wg.Wait()
	close(results)
	passed, conflicted := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, auth.ErrConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || conflicted != 1 {
		t.Fatal("concurrent changes lost revision check")
	}
	before, _ := s.Get(ctx, token)
	exec(t, pool, `CREATE FUNCTION reject_system_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`)
	exec(t, pool, `CREATE TRIGGER reject_system_event BEFORE INSERT ON system_events FOR EACH ROW EXECUTE FUNCTION reject_system_event()`)
	if _, err := s.Save(ctx, token, SaveInput{Revision: before.Revision, BaseURL: "https://rollback.example", CredentialAction: "replace", Credential: "other-secret", Confirmation: "更换地址"}); err == nil {
		t.Fatal("audit failure reported success")
	}
	after, _ := s.Get(ctx, token)
	if before.Revision != after.Revision || before.BaseURL != after.BaseURL || *before.SourceID != *after.SourceID {
		t.Fatal("audit failure partially changed config")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_sources WHERE base_url='https://rollback.example'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed save left a source identity")
	}
}

func TestDiscoveryCoversPagesAndNeverReturnsUpstreamSecrets(t *testing.T) {
	s, _, token := setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Method != "GET" {
			t.Error("wrong request authentication")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items := []any{}
		start, end := 1, 100
		if page == 2 {
			start, end = 101, 101
		}
		for i := start; i <= end; i++ {
			items = append(items, accountData(i))
		}
		respond(w, map[string]any{"items": items, "total": 101, "page": page, "page_size": 100})
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	first, err := s.Discover(context.Background(), token, DiscoverInput{Revision: c.Revision, SourceID: *c.SourceID, Page: 1, Search: "Account + test"})
	if err != nil || first.Total == nil || *first.Total != "101" || first.NextPage == nil || *first.NextPage != 2 || len(first.Accounts) != 100 {
		t.Fatal("first page concealed remaining accounts")
	}
	second, err := s.Discover(context.Background(), token, DiscoverInput{Revision: c.Revision, SourceID: *c.SourceID, Page: 2})
	if err != nil || second.NextPage != nil || len(second.Accounts) != 1 || second.Accounts[0].UpstreamID != "101" {
		t.Fatal("final page incomplete")
	}
	body, _ := json.Marshal(first)
	if strings.Contains(string(body), "must-never") || strings.Contains(string(body), "credentials") || first.Accounts[0].Eligibility != "unverified" {
		t.Fatal("secret or unverified resource eligibility leaked")
	}
}

func TestConfigLockAndUpstreamWaitRecheckSessionExpiry(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	c := save(t, s, token, "https://original.example")
	digest := sha256.Sum256([]byte(token))
	deadline := time.Now().Add(time.Second)
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, `SELECT singleton FROM connection_config FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: c.BaseURL, CredentialAction: "clear"})
		done <- err
	}()
	observed := false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
		select {
		case err := <-done:
			t.Fatalf("save ended before the configuration lock: %v", err)
		default:
		}
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT c.revision,c.source_id%')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observed {
		t.Fatal("did not observe actual config lock wait")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	_ = lock.Rollback(ctx)
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("session expiry during config wait allowed mutation")
	}
	var revision string
	if err = pool.QueryRow(ctx, `SELECT revision::text FROM connection_config`).Scan(&revision); err != nil || revision != c.Revision {
		t.Fatal("expired write changed config")
	}
	_, token, err = s.auth.Login(ctx, "admin", "TestPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		validUpstream(w, r)
	}))
	defer server.Close()
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	c = save(t, s, token, server.URL)
	digest = sha256.Sum256([]byte(token))
	deadline = time.Now().Add(time.Second)
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	go func() { _, err := s.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: c.BaseURL}); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("diagnostic ended before the upstream barrier: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("upstream barrier was not reached")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	unblock()
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired session received upstream data")
	}
	var empty bool
	if err = pool.QueryRow(ctx, `SELECT diagnostic IS NULL FROM connection_config`).Scan(&empty); err != nil || !empty {
		t.Fatal("expired diagnostic was persisted")
	}
}
