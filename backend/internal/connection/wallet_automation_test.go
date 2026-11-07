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

func TestWalletAutomationDefaultOffHasNoSideEffectsAndRequiresCurrentAdmin(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "automation-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err = f.db.QueryRow(ctx, "SELECT count(*) FROM management_events").Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	callsBefore := f.calls.Load()
	want := WalletAutomation{Enabled: false, CanEnable: false, State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", ExecutionState: "not_implemented"}
	actual, err := f.s.GetWalletAutomation(ctx, f.token)
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatal("unqualified automation was not explicitly disabled", actual, err)
	}
	restarted, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	actual, err = restarted.GetWalletAutomation(ctx, f.token)
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatal("service replacement enabled automation", actual, err)
	}
	if out, err := f.s.GetWalletAutomation(ctx, reader); !errors.Is(err, auth.ErrForbidden) || out != (WalletAutomation{}) {
		t.Fatal("balances read scope exposed administrator automation", out, err)
	}
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(100 * time.Millisecond)
	exec(t, f.db, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest[:], deadline)
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if out, err := f.s.GetWalletAutomation(ctx, f.token); !errors.Is(err, auth.ErrUnauthorized) || out != (WalletAutomation{}) {
		t.Fatal("expired authority received protected automation", out, err)
	}
	var events, applications int
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM management_events),(SELECT count(*) FROM wallet_application_requests)").Scan(&events, &applications); err != nil || events != eventsBefore || applications != 0 || f.calls.Load() != callsBefore {
		t.Fatal("automation read invented a run, audit, application or upstream operation", events, applications, err)
	}
}
