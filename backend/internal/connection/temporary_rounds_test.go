package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func temporaryFixture(t *testing.T) *simulatedApplicationFixture {
	t.Helper()
	f := simulatedApplication(t)
	f.mu.Lock()
	previous := f.serve
	f.serve = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == simulationPrefix+"/wallets/22" {
			f.mu.RLock()
			raw, _ := json.Marshal(f.scope)
			f.mu.RUnlock()
			var scope map[string]any
			_ = json.Unmarshal(raw, &scope)
			scope["user_id"] = "22"
			scope["wallet_version"] = "3"
			respond(w, scope)
			return true
		}
		if r.URL.Path == simulationPrefix+"/wallets/22/write-contract" {
			respond(w, map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": simulationTestEpoch, "user_id": "22", "quantum": simulationWalletQuantum, "write_allowed": true, "lookup_allowed": true})
			return true
		}
		return previous(w, r)
	}
	f.mu.Unlock()
	if _, err := f.s.RefreshWallet(context.Background(), f.token, walletExpected(f.q, f.c, "0")); err != nil {
		t.Fatal(err)
	}
	return f
}
func temporaryStartInput(t *testing.T, f *simulatedApplicationFixture) TemporaryStart {
	t.Helper()
	preflight, err := f.s.GetTemporaryPreflight(context.Background(), f.token)
	if err != nil || preflight.State != "ready" || preflight.Scope == nil {
		t.Fatal("preflight", preflight, err)
	}
	in := TemporaryStart{RequestID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{13}, 16)), ScopeHash: preflight.ScopeHash, Mode: "carry", Deadline: time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond), Confirmed: true, Caps: []TemporaryCap{}, AcknowledgedParticipants: []string{}}
	for _, wallet := range preflight.Scope.Wallets {
		in.AcknowledgedParticipants = append(in.AcknowledgedParticipants, wallet.ParticipantID)
	}
	for _, account := range preflight.Scope.Accounts {
		for _, member := range account.Members {
			value, ok := member.Baseline.Rational()
			if !ok {
				t.Fatal("baseline")
			}
			units := new(big.Int).Quo(new(big.Int).Mul(value.Num(), big.NewInt(100000000)), value.Denom())
			cap := new(big.Rat).SetFrac(units, big.NewInt(100000000))
			in.Caps = append(in.Caps, TemporaryCap{AccountID: account.AccountID, ParticipantID: member.ParticipantID, Amount: cap.FloatString(8)})
		}
	}
	return in
}
func TestTemporaryFrozenStartCurrentAdviceExitAndReplacementNeverDispatch(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	in := temporaryStartInput(t, f)
	before, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil || r.Exit != nil || r.Mode != "carry" {
		t.Fatal("start", r, err)
	}
	again, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("duplicate start", err)
	}
	stale, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || stale.CurrentTarget != nil {
		t.Fatal("old target survived start", stale, err)
	}
	f.refresh(t, before.LatestRead.ID)
	active, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || active.CurrentTarget == nil || active.LatestRecommendation.ExpiresAt.After(r.Deadline) {
		t.Fatal("active caps", active, err)
	}
	stopped, err := f.s.StopTemporary(ctx, f.token, r.ID)
	if err != nil || stopped.Exit == nil || stopped.Exit.Reason != "terminated" {
		t.Fatal("stop", stopped, err)
	}
	if err = f.s.processTemporary(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.GetTemporaryStatus(ctx, f.token)
	if err != nil || status.State != "exited" || status.RightsState != "processed" || status.Settlements[0].State != "cancelled" || status.Restorations[0].RemoteState != "not_required" {
		t.Fatal("separate exit/cancellation/restoration", status, err)
	}
	after, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || after.CurrentTarget != nil {
		t.Fatal("old round target survived stop", err)
	}
	f.refresh(t, active.LatestRead.ID)
	restored, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || !reflect.DeepEqual(restored.CurrentTarget, before.CurrentTarget) {
		t.Fatal("ordinary advice not restored", restored, err)
	}
	replacement, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	bindSimulation(t, replacement, f.c)
	retained, err := replacement.GetTemporaryRound(ctx, f.token, r.ID)
	if err != nil || retained.Round.Exit == nil || retained.Round.Exit.ExitedAt.Location() != time.UTC || !retained.Round.Exit.ExitedAt.Equal(stopped.Exit.ExitedAt) || f.writes.Load() != 0 {
		t.Fatal("replacement altered exit or dispatched", retained, err)
	}
}
func TestTemporaryStartRejectsUnfundedZeroHiddenMemberAndChangedScope(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	good := temporaryStartInput(t, f)
	raw, _ := json.Marshal(good)
	cases := []struct {
		name   string
		change func(*TemporaryStart)
	}{
		{"unfunded", func(in *TemporaryStart) { in.Caps[0].Amount = "999999" }},
		{"zero-wallet", func(in *TemporaryStart) { in.Caps[0].Amount = "0" }},
		{"missing-ack", func(in *TemporaryStart) { in.AcknowledgedParticipants = in.AcknowledgedParticipants[:1] }},
		{"missing-cap", func(in *TemporaryStart) { in.Caps = in.Caps[:1] }},
		{"duplicate-cap", func(in *TemporaryStart) { in.Caps = append(in.Caps, in.Caps[0]) }},
		{"no-carry-uninformed", func(in *TemporaryStart) { in.Mode = "no_carry" }},
		{"deadline", func(in *TemporaryStart) { in.Deadline = time.Now().Add(365 * 24 * time.Hour) }},
		{"hash", func(in *TemporaryStart) { in.ScopeHash = "forged" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var in TemporaryStart
			_ = json.Unmarshal(raw, &in)
			tc.change(&in)
			if _, err := f.s.StartTemporary(ctx, f.token, in); !errors.Is(err, auth.ErrConflict) {
				t.Fatal("unsafe round started", err)
			}
		})
	}
	exec(t, f.db, "UPDATE participants SET revision=revision+1 WHERE id=$1", f.q.ID)
	if _, err := f.s.StartTemporary(ctx, f.token, good); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("changed frozen member started", err)
	}
	var count int
	if err := f.db.QueryRow(ctx, "SELECT count(*) FROM temporary_rounds").Scan(&count); err != nil || count != 0 || f.writes.Load() != 0 {
		t.Fatal("rejected starts mutated", count, err)
	}
}
func TestTemporaryDeadlineAndEarliestOfficialPeriodExitIgnoreWorkerLiveness(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	in := temporaryStartInput(t, f)
	r, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	at := r.Deadline.Add(time.Hour)
	exit, err := temporaryEffectiveExit(ctx, tx, r, at)
	if err != nil || exit == nil || !exit.ExitedAt.Equal(r.Deadline) {
		t.Fatal("late worker extended deadline", exit, err)
	}
	earlier := r.StartedAt.Add(time.Second)
	var snapshot QuotaSnapshot
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT snapshot FROM quota_observations WHERE account_id=$1 ORDER BY id DESC LIMIT 1", r.Scope.Accounts[0].AccountID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), raw...)
	if snapshot.Simulation == nil || *snapshot.Simulation != r.Scope.Simulation || snapshot.SourceID != r.Scope.SourceID || snapshot.AccountRevision != r.Scope.Accounts[0].AccountRevision {
		t.Fatal("changed-period fixture has a different frozen identity")
	}
	snapshot.SourceAt = &earlier
	snapshot.ReceivedAt = earlier
	reset := earlier.Add(10 * time.Minute)
	for i := range snapshot.Windows {
		if snapshot.Windows[i].Kind == r.Scope.Accounts[0].Periods[0].Kind {
			snapshot.Windows[i].ResetAt = &reset
		}
	}
	if check := nativeObservationPeriod(snapshot, r.Scope.Accounts[0].Periods[0].Kind); check == nil || check.EndAt.Equal(r.Scope.Accounts[0].Periods[0].EndAt) {
		t.Fatal("invalid changed-period fixture", snapshot.Windows)
	}
	raw, _ = json.Marshal(snapshot)
	if _, err = tx.Exec(ctx, "UPDATE quota_observations SET snapshot=$1 WHERE id=(SELECT max(id) FROM quota_observations WHERE account_id=$2)", raw, r.Scope.Accounts[0].AccountID); err != nil {
		t.Fatal(err)
	}
	exit, err = temporaryEffectiveExit(ctx, tx, r, at)
	if err != nil || exit == nil || exit.Reason != "period_changed" || !exit.ExitedAt.Equal(earlier) {
		t.Fatal("earliest official change ignored", exit, err, "started", r.StartedAt, "source", snapshot.SourceAt, "frozen", r.Scope.Accounts[0].Periods, "current", nativeObservationPeriod(snapshot, r.Scope.Accounts[0].Periods[0].Kind), "savedExit", r.Exit)
	}
	if _, err = tx.Exec(ctx, "UPDATE quota_observations SET snapshot=$1 WHERE id=(SELECT id FROM quota_observations WHERE account_id=$2 ORDER BY id DESC OFFSET 1 LIMIT 1)", raw, r.Scope.Accounts[0].AccountID); err != nil {
		t.Fatal(err)
	}
	var later QuotaSnapshot
	if err = json.Unmarshal(original, &later); err != nil {
		t.Fatal(err)
	}
	laterAt := earlier.Add(time.Second)
	later.SourceAt, later.ReceivedAt = &laterAt, laterAt
	raw, _ = json.Marshal(later)
	if _, err = tx.Exec(ctx, "UPDATE quota_observations SET snapshot=$1 WHERE id=(SELECT max(id) FROM quota_observations WHERE account_id=$2)", raw, r.Scope.Accounts[0].AccountID); err != nil {
		t.Fatal(err)
	}
	exit, err = temporaryEffectiveExit(ctx, tx, r, at)
	if err != nil || exit == nil || !exit.ExitedAt.Equal(earlier) {
		t.Fatal("a later observation hid an earlier official period change", exit, err)
	}
}

