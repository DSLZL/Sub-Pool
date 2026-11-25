package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

type simulatedWalletFixture struct {
	*simulationCostFixture
	p, q         Participant
	scope        map[string]any
	start        time.Time
	changeWallet func(map[string]any)
	changeLedger func(map[string]any)
}

func simulatedWallet(t *testing.T) *simulatedWalletFixture {
	t.Helper()
	cost := simulatedCostFixture(t)
	f := &simulatedWalletFixture{simulationCostFixture: cost, start: cost.times[0].Add(-time.Hour)}
	closed := cost.times[2].Add(time.Second)
	f.scope = map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": simulationTestEpoch, "user_id": "21", "balance": "95", "frozen_balance": "0", "wallet_version": "7", "ledger_revision": "17", "closed_through": closed.Format(time.RFC3339Nano), "pending_requests": "0", "source_account_ids": []string{"1"}, "scope_complete": true, "billing_type": "standard_wallet", "ledger_closed": true}
	patch := func(page map[string]any) {
		page["source_start"], page["closed_through"] = f.start.Add(-time.Minute).Format(time.RFC3339Nano), closed.Format(time.RFC3339Nano)
		for _, entry := range page["entries"].([]any) {
			row := entry.(map[string]any)
			if row["standard_cost"] == "3.00000000" {
				row["user_id"] = "21"
			} else {
				row["user_id"] = "22"
			}
		}
	}
	f.mu.Lock()
	f.change = func(page map[string]any, _ *http.Request) { patch(page) }
	f.serve = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
			return true
		}
		if r.URL.Path == simulationPrefix+"/wallets/21" {
			f.calls.Add(1)
			f.mu.RLock()
			raw, _ := json.Marshal(f.scope)
			change := f.changeWallet
			f.mu.RUnlock()
			var page map[string]any
			_ = json.Unmarshal(raw, &page)
			if change != nil {
				change(page)
			}
			respond(w, page)
			return true
		}
		if r.URL.Path == simulationPrefix+"/coverage" && r.URL.Query().Get("start") == f.start.Format(time.RFC3339Nano) {
			f.calls.Add(1)
			pages := []map[string]any{}
			for i := range 2 {
				request := r.Clone(r.Context())
				u := *r.URL
				request.URL = &u
				q := u.Query()
				q.Set("start", f.times[i].Format(time.RFC3339Nano))
				q.Set("end", f.times[i+1].Format(time.RFC3339Nano))
				request.URL.RawQuery = q.Encode()
				page := f.page(t, request)
				patch(page)
				pages = append(pages, page)
			}
			page := pages[0]
			page["entries"] = append(page["entries"].([]any), pages[1]["entries"].([]any)...)
			page["start"], page["end"], page["standard_cost"], page["actual_cost"] = r.URL.Query().Get("start"), r.URL.Query().Get("end"), "10", "5"
			f.mu.RLock()
			change := f.changeLedger
			f.mu.RUnlock()
			if change != nil {
				change(page)
			}
			respond(w, page)
			return true
		}
		return false
	}
	f.mu.Unlock()
	f.p = createParticipant(t, f.s, f.token, f.c, 21, "Simulated wallet member")
	f.q = createParticipant(t, f.s, f.token, f.c, 22, "Other simulated member")
	contract := savePool(t, f.s, f.token, PoolUpdate{PoolInput: poolInput(f.c, []ManagedAccount{f.a}, []Participant{f.p, f.q}, []string{"50", "50"})})
	current := savePool(t, f.s, f.token, PoolUpdate{ID: contract.PoolID, Revision: contract.Revision, PoolInput: poolInput(f.c, []ManagedAccount{f.a}, []Participant{f.p, f.q}, []string{"25", "50"})})
	// Unit fixtures set a historical clock explicitly; owned service acceptance creates contracts before real charges.
	exec(t, f.db, `UPDATE pool_contracts SET effective_at=$2 WHERE id=$1`, contract.ID, f.start)
	exec(t, f.db, `UPDATE pool_contracts SET ended_at=$2 WHERE id=$1`, contract.ID, f.times[1])
	exec(t, f.db, `UPDATE pool_contracts SET effective_at=$2 WHERE id=$1`, current.ID, f.times[1])
	exec(t, f.db, `UPDATE participant_bindings SET started_at=$2 WHERE participant_id=ANY($1::text[])`, []string{f.p.ID, f.q.ID}, f.start.Add(-time.Second))
	r, err := f.s.SaveEstimateConfiguration(context.Background(), f.token, f.input())
	if err != nil || r.State != "scenario" {
		t.Fatal("simulated wallet estimator prerequisite", r, err)
	}
	return f
}

