package backups

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/connection"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/testdb"
)

const fixturePassword = "OwnedRecoveryPassword123"
const fixtureSecret = "adm_sc_owned_restore_fixture_secret"

var fixtureKey = bytes.Repeat([]byte{29}, 32)

type fixture struct {
	s          *Service
	pool       *pgxpool.Pool
	identity   *auth.Service
	token      string
	admin      auth.Identity
	connection *connection.Service
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.Open(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	identity := auth.New(pool)
	admin, err := identity.Bootstrap(ctx, "administrator", fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, admin.Username, fixturePassword, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	gate, err := database.NewRuntime(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.Close)
	s, err := New(pool, identity, gate, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := connection.New(pool, identity, fixtureKey, true)
	if err != nil {
		t.Fatal(err)
	}
	c.Runtime = gate
	return fixture{s, pool, identity, token, admin, c}
}
func exec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func id(t *testing.T) string {
	t.Helper()
	value, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func export(t *testing.T, f fixture, secrets bool) File {
	t.Helper()
	a, err := f.s.Export(context.Background(), f.token, id(t), secrets)
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.s.GetFile(context.Background(), f.token, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return file
}
func preflight(t *testing.T, f fixture, file File) Plan {
	t.Helper()
	p, err := f.s.Preflight(context.Background(), f.token, id(t), file.Content)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != "ready" || p.BeforeArchiveID == nil {
		t.Fatal("restore not ready", p.State, p.Blockers)
	}
	return p
}
func login(t *testing.T, f fixture, username string) string {
	t.Helper()
	_, token, err := f.identity.Login(context.Background(), username, fixturePassword, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func savedConnection(t *testing.T, f fixture) connection.Configuration {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Error("restore replayed an upstream write")
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if r.Header.Get("x-api-key") != fixtureSecret {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/system/version":
			_, _ = w.Write([]byte(`{"code":0,"data":{"version":"owned-native-restore"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":1,"pages":0}}`))
		}
	}))
	t.Cleanup(server.Close)
	c, err := f.connection.Get(context.Background(), f.token)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.connection.Save(context.Background(), f.token, connection.SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: fixtureSecret})
	if err != nil {
		t.Fatal(err)
	}
	return saved.Configuration
}

func TestProtectedArchiveNativeReplacementAndFreshVerification(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := savedConnection(t, f)
	keyID := id(t)
	exec(t, f.pool, `INSERT INTO auth_api_keys(id,user_id,name,mode,digest,created_at) VALUES($1,$2,'owned key','read',decode(repeat('ab',32),'hex'),clock_timestamp())`, keyID, f.admin.ID)
	exec(t, f.pool, `UPDATE monitoring_config SET enabled=true`)
	exec(t, f.pool, `UPDATE notification_config SET enabled=true,secret=decode('010203','hex')`)
	exec(t, f.pool, `UPDATE monitoring_worker SET heartbeat_at=clock_timestamp()`)
	file := export(t, f, true)
	if strings.Contains(file.Content, fixtureSecret) || !strings.HasSuffix(file.Filename, ".spbak") || file.Archive.Bytes != number(len(file.Content)) {
		t.Fatal("file protection/metadata invalid")
	}
	data, err := f.s.unprotect(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Tables) != 40 || !reflect.DeepEqual(data.Excluded, excludedTables) || data.SchemaFingerprint == "" {
		t.Fatal("incomplete business snapshot")
	}
	// The confirming administrator intentionally does not exist in the incoming archive.
	otherID := id(t)
	exec(t, f.pool, `INSERT INTO auth_users(id,username,role,password_hash,created_at) SELECT $1,'restorer','admin',password_hash,clock_timestamp() FROM auth_users WHERE id=$2`, otherID, f.admin.ID)
	f.token = login(t, f, "restorer")
	exec(t, f.pool, `UPDATE system_settings SET timezone='Asia/Taipei'`)
	p := preflight(t, f, file)
	before, err := f.s.GetFile(ctx, f.token, *p.BeforeArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	beforeData, err := f.s.unprotect(before.Content)
	if err != nil || !beforeData.IncludeSecrets || basis(beforeData) != p.TargetBasis {
		t.Fatal("before-image does not bind the actual target", err)
	}
	second, err := database.NewRuntime(ctx, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Apply(ctx, f.token, ApplyInput{p.ID, p.TargetBasis, true}); !errors.Is(err, ErrBusy) {
		t.Fatal("competing runtime did not block replacement", err)
	}
	second.Close()
	result, err := f.s.Apply(ctx, f.token, ApplyInput{p.ID, p.TargetBasis, true})
	if err != nil || result.State != "applied" {
		t.Fatal("native replacement failed", result.State, result.Reason, err)
	}
	if _, err = f.identity.Current(ctx, f.token, false); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("old session survived", err)
	}
	f.token = login(t, f, "administrator")
	var timezone string
	var sessions, remainingRestorer, liveKeys int
	if err = f.pool.QueryRow(ctx, `SELECT timezone,(SELECT count(*) FROM auth_sessions),(SELECT count(*) FROM auth_users WHERE id=$1),(SELECT count(*) FROM auth_api_keys WHERE revoked_at IS NULL) FROM system_settings`, otherID).Scan(&timezone, &sessions, &remainingRestorer, &liveKeys); err != nil || timezone != "UTC" || sessions != 1 || remainingRestorer != 0 || liveKeys != 0 {
		t.Fatal("replacement/revocation failed", timezone, sessions, remainingRestorer, liveKeys, err)
	}
	checks, err := f.s.Checks(ctx, f.token, p.ID)
	if err != nil || !checks.MonitoringPaused || !checks.MailPaused || checks.ActiveAPIKeys != "0" || checks.ConnectionState != "verification_required" {
		t.Fatal("unsafe restore checks", checks, err)
	}
	if _, err = f.s.Review(ctx, f.token, p.ID, true); err != nil {
		t.Fatal(err)
	}
	checks, err = f.s.Checks(ctx, f.token, p.ID)
	if err != nil || checks.ConnectionState != "verification_required" || checks.ReviewedAt == nil {
		t.Fatal("manual review invented verification", err)
	}
	current, err := f.connection.Get(ctx, f.token)
	if err != nil || current.Diagnostic != nil || current.Revision == c.Revision {
		t.Fatal("old diagnosis was retained", err)
	}
	var denied *auth.Failure
	if _, err = f.connection.Discover(ctx, f.token, connection.DiscoverInput{Revision: current.Revision, SourceID: *current.SourceID, Page: 1}); !errors.As(err, &denied) || denied.Code != "CONNECTION_REVERIFICATION_REQUIRED" {
		t.Fatal("restored secret was usable without verification", err)
	}
	diagnostic, err := f.connection.Test(ctx, f.token, connection.TestInput{Revision: current.Revision, BaseURL: current.BaseURL})
	if err != nil {
		t.Fatal(err)
	}
	verified := false
	for _, check := range diagnostic.Checks {
		if check.ID == "management_auth" && check.State == "available" {
			verified = true
		}
	}
	if !verified {
		t.Fatal("owned actual connection check did not authenticate")
	}
	// Go and PostgreSQL may have different clocks; proof comes from the cleared/current diagnostic.
	exec(t, f.pool, `UPDATE connection_config SET diagnostic=jsonb_set(diagnostic,'{checked_at}',to_jsonb('2000-01-01T00:00:00Z'::text))`)
	checks, err = f.s.Checks(ctx, f.token, p.ID)
	if err != nil || checks.ConnectionState != "verified_after_restore" || !checks.MonitoringPaused || !checks.MailPaused {
		t.Fatal("actual verification/resume boundary wrong", checks, err)
	}
	if err = f.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.s.GetPlan(ctx, f.token, p.ID)
	if err != nil || receipt.State != "applied" {
		t.Fatal("recovery downgraded committed receipt", err)
	}
	if _, err = f.s.GetFile(ctx, f.token, *p.BeforeArchiveID); err != nil {
		t.Fatal("rollback archive was lost", err)
	}
	rollback, err := f.s.Preflight(ctx, f.token, id(t), before.Content)
	if err != nil || rollback.State != "ready" {
		t.Fatal("protected before-image cannot be restored", rollback.State, err)
	}
	rolledBack, err := f.s.Apply(ctx, f.token, ApplyInput{rollback.ID, rollback.TargetBasis, true})
	if err != nil || rolledBack.State != "applied" {
		t.Fatal("protected before-image replacement failed", rolledBack.State, err)
	}
	f.token = login(t, f, "restorer")
	if err = f.pool.QueryRow(ctx, `SELECT timezone FROM system_settings`).Scan(&timezone); err != nil || timezone != "Asia/Taipei" {
		t.Fatal("before-image did not recover prior target data", timezone, err)
	}
}

func TestPreflightRejectsChangedTargetAndRetainsRollback(t *testing.T) {
	f := setup(t)
	file := export(t, f, false)
	p := preflight(t, f, file)
	exec(t, f.pool, `UPDATE system_settings SET timezone='Asia/Taipei'`)
	result, err := f.s.Apply(context.Background(), f.token, ApplyInput{p.ID, p.TargetBasis, true})
	if err != nil || result.State != "expired" || result.Reason != "BASIS_CHANGED" {
		t.Fatal("stale preflight applied", result.State, result.Reason, err)
	}
	var timezone string
	if err = f.pool.QueryRow(context.Background(), `SELECT timezone FROM system_settings`).Scan(&timezone); err != nil || timezone != "Asia/Taipei" {
		t.Fatal("stale restore changed business rows", err)
	}
	if _, err = f.s.GetFile(context.Background(), f.token, *p.BeforeArchiveID); err != nil {
		t.Fatal(err)
	}
}

func TestSequenceRestartRollsBackNatively(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	var called bool
	if err = tx.QueryRow(ctx, `SELECT last_value,is_called FROM system_events_id_seq`).Scan(&before, &called); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `ALTER SEQUENCE system_events_id_seq RESTART WITH 9007199254740993`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var after int64
	var afterCalled bool
	if err = f.pool.QueryRow(ctx, `SELECT last_value,is_called FROM system_events_id_seq`).Scan(&after, &afterCalled); err != nil || before != after || called != afterCalled {
		t.Fatal("native rollback consumed sequence", err)
	}
}
