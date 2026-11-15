package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestTemporaryAvailabilityRequiresCurrentAdminWithoutCreatingRoundsOrEffects(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "temporary-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore, events, applications, notifications int
	if err = f.db.QueryRow(ctx, "SELECT count(*) FROM management_events").Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	callsBefore := f.calls.Load()
	want := TemporaryStatus{State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", RoundState: "not_implemented", RightsState: "not_implemented"}
	actual, err := f.s.GetTemporaryStatus(ctx, f.token)
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatal("temporary availability invented an enabled round or rights", actual, err)
	}
	restarted, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	if actual, err = restarted.GetTemporaryStatus(ctx, f.token); err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatal("service replacement changed temporary availability", actual, err)
	}
	if out, err := f.s.GetTemporaryStatus(ctx, reader); !errors.Is(err, auth.ErrForbidden) || !reflect.DeepEqual(out, TemporaryStatus{}) {
		t.Fatal("ordinary scope exposed global temporary capability", out, err)
	}
	exec(t, f.db, "UPDATE auth_users SET role='user' WHERE username='admin'")
	if out, err := f.s.GetTemporaryStatus(ctx, f.token); !errors.Is(err, auth.ErrForbidden) || !reflect.DeepEqual(out, TemporaryStatus{}) {
		t.Fatal("former administrator retained temporary authority", out, err)
	}
	exec(t, f.db, "UPDATE auth_users SET role='admin' WHERE username='admin'")
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(100 * time.Millisecond)
	exec(t, f.db, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest[:], deadline)
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if out, err := f.s.GetTemporaryStatus(ctx, f.token); !errors.Is(err, auth.ErrUnauthorized) || !reflect.DeepEqual(out, TemporaryStatus{}) {
		t.Fatal("expired session received temporary capability", out, err)
	}
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM management_events),(SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM notifications)").Scan(&events, &applications, &notifications); err != nil || events != eventsBefore || applications != 0 || notifications != 0 || f.calls.Load() != callsBefore {
		t.Fatal("availability created an audit, financial request, notification or upstream call", events, applications, notifications, err)
	}
}

func TestTemporaryGlobalProceduresRejectOrdinaryAndFormerAdministratorsBeforeEffects(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	in := temporaryStartInput(t, f)
	r, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil {
		t.Fatal(err)
	}
	accountIDs := []string{}
	for _, account := range r.Scope.Accounts {
		accountIDs = append(accountIDs, account.AccountID)
	}
	user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "temporary-scoped-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"temporary", "balances", "notifications"}, AccountIDs: accountIDs, ParticipantIDs: []string{f.p.ID, f.q.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, ordinary, err := f.s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	a, m := r.Scope.Accounts[0], r.Scope.Accounts[0].Members[0]
	change := TemporaryRightsChange{RequestID: r.ID, RoundID: r.ID, AccountID: a.AccountID, ParticipantID: m.ParticipantID, BindingID: m.BindingID, Revision: "0", Period: a.SettlementPeriod, After: "0", Reason: "unauthorized edit"}
	actions := []struct {
		name string
		run  func(string) error
	}{
		{"preflight", func(token string) error { _, err := f.s.GetTemporaryPreflight(ctx, token); return err }},
		{"history", func(token string) error { _, err := f.s.ListTemporaryHistory(ctx, token, "", 1); return err }},
		{"round", func(token string) error { _, err := f.s.GetTemporaryRound(ctx, token, r.ID); return err }},
		{"start-replay", func(token string) error { _, err := f.s.StartTemporary(ctx, token, in); return err }},
		{"stop", func(token string) error { _, err := f.s.StopTemporary(ctx, token, r.ID); return err }},
		{"rights", func(token string) error { _, err := f.s.GetTemporaryRights(ctx, token, r.ID, a.AccountID); return err }},
		{"rights-change", func(token string) error { _, err := f.s.ChangeTemporaryRights(ctx, token, change); return err }},
		{"reminders", func(token string) error {
			_, err := f.s.ConfigureTemporaryReminders(ctx, token, TemporaryRemindersInput{RoundID: r.ID, Revision: "0", Enabled: true, Threshold: "25"})
			return err
		}},
	}
	var before, after int
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM temporary_rounds)+(SELECT count(*) FROM temporary_exits)+(SELECT count(*) FROM temporary_events)+(SELECT count(*) FROM management_events)+(SELECT count(*) FROM wallet_application_requests)+(SELECT count(*) FROM notifications)").Scan(&before); err != nil {
		t.Fatal(err)
	}
	calls := f.calls.Load()
	for _, token := range []string{ordinary, f.token} {
		if token == f.token {
			exec(t, f.db, "UPDATE auth_users SET role='user' WHERE username='admin'")
		}
		for _, action := range actions {
			if err = action.run(token); !errors.Is(err, auth.ErrForbidden) {
				t.Fatal(action.name, "global operation accepted an unauthorized actor", err)
			}
		}
	}
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM temporary_rounds)+(SELECT count(*) FROM temporary_exits)+(SELECT count(*) FROM temporary_events)+(SELECT count(*) FROM management_events)+(SELECT count(*) FROM wallet_application_requests)+(SELECT count(*) FROM notifications)").Scan(&after); err != nil || before != after || calls != f.calls.Load() || f.writes.Load() != 0 {
		t.Fatal("denied operations created effects", before, after, err)
	}
}