func (f *simulatedWalletFixture) refresh(t *testing.T, head string) WalletRead {
	t.Helper()
	r, err := f.s.RefreshWallet(context.Background(), f.token, walletExpected(f.p, f.c, head))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSimulationWalletExactActualDeductionGlobalPrivacyAndReplacement(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	read := f.refresh(t, "0")
	if read.Simulation == nil || read.SimulationScope == nil || read.Balance.Value == nil || *read.Balance.Value != "95.00000000" || *read.FrozenBalance.Value != "0.00000000" {
		t.Fatal("independent simulated fields lost", read)
	}
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.CurrentState != "scenario" || view.CurrentTarget == nil || view.CurrentTarget.Numerator != "77" || view.CurrentTarget.Denominator != "40" || view.Applicable || !view.LatestRecommendation.Complete {
		raw, _ := json.Marshal(view)
		t.Fatal("wallet units/historical cost/remaining or global target wrong", string(raw), err)
	}
	proof := view.Contributions[0].SimulationProof
	if proof.Consumed.Numerator != "5" || proof.Spent.Numerator != "3" || proof.Spent.Denominator != "2" || proof.Remaining.Numerator != "36" || proof.Remaining.Denominator != "5" || proof.Reserve.Numerator != "107" || proof.Reserve.Denominator != "40" {
		t.Fatal("standard cost substituted for actual deduction or reserve renormalized", proof)
	}
	before := f.calls.Load()
	scanWalletInputs(t, f.s)
	same, _ := f.s.GetWallet(ctx, f.token, f.p.ID)
	if same.LatestRecommendation.ID != view.LatestRecommendation.ID || f.calls.Load() != before {
		t.Fatal("unchanged worker duplicated receipt or reread upstream")
	}
	user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "simulated.wallet.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{f.a.ID}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	visible, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || visible.CurrentTarget != nil || visible.LatestRecommendation != nil || visible.NeedsRecalculation != nil || len(visible.Contributions) != 1 || visible.Contributions[0].Amount == nil || visible.Contributions[0].SimulationProof != nil || visible.LatestRead.SimulationScope != nil || visible.LastConfirmedRead.SimulationScope != nil {
		t.Fatal("ordinary wallet projection leaked global evidence", visible, err)
	}
	raw, _ := json.Marshal(visible)
	for _, hidden := range []string{f.q.ID, "source_account_ids", "input_hash", "ledger_revision", "request_id", "user_id\":\"22"} {
		if bytes.Contains(raw, []byte(hidden)) {
			t.Fatal("hidden proof data exposed", hidden)
		}
	}
	if _, err = f.s.ListWalletHistory(ctx, reader, f.p.ID, "recommendation", "0", 100); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary global history", err)
	}
	restarted, err := New(f.db, f.s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := restarted.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || unbound.CurrentTarget != nil || !reflect.DeepEqual(unbound.LatestRecommendation, view.LatestRecommendation) {
		t.Fatal("unbound replacement exposed current money or rewrote history", unbound, err)
	}
	bindSimulation(t, restarted, f.c)
	rebound, err := restarted.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || !reflect.DeepEqual(rebound.CurrentTarget, view.CurrentTarget) || f.calls.Load() != before {
		t.Fatal("replacement replayed source or lost current proof", err)
	}
	exec(t, f.db, `UPDATE auth_users SET account_ids='{}'::text[] WHERE id=$1`, user.ID)
	contracted, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || len(contracted.Contributions) != 0 || contracted.CurrentTarget != nil {
		t.Fatal("permission contraction retained source money", contracted, err)
	}
}

