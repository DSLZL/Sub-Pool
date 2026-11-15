package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func temporaryPersonalEffect(t *testing.T, row WalletContribution) map[string]any {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	effect, ok := value["temporary_effect"].(map[string]any)
	if !ok {
		t.Fatal("qualified contribution must record its own temporary effect", string(raw))
	}
	return effect
}

func TestTemporaryPersonalAdviceKeepsFullAllocationAndProjectsOnlyAuthorizedInputs(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	before, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || len(before.Contributions) != 1 {
		t.Fatal("ordinary prerequisite", before, err)
	}
	if got := temporaryPersonalEffect(t, before.Contributions[0]); !reflect.DeepEqual(got, map[string]any{"state": "ordinary"}) {
		t.Fatal("ordinary input", got)
	}
	in := temporaryStartInput(t, f)
	round, err := f.s.StartTemporary(ctx, f.token, in)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh(t, before.LatestRead.ID)
	active, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || active.CurrentTarget == nil || len(active.Contributions) != 1 {
		t.Fatal("active prerequisite", active, err)
	}
	var ownCap string
	for _, cap := range round.Caps {
		if cap.AccountID == f.a.ID && cap.ParticipantID == f.p.ID {
			ownCap = cap.Amount
		}
	}
	effect := temporaryPersonalEffect(t, active.Contributions[0])
	if len(effect) != 4 || effect["state"] != "active" || effect["mode"] != round.Mode || effect["cap"] != ownCap || effect["deadline"] != round.Deadline.Format(time.RFC3339Nano) {
		t.Fatal("effect must contain only the member's frozen input", effect)
	}
	create := func(name string, accounts, participants []string) string {
		t.Helper()
		user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: name, Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: accounts, ParticipantIDs: participants}})
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := f.s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	reader := create("temporary.personal.reader", []string{f.a.ID}, []string{f.p.ID})
	own, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || len(own.Contributions) != 1 || own.LatestRecommendation != nil || own.CurrentTarget != nil || own.Contributions[0].SimulationProof != nil || !reflect.DeepEqual(own.Contributions[0].Amount, active.Contributions[0].Amount) || !reflect.DeepEqual(temporaryPersonalEffect(t, own.Contributions[0]), effect) {
		t.Fatal("ordinary projection changed allocation or exposed full proof", own, err)
	}
	if _, err = f.s.GetTemporaryStatus(ctx, reader); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("reader reached global round", err)
	}
	if _, err = f.s.GetWallet(ctx, reader, f.q.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("reader reached another participant", err)
	}
	noAccount := create("temporary.personal.noaccount", []string{}, []string{f.p.ID})
	hidden, err := f.s.GetWallet(ctx, noAccount, f.p.ID)
	if err != nil || len(hidden.Contributions) != 0 {
		t.Fatal("account intersection leaked personal input", hidden, err)
	}
	if _, err = f.s.StopTemporary(ctx, f.token, round.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || stale.Contributions[0].Amount != nil {
		t.Fatal("stopped advice stayed current", stale, err)
	}
	raw, _ := json.Marshal(stale.Contributions[0])
	var staleJSON map[string]any
	_ = json.Unmarshal(raw, &staleJSON)
	if _, found := staleJSON["temporary_effect"]; found {
		t.Fatal("stale advice retained temporary input", string(raw))
	}
	if f.writes.Load() != 0 {
		t.Fatal("personal advice dispatched a wallet write")
	}
}

func TestTemporaryPersonalCurrentRightsAreExactAndClearWithoutWalletWrites(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	round, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.StopTemporary(ctx, f.token, round.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.processTemporary(ctx); err != nil {
		t.Fatal(err)
	}
	account := round.Scope.Accounts[0]
	member := account.Members[0]
	for _, candidate := range account.Members {
		if candidate.ParticipantID == f.p.ID {
			member = candidate
		}
	}
	rights, err := f.s.GetTemporaryRights(ctx, f.token, round.ID, account.AccountID)
	if err != nil || rights.Period == nil {
		t.Fatal("rights prerequisite", rights, err)
	}
	change := TemporaryRightsChange{RequestID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{61}, 16)), RoundID: round.ID, AccountID: account.AccountID, ParticipantID: member.ParticipantID, BindingID: member.BindingID, Revision: rights.Revision, Period: *rights.Period, After: "-0.00000001", Reason: "personal input exactness"}
	if _, err = f.s.ChangeTemporaryRights(ctx, f.token, change); err != nil {
		t.Fatal(err)
	}
	old, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh(t, old.LatestRead.ID)
	current, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || len(current.Contributions) != 1 {
		t.Fatal("rights current prerequisite", current, err)
	}
	got := temporaryPersonalEffect(t, current.Contributions[0])
	if !reflect.DeepEqual(got, map[string]any{"state": "rights", "adjustment": "-0.00000001"}) {
		t.Fatal("signed rights were rounded or mistaken for target delta", got)
	}
	rights, err = f.s.GetTemporaryRights(ctx, f.token, round.ID, account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	change.RequestID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{62}, 16))
	change.Revision, change.After = rights.Revision, "0"
	if _, err = f.s.ChangeTemporaryRights(ctx, f.token, change); err != nil {
		t.Fatal(err)
	}
	f.refresh(t, current.LatestRead.ID)
	cleared, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got = temporaryPersonalEffect(t, cleared.Contributions[0]); !reflect.DeepEqual(got, map[string]any{"state": "ordinary"}) {
		t.Fatal("cleared rights remained active", got)
	}
	if f.writes.Load() != 0 {
		t.Fatal("rights explanation dispatched a wallet write")
	}
}

func TestTemporaryPersonalDeadlineInvalidatesAdviceWithoutWaitingForWorker(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	input := temporaryStartInput(t, f)
	input.Deadline = time.Now().UTC().Add(3 * time.Second).Truncate(time.Microsecond)
	round, err := f.s.StartTemporary(ctx, f.token, input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh(t, before.LatestRead.ID)
	active, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || active.CurrentTarget == nil {
		t.Fatal("deadline prerequisite", active, err)
	}
	if got := temporaryPersonalEffect(t, active.Contributions[0]); got["state"] != "active" {
		t.Fatal(got)
	}
	time.Sleep(time.Until(round.Deadline) + time.Millisecond)
	expired, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || expired.CurrentTarget != nil || expired.Contributions[0].Amount != nil || expired.Contributions[0].TemporaryEffect != nil {
		t.Fatal("logical deadline retained current personal input", expired, err)
	}
	if f.writes.Load() != 0 {
		t.Fatal("deadline read dispatched a wallet write")
	}
}
