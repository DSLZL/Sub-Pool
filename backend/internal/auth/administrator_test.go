package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func requireKeyState(t *testing.T, s *Service, key APIKeyIssuance, want error) {
	t.Helper()
	ctx := context.WithValue(context.Background(), machineKey{}, machineCredential{ID: key.Key.ID})
	if _, err := s.CurrentAccess(ctx, key.Secret); !errors.Is(err, want) {
		t.Fatalf("key state: got %v, want %v", err, want)
	}
}

func issueTestKey(t *testing.T, s *Service, session, mode string) APIKeyIssuance {
	t.Helper()
	key, err := s.IssueAPIKey(context.Background(), session, mode+"-test", mode)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAdministratorPasswordRecovery(t *testing.T) {
	s, pool := setup(t)
	admin := bootstrap(t, s)
	_, first := login(t, s)
	_, second := login(t, s)
	member, _ := newMember(t, s, first, "recovery.member")
	readKey := issueTestKey(t, s, first, "read")
	managementKey := issueTestKey(t, s, second, "management")
	ctx := context.Background()
	const replacement = "RecoveryPassword2026"

	if err := s.ResetAdministratorPassword(ctx, member.Username, replacement); !errors.Is(err, ErrNotFound) {
		t.Fatal("recovery accepted an ordinary user", err)
	}
	if err := s.ResetAdministratorPassword(ctx, admin.Username, testPassword); !errors.Is(err, ErrInput) {
		t.Fatal("recovery accepted the current password", err)
	}
	exec(t, pool, `CREATE FUNCTION fail_local_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
		CREATE TRIGGER fail_local_recovery_audit BEFORE INSERT ON management_events FOR EACH ROW
		WHEN (NEW.action = 'user.reset_password_local') EXECUTE FUNCTION fail_local_recovery_audit()`)
	if err := s.ResetAdministratorPassword(ctx, admin.Username, replacement); err == nil {
		t.Fatal("recovery committed after audit failure")
	}
	exec(t, pool, "DROP TRIGGER fail_local_recovery_audit ON management_events")
	if _, err := s.Current(ctx, first, false); err != nil {
		t.Fatal("failed recovery revoked the session", err)
	}
	requireKeyState(t, s, readKey, nil)
	requireKeyState(t, s, managementKey, nil)
	if _, _, err := s.Login(ctx, admin.Username, testPassword, testIP); err != nil {
		t.Fatal("failed recovery changed the password", err)
	}

	if err := s.ResetAdministratorPassword(ctx, admin.Username, replacement); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, err := s.Current(ctx, token, false); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("old session survived recovery", err)
		}
	}
	requireKeyState(t, s, readKey, ErrUnauthorized)
	requireKeyState(t, s, managementKey, ErrUnauthorized)
	if _, _, err := s.Login(ctx, admin.Username, testPassword, testIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old password survived recovery", err)
	}
	session, _, err := s.Login(ctx, admin.Username, replacement, testIP)
	if err != nil || session.User.ID != admin.ID || session.User.Role != "admin" {
		t.Fatal("replacement password did not recover administrator", err)
	}
	var authCount, managementCount int
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM auth_events WHERE action='user.reset_password_local' AND target_user_id=$1 AND source_kind='local' AND user_id IS NULL),
		(SELECT count(*) FROM management_events WHERE action='user.reset_password_local' AND task_id='local:reset-password' AND target_id=$1 AND target_revision=$2::bigint)`,
		admin.ID, session.User.Revision).Scan(&authCount, &managementCount)
	if err != nil || authCount != 1 || managementCount != 1 {
		t.Fatal("missing or duplicate local recovery audit", authCount, managementCount, err)
	}
}

func TestAdministratorDelegationRequiresCurrentPasswordAndRevokesCredentials(t *testing.T) {
	s, pool := setup(t)
	admin := bootstrap(t, s)
	_, token := login(t, s)
	ctx := context.Background()
	partner := createInput("delegation.admin")
	partner.Role = "admin"
	if _, err := s.CreateUser(ctx, token, partner); !errors.Is(err, ErrCurrentPassword) {
		t.Fatal("administrator create lacked password confirmation", err)
	}
	partner.CurrentPassword = testPassword
	second, err := s.CreateUser(ctx, token, partner)
	if err != nil || second.Role != "admin" {
		t.Fatal("administrator create failed", err)
	}
	admins, err := s.listUsers(ctx, token, "", "", 10, "admin")
	if err != nil || len(admins.Users) != 2 {
		t.Fatal("administrator directory did not contain both admins", err)
	}

	input := createInput("delegation.member")
	input.Password = "MemberPassword2026"
	input.Features = []string{"overview"}
	member, err := s.CreateUser(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := s.Login(ctx, member.Username, input.Password, testIP)
	if err != nil {
		t.Fatal(err)
	}
	memberKey := issueTestKey(t, s, memberToken, "read")
	change := userInput(member)
	change.Role, change.CurrentPassword = "admin", input.Password
	if _, err = s.UpdateUser(ctx, token, change); !errors.Is(err, ErrCurrentPassword) {
		t.Fatal("promotion accepted the target's password", err)
	}
	requireKeyState(t, s, memberKey, nil)
	change.CurrentPassword = testPassword
	if _, err = s.UpdateUser(ctx, memberToken, change); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user gained promotion authority", err)
	}
	promoted, err := s.UpdateUser(ctx, token, change)
	if err != nil || promoted.Role != "admin" {
		t.Fatal("promotion failed", err)
	}
	if _, err = s.Current(ctx, memberToken, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("promotion inherited the old session", err)
	}
	requireKeyState(t, s, memberKey, ErrUnauthorized)
	_, promotedToken, err := s.Login(ctx, member.Username, input.Password, testIP)
	if err != nil {
		t.Fatal(err)
	}
	read := issueTestKey(t, s, promotedToken, "read")
	management := issueTestKey(t, s, promotedToken, "management")

	change = userInput(promoted)
	change.Role, change.CurrentPassword = "user", testPassword
	demoted, err := s.UpdateUser(ctx, token, change)
	if err != nil || demoted.Role != "user" || len(demoted.Features) != 1 || demoted.Features[0] != "overview" {
		t.Fatal("demotion lost the ordinary grants", err)
	}
	if _, err = s.Current(ctx, promotedToken, false); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("demotion kept the old session", err)
	}
	requireKeyState(t, s, read, ErrUnauthorized)
	requireKeyState(t, s, management, ErrUnauthorized)
	change = userInput(demoted)
	change.Role, change.CurrentPassword = "admin", testPassword
	if _, err = s.UpdateUser(ctx, token, change); err != nil {
		t.Fatal(err)
	}
	for _, key := range []APIKeyIssuance{memberKey, read, management} {
		requireKeyState(t, s, key, ErrUnauthorized)
	}
	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE actor_id=$1 AND outcome='succeeded'
		AND ((target_id=$2 AND action='user.create_admin') OR (target_id=$3 AND action IN ('user.promote','user.demote')))`,
		admin.ID, second.ID, member.ID).Scan(&count)
	if err != nil || count != 4 {
		t.Fatal("delegation audit did not identify the actor and targets", count, err)
	}
}