func TestTemporaryFrozenObligationsAndManualRightsAreFundedAndSourceBound(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	r, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = participantObligations(ctx, tx, f.p.ID); !errors.Is(err, errParticipantExitBlocked) {
		t.Fatal("active frozen member lost lifecycle guard", err)
	}
	_ = tx.Rollback(ctx)
	if _, err = f.s.Save(ctx, f.token, SaveInput{Revision: f.c.Revision, BaseURL: f.c.BaseURL, CredentialAction: "keep"}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("active frozen connection changed", err)
	}
	if _, err = f.s.StopTemporary(ctx, f.token, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.processTemporary(ctx); err != nil {
		t.Fatal(err)
	}
	a := r.Scope.Accounts[0]
	p := a.Members[0]
	rights, err := f.s.GetTemporaryRights(ctx, f.token, r.ID, a.AccountID)
	if err != nil || rights.Period == nil {
		t.Fatal(rights, err)
	}
	input := TemporaryRightsChange{RequestID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{41}, 16)), RoundID: r.ID, AccountID: a.AccountID, ParticipantID: p.ParticipantID, BindingID: p.BindingID, Revision: rights.Revision, Period: *rights.Period, After: "-0.00000001", Reason: "explicit owned adjustment"}
	debit, err := f.s.ChangeTemporaryRights(ctx, f.token, input)
	if err != nil || debit.Before != "0.00000000" || debit.Delta != "-0.00000001" {
		t.Fatal("funded debit", debit, err)
	}
	replay, err := f.s.ChangeTemporaryRights(ctx, f.token, input)
	if err != nil || !reflect.DeepEqual(debit, replay) {
		t.Fatal("immutable manual receipt replay", replay, err)
	}
	input.After = "0"
	if _, err = f.s.ChangeTemporaryRights(ctx, f.token, input); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("manual key input replaced", err)
	}
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = participantObligations(ctx, tx, p.ParticipantID); !errors.Is(err, errParticipantExitBlocked) {
		t.Fatal("live rights lost lifecycle guard", err)
	}
	_ = tx.Rollback(ctx)
	rights, err = f.s.GetTemporaryRights(ctx, f.token, r.ID, a.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 16))
	input.Revision = rights.Revision
	clear, err := f.s.ChangeTemporaryRights(ctx, f.token, input)
	if err != nil || clear.Before != "-0.00000001" || clear.After != "0" || clear.Delta != "0.00000001" {
		t.Fatal("clearing debit did not consume exactly funded reserve", clear, err)
	}
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = participantObligations(ctx, tx, p.ParticipantID); err != nil {
		t.Fatal("zero net rights blocked resolved lifecycle", err)
	}
	_ = tx.Rollback(ctx)
	rights, err = f.s.GetTemporaryRights(ctx, f.token, r.ID, a.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{43}, 16))
	input.Revision = rights.Revision
	input.After = "999999"
	if _, err = f.s.ChangeTemporaryRights(ctx, f.token, input); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("unfunded positive rights issued", err)
	}
	if f.writes.Load() != 0 {
		t.Fatal("local rounds or rights dispatched wallet changes")
	}
}