func TestSimulationWalletMissingScopeLedgerOwnershipAndDeficitNeverCreateTarget(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	cases := []struct {
		name           string
		wallet, ledger func(map[string]any)
	}{
		{"foreign-funds", func(p map[string]any) { p["scope_complete"] = false }, nil},
		{"pending", func(p map[string]any) { p["pending_requests"] = "1" }, nil},
		{"frozen", func(p map[string]any) { p["frozen_balance"] = "1" }, nil},
		{"unsealed", func(p map[string]any) { p["ledger_closed"] = false }, nil},
		{"unmanaged-source", func(p map[string]any) { p["source_account_ids"] = []string{"1", "999"} }, nil},
		{"mixed-billing", func(p map[string]any) { p["billing_type"] = "unqualified" }, nil},
		{"scope-epoch", func(p map[string]any) { p["epoch"] = "AwMDAwMDAwMDAwMDAwMDAw" }, nil},
		{"missing-field", func(p map[string]any) { delete(p, "ledger_closed") }, nil},
		{"unowned-charge", nil, func(p map[string]any) { p["entries"].([]any)[0].(map[string]any)["user_id"] = "999" }},
		{"ledger-revision", nil, func(p map[string]any) { p["ledger_revision"] = "18" }},
		{"new-charge-after-quota", nil, func(p map[string]any) {
			p["entries"].([]any)[1].(map[string]any)["charged_at"] = f.times[2].Add(time.Millisecond).Format(time.RFC3339Nano)
		}},
		{"member-deficit", nil, func(p map[string]any) { p["entries"].([]any)[1].(map[string]any)["user_id"] = "21" }},
	}
	head := "0"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			f.changeWallet, f.changeLedger = tc.wallet, tc.ledger
			f.mu.Unlock()
			r := f.refresh(t, head)
			head = r.ID
			view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
			if err != nil || view.CurrentTarget != nil || view.LatestRecommendation.Target != nil || view.LatestRecommendation.Complete || view.Applicable {
				t.Fatal("incomplete/mixed wallet created target", view, err)
			}
		})
	}
	f.mu.Lock()
	f.changeWallet, f.changeLedger = nil, nil
	f.mu.Unlock()
	f.refresh(t, head)
	good, _ := f.s.GetWallet(ctx, f.token, f.p.ID)
	if good.CurrentTarget == nil {
		t.Fatal("new deliberate complete refresh could not recover")
	}
	frozen, _ := json.Marshal(good.LatestRecommendation)
	exec(t, f.db, `UPDATE participants SET enabled=false,revision=revision+1 WHERE id=$1`, f.q.ID)
	changed, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	retained, _ := json.Marshal(changed.LatestRecommendation)
	if err != nil || changed.CurrentTarget != nil || changed.CurrentState != "recalculation_required" || !bytes.Equal(frozen, retained) {
		t.Fatal("member change retained current money or rewrote history", changed, err)
	}
}

