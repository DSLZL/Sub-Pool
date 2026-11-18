package auth

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/testdb"
)

const testPassword = "OriginalPassword123"

var testIP = netip.MustParseAddr("127.0.0.1")

func setup(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.Open(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool
}

func bootstrap(t *testing.T, s *Service) Identity {
	t.Helper()
	user, err := s.Bootstrap(context.Background(), "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func login(t *testing.T, s *Service) (Session, string) {
	t.Helper()
	view, token, err := s.Login(context.Background(), "ADMIN", testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	return view, token
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapConcurrentDifferentNamesAndRestart(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			_, err := New(pool).Bootstrap(ctx, name, testPassword)
			errs <- err
		}(name)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrAlreadyInitialized) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("bootstrap created multiple administrators")
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT username FROM auth_users`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	_, before, err := s.Login(ctx, name, testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(pool).Bootstrap(ctx, "replacement", "Replacement1234"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatal("restart allowed administrator replacement")
	}
	if err := New(pool).EnsureDefaultAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(pool).Login(ctx, "admin", "admin", testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("auto-initialization added default credentials to an existing deployment")
	}
	if _, err := New(pool).Current(ctx, before, false); err != nil {
		t.Fatal("bootstrap replaced persisted session")
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(pool).Login(ctx, name, testPassword, testIP); err != nil {
		t.Fatal("migration or restart reset credentials")
	}
}

func TestDefaultAdminAndPasswordAcrossRestart(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	if _, err := s.Bootstrap(ctx, "admin", "admin"); !errors.Is(err, ErrInput) {
		t.Fatal("manual bootstrap accepted a weak password")
	}
	if err := s.EnsureDefaultAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	first, token, err := s.Login(ctx, "admin", "admin", testIP)
	if err != nil || first.User.Username != "admin" || first.User.Role != "admin" {
		t.Fatal("default administrator could not log in", err)
	}
	if err := s.ChangePassword(ctx, token, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	restarted := New(pool)
	if err := restarted.EnsureDefaultAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.Login(ctx, "admin", "admin", testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("restart restored the default password")
	}
	current, _, err := restarted.Login(ctx, "admin", testPassword, testIP)
	if err != nil || current.User.ID != first.User.ID {
		t.Fatal("restart lost the administrator or changed password", err)
	}
}

func TestLoginLimitsPersistAndDisabledIsGeneric(t *testing.T) {
	s, pool := setup(t)
	user := bootstrap(t, s)
	ctx := context.Background()
	for range identifierFailures {
		if _, _, err := s.Login(ctx, "ADMIN", "WrongPassword123", testIP); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("failure was not generic")
		}
	}
	_, _, err := New(pool).Login(ctx, "admin", testPassword, testIP)
	var limited *Failure
	if !errors.As(err, &limited) || limited.Status != 429 || limited.RetryAt == nil || !limited.RetryAt.After(time.Now()) {
		t.Fatal("restart bypassed persistent restriction")
	}
	exec(t, pool, `UPDATE auth_login_limits SET window_start=$1`, time.Now().Add(-loginWindow-time.Second))
	if _, _, err := s.Login(ctx, "admin", testPassword, testIP); err != nil {
		t.Fatal("expired restriction did not release")
	}
	exec(t, pool, `UPDATE auth_users SET enabled=false WHERE id=$1`, user.ID)
	if _, _, err := s.Login(ctx, "admin", testPassword, testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled identity authenticated or leaked state")
	}
	if _, _, err := s.Login(ctx, "missing", testPassword, testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unknown identity leaked state")
	}
	exec(t, pool, `UPDATE auth_login_limits SET failures=$2 WHERE bucket=$1`, bucketKey("source", testIP.String()), sourceFailures)
	if _, _, err := New(pool).Login(ctx, "another", testPassword, testIP); !errors.As(err, &limited) || limited.Status != 429 {
		t.Fatal("new identifier bypassed source restriction")
	}
}

func TestConcurrentLoginLimitIsAtomicAcrossServiceInstances(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	ctx := context.Background()
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := New(pool).Login(ctx, "admin", "WrongPassword123", testIP)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	failed, restricted := 0, 0
	for err := range errs {
		var failure *Failure
		if errors.Is(err, ErrUnauthorized) {
			failed++
		} else if errors.As(err, &failure) && failure.Status == 429 {
			restricted++
		} else {
			t.Fatal("unexpected concurrent authentication result")
		}
	}
	if failed != identifierFailures || restricted != 8-identifierFailures {
		t.Fatal("parallel instances bypassed persistent login limit")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT failures FROM auth_login_limits WHERE bucket=$1`, bucketKey("identity", "admin")).Scan(&count); err != nil || count != identifierFailures {
		t.Fatal("failure count was lost or overrun")
	}
}

func TestSessionReadRenewExpiryLogoutAndDisable(t *testing.T) {
	s, pool := setup(t)
	user := bootstrap(t, s)
	ctx := context.Background()
	first, token := login(t, s)
	read, err := New(pool).Current(ctx, token, false)
	if err != nil || !read.IdleExpiresAt.Equal(first.IdleExpiresAt) {
		t.Fatal("read renewed or lost persisted session")
	}
	for _, deadline := range []time.Time{read.CreatedAt, read.IdleExpiresAt, read.AbsoluteExpiresAt, read.ServerTime} {
		if deadline.Location() != time.UTC {
			t.Fatal("restored session did not preserve UTC wire timestamps")
		}
	}
	digest, _ := tokenDigest(token)
	absolute := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	exec(t, pool, `UPDATE auth_sessions SET created_at=$2,idle_expires_at=$3,absolute_expires_at=$4 WHERE digest=$1`, digest, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), absolute)
	renewed, err := s.Current(ctx, token, true)
	if err != nil || !renewed.IdleExpiresAt.Equal(absolute) {
		t.Fatal("renewal exceeded absolute lifetime")
	}
	exec(t, pool, `UPDATE auth_users SET enabled=false WHERE id=$1`, user.ID)
	if _, err := s.Current(ctx, token, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled identity retained access")
	}
	exec(t, pool, `UPDATE auth_users SET enabled=true WHERE id=$1`, user.ID)
	if err := New(pool).Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal("logout is not idempotent")
	}
	if _, err := s.Current(ctx, token, true); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logout did not revoke session")
	}
	if err := s.Logout(ctx, ""); err != nil {
		t.Fatal("absent logout is not idempotent")
	}
	for _, deadline := range []string{"idle", "absolute"} {
		_, token := login(t, s)
		digest, _ := tokenDigest(token)
		if deadline == "idle" {
			exec(t, pool, `UPDATE auth_sessions SET created_at=$2,idle_expires_at=$3 WHERE digest=$1`, digest, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Second))
		} else {
			exec(t, pool, `UPDATE auth_sessions SET created_at=$2,idle_expires_at=$3,absolute_expires_at=$3 WHERE digest=$1`, digest, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Second))
		}
		if _, err := s.Current(ctx, token, true); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("expired session was resurrected")
		}
	}
}

