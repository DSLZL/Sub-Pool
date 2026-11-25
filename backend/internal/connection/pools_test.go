package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestPoolSharePercentAndNestedBoundary(t *testing.T) {
	for value, want := range map[string]int64{"0": 0, "0.000001": 1, "33.333333": 33333333, "100": 100000000, "100.000000": 100000000} {
		got, ok := shareUnits(value)
		if !ok || got != want {
			t.Fatalf("lost exact contractual fraction %s: %d %v", value, got, ok)
		}
		again, ok := shareUnits(sharePercent(got))
		if !ok || again != want {
			t.Fatal("fraction formatting changed its value")
		}
	}
	for _, value := range []string{"-0", "01", "1.", ".5", "+1", "1e1", "NaN", "100.000001", "999", "0.0000001", " 10", "10\n"} {
		if _, ok := shareUnits(value); ok {
			t.Fatalf("invalid contractual fraction accepted: %q", value)
		}
	}
	for _, raw := range []string{
		`[{"id":"x","revision":"1","id":"y"}]`,
		`[{"id":"x","revision":"1","extra":true}]`,
		`[{"id":"x","revision":null}]`,
		`[null]`, `null`, `{}`,
	} {
		if validatePoolMemberJSON(map[string]json.RawMessage{"accounts": json.RawMessage(raw)}) == nil {
			t.Fatalf("ambiguous nested member accepted: %s", raw)
		}
	}
	if validatePoolMemberJSON(map[string]json.RawMessage{"participants": json.RawMessage(`[]`)}) != nil {
		t.Fatal("explicit empty membership was lost")
	}
}