func TestSimulationWalletAllSourcesBeforeProjectionAndHiddenChanges(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	f.mu.Lock()
	original := f.serve
	f.serve = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v1/admin/accounts/2" || r.URL.Path == simulationPrefix+"/accounts/2/quota" {
			row := fullManagedAccount(2, "other-simulated-wallet-resource")
			if strings.HasPrefix(r.URL.Path, simulationPrefix) {
				index := int(f.index.Load())
				reset := f.times[0].Add(2 * time.Hour)
				row["extra"] = map[string]any{"codex_usage_updated_at": f.times[index].Format(time.RFC3339Nano), "codex_5h_used_percent": []int{10, 20, 40, 100}[index], "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339Nano)}
				respond(w, map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": simulationTestEpoch, "read_only": true, "account": row})
			} else {
				respond(w, row)
			}
			return true
		}
		if r.URL.Path == simulationPrefix+"/coverage" && r.URL.Query().Get("account_id") == "2" {
			f.calls.Add(1)
			full := r.URL.Query().Get("start") == f.start.Format(time.RFC3339Nano)
			pages := []map[string]any{}
			for i := range 2 {
				request := r.Clone(r.Context())
				u := *r.URL
				request.URL = &u
				q := u.Query()
				q.Set("account_id", "1")
				if full {
					q.Set("start", f.times[i].Format(time.RFC3339Nano))
					q.Set("end", f.times[i+1].Format(time.RFC3339Nano))
				}
				request.URL.RawQuery = q.Encode()
				page := f.page(t, request)
				f.mu.RLock()
				patch := f.change
				f.mu.RUnlock()
				patch(page, request)
				page["account_id"], page["resource_id"] = "2", "other-simulated-wallet-resource"
				page["prices"].([]any)[0].(map[string]any)["account_id"] = "2"
				for _, entry := range page["entries"].([]any) {
					row := entry.(map[string]any)
					seq, _ := strconv.Atoi(row["seq"].(string))
					row["seq"], row["account_id"], row["request_id"] = strconv.Itoa(seq+100), "2", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(seq + 50)}, 16))
				}
				pages = append(pages, page)
				if !full {
					break
				}
			}
			page := pages[0]
			if full {
				page["entries"] = append(page["entries"].([]any), pages[1]["entries"].([]any)...)
				page["standard_cost"], page["actual_cost"] = "10", "5"
			}
			page["start"], page["end"] = r.URL.Query().Get("start"), r.URL.Query().Get("end")
			respond(w, page)
			return true
		}
		return original(w, r)
	}
	f.mu.Unlock()
	second := adoptManaged(t, f.s, f.token, f.c, 2, "Other wallet source")
	pool := savePool(t, f.s, f.token, PoolUpdate{PoolInput: poolInput(f.c, []ManagedAccount{second}, []Participant{f.p, f.q}, []string{"25", "50"})})
	exec(t, f.db, `UPDATE pool_contracts SET effective_at=$2 WHERE id=$1`, pool.ID, f.start)
	for i := range 3 {
		f.index.Store(int32(i))
		collectObservation(t, f.s, f.db, f.token, f.c, second)
	}
	input := estimateInput(second, f.c)
	input.Windows = []string{"short"}
	record, err := f.s.SaveEstimateConfiguration(ctx, f.token, input)
	if err != nil || record.State != "scenario" {
		t.Fatal("second complete source prerequisite", record, err)
	}
	f.mu.Lock()
	f.scope["source_account_ids"] = []string{"1", "2"}
	f.mu.Unlock()
	f.refresh(t, "0")
	global, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || global.CurrentTarget == nil || global.CurrentTarget.Numerator != "139" || global.CurrentTarget.Denominator != "40" || global.LatestRecommendation.Global.SourceCount != 2 {
		raw, _ := json.Marshal(global)
		t.Fatal("full wallet did not sum independently qualified sources", string(raw), err)
	}
	user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "hidden.wallet.source.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{f.a.ID}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	visible, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || len(visible.Contributions) != 1 {
		t.Fatal(err)
	}
	before, _ := json.Marshal(visible.Contributions)
	calls := f.calls.Load()
	exec(t, f.db, `UPDATE estimate_config SET revision=revision+1 WHERE account_id=$1`, second.ID)
	stale, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || stale.CurrentTarget != nil || !*stale.NeedsRecalculation {
		t.Fatal("hidden missing source did not invalidate whole target", stale, err)
	}
	visible, err = f.s.GetWallet(ctx, reader, f.p.ID)
	after, _ := json.Marshal(visible.Contributions)
	if err != nil || !bytes.Equal(before, after) || visible.CurrentTarget != nil || visible.NeedsRecalculation != nil || f.calls.Load() != calls {
		t.Fatal("hidden change leaked global state or current read called source", visible, err)
	}
	scanWalletInputs(t, f.s)
	visible, err = f.s.GetWallet(ctx, reader, f.p.ID)
	after, _ = json.Marshal(visible.Contributions)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("producer erased an unchanged authorized contribution because a hidden source failed", visible, err)
	}
	snapshot := currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	calls = f.calls.Load()
	scanWalletInputs(t, f.s)
	if currentWalletRecommendation(t, f.s, f.token, f.p.ID).ID != snapshot.ID || f.calls.Load() != calls {
		t.Fatal("unchanged partial proof did not coalesce")
	}
}

func TestSimulationWalletKnownZeroAndCompleteAdviceNeverEnableApplication(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	zeroActual := func(page map[string]any) {
		page["actual_cost"] = "0"
		for _, price := range page["prices"].([]any) {
			price.(map[string]any)["wallet_multiplier"] = "0"
		}
		for _, charge := range page["entries"].([]any) {
			charge.(map[string]any)["actual_cost"] = "0"
		}
	}
	f.mu.Lock()
	old := f.change
	f.change = func(page map[string]any, r *http.Request) { old(page, r); zeroActual(page) }
	f.changeLedger = zeroActual
	f.scope["balance"] = "100"
	f.mu.Unlock()
	latest, err := f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RecomputeEstimate(ctx, f.token, f.a.ID, latest.Configuration.Revision, latest.Latest.ID); err != nil {
		t.Fatal(err)
	}
	read := f.refresh(t, "0")
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.CurrentState != "scenario" || view.CurrentTarget == nil || view.CurrentTarget.Numerator != "0" || view.CurrentTarget.Denominator != "1" || view.Applicable {
		t.Fatal("known zero was hidden or became applicable", view, err)
	}
	before := f.calls.Load()
	result, err := f.s.RequestWalletApplication(ctx, f.token, WalletApplicationInput{WalletExpected: walletExpected(f.p, f.c, read.ID), RequestID: "AgICAgICAgICAgICAgICAg", RecommendationID: view.LatestRecommendation.ID})
	if err != nil || result.State != "rejected" || result.DispatchState != "not_dispatched" || result.Target != nil || f.calls.Load() != before {
		t.Fatal("conditional advice reached financial writer", result, err)
	}
}

