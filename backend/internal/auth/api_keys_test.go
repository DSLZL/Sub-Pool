package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"sync"
	"testing"
	"time"
)

func issueKey(t *testing.T, s *Service, token, mode string) APIKeyIssuance {
	t.Helper()
	out, err := s.IssueAPIKey(context.Background(), token, "report client", mode)
	if err != nil || out.Secret == "" || out.Key.Revision != "1" || out.Key.RevokedAt != nil {
		t.Fatal("key issuance failed", err)
	}
	return out
}
func keyContext(key APIKeyIssuance, management bool) context.Context {
	return context.WithValue(context.Background(), machineKey{}, machineCredential{ID: key.Key.ID, Management: management})
}

func TestAPIKeyOwnershipDigestLifecycleAndCurrentGrants(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "key.reader")
	_, outsider := newMember(t, s, admin, "key.outsider")
	ctx := context.Background()
	key := issueKey(t, s, member, "read")
	keyCtx := keyContext(key, false)
	if _, err := s.Current(ctx, key.Secret, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("key became a session", err)
	}
	if _, err := s.CurrentAccess(ctx, key.Secret); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("key accepted outside explicit transport", err)
	}
	if _, err := s.IssueAPIKey(ctx, member, "escalated", "management"); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary management mode", err)
	}
	for _, actor := range []string{outsider, admin} {
		if _, err := s.RotateAPIKey(ctx, actor, key.Key.ID, "1"); !errors.Is(err, ErrNotFound) {
			t.Fatal("non-owner rotation", err)
		}
		if _, err := s.RevokeAPIKey(ctx, actor, key.Key.ID, "1"); !errors.Is(err, ErrNotFound) {
			t.Fatal("non-owner revoke", err)
		}
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT row_to_json(k)::text FROM auth_api_keys k WHERE id=$1", key.Key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, key.Secret) || strings.Contains(stored, strings.TrimPrefix(key.Secret, key.Key.Prefix+"_")) {
		t.Fatal("recoverable secret persisted")
	}
	access, err := New(pool).CurrentAccess(keyCtx, key.Secret)
	if err != nil || access.User.ID != user.ID || access.AllData || len(access.Features) != 0 {
		t.Fatal("implicit grants", err)
	}
	input := userInput(user)
	input.Features = []string{"usage"}
	input.AccountIDs = []string{objectID(t)}
	input.ParticipantIDs = []string{objectID(t)}
	user, err = s.UpdateUser(ctx, admin, input)
	if err != nil {
		t.Fatal(err)
	}
	access, err = s.CurrentAccess(keyCtx, key.Secret)
	if err != nil || access.User.Revision != user.Revision || access.CheckAttribution(input.AccountIDs[0], input.ParticipantIDs[0]) != nil || access.CheckAttribution(input.AccountIDs[0], objectID(t)) == nil {
		t.Fatal("dynamic independent scopes failed", err)
	}
	catalog, err := s.APIKeyCatalog(keyCtx, key.Secret)
	if err != nil || catalog.CredentialMode != "read" || len(catalog.Operations) != 6 {
		t.Fatal("usage catalog", err)
	}
	input = userInput(user)
	input.Features = []string{}
	input.AccountIDs = []string{}
	input.ParticipantIDs = []string{}
	if _, err = s.UpdateUser(ctx, admin, input); err != nil {
		t.Fatal(err)
	}
	catalog, err = s.APIKeyCatalog(keyCtx, key.Secret)
	if err != nil || len(catalog.Operations) != 2 || len(catalog.Access.AccountIDs) != 0 {
		t.Fatal("stale catalog scope", err)
	}
	if _, err = s.IssueAPIKey(keyCtx, key.Secret, "nested", "read"); !errors.Is(err, ErrForbidden) {
		t.Fatal("key managed credentials", err)
	}
	if err = s.Logout(ctx, member); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentAccess(keyCtx, key.Secret); err != nil {
		t.Fatal("logout revoked independent key", err)
	}
	_, member, err = s.Login(ctx, user.Username, testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := New(pool).RotateAPIKey(ctx, member, key.Key.ID, "1")
	if err != nil || rotated.Secret == "" || rotated.Secret == key.Secret || rotated.Key.ID != key.Key.ID || rotated.Key.Revision != "2" || rotated.Key.RotatedAt == nil || rotated.Key.LastUsedAt != nil {
		t.Fatal("rotation failed", err)
	}
	if _, err = s.CurrentAccess(keyCtx, key.Secret); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old secret survived rotation", err)
	}
	if _, err = s.CurrentAccess(keyCtx, rotated.Secret); err != nil {
		t.Fatal("new secret invalid", err)
	}
	second := issueKey(t, s, member, "read")
	page, err := s.ListAPIKeys(ctx, member, "", 1)
	if err != nil || len(page.Keys) != 1 || page.NextAfter == nil {
		t.Fatal("owned page", err)
	}
	tail, err := s.ListAPIKeys(ctx, member, *page.NextAfter, 1)
	if err != nil || len(tail.Keys) != 1 || tail.NextAfter != nil || page.Keys[0].ID == tail.Keys[0].ID {
		t.Fatal("lost/repeated key page", err)
	}
	body, _ := json.Marshal([]APIKeyPage{page, tail})
	if strings.Contains(string(body), rotated.Secret) || strings.Contains(string(body), second.Secret) || strings.Contains(string(body), "digest") {
		t.Fatal("metadata exposed credential")
	}
	revoked, err := s.RevokeAPIKey(ctx, member, key.Key.ID, "2")
	if err != nil || revoked.Revision != "3" || revoked.RevokedAt == nil {
		t.Fatal("revoke failed", err)
	}
	if _, err = s.CurrentAccess(keyCtx, rotated.Secret); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked key remained live", err)
	}
	if _, err = s.RotateAPIKey(ctx, member, key.Key.ID, "3"); !errors.Is(err, ErrConflict) {
		t.Fatal("revoked key revived", err)
	}
}

