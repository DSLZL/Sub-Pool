package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestTemporaryHistoryAndNoCarryReplacementPreserveEarlierRights(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	first, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.StopTemporary(ctx, f.token, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.processTemporary(ctx); err != nil {
		t.Fatal(err)
	}
	a, member := first.Scope.Accounts[0], first.Scope.Accounts[0].Members[0]
	rights, err := f.s.GetTemporaryRights(ctx, f.token, first.ID, a.AccountID)
	if err != nil || rights.Period == nil {
		t.Fatal(rights, err)
	}
	_, err = f.s.ChangeTemporaryRights(ctx, f.token, TemporaryRightsChange{RequestID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{51}, 16)), RoundID: first.ID, AccountID: a.AccountID, ParticipantID: member.ParticipantID, BindingID: member.BindingID, Revision: rights.Revision, Period: *rights.Period, After: "-0.00000001", Reason: "retain current rights across no-carry replacement"})
	if err != nil {
		t.Fatal(err)
	}
	refresh := func() {
		t.Helper()
		for _, p := range []Participant{f.p, f.q} {
			view, err := f.s.GetWallet(ctx, f.token, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.RefreshWallet(ctx, f.token, walletExpected(p, f.c, view.LatestRead.ID)); err != nil {
				t.Fatal(err)
			}
		}
	}
	refresh()
	in := temporaryStartInput(t, f)
	in.RequestID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{52}, 16))
	in.Mode, in.Informed, in.Deadline = "no_carry", true, time.Now().UTC().Add(500*time.Millisecond).Truncate(time.Microsecond)
	second, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(second.Deadline) + 10*time.Millisecond)
	refresh()
	in = temporaryStartInput(t, f)
	in.RequestID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{53}, 16))
	third, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil {
		t.Fatal("replacement must finish the prior no-carry facts atomically", err)
	}
	old, err := f.s.GetTemporaryRound(ctx, f.token, second.ID)
	if err != nil || old.Round.Exit == nil || old.Round.Exit.Reason != "deadline" || len(old.Settlements) != len(second.Scope.Accounts) {
		t.Fatal(old, err)
	}
	for _, s := range old.Settlements {
		if s.State != "no_carry" {
			t.Fatal(s)
		}
	}
	rights, err = f.s.GetTemporaryRights(ctx, f.token, first.ID, a.AccountID)
	if err != nil || rights.Adjustments[member.ParticipantID] != "-0.00000001" {
		t.Fatal("no-carry erased previous current rights", rights, err)
	}
	before, err := f.s.GetTemporaryRound(ctx, f.token, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM temporary_rounds)+(SELECT count(*) FROM temporary_exits)+(SELECT count(*) FROM temporary_events)").Scan(&count); err != nil {
		t.Fatal(err)
	}
	after := ""
	for i, id := range []string{third.ID, second.ID, first.ID} {
		page, err := f.s.ListTemporaryHistory(ctx, f.token, after, 1)
		if err != nil || len(page.Rounds) != 1 || page.Rounds[0].ID != id {
			t.Fatal("history order/cursor", page, err)
		}
		if i < 2 {
			if page.NextAfter == nil || *page.NextAfter != id {
				t.Fatal(page)
			}
			after = *page.NextAfter
		} else if page.NextAfter != nil {
			t.Fatal(page)
		}
	}
	if _, err = f.s.ListTemporaryHistory(ctx, f.token, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{54}, 16)), 1); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("unknown cursor", err)
	}
	retained, err := f.s.GetTemporaryRound(ctx, f.token, first.ID)
	if err != nil || !reflect.DeepEqual(before, retained) {
		t.Fatal("history changed old facts", retained, err)
	}
	var final int
	if err = f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM temporary_rounds)+(SELECT count(*) FROM temporary_exits)+(SELECT count(*) FROM temporary_events)").Scan(&final); err != nil || count != final || f.writes.Load() != 0 {
		t.Fatal("history performed mutations", count, final, err)
	}
}