func removeAdministrator(ctx context.Context, s *Service, token string, user ManagedUser, action string) error {
	if action == "delete" {
		return s.deleteUser(ctx, token, user.ID, user.Revision, user.Username, "admin")
	}
	input := userInput(user)
	input.Role = "admin"
	if action == "disable" {
		input.Enabled = false
	} else {
		input.Role, input.CurrentPassword = "user", testPassword
	}
	_, err := s.UpdateUser(ctx, token, input)
	return err
}

func TestLastAdministratorRejectsEachRemoval(t *testing.T) {
	s, _ := setup(t)
	id := bootstrap(t, s)
	_, token := login(t, s)
	ctx := context.Background()
	disabled := createInput("disabled.admin")
	disabled.Enabled, disabled.Role, disabled.CurrentPassword = false, "admin", testPassword
	if _, err := s.CreateUser(ctx, token, disabled); err != nil {
		t.Fatal(err)
	}
	admin, err := s.getUser(ctx, token, id.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"disable", "delete", "demote"} {
		if err := removeAdministrator(ctx, s, token, admin, action); !errors.Is(err, ErrLastAdministrator) {
			t.Fatalf("%s of last enabled administrator: %v", action, err)
		}
	}
	if read, err := s.getUser(ctx, token, admin.ID, "admin"); err != nil || !read.Enabled || read.Revision != admin.Revision {
		t.Fatal("rejected removal changed administrator", err)
	}
}

func TestConcurrentAdministratorRemovalsKeepAnAdministrator(t *testing.T) {
	for _, actions := range [][2]string{{"disable", "delete"}, {"delete", "demote"}, {"demote", "disable"}} {
		t.Run(actions[0]+"-"+actions[1], func(t *testing.T) {
			s, pool := setup(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			id := bootstrap(t, s)
			_, firstToken := login(t, s)
			first, err := s.getUser(ctx, firstToken, id.ID, "admin")
			if err != nil {
				t.Fatal(err)
			}
			input := createInput("competing.admin")
			input.Role, input.CurrentPassword = "admin", testPassword
			second, err := s.CreateUser(ctx, firstToken, input)
			if err != nil {
				t.Fatal(err)
			}
			_, secondToken, err := s.Login(ctx, second.Username, testPassword, testIP)
			if err != nil {
				t.Fatal(err)
			}
			start, results := make(chan struct{}), make(chan error, 2)
			go func() {
				<-start
				results <- removeAdministrator(ctx, s, firstToken, first, actions[0])
			}()
			go func() {
				<-start
				results <- removeAdministrator(ctx, New(pool), secondToken, second, actions[1])
			}()
			close(start)
			success, denied := 0, 0
			for range 2 {
				switch err := <-results; {
				case err == nil:
					success++
				case errors.Is(err, ErrLastAdministrator):
					denied++
				default:
					t.Fatal(err)
				}
			}
			var active int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM auth_users WHERE role='admin' AND enabled AND deleted_at IS NULL`).Scan(&active)
			if err != nil || success != 1 || denied != 1 || active != 1 {
				t.Fatalf("handoff had %d successes, %d denials and %d enabled admins: %v", success, denied, active, err)
			}
		})
	}
}