type poolFixtureState struct {
	mu      sync.Mutex
	logs    map[int][]map[string]any
	calls   atomic.Int32
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func poolUpstream(t *testing.T) (*httptest.Server, *poolFixtureState) {
	t.Helper()
	f := &poolFixtureState{logs: map[int][]map[string]any{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("pool workflow sent unsafe upstream request")
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			row := fullManagedAccount(id, fmt.Sprintf("pool-resource-%d", id))
			if id == 10 {
				row["parent_account_id"] = 1
			}
			if id == 11 {
				row["platform"] = "anthropic"
			}
			if id == 12 {
				delete(row, "extra")
			}
			respond(w, row)
			return
		}
		if r.URL.Path == "/api/v1/admin/usage" {
			if f.blocked.Load() {
				f.entered <- struct{}{}
				select {
				case <-f.release:
				case <-r.Context().Done():
					return
				}
			}
			id, _ := strconv.Atoi(r.URL.Query().Get("account_id"))
			f.mu.Lock()
			items := append([]map[string]any{}, f.logs[id]...)
			f.mu.Unlock()
			respond(w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100})
			return
		}
		t.Errorf("unexpected pool upstream route %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server, f
}
func poolInput(c Configuration, accounts []ManagedAccount, participants []Participant, shares []string) PoolInput {
	input := PoolInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, Name: "Shared local pool", Enabled: true, Accounts: []PoolMemberInput{}, Participants: []PoolShareInput{}}
	for _, a := range accounts {
		input.Accounts = append(input.Accounts, PoolMemberInput{ID: a.ID, Revision: a.Revision})
	}
	for i, p := range participants {
		input.Participants = append(input.Participants, PoolShareInput{PoolMemberInput: PoolMemberInput{ID: p.ID, Revision: p.Revision}, SharePercent: shares[i]})
	}
	return input
}
func savePool(t *testing.T, s *Service, token string, input PoolUpdate) PoolContract {
	t.Helper()
	result, err := s.SavePool(context.Background(), token, input)
	if err != nil {
		t.Fatal("pool contract failed", err)
	}
	return result
}

func TestPoolLifecycleExactSharesAndImmutableHistory(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Account one")
	b := adoptManaged(t, s, token, c, 2, "Account two")
	p := createParticipant(t, s, token, c, 21, "Original member")
	q := createParticipant(t, s, token, c, 22, "Other member")
	before := f.calls.Load()
	first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p, q}, []string{"33.333333", "33.333333"})})
	if first.Revision != "1" || !first.IsCurrent || len(first.Accounts) != 1 || *first.UnallocatedPercent != "33.333334" {
		t.Fatal("single-account exact remainder was lost", first)
	}
	overflow := poolInput(c, []ManagedAccount{b}, []Participant{p, q}, []string{"50", "50.000001"})
	if _, err := s.SavePool(ctx, token, PoolUpdate{PoolInput: overflow}); !errors.Is(err, auth.ErrInput) {
		t.Fatal("overallocated pool persisted", err)
	}
	if _, err := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, nil, nil)}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("account entered two current pools", err)
	}
	nextInput := poolInput(c, []ManagedAccount{a, b}, []Participant{p, q}, []string{"75", "25"})
	nextInput.Name = "Expanded pool"
	second := savePool(t, s, token, PoolUpdate{ID: first.PoolID, Revision: first.Revision, PoolInput: nextInput})
	if len(second.Accounts) != 2 || *second.UnallocatedPercent != "0" {
		t.Fatal("multi-account full contract was lost")
	}
	if _, err := s.SavePool(ctx, token, PoolUpdate{ID: first.PoolID, Revision: first.Revision, PoolInput: nextInput}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale pool edit overwrote current contract")
	}
	current := second
	for i := 0; i < 8; i++ {
		nextInput.Name = fmt.Sprintf("Revision %d", i+3)
		current = savePool(t, s, token, PoolUpdate{ID: first.PoolID, Revision: current.Revision, PoolInput: nextInput})
	}
	page, err := s.ListPoolHistory(ctx, token, first.PoolID, "8", 1)
	if err != nil || len(page.Contracts) != 1 || page.Contracts[0].ID != "9" || page.NextAfter == nil || *page.NextAfter != "9" {
		t.Fatal("contract history is not numeric across 9/10", page, err)
	}
	page, err = s.ListPoolHistory(ctx, token, first.PoolID, *page.NextAfter, 100)
	if err != nil || len(page.Contracts) != 2 || page.Contracts[0].ID != "10" || page.Contracts[1].ID != current.ID || page.NextAfter != nil {
		t.Fatal("numeric final history page was lost", page, err)
	}
	edit := participantEdit(p, c)
	edit.Name = "Later name"
	edit.Enabled = false
	changed, err := s.SaveParticipant(ctx, token, edit)
	if err != nil || changed.Participant == nil {
		t.Fatal(err)
	}
	old, err := s.GetPoolContract(ctx, token, first.ID)
	if err != nil || old.Name != first.Name || old.IsCurrent || old.EndedAt == nil || old.CurrentRevision == nil || *old.CurrentRevision != "10" || *old.UnallocatedPercent != "33.333334" {
		t.Fatal("old contract was rewritten", old, err)
	}
	for _, member := range old.Participants {
		if member.ID == p.ID && (member.Name != "Original member" || member.SharePercent != "33.333333" || member.Enabled) {
			t.Fatal("old identity/share or current disabled state was lost")
		}
	}
	if err = s.DeleteParticipant(ctx, token, p.ID, changed.Participant.Revision, changed.Participant.Name); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("pool history owner was deleted")
	}
	rebind := participantEdit(*changed.Participant, c)
	rebind.UpstreamID = "23"
	if _, err = s.SaveParticipant(ctx, token, rebind); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("pool history owner was rebound")
	}
	disabled, err := s.DisablePool(ctx, token, current.PoolID, current.Revision)
	if err != nil || disabled.Enabled || *disabled.UnallocatedPercent != "0" || len(disabled.Accounts) != 2 {
		t.Fatal("disable lost recorded contract or identity", disabled, err)
	}
	if _, err = s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, nil, nil)}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("disabled pool silently released ownership")
	}
	if f.calls.Load() != before {
		t.Fatal("local pools or protected lifecycle contacted upstream")
	}
	encoded, _ := json.Marshal(old)
	if bytes.Contains(encoded, []byte("fingerprint")) || bytes.Contains(encoded, []byte("private-")) || bytes.Contains(encoded, []byte("pool-resource")) {
		t.Fatal("private upstream identity escaped pool contract")
	}
	restarted, _ := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	restored, err := restarted.GetPool(ctx, token, first.PoolID)
	if err != nil || restored.ID != disabled.ID || restored.Enabled {
		t.Fatal("restart lost disabled pool history", err)
	}
}