func TestPasswordChangeAtomicAllSessionsAndNoOverwrite(t *testing.T) {
	s, pool := setup(t)
	user := bootstrap(t, s)
	ctx := context.Background()
	_, first := login(t, s)
	_, second := login(t, s)
	if err := s.ChangePassword(ctx, first, "WrongPassword123", "ReplacementPassword456"); !errors.Is(err, ErrInput) {
		t.Fatal("incorrect current proof changed password")
	}
	if err := s.ChangePassword(ctx, first, testPassword, testPassword); !errors.Is(err, ErrInput) {
		t.Fatal("accepted same password")
	}
	if _, err := s.Current(ctx, second, false); err != nil {
		t.Fatal("failed proof revoked valid sessions")
	}
	exec(t, pool, `CREATE FUNCTION reject_session_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected revoke failure'; END $$`)
	exec(t, pool, `CREATE TRIGGER reject_session_revoke BEFORE UPDATE ON auth_sessions FOR EACH ROW EXECUTE FUNCTION reject_session_revoke()`)
	if err := s.ChangePassword(ctx, first, testPassword, "ReplacementPassword456"); err == nil {
		t.Fatal("reported success after failed session revocation")
	}
	exec(t, pool, `DROP TRIGGER reject_session_revoke ON auth_sessions`)
	if _, _, err := s.Login(ctx, "admin", testPassword, testIP); err != nil {
		t.Fatal("failed revocation partially changed password")
	}
	if _, err := s.Current(ctx, second, false); err != nil {
		t.Fatal("failed revocation partially invalidated sessions")
	}
	if err := s.ChangePassword(ctx, first, testPassword, "ReplacementPassword456"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, err := New(pool).Current(ctx, token, false); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("password change retained old session")
		}
	}
	if _, _, err := s.Login(ctx, "admin", testPassword, testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old credentials still accepted")
	}
	view, token, err := s.Login(ctx, "admin", "ReplacementPassword456", testIP)
	if err != nil || view.User.ID != user.ID || view.User.Revision != "2" {
		t.Fatal("new credentials or revision not persisted")
	}
	digest, _ := tokenDigest(token)
	var leaked bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE digest=$1 AND digest=$2)`, []byte(token), digest).Scan(&leaked); err != nil || leaked {
		t.Fatal("raw token was stored")
	}
	var raw bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_events WHERE action LIKE '%'||$1||'%' OR outcome LIKE '%'||$1||'%')`, testPassword).Scan(&raw); err != nil || raw {
		t.Fatal("audit event contains credentials")
	}
}

func TestLockWaitRechecksExpiryAndKDFBudget(t *testing.T) {
	s, pool := setup(t)
	user := bootstrap(t, s)
	ctx := context.Background()
	_, token := login(t, s)
	digest, _ := tokenDigest(token)
	exec(t, pool, `UPDATE auth_sessions SET created_at=$2,idle_expires_at=$3 WHERE digest=$1`, digest, time.Now().Add(-time.Hour), time.Now().Add(350*time.Millisecond))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM auth_users WHERE id=$1 FOR UPDATE`, user.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Current(ctx, token, true); done <- err }()
	observed := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT id,username,role,revision,enabled FROM auth_users%')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observed {
		t.Fatal("session check never reached the held user lock")
	}
	time.Sleep(450 * time.Millisecond)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatal("lock wait authorized an expired session")
	}
	s.kdf <- struct{}{}
	s.kdf <- struct{}{}
	if _, _, err := s.Login(ctx, "admin", testPassword, testIP); err == nil {
		t.Fatal("unbounded login KDF")
	}
	if err := s.ChangePassword(ctx, token, testPassword, "ReplacementPassword456"); err == nil {
		t.Fatal("password proof bypassed KDF budget")
	}
	<-s.kdf
	<-s.kdf
}
