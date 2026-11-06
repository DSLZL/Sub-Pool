package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func userInput(user ManagedUser) UserUpdate {
	return UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: user.Enabled, Grants: user.Grants}
}

func createInput(name string) UserCreate {
	return UserCreate{Username: name, Password: testPassword, Enabled: true, Grants: Grants{[]string{}, []string{}, []string{}}}
}

func newMember(t *testing.T, s *Service, admin, name string) (ManagedUser, string) {
	t.Helper()
	user, err := s.CreateUser(context.Background(), admin, createInput(name))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(context.Background(), name, testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	return user, token
}

func objectID(t *testing.T) string {
	t.Helper()
	id, err := randomValue(16)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOrdinaryUserLifecycleAuthorizationAndHistory(t *testing.T) {
	s, pool := setup(t)
	admin := bootstrap(t, s)
	_, token := login(t, s)
	ctx := context.Background()
	user, member := newMember(t, s, token, "reader")
	_, second, err := s.Login(ctx, "reader", testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	access, err := s.CurrentAccess(ctx, member)
	if err != nil || access.AllData || len(access.Features)+len(access.AccountIDs)+len(access.ParticipantIDs) != 0 {
		t.Fatal("new identity received implicit grants", err)
	}
	for _, call := range []func() error{
		func() error { _, err := s.ListUsers(ctx, member, "", "", 20); return err },
		func() error { _, err := s.GetUser(ctx, member, user.ID); return err },
		func() error { _, err := s.CreateUser(ctx, member, createInput("another")); return err },
		func() error { _, err := s.UpdateUser(ctx, member, userInput(user)); return err },
		func() error { return s.DeleteUser(ctx, member, user.ID, user.Revision, user.Username) },
	} {
		if !errors.Is(call(), ErrForbidden) {
			t.Fatal("ordinary identity gained management access")
		}
	}
	if _, err := s.GetUser(ctx, token, admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("ordinary-user API exposed administrator")
	}
	adminUpdate := userInput(user)
	adminUpdate.ID = admin.ID
	if _, err := s.UpdateUser(ctx, token, adminUpdate); !errors.Is(err, ErrNotFound) {
		t.Fatal("ordinary-user API edited administrator")
	}
	adminUpdate.NewPassword = "ReplacementPassword234"
	if _, err := s.UpdateUser(ctx, token, adminUpdate); !errors.Is(err, ErrNotFound) {
		t.Fatal("ordinary-user API reset administrator")
	}
	if err := s.DeleteUser(ctx, token, admin.ID, "1", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatal("ordinary-user API deleted administrator")
	}
	input := userInput(user)
	input.Username = "Reader.Renamed"
	input.Features = []string{"usage", "accounts"}
	input.AccountIDs, input.ParticipantIDs = []string{objectID(t)}, []string{objectID(t)}
	user, err = s.UpdateUser(ctx, token, input)
	if err != nil || user.Username != "reader.renamed" || user.Revision != "2" || user.FeatureCount != 2 || user.AccountCount != 1 || user.ParticipantCount != 1 || user.LastLoginAt == nil || user.LastLoginAt.Location() != time.UTC {
		t.Fatal("ordinary user edit failed", user, err)
	}
	access, err = New(pool).CurrentAccess(ctx, member)
	if err != nil || access.User.Revision != "2" || access.User.Username != user.Username || len(access.AccountIDs) != 1 {
		t.Fatal("current session used stale grants", access, err)
	}
	if _, err := s.CreateUser(ctx, token, createInput(user.Username)); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate username replaced identity")
	}
	input = userInput(user)
	input.NewPassword = "ReplacementPassword234"
	if user, err = s.UpdateUser(ctx, token, input); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{member, second} {
		if _, err = New(pool).CurrentAccess(ctx, old); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("credential reset left a live session")
		}
	}
	if _, _, err = s.Login(ctx, user.Username, testPassword, testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password survived reset")
	}
	_, member, err = s.Login(ctx, user.Username, "ReplacementPassword234", testIP)
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.GetUser(ctx, token, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	input = userInput(user)
	input.Enabled = false
	user, err = s.UpdateUser(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentAccess(ctx, member); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled identity remained authorized")
	}
	if _, _, err = s.Login(ctx, user.Username, "ReplacementPassword234", testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled identity logged in")
	}
	input = userInput(user)
	input.Enabled = true
	user, err = s.UpdateUser(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentAccess(ctx, member); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("re-enable resurrected revoked session")
	}
	_, member, err = s.Login(ctx, user.Username, "ReplacementPassword234", testIP)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteUser(ctx, token, user.ID, user.Revision, "wrong confirmation"); !errors.Is(err, ErrInput) {
		t.Fatal("deletion did not require exact confirmation")
	}
	exec(t, pool, "CREATE TABLE retained_business (login_id text REFERENCES auth_users(id), fact text NOT NULL)")
	exec(t, pool, "INSERT INTO retained_business VALUES ($1,'retain participant and history facts')", user.ID)
	if err = s.DeleteUser(ctx, token, user.ID, user.Revision, user.Username); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CurrentAccess(ctx, member); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deleted identity remained authorized")
	}
	if _, err = s.GetUser(ctx, token, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted identity remained manageable")
	}
	if _, err = s.CreateUser(ctx, token, createInput(user.Username)); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted username was reused ambiguously")
	}
	page, err := s.ListUsers(ctx, token, "", "reader", 20)
	if err != nil || len(page.Users) != 0 {
		t.Fatal("deleted identity leaked into ordinary list", err)
	}
	var retained bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM retained_business WHERE login_id=$1)
		AND EXISTS(SELECT 1 FROM auth_users WHERE id=$1 AND deleted_at IS NOT NULL AND NOT enabled AND password_hash='' AND cardinality(read_features)=0)
		AND (SELECT count(*) FROM auth_events WHERE target_user_id=$1 AND user_id=$2 AND outcome='success')=6`, user.ID, admin.ID).Scan(&retained); err != nil || !retained {
		t.Fatal("delete lost history, retained credentials, or missed audit", err)
	}
}

func TestIndependentScopesAndSQLAggregateIsolation(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "scope.reader")
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthorizeRead(ctx, tx, member, "usage"); !errors.Is(err, ErrForbidden) {
		t.Fatal("missing feature allowed read")
	}
	tx.Rollback(ctx)
	a1, a2, p1, p2 := objectID(t), objectID(t), objectID(t), objectID(t)
	input := userInput(user)
	input.Features, input.AccountIDs, input.ParticipantIDs = []string{"usage"}, []string{a1}, []string{p1}
	user, err = s.UpdateUser(ctx, admin, input)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pool, "CREATE TABLE scoped_facts(account_id text, participant_id text, amount integer NOT NULL)")
	for _, fact := range []struct {
		account, participant any
		amount               int
	}{
		{a1, p1, 3}, {a1, p2, 100}, {a2, p1, 200}, {a2, p2, 400}, {a1, nil, 800}, {nil, p1, 1600},
	} {
		exec(t, pool, "INSERT INTO scoped_facts VALUES ($1,$2,$3)", fact.account, fact.participant, fact.amount)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	access, err := New(pool).AuthorizeRead(ctx, tx, member, "usage")
	if err != nil || access.AllData || access.CheckAccount(a1) != nil || access.CheckParticipant(p1) != nil {
		t.Fatal("authorized independent scope denied", err)
	}
	for _, pair := range [][2]string{{a1, p2}, {a2, p1}, {a1, ""}, {"", p1}} {
		if !errors.Is(access.CheckAttribution(pair[0], pair[1]), ErrNotFound) {
			t.Fatal("cross-scope or unknown attribution leaked")
		}
	}
	var total, count int
	predicate, args, err := access.ScopeSQL(pgx.Identifier{"f", "account_id"}, pgx.Identifier{"f", "participant_id"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(sum(amount),0),count(*) FROM scoped_facts AS f WHERE $1::boolean AND `+predicate, append([]any{true}, args...)...).Scan(&total, &count)
	if err != nil || total != 3 || count != 1 {
		t.Fatal("aggregate included another or unknown scope", total, count, err)
	}
	tx.Rollback(ctx)
	input = userInput(user)
	input.AccountIDs = []string{}
	if _, err = s.UpdateUser(ctx, admin, input); err != nil {
		t.Fatal(err)
	}
	access, err = s.CurrentAccess(ctx, member)
	if err != nil || access.CheckParticipant(p1) != nil || !errors.Is(access.CheckAccount(a1), ErrNotFound) {
		t.Fatal("scope dimensions were conflated or old grant survived", err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = s.AuthorizeAdmin(ctx, tx, member); !errors.Is(err, ErrForbidden) {
		t.Fatal("read grant conferred management writes")
	}
	adminAccess, err := s.AuthorizeRead(ctx, tx, admin, "usage")
	if err != nil || !adminAccess.AllData || adminAccess.CheckAttribution("", "") != nil {
		t.Fatal("administrator lost explicit global access", err)
	}
	predicate, args, err = adminAccess.ScopeSQL(pgx.Identifier{"account_id"}, pgx.Identifier{"participant_id"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.QueryRow(ctx, `SELECT sum(amount) FROM scoped_facts WHERE `+predicate, args...).Scan(&total)
	if err != nil || total != 3103 {
		t.Fatal("administrator aggregate mismatch", total, err)
	}
	for _, columns := range [][2]pgx.Identifier{{nil, nil}, {{""}, nil}, {{"a", "b", "c"}, nil}} {
		if _, _, err = access.ScopeSQL(columns[0], columns[1], 1); !errors.Is(err, ErrInput) {
			t.Fatal("unusable scope columns accepted")
		}
	}
	for _, position := range []int{0, 65535} {
		if _, _, err = access.ScopeSQL(pgx.Identifier{"account_id"}, nil, position); !errors.Is(err, ErrInput) {
			t.Fatal("invalid bind position accepted")
		}
	}
}

func TestUserRevisionConcurrencyAndAuditRollback(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "concurrent.reader")
	ctx := context.Background()
	input := userInput(user)
	input.Features = []string{"accounts"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := New(pool).UpdateUser(ctx, admin, input); errs <- err }()
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent edits silently overwrote", success, conflict)
	}
	user, err := s.GetUser(ctx, admin, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `CREATE FUNCTION reject_user_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
		CREATE TRIGGER reject_user_event BEFORE INSERT ON auth_events FOR EACH ROW EXECUTE FUNCTION reject_user_event()`)
	if err = s.DeleteUser(ctx, admin, user.ID, user.Revision, user.Username); err == nil {
		t.Fatal("deletion escaped failed audit")
	}
	input = userInput(user)
	input.Enabled = false
	input.NewPassword = "ReplacementPassword234"
	if _, err = s.UpdateUser(ctx, admin, input); err == nil {
		t.Fatal("disable escaped failed audit")
	}
	if _, err = s.CreateUser(ctx, admin, createInput("rollback.reader")); err == nil {
		t.Fatal("create escaped failed audit")
	}
	restored, err := s.GetUser(ctx, admin, user.ID)
	if err != nil || restored.Revision != user.Revision || !restored.Enabled {
		t.Fatal("audit failure partially committed", restored, err)
	}
	exec(t, pool, "DROP TRIGGER reject_user_event ON auth_events")
	if _, err = s.CurrentAccess(ctx, member); err != nil {
		t.Fatal("failed save revoked a session outside its transaction", err)
	}
	page, err := s.ListUsers(ctx, admin, "", "rollback.reader", 20)
	if err != nil || len(page.Users) != 0 {
		t.Fatal("failed create left a partial identity", err)
	}
	if _, _, err = s.Login(ctx, user.Username, testPassword, testIP); err != nil {
		t.Fatal("rollback lost original credentials", err)
	}
}

func TestUserMutationRechecksExpiryAfterTargetLockWait(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, _ := newMember(t, s, admin, "locked.reader")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, "SELECT id FROM auth_users WHERE id=$1 FOR UPDATE", user.ID); err != nil {
		t.Fatal(err)
	}
	digest, _ := tokenDigest(admin)
	deadline := time.Now().Add(800 * time.Millisecond)
	exec(t, pool, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest, deadline)
	done := make(chan error, 1)
	go func() { _, err := s.UpdateUser(ctx, admin, userInput(user)); done <- err }()
	blocked := false
	for time.Now().Before(deadline) {
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock'
			AND query LIKE 'SELECT id,username,role,revision,enabled,created_at%FOR UPDATE')`).Scan(&blocked)
		if err != nil || blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || !blocked {
		t.Fatal("did not observe target row lock wait", err)
	}
	time.Sleep(time.Until(deadline) + 20*time.Millisecond)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired actor performed a management mutation", err)
	}
	var revision int
	if err = pool.QueryRow(ctx, "SELECT revision FROM auth_users WHERE id=$1", user.ID).Scan(&revision); err != nil || revision != 1 {
		t.Fatal("expired actor changed target", err)
	}
}

func TestUserGrantValidationAndPagination(t *testing.T) {
	s, _ := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, _ := newMember(t, s, admin, "page.alpha")
	newMember(t, s, admin, "page.beta")
	ctx := context.Background()
	initial := createInput("initial.disabled")
	initial.Enabled = false
	initial.Features, initial.AccountIDs = []string{"usage"}, []string{objectID(t)}
	created, err := s.CreateUser(ctx, admin, initial)
	if err != nil || created.Enabled || created.FeatureCount != 1 || created.AccountCount != 1 || created.LastLoginAt != nil {
		t.Fatal("initial identity and permissions were not one saved result", created, err)
	}
	if _, _, err = s.Login(ctx, created.Username, testPassword, testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("initially disabled identity acquired a session", err)
	}
	for _, grants := range []Grants{
		{nil, []string{}, []string{}}, {[]string{"users"}, []string{}, []string{}},
		{[]string{"usage", "usage"}, []string{}, []string{}},
		{[]string{}, []string{"invalid"}, []string{}},
		{[]string{}, []string{user.ID, user.ID}, []string{}},
		{[]string{}, []string{}, make([]string, 501)},
	} {
		input := userInput(user)
		input.Grants = grants
		if _, err := s.UpdateUser(ctx, admin, input); !errors.Is(err, ErrInput) {
			t.Fatal("invalid grants accepted", err)
		}
	}
	first, err := s.ListUsers(ctx, admin, "", " PAGE. ", 1)
	if err != nil || len(first.Users) != 1 || first.Next == nil {
		t.Fatal("bounded prefix page failed", first, err)
	}
	second, err := s.ListUsers(ctx, admin, *first.Next, "page.", 1)
	if err != nil || len(second.Users) != 1 || second.Next != nil || first.Users[0].ID == second.Users[0].ID {
		t.Fatal("cursor repeated or lost identities", second, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := s.ListUsers(ctx, admin, "", "", limit); !errors.Is(err, ErrInput) {
			t.Fatal("unbounded list accepted")
		}
	}
}

func TestReadScopeGuardSerializesPermissionContraction(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "contracting.reader")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := userInput(user)
	input.Features, input.AccountIDs = []string{"usage"}, []string{objectID(t)}
	user, err := s.UpdateUser(ctx, admin, input)
	if err != nil {
		t.Fatal(err)
	}
	read, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback(ctx)
	if _, err = s.AuthorizeRead(ctx, read, member, "usage"); err != nil {
		t.Fatal(err)
	}
	input = userInput(user)
	input.Features, input.AccountIDs = []string{}, []string{}
	done := make(chan error, 1)
	go func() { _, err := s.UpdateUser(ctx, admin, input); done <- err }()
	blocked := false
	for !blocked && ctx.Err() == nil {
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock'
			AND query LIKE 'SELECT id,username,role,revision,enabled,created_at%FOR UPDATE')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("permission edit did not wait for protected transaction")
	}
	if err = read.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	read, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback(ctx)
	if _, err = s.AuthorizeRead(ctx, read, member, "usage"); !errors.Is(err, ErrForbidden) {
		t.Fatal("next operation retained removed grant", err)
	}
}