func TestPoolConcurrentMembershipIdentityAndSourceGates(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	server, _ := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Root")
	b := adoptManaged(t, s, token, c, 2, "Replacement")
	p := createParticipant(t, s, token, c, 21, "Member")
	for _, id := range []int{10, 11, 12} {
		invalid := adoptManaged(t, s, token, c, id, "Unavailable root")
		if _, err := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{invalid}, []Participant{p}, []string{"100"})}); !errors.Is(err, auth.ErrConflict) {
			t.Fatal("shadow/unsupported/unverified quota entered pool", id, err)
		}
	}
	type outcome struct {
		contract PoolContract
		err      error
	}
	done := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, e := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"100"})})
			done <- outcome{r, e}
		}()
	}
	var winner PoolContract
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err == nil {
			success++
			winner = r.contract
		} else if errors.Is(r.err, auth.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(r.err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("concurrent membership was not exclusive")
	}
	next := savePool(t, s, token, PoolUpdate{ID: winner.PoolID, Revision: winner.Revision, PoolInput: poolInput(c, []ManagedAccount{b}, []Participant{p}, []string{"100"})})
	released := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, nil, nil)})
	if *released.UnallocatedPercent != "100" {
		t.Fatal("unallocated pool invented a recipient")
	}
	changed := save(t, s, token, "https://changed.example")
	if changed.SourceID == nil || *changed.SourceID == *c.SourceID {
		t.Fatal("source fixture did not change")
	}
	if _, err := s.SavePool(ctx, token, PoolUpdate{ID: next.PoolID, Revision: next.Revision, PoolInput: poolInput(c, []ManagedAccount{b}, []Participant{p}, []string{"100"})}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale source could rewrite pool contract")
	}
	paused, err := s.DisablePool(ctx, token, next.PoolID, next.Revision)
	if err != nil || paused.Enabled || paused.SourceCurrent || paused.SourceID != *c.SourceID {
		t.Fatal("source change prevented local disable or retargeted history", paused, err)
	}
	if paused.Accounts[0].IdentityCurrent {
		t.Fatal("old source identity remained current")
	}
}