func TestAPIKeyCredentialInvalidationAndManagementRole(t *testing.T) {
	s, pool := setup(t)
	adminUser := bootstrap(t, s)
	_, admin := login(t, s)
	ctx := context.Background()
	for _, action := range []string{"disable", "reset", "delete", "password"} {
		t.Run(action, func(t *testing.T) {
			user, member := newMember(t, s, admin, "key."+action)
			key := issueKey(t, s, member, "read")
			input := userInput(user)
			var err error
			switch action {
			case "disable":
				input.Enabled = false
				user, err = s.UpdateUser(ctx, admin, input)
			case "reset":
				input.NewPassword = "ReplacementPassword234"
				_, err = s.UpdateUser(ctx, admin, input)
			case "delete":
				err = s.DeleteUser(ctx, admin, user.ID, user.Revision, user.Username)
			default:
				err = s.ChangePassword(ctx, member, testPassword, "ReplacementPassword234")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CurrentAccess(keyContext(key, false), key.Secret); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("credential operation left key live", err)
			}
			if action == "disable" {
				input = userInput(user)
				input.Enabled = true
				if _, err = s.UpdateUser(ctx, admin, input); err != nil {
					t.Fatal(err)
				}
				if _, err = s.CurrentAccess(keyContext(key, false), key.Secret); !errors.Is(err, ErrUnauthorized) {
					t.Fatal("re-enable resurrected key", err)
				}
			}
		})
	}
	read := issueKey(t, s, admin, "read")
	if _, err := s.CurrentAccess(keyContext(read, true), read.Secret); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin read key got write", err)
	}
	management := issueKey(t, s, admin, "management")
	catalog, err := s.APIKeyCatalog(keyContext(management, false), management.Secret)
	if err != nil || len(catalog.Operations) != len(machineOperations) {
		t.Fatal("false financial availability", err)
	}
	for _, operation := range catalog.Operations {
		switch operation.Name {
		case "walletApplications.request":
			if operation.Availability != "not_dispatched_only" {
				t.Fatal("native dispatch advertised", operation)
			}
		case "walletApplications.simulate", "walletApplications.reconcile":
			if operation.Availability != "protocol_simulation_only" || !operation.Management || operation.Contract != "wallet-applications-v2" {
				t.Fatal("simulation boundary missing", operation)
			}
		}
	}
	exec(t, pool, `UPDATE auth_users SET role='user',revision=revision+1 WHERE id=$1`, adminUser.ID)
	if _, err = s.CurrentAccess(keyContext(management, false), management.Secret); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("management role contraction ignored", err)
	}
	access, err := s.CurrentAccess(keyContext(read, false), read.Secret)
	if err != nil || access.AllData {
		t.Fatal("read key retained admin scope", err)
	}
}

