package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestWeeklyWalletDebtCarriesWithoutChargingUsageTwice(t *testing.T) {
	old := &weeklyWalletEntry{Plan: weeklyWalletPlan{Gross: "100", Debt: "0"}}
	advance, ok := calculateWeeklyWallet(weeklyWalletAmount("50"), "8", old, false)
	if !ok || advance.Target != "50" || advance.Debt != "0" {
		t.Fatal(advance, ok)
	}
	previous := &weeklyWalletEntry{Plan: advance}
	final, ok := calculateWeeklyWallet(weeklyWalletAmount("30"), "15", previous, true)
	if !ok || final.Spent != "35" || final.Target != "0" || final.Debt != "5" {
		t.Fatal(final, ok)
	}
	previous.Plan = final
	nextAdvance, ok := calculateWeeklyWallet(weeklyWalletAmount("3"), "0", previous, false)
	if !ok || nextAdvance.Target != "0" || nextAdvance.Debt != "2" || nextAdvance.OpeningDebt != "5" {
		t.Fatal(nextAdvance, ok)
	}
	previous.Plan = nextAdvance
	nextFinal, ok := calculateWeeklyWallet(weeklyWalletAmount("3"), "0", previous, true)
	if !ok || nextFinal.Target != "0" || nextFinal.Debt != "2" {
		t.Fatal("a final calculation credited or recovered the same opening debt twice", nextFinal, ok)
	}
	previous.Plan = nextFinal
	following, ok := calculateWeeklyWallet(weeklyWalletAmount("10"), "0", previous, false)
	if !ok || following.Target != "8" || following.Debt != "0" {
		t.Fatal("the next grant did not recover the remaining debt", following, ok)
	}
	if pre, ok := calculateWeeklyWallet(weeklyWalletAmount("50"), "-2", old, false); !ok || pre.Target != "48" || pre.OpeningDebt != "2" {
		t.Fatal("native overdraft was not carried independently", pre, ok)
	}
	if _, ok := calculateWeeklyWallet(weeklyWalletAmount("60"), "51", &weeklyWalletEntry{Plan: advance}, true); ok {
		t.Fatal("an unexpected credit was counted as negative usage")
	}
}

func TestWeeklyWalletWorkerNativeAdvanceSettlementAndNoReplay(t *testing.T) {
	t.Run("automatic without prepay enabled", func(t *testing.T) { testWeeklyWalletSettlement(t, false) })
	t.Run("manual recommendation without automation", func(t *testing.T) { testWeeklyWalletSettlement(t, true) })
}