func TestPoolCapturedContractBoundariesAndCurrentTwoDimensionalScope(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "One")
	b := adoptManaged(t, s, token, c, 2, "Two")
	p := createParticipant(t, s, token, c, 21, "P")
	q := createParticipant(t, s, token, c, 22, "Q")
	_ = createParticipant(t, s, token, c, 23, "Outside contract")
	start, end := time.Now().Add(-time.Hour).UTC(), time.Now().Add(time.Hour).UTC()
	input := UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: start.Format(time.RFC3339Nano), EndAt: end.Format(time.RFC3339Nano)}
	f.logs[1] = []map[string]any{usageRow(1, p.Binding.StartedAt.Format(time.RFC3339Nano))}
	before, err := s.CaptureUsage(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{p, q}, []string{"25", "25"})})
	rows := []map[string]any{usageRow(9, first.EffectiveAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)), usageRow(10, first.EffectiveAt.Format(time.RFC3339Nano)), usageRow(11, first.EffectiveAt.Format(time.RFC3339Nano)), usageRow(12, first.EffectiveAt.Format(time.RFC3339Nano)), usageRow(13, first.EffectiveAt.Format(time.RFC3339Nano))}
	rows[2]["user_id"], rows[3]["user_id"], rows[4]["user_id"] = 22, 23, nil
	second := savePool(t, s, token, PoolUpdate{ID: first.PoolID, Revision: first.Revision, PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{p, q}, []string{"75", "25"})})
	rows = append(rows, usageRow(14, second.EffectiveAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)), usageRow(15, second.EffectiveAt.Format(time.RFC3339Nano)))
	f.mu.Lock()
	f.logs[1] = rows
	other := usageRow(1, second.EffectiveAt.Format(time.RFC3339Nano))
	other["account_id"] = 2
	f.logs[2] = []map[string]any{other}
	f.mu.Unlock()
	captured, err := s.CaptureUsage(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	input.AccountReadInput = readInput(b, c)
	hidden, err := s.CaptureUsage(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := s.GetPoolUsageEvidence(ctx, token, captured.ID, "0", 100)
	if err != nil || len(evidence.Records) != 7 {
		t.Fatal("lost captured contract evidence", evidence, err)
	}
	want := []string{"no_recorded_contract", "recorded_contract", "recorded_contract", "contract_member_missing", "unassigned_participant", "recorded_contract", "recorded_contract"}
	for i, r := range evidence.Records {
		if r.Basis != want[i] {
			t.Fatal("invented or lost historical contract basis", i, r)
		}
	}
	if *evidence.Records[1].ContractID != first.ID || *evidence.Records[5].ContractID != first.ID || *evidence.Records[6].ContractID != second.ID {
		t.Fatal("nanosecond boundaries used current shares retroactively")
	}
	old, err := s.GetPoolUsageEvidence(ctx, token, before.ID, "0", 100)
	if err != nil || old.Records[0].ContractID != nil || old.Records[0].Basis != "no_recorded_contract" {
		t.Fatal("new pool backfilled old captured evidence")
	}
	reader, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "pool-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"pools", "usage"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, member, err := s.auth.Login(ctx, reader.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := s.GetPool(ctx, member, first.PoolID)
	if err != nil || scoped.Visibility != "scoped_members" || scoped.CurrentRevision != nil || scoped.UnallocatedPercent != nil || len(scoped.Accounts) != 1 || scoped.Accounts[0].ID != a.ID || len(scoped.Participants) != 1 || scoped.Participants[0].ID != p.ID || scoped.Participants[0].SharePercent != "75" {
		t.Fatal("global or renormalized contract escaped current AND scopes", scoped, err)
	}
	own, err := s.GetPoolUsageEvidence(ctx, member, captured.ID, "0", 2)
	if err != nil || len(own.Records) != 2 || own.Records[0].UpstreamID != "9" || own.Records[1].UpstreamID != "10" || *own.NextAfter != "10" {
		t.Fatal("scope was applied after evidence pagination", own, err)
	}
	own, err = s.GetPoolUsageEvidence(ctx, member, captured.ID, *own.NextAfter, 100)
	if err != nil || len(own.Records) != 2 || own.Records[0].UpstreamID != "14" || own.Records[1].UpstreamID != "15" || own.NextAfter != nil {
		t.Fatal("numeric scoped evidence cursor leaked hidden rows", own, err)
	}
	if _, err = s.GetPoolUsageEvidence(ctx, member, hidden.ID, "0", 100); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("other-account contract facts escaped participant-only scope")
	}
	if _, err = s.DisablePool(ctx, member, first.PoolID, second.Revision); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("reader changed pool")
	}
	third := savePool(t, s, token, PoolUpdate{ID: first.PoolID, Revision: second.Revision, PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{q}, []string{"100"})})
	otherAccount := adoptManaged(t, s, token, c, 3, "Hidden account")
	hiddenPool := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{otherAccount}, []Participant{p}, []string{"100"})})
	if _, err = s.GetPool(ctx, member, first.PoolID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("current membership scope was bypassed")
	}
	history, err := s.ListPoolHistory(ctx, member, first.PoolID, "0", 1)
	if err != nil || len(history.Contracts) != 1 || history.Contracts[0].ID != first.ID || *history.NextAfter != first.ID {
		t.Fatal("history did not filter each immutable revision before page", history, err)
	}
	history, err = s.ListPoolHistory(ctx, member, first.PoolID, *history.NextAfter, 1)
	if err != nil || len(history.Contracts) != 1 || history.Contracts[0].ID != second.ID || history.NextAfter != nil {
		t.Fatal("hidden current contract contaminated history cursor", history, err)
	}
	if _, err = s.GetPoolContract(ctx, member, third.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("forged contract ID bypassed current scopes")
	}
	listed, err := s.ListPools(ctx, member, "", "", 100)
	if err != nil || len(listed.Pools) != 0 {
		t.Fatal("current list leaked historical or other-account membership", listed, err)
	}
	history, err = s.ListPoolHistory(ctx, member, "", "0", 1)
	if err != nil || len(history.Contracts) != 1 || history.Contracts[0].ID != first.ID || history.Contracts[0].CurrentRevision != nil || history.NextAfter == nil || *history.NextAfter != first.ID {
		t.Fatal("global history could not discover authorized old contracts", history, err)
	}
	history, err = s.ListPoolHistory(ctx, member, "", *history.NextAfter, 1)
	if err != nil || len(history.Contracts) != 1 || history.Contracts[0].ID != second.ID || history.NextAfter != nil {
		t.Fatal("global history scope was applied after pagination", history, err)
	}
	if _, err = s.ListPoolHistory(ctx, member, hiddenPool.PoolID, "0", 100); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("explicit hidden history changed its not-found boundary", err)
	}
	reader, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: reader.ID, Revision: reader.Revision, Username: reader.Username, Enabled: true, Grants: auth.Grants{Features: []string{"pools", "usage"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetPoolContract(ctx, member, first.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("revoked participant grant retained historical access")
	}
	if _, err = s.GetPoolUsageEvidence(ctx, member, captured.ID, "0", 100); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("revoked participant scope retained evidence metadata")
	}
	history, err = s.ListPoolHistory(ctx, member, "", "0", 100)
	if err != nil || len(history.Contracts) != 0 || history.NextAfter != nil {
		t.Fatal("global history retained revoked participant metadata", history, err)
	}
}
