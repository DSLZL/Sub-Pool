package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInitialAdminConcurrentCreationAndOverride(t *testing.T) {
	t.Run("concurrent", func(t *testing.T) {
		_, pool := setup(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		type outcome struct {
			setup InitialAdminSetup
			err   error
		}
		start, results := make(chan struct{}), make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				result, err := New(pool).EnsureInitialAdmin(ctx, nil)
				results <- outcome{result, err}
			}()
		}
		close(start)
		deliveries := 0
		for range 2 {
			result := <-results
			if result.err != nil || result.setup.LegacyRevoked != 0 {
				t.Fatal("concurrent initialization failed", result.err)
			}
			if result.setup.GeneratedPassword != "" {
				deliveries++
				if !ValidPassword(result.setup.GeneratedPassword) || len(result.setup.GeneratedPassword) != 20 {
					t.Fatal("unexpected credential policy")
				}
			}
		}
		if deliveries != 1 {
			t.Fatal("expected a single committed initial credential", deliveries)
		}
	})
	t.Run("override", func(t *testing.T) {
		s, _ := setup(t)
		ctx := context.Background()
		for _, weak := range []string{"", "weak"} {
			if result, err := s.EnsureInitialAdmin(ctx, &weak); !errors.Is(err, ErrInput) || result != (InitialAdminSetup{}) {
				t.Fatal("invalid explicit input was accepted", err)
			}
		}
		configured := "ConfiguredInitialPassword269"
		if result, err := s.EnsureInitialAdmin(ctx, &configured); err != nil || result != (InitialAdminSetup{}) {
			t.Fatal("explicit password was returned or not accepted", err)
		}
		session, _, err := s.Login(ctx, "admin", configured, testIP)
		if err != nil || !session.MustChangePassword {
			t.Fatal("configured initial password did not require self-change", err)
		}
	})
}

func TestPendingPasswordGatesBrowserKeysAndAutomation(t *testing.T) {
	s, pool := setup(t)
	admin := bootstrap(t, s)
	session, token := login(t, s)
	if session.MustChangePassword {
		t.Fatal("manual bootstrap changed its existing contract")
	}
	ctx := context.Background()
	readKey := issueTestKey(t, s, token, "read")
	managementKey := issueTestKey(t, s, token, "management")
	exec(t, pool, `UPDATE auth_users SET must_change_password=true WHERE id=$1`, admin.ID)
	view, err := s.Current(ctx, token, false)
	if err != nil || !view.MustChangePassword || !view.IdleExpiresAt.Equal(session.IdleExpiresAt) {
		t.Fatal("current session should show the restriction without renewing", err)
	}
	if _, err := s.Current(ctx, token, true); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatal("restricted session renewed", err)
	}
	if _, err := s.CurrentAccess(ctx, token); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatal("restricted session received access", err)
	}
	if _, err := s.CreateUser(ctx, token, createInput("pending.member")); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatal("restricted session mutated users", err)
	}
	if _, err := s.IssueAPIKey(ctx, token, "pending", "read"); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatal("restricted session could issue credentials", err)
	}
	requireKeyState(t, s, readKey, ErrPasswordChangeRequired)
	requireKeyState(t, s, managementKey, ErrPasswordChangeRequired)
	digest, err := tokenDigest(token)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queued := s.AuthorizeQueuedAdmin(ctx, tx, digest, admin.ID, admin.Revision)
	_, automation := s.AuthorizeAutomationAdmin(ctx, tx, admin.ID, admin.Revision)
	_ = tx.Rollback(ctx)
	if !errors.Is(queued, ErrPasswordChangeRequired) || !errors.Is(automation, ErrPasswordChangeRequired) {
		t.Fatal("background authority did not enforce the restriction", queued, automation)
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal("restricted session could not log out", err)
	}
	if _, err := s.Current(ctx, token, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked session did not retain its normal error", err)
	}
}

func TestLegacyInitialPasswordRevocationAndRecovery(t *testing.T) {
	s, pool := setup(t)
	admin := bootstrap(t, s)
	_, oldToken := login(t, s)
	oldKey := issueTestKey(t, s, oldToken, "management")
	ctx := context.Background()
	peer := createInput("retained.admin")
	peer.Role, peer.CurrentPassword = "admin", testPassword
	if _, err := s.CreateUser(ctx, oldToken, peer); err != nil {
		t.Fatal(err)
	}
	legacy, err := derivePasswordHash("admin")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `UPDATE auth_users SET username='renamed.legacy',password_hash=$2 WHERE id=$1`, admin.ID, legacy)
	exec(t, pool, `CREATE FUNCTION reject_initial_password_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
		CREATE TRIGGER reject_initial_password_audit BEFORE INSERT ON management_events FOR EACH ROW
		WHEN (NEW.action = 'user.revoke_legacy_initial_password') EXECUTE FUNCTION reject_initial_password_audit()`)
	if result, err := s.EnsureInitialAdmin(ctx, nil); err == nil || result != (InitialAdminSetup{}) {
		t.Fatal("failed legacy audit exposed a partial result", err)
	}
	exec(t, pool, "DROP TRIGGER reject_initial_password_audit ON management_events")
	if _, err := s.Current(ctx, oldToken, false); err != nil {
		t.Fatal("failed upgrade revoked a session", err)
	}
	requireKeyState(t, s, oldKey, nil)
	if _, _, err := s.Login(ctx, "renamed.legacy", "admin", testIP); err != nil {
		t.Fatal("failed upgrade changed the old fixture credential", err)
	}
	if result, err := s.EnsureInitialAdmin(ctx, nil); err != nil || result.LegacyRevoked != 1 || result.GeneratedPassword != "" {
		t.Fatal("upgrade did not revoke the legacy credential once", err)
	}
	if _, err := s.Current(ctx, oldToken, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("legacy session survived upgrade", err)
	}
	requireKeyState(t, s, oldKey, ErrUnauthorized)
	if _, _, err := s.Login(ctx, "renamed.legacy", "admin", testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password survived upgrade", err)
	}
	retained, _, err := s.Login(ctx, peer.Username, testPassword, testIP)
	if err != nil || retained.MustChangePassword {
		t.Fatal("upgrade affected the existing secure administrator", err)
	}
	recovery := "LocalRecoveryPassword350"
	if err := s.ResetAdministratorPassword(ctx, "renamed.legacy", recovery); err != nil {
		t.Fatal(err)
	}
	session, token, err := s.Login(ctx, "renamed.legacy", recovery, testIP)
	if err != nil || !session.MustChangePassword {
		t.Fatal("local recovery did not preserve the required password change", err)
	}
	if err := s.ChangePassword(ctx, token, recovery, testPassword); err != nil {
		t.Fatal(err)
	}
	if result, err := s.EnsureInitialAdmin(ctx, nil); err != nil || result != (InitialAdminSetup{}) {
		t.Fatal("repeat startup replayed credential actions", err)
	}
	var auditCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE task_id='local:legacy-initial-password' AND target_id=$1`, admin.ID).Scan(&auditCount)
	if err != nil || auditCount != 1 {
		t.Fatal("expected one durable revocation audit", auditCount, err)
	}
}