func testWeeklyWalletSettlement(t *testing.T, manual bool) {
	s, db, token := setup(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	reset := start.Add(nativeWeek)
	var mu sync.Mutex
	point, percent := start.Add(time.Hour), 8
	balance, writes := 0, 0
	hideProof := false
	var ledger []map[string]any
	share, cost := "100", json.Number("2.4")
	if manual {
		share, cost = "40", json.Number("6")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("unexpected source authentication")
		}
		switch r.URL.Path {
		case "/api/v1/admin/system/version":
			respond(w, map[string]string{"version": "0.2.23"})
		case "/api/v1/admin/groups/0/model-allowlist-candidates":
			respond(w, map[string][]string{"models": {"gpt-5"}})
		case "/api/v1/admin/settings":
			respond(w, map[string]bool{"affiliate_admin_recharge_enabled": false})
		case "/api/v1/admin/accounts/8":
			a := fullManagedAccount(8, "weekly-allocation-test")
			a["extra"] = map[string]any{"codex_usage_updated_at": point, "codex_5h_window_minutes": 0,
				"codex_7d_window_minutes": 10080, "codex_7d_used_percent": percent, "codex_7d_reset_at": reset}
			respond(w, a)
		case "/api/v1/admin/usage":
			items := []map[string]any{
				{"id": 1, "account_id": 8, "created_at": start, "total_cost": 4, "actual_cost": cost, "billing_type": 0},
				{"id": 2, "account_id": 8, "created_at": start.Add(time.Hour), "total_cost": 4, "actual_cost": cost, "billing_type": 1},
				{"id": 3, "account_id": 8, "created_at": start.Add(2 * time.Hour), "total_cost": 4, "actual_cost": cost, "billing_type": 0},
			}
			size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
			total := len(items)
			if r.URL.Query().Get("sort_order") == "desc" {
				items = items[2:]
			}
			respond(w, map[string]any{"items": items, "total": total, "page": 1, "page_size": size})
		case "/api/v1/admin/users/21":
			row := nativeUserRow(21)
			row["balance"], row["frozen_balance"] = balance, 0
			respond(w, row)
		case "/api/v1/admin/users/21/balance":
			var in struct {
				Balance   int    `json:"balance"`
				Operation string `json:"operation"`
				Notes     string `json:"notes"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil || in.Notes == "" ||
				in.Notes != r.Header.Get("Idempotency-Key") || in.Balance <= 0 {
				t.Error("missing atomic adjustment identity")
				http.Error(w, "bad", 400)
				return
			}
			delta := in.Balance
			if in.Operation == "subtract" {
				delta = -delta
			} else if in.Operation != "add" {
				t.Error("absolute balance replacement attempted")
			}
			balance += delta
			writes++
			ledger = append([]map[string]any{{"id": writes, "notes": in.Notes, "type": "admin_balance", "status": "used", "used_by": 21, "value": delta}}, ledger...)
			// The operation succeeds even though its transport acknowledgement is lost.
			if hideProof {
				http.Error(w, "ack lost", 502)
				return
			}
			respond(w, map[string]int{"id": 21})
		case "/api/v1/admin/users/21/balance-history":
			items := ledger
			if items == nil || hideProof {
				items = []map[string]any{}
			}
			respond(w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100})
		default:
			t.Errorf("unexpected native call: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 8)
	member := createParticipant(t, s, token, c, 21, "Owned weekly member")
	pool := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{member}, []string{share})})
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled, policy.StaleAfterSeconds = true, 3*24*60*60
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	status := func() WeeklyWalletStatus {
		t.Helper()
		got, err := s.GetWeeklyWallet(ctx, token, pool.PoolID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	configure := func(enabled, settle bool) WeeklyWalletStatus {
		t.Helper()
		current := status()
		got, err := s.ConfigureWeeklyWallet(ctx, token, WeeklyWalletInput{ID: pool.PoolID, Revision: current.Revision,
			PoolRevision: pool.Revision, Enabled: enabled, AutoAllocate: settle})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if out := status(); out.State != "disabled" || out.Enabled {
		t.Fatal("unexpected initial control", out)
	}
	exec(t, db, `INSERT INTO weekly_wallet_allocations(pool_id,account_id,participant_id,binding_id,period_end,phase,recorded_at,snapshot)
	 VALUES($1,$2,$3,$4,$5,'settlement',$6,$7)`, pool.PoolID, a.ID, member.ID, member.Binding.ID, start, start.Add(-time.Hour),
		weeklyWalletPlan{Gross: "100", Spent: "0", OpeningDebt: "0", Debt: "0", BalanceBefore: "100", Target: "100", ContractID: pool.ID, EstimateID: &pool.ID})
	collectObservation(t, s, db, token, c, a)
	step := func() {
		t.Helper()
		if _, err := s.processWeeklyWallet(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	step()
	if len(status().Entries) != 1 {
		t.Fatal("disabled control attempted an advance")
	}
	if out := configure(true, false); out.State != "enabled" || out.AutoAllocate {
		t.Fatal("advance-only state not activated", out)
	}
	step()
	step()
	now := status()
	if len(now.Entries) != 2 || now.Entries[0].State != "applied" || now.Entries[0].Plan.Target != "50" {
		t.Fatal("the previous final amount was not advanced exactly once", now)
	}
	if _, err = s.ConfigureWeeklyWallet(ctx, token, WeeklyWalletInput{ID: pool.PoolID, Revision: "1", PoolRevision: pool.Revision}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale disable succeeded", err)
	}
	if _, err = s.GetWeeklyWallet(ctx, "", pool.PoolID); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("unauthenticated reader saw allocation history", err)
	}
	mu.Lock()
	balance, point, percent = 15, start.Add(2*time.Hour), 16
	mu.Unlock()
	collectObservation(t, s, db, token, c, a)
	mu.Lock()
	point, percent = start.Add(3*time.Hour), 24
	mu.Unlock()
	collectObservation(t, s, db, token, c, a)
	for i := 0; i < 4; i++ {
		if _, err = s.processNativeWeekly(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	step()
	if len(status().Entries) != 2 {
		t.Fatal("advance-only control performed a settlement")
	}
	automatic := !manual
	if out := configure(false, automatic); out.Enabled || out.AutoAllocate != automatic {
		t.Fatal("settlement depends on enabling prepay", out)
	}
	if _, err = s.GetWeeklyWalletRecommendation(ctx, "", member.ID); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("unauthenticated reader saw the recommendation", err)
	}
	quote, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || quote.State != "ready" || quote.Pool == nil || quote.Pool.ID != pool.PoolID || quote.Proposal == nil || quote.Proposal.Delta != "-15" ||
		quote.Proposal.Plan.Gross != "30" || quote.Proposal.Plan.SharePercent != share || quote.Proposal.Plan.Debt != "5" {
		t.Fatal("failed to generate the native recommendation across mixed billing types", quote, err)
	}
	request := WeeklyWalletApplyInput{PoolID: pool.PoolID, ParticipantID: member.ID, Basis: quote.Proposal.Basis}
	if manual {
		mu.Lock()
		writesBefore := writes
		balance = 14
		mu.Unlock()
		if _, err = s.ApplyWeeklyWallet(ctx, token, request); !errors.Is(err, auth.ErrConflict) {
			t.Fatal("an earlier recommendation applied after the native balance changed", err)
		}
		mu.Lock()
		if writes != writesBefore {
			t.Error("stale recommendation dispatched a native write")
		}
		balance = 15
		mu.Unlock()
	}
	mu.Lock()
	hideProof = true
	mu.Unlock()
	if manual {
		got, err := s.ApplyWeeklyWallet(ctx, token, request)
		if err != nil || got.State != "unknown" || got.ApplicationID == nil {
			t.Fatal("manual settlement lost its native receipt", got, err)
		}
		if _, err = s.ApplyWeeklyWallet(ctx, token, request); !errors.Is(err, auth.ErrConflict) {
			t.Fatal("repeat request bypassed an uncertain settlement", err)
		}
	} else {
		step()
	}
	now = status()
	last := now.Entries[0]
	if len(now.Entries) != 3 || last.State != "unknown" || last.Phase != "settlement" || last.Plan.Gross != "30" ||
		last.Plan.Spent != "35" || last.Plan.Target != "0" || last.Plan.Debt != "5" {
		t.Fatal("uncertain or inexact native settlement", now)
	}
	s, err = New(db, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	step()
	mu.Lock()
	gotBalance, sent := balance, writes
	hideProof = false
	mu.Unlock()
	if gotBalance != 0 || sent != 2 {
		t.Fatal("restart re-dispatched a possibly applied deduction", gotBalance, sent)
	}
	out, err := s.ReconcileWalletApplication(ctx, token, *last.ApplicationID)
	if err != nil || out.State != "applied" {
		t.Fatal("deduction was not confirmed by its original native record", out.State, err)
	}
	step()
	now = status()
	if len(now.Entries) != 3 || now.Entries[0].State != "applied" || now.Entries[0].Plan.Debt != "5" {
		t.Fatal("lost the confirmed current debt", now)
	}
	mu.Lock()
	sent = writes
	mu.Unlock()
	if sent != 2 {
		t.Fatalf("expected one credit and one debit, got %d adjustments", sent)
	}
}