func TestSimulationWalletFinalAuthorizationAndAuditFailureRollBackIndependentReceipts(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(strconv.FormatBool(expiry), func(t *testing.T) {
			f := simulatedWallet(t)
			ctx := context.Background()
			if expiry {
				digest := sha256.Sum256([]byte(f.token))
				deadline := time.Now().Add(time.Second)
				exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
				arrived, release := make(chan struct{}, 1), make(chan struct{})
				f.mu.Lock()
				f.changeLedger = func(map[string]any) { arrived <- struct{}{}; <-release }
				f.mu.Unlock()
				done := make(chan error, 1)
				go func() {
					r, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
					if r.ID != "" {
						t.Error("expired call returned a protected receipt")
					}
					done <- err
				}()
				select {
				case <-arrived:
				case <-time.After(3 * time.Second):
					close(release)
					t.Fatal("ledger read did not start")
				}
				time.Sleep(max(0, time.Until(deadline)+30*time.Millisecond))
				close(release)
				if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatal("expired authorization accepted complete simulated money", err)
				}
			} else {
				exec(t, f.db, `CREATE FUNCTION simulation_wallet_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='wallet.recommendation' THEN RAISE EXCEPTION 'forced simulated audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER simulation_wallet_audit_fail BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION simulation_wallet_audit_fail()`)
				if r, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0")); err == nil || r.ID != "" {
					t.Fatal("failed audit acknowledged simulated receipt", r, err)
				}
			}
			var count int
			if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_reads)+(SELECT count(*) FROM wallet_recommendations)`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed final boundary partially committed simulated receipts", count, err)
			}
		})
	}
}

func TestSimulationWalletMemberHistoryBoundPrecedesLedgerAndAllocation(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	var contracts []int64
	rows, err := f.db.Query(ctx, `SELECT ca.contract_id FROM pool_contract_accounts ca WHERE ca.account_id=$1 ORDER BY ca.contract_id`, f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		contracts = append(contracts, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(contracts) != 2 {
		t.Fatal(err)
	}
	var existing int
	if err := f.db.QueryRow(ctx, `SELECT count(DISTINCT (cp.participant_id,cp.binding_id))
		FROM pool_contract_participants cp WHERE cp.contract_id=ANY($1::bigint[])`, contracts).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	head := "0"
	for _, size := range []int{1000, 10_000, 10_001} {
		first, last := 1000+existing, 999+size
		seedCapacityMembers(t, f.db, f.p.Binding.SourceID, first, last, f.start.Add(-time.Second))
		exec(t, f.db, `INSERT INTO pool_contract_participants(contract_id,participant_id,revision,binding_id,name,share_units)
			SELECT (CASE WHEN b.upstream_user_id%2=0 THEN $1::bigint ELSE $2::bigint END),p.id,1,b.id,p.name,0
			FROM participant_bindings b JOIN participants p ON p.id=b.participant_id
			WHERE b.source_id=$3 AND b.upstream_user_id BETWEEN $4 AND $5`,
			contracts[0], contracts[1], f.p.Binding.SourceID, first, last)
		existing = size
		before := f.calls.Load()
		head = f.refresh(t, head).ID
		view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
		if err != nil || view.LatestRecommendation == nil {
			t.Fatal("wallet view unavailable", err)
		}
		if size <= 10_000 {
			if view.CurrentState != "scenario" || view.CurrentTarget == nil || view.CurrentTarget.Numerator != "77" || view.CurrentTarget.Denominator != "40" {
				t.Fatalf("%d members changed the exact recommendation", size)
			}
		} else if view.LatestRecommendation.Target != nil || view.LatestRecommendation.Complete ||
			view.LatestRecommendation.Global.State != "evidence_limit" ||
			view.LatestRecommendation.Contributions[0].State != "evidence_limit" ||
			view.Contributions[0].Amount != nil || f.calls.Load()-before != 1 {
			t.Fatal("overflow used a partial ledger or returned a target")
		}
	}
}
