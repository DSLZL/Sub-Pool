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
	if actual, err = restarted.GetTemporaryStatus(ctx, f.token); err != nil || actual != want {
		t.Fatal("service replacement changed temporary availability", actual, err)
	}
	if out, err := f.s.GetTemporaryStatus(ctx, reader); !errors.Is(err, auth.ErrForbidden) || out != (TemporaryStatus{}) {
		t.Fatal("ordinary scope exposed global temporary capability", out, err)
	}
	exec(t, f.db, "UPDATE auth_users SET role='user' WHERE username='admin'")
	if out, err := f.s.GetTemporaryStatus(ctx, f.token); !errors.Is(err, auth.ErrForbidden) || out != (TemporaryStatus{}) {
		t.Fatal("former administrator retained temporary authority", out, err)
	}
	exec(t, f.db, "UPDATE auth_users SET role='admin' WHERE username='admin'")
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(100 * time.Millisecond)
	exec(t, f.db, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest[:], deadline)
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if out, err := f.s.GetTemporaryStatus(ctx, f.token); !errors.Is(err, auth.ErrUnauthorized) || out != (TemporaryStatus{}) {
		t.Fatal("expired session received temporary capability", out, err)
	}
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM management_events),(SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM notifications)").Scan(&events, &applications, &notifications); err != nil || events != eventsBefore || applications != 0 || notifications != 0 || f.calls.Load() != callsBefore {
		t.Fatal("availability created an audit, financial request, notification or upstream call", events, applications, notifications, err)
	}
}