func TestAPIKeyAuditFailureRollsBackSecretsAndCredentialInvalidation(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "key.rollback")
	ctx := context.Background()
	key := issueKey(t, s, member, "read")
	exec(t, pool, `CREATE FUNCTION reject_key_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
 CREATE TRIGGER reject_key_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_key_audit()`)
	if out, err := s.IssueAPIKey(ctx, member, "lost", "read"); err == nil || out.Secret != "" {
		t.Fatal("issue escaped audit failure")
	}
	if out, err := s.RotateAPIKey(ctx, member, key.Key.ID, "1"); err == nil || out.Secret != "" {
		t.Fatal("rotate escaped audit failure")
	}
	if _, err := s.RevokeAPIKey(ctx, member, key.Key.ID, "1"); err == nil {
		t.Fatal("revoke escaped audit failure")
	}
	input := userInput(user)
	input.Enabled = false
	input.NewPassword = "ReplacementPassword234"
	if _, err := s.UpdateUser(ctx, admin, input); err == nil {
		t.Fatal("credential reset escaped audit failure")
	}
	if _, err := s.CurrentAccess(keyContext(key, false), key.Secret); err != nil {
		t.Fatal("failed reset invalidated key", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM auth_api_keys").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed issue left a key", err)
	}
}

func waitAPIKeyLock(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	for ctx.Err() == nil {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
  AND wait_event_type='Lock' AND query LIKE '%auth_api_keys%FOR UPDATE%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("did not witness key lock wait")
}

func TestAPIKeyConcurrentRotationReadsAndRevocationSerialize(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	key := issueKey(t, s, admin, "read")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type result struct {
		out APIKeyIssuance
		err error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { out, err := New(pool).RotateAPIKey(ctx, admin, key.Key.ID, "1"); done <- result{out, err} }()
	}
	success, conflict := 0, 0
	var current APIKeyIssuance
	for range 2 {
		r := <-done
		if r.err == nil {
			success++
			current = r.out
		} else if errors.Is(r.err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(r.err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent rotations overwrote each other")
	}
	keyCtx := context.WithValue(ctx, machineKey{}, machineCredential{ID: key.Key.ID})
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := New(pool).CurrentAccess(keyCtx, current.Secret); errs <- err }()
	}
	wg.Wait()
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatal("parallel key read failed", err)
		}
	}
	read, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback(ctx)
	if _, err = s.AuthorizeRead(keyCtx, read, current.Secret, "accounts"); err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() {
		_, err := New(pool).RevokeAPIKey(ctx, admin, current.Key.ID, current.Key.Revision)
		revoked <- err
	}()
	waitAPIKeyLock(t, pool, ctx)
	if err = read.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentAccess(keyCtx, current.Secret); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("key live after committed revoke", err)
	}
}

func TestAPIKeyRotationRechecksSessionExpiryAfterKeyWait(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	key := issueKey(t, s, admin, "read")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, "SELECT id FROM auth_api_keys WHERE id=$1 FOR UPDATE", key.Key.ID); err != nil {
		t.Fatal(err)
	}
	digest, _ := tokenDigest(admin)
	deadline := time.Now().Add(800 * time.Millisecond)
	exec(t, pool, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest, deadline)
	done := make(chan error, 1)
	go func() {
		out, err := s.RotateAPIKey(ctx, admin, key.Key.ID, "1")
		if out.Secret != "" {
			err = errors.New("expired actor received secret")
		}
		done <- err
	}()
	waitAPIKeyLock(t, pool, ctx)
	time.Sleep(time.Until(deadline) + 20*time.Millisecond)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired actor rotated key", err)
	}
	if _, err = s.CurrentAccess(keyContext(key, false), key.Secret); err != nil {
		t.Fatal("expired rotation changed key", err)
	}
	var revision string
	if err = pool.QueryRow(ctx, "SELECT revision::text FROM auth_api_keys WHERE id=$1", key.Key.ID).Scan(&revision); err != nil || revision != "1" {
		t.Fatal("expired rotation persisted", err)
	}
}
