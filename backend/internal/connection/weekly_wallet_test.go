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
	hideProof, blockAccount, hideBalance, blockAffiliate := false, false, false, false
	frozen := 0
	var ledger []map[string]any
	share, cost := "100", json.Number("2.4")
	if manual {
		share, cost = "40", json.Number("6")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("x-api-key") == "fixture-rejected" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("unexpected source authentication")
		}
		switch r.URL.Path {
		case "/api/v1/admin/system/version":
			respond(w, map[string]string{"version": "0.2.23"})
		case "/api/v1/admin/groups/0/model-allowlist-candidates":
			respond(w, map[string][]string{"models": {"gpt-5"}})
		case "/api/v1/admin/settings":
			respond(w, map[string]bool{"affiliate_enabled": true, "affiliate_admin_recharge_enabled": blockAffiliate})
		case "/api/v1/admin/accounts/8":
			if blockAccount {
				http.Error(w, "account read failed", http.StatusForbidden)
				return
			}
			a := fullManagedAccount(8, "weekly-allocation-test")
			a["extra"] = map[string]any{"codex_usage_updated_at": point, "codex_5h_window_minutes": 0,
				"codex_7d_window_minutes": 10080, "codex_7d_used_percent": percent, "codex_7d_reset_at": reset}
			respond(w, a)
		case "/api/v1/admin/usage":
			items := []map[string]any{
				{"id": 1, "account_id": 8, "user_id": 21, "model": "gpt-5", "created_at": start, "total_cost": 4, "actual_cost": cost, "billing_type": 0},
				{"id": 2, "account_id": 8, "user_id": 21, "model": "gpt-5", "created_at": start.Add(time.Hour), "total_cost": 4, "actual_cost": cost, "billing_type": 1},
				{"id": 3, "account_id": 8, "user_id": 21, "model": "gpt-5", "created_at": start.Add(2 * time.Hour), "total_cost": 4, "actual_cost": cost, "billing_type": 0},
			}
			size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
			total := len(items)
			if r.URL.Query().Get("start_date") != start.Format(weeklyDate) {
				items, total = items[:0], 0
			} else if r.URL.Query().Get("sort_order") == "desc" {
				items = items[total-1:]
			}
			respond(w, map[string]any{"items": items, "total": total, "page": 1, "page_size": size})
		case "/api/v1/admin/users/21":
			row := nativeUserRow(21)
			row["frozen_balance"] = frozen
			if hideBalance {
				delete(row, "balance")
			} else {
				row["balance"] = balance
			}
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
	historical, advanceAmount, settlementAmount := 1, "10", "20"
	var firstCapacity *map[string]string
	if manual {
		historical, advanceAmount, settlementAmount = 0, "20", "10"
	}
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
			PoolRevision: pool.Revision, Enabled: enabled, AutoAllocate: settle, FirstWeekCapacity: firstCapacity})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if out := status(); out.State != "disabled" || out.Enabled {
		t.Fatal("unexpected initial control", out)
	}
	if manual {
		point, percent = start.Add(time.Minute), 0
	} else {
		exec(t, db, `INSERT INTO weekly_wallet_allocations(pool_id,account_id,participant_id,binding_id,period_end,phase,recorded_at,snapshot)
		 VALUES($1,$2,$3,$4,$5,'settlement',$6,$7)`, pool.PoolID, a.ID, member.ID, member.Binding.ID, start, start.Add(-time.Hour),
			weeklyWalletPlan{Gross: "20", Spent: "0", OpeningDebt: "0", Debt: "0", BalanceBefore: "20", Target: "20", ContractID: pool.ID, EstimateID: &pool.ID})
	}
	collectObservation(t, s, db, token, c, a)
	processEstimate := func() {
		t.Helper()
		for range 4 {
			collectWeeklyTestRound(t, s, ctx, "")
		}
	}
	assertReference := func(samples int, used, reason string, at time.Time) {
		t.Helper()
		quote, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
		if err != nil || quote.State != "waiting_estimate" || quote.Proposal != nil || quote.Reference == nil ||
			quote.Reference.Amount == nil || *quote.Reference.Amount != "30" || len(quote.Reference.Accounts) != 1 {
			t.Fatalf("expected a read-only reference before formal qualification: %+v %v", quote, err)
		}
		row := quote.Reference.Accounts[0]
		if row.AccountID != a.ID || row.Amount == nil || *row.Amount != "30" || row.SharePercent == nil || *row.SharePercent != "40" ||
			row.Samples != samples || row.BasisAt == nil || !row.BasisAt.Equal(at) || row.UsedPercent == nil || *row.UsedPercent != used || row.Reason != reason {
			t.Fatalf("incorrect reference evidence: %+v", row)
		}
	}
	step := func() {
		t.Helper()
		if _, err := s.processWeeklyWallet(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	step()
	if len(status().Issuances) != 0 {
		t.Fatal("disabled control attempted an advance")
	}
	if out := configure(true, false); out.State != "enabled" || out.AutoAllocate {
		t.Fatal("advance-only state not activated", out)
	}
	step()
	if len(status().Issuances) != 0 {
		t.Fatal("ungranted worker created an advance")
	}
	setTestWriteGrant(t, s, token, writeWallet, true)
	if manual {
		step()
		if len(status().Issuances) != 0 {
			t.Fatal("empty first-week capacity issued a credit")
		}
		firstCapacity = &map[string]string{a.ID: "100"}
		if got := configure(true, false); got.FirstWeekCapacity[a.ID] != "100" {
			t.Fatal("first-week capacity was not stored", got)
		}
	}
	s.UpstreamWritesDisabled = true
	step()
	if len(status().Issuances) != 0 {
		t.Fatal("worker created an advance during the emergency stop")
	}
	s.UpstreamWritesDisabled = false
	if !manual {
		legacy := `UPDATE pools SET weekly_wallet=weekly_wallet-'source_continuity' WHERE id=$1`
		exec(t, db, legacy, pool.PoolID)
		before := status()
		saved, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: server.URL + "/", CredentialAction: "keep"})
		if err != nil || saved.SourceChanged || !equalStringPtr(c.SourceID, saved.Configuration.SourceID) || saved.Configuration.Revision == c.Revision {
			t.Fatal("same-source save did not advance the configuration", saved, err)
		}
		c = saved.Configuration
		if got := status(); got.State != "enabled" || got.Revision != before.Revision || !got.Enabled || got.AutoAllocate {
			t.Fatal("current legacy settings did not continue on save", got)
		}

		// Reproduce settings already paused by a revision change before the upgrade.
		exec(t, db, legacy, pool.PoolID)
		c = save(t, s, token, server.URL)
		collectObservation(t, s, db, token, c, a)
		step()
		if got := status(); got.State != "paused" || !got.Enabled || got.AutoAllocate || len(got.Entries) != 1 {
			t.Fatal("previously paused settings resumed without confirmation", got)
		}
		if got := configure(true, false); got.State != "enabled" {
			t.Fatal("saving the existing switches did not resume the control", got)
		}
		rejected, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "fixture-rejected"})
		if err != nil || rejected.SourceChanged {
			t.Fatal("fixture credential rotation failed", rejected, err)
		}
		advanceCollectionFixture(t, db)
		queueAccount(t, s, token, rejected.Configuration, a.ID)
		processQueued(t, s)
		step()
		view, err := s.GetEstimateCurrent(ctx, token, a.ID)
		if err != nil || view.NativeWeekly == nil || view.NativeWeekly.State != "quota_unavailable" || len(status().Entries) != 1 {
			t.Fatalf("a rejected credential reused old proof to qualify new work: %+v %v", view.NativeWeekly, err)
		}
		c = save(t, s, token, server.URL)
		collectObservation(t, s, db, token, c, a)
	}
	mu.Lock()
	blockAffiliate = true
	mu.Unlock()
	step()
	step()
	var blockedRequests int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM wallet_application_requests").Scan(&blockedRequests); err != nil || blockedRequests != 0 || len(status().Issuances) != 0 {
		t.Fatal("an unavailable credit created repeat attempts", blockedRequests, err)
	}
	mu.Lock()
	blockAffiliate = false
	mu.Unlock()
	step()
	step()
	now := status()
	if len(now.Entries) != historical || len(now.Issuances) != 1 || now.Issuances[0].State != "applied" || now.Issuances[0].Issuance.Amount != advanceAmount {
		t.Fatal("the previous final amount was not advanced exactly once", now)
	}
	if manual {
		mu.Lock()
		point, percent = start.Add(time.Hour), 8
		mu.Unlock()
		collectObservation(t, s, db, token, c, a)
		processEstimate()
		assertReference(1, "8", "collecting_samples", start.Add(time.Hour))
		collectObservation(t, s, db, token, c, a)
		assertReference(1, "8", "collecting_samples", start.Add(time.Hour))
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
	if manual {
		assertReference(1, "8", "waiting_latest_estimate", start.Add(time.Hour))
		processEstimate()
		assertReference(2, "16", "collecting_samples", start.Add(2*time.Hour))
		assertUnavailable := func(reason string) {
			t.Helper()
			quote, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
			if err != nil || quote.Proposal != nil || quote.Reference == nil || quote.Reference.Amount != nil ||
				len(quote.Reference.Accounts) != 1 || quote.Reference.Accounts[0].Reason != reason || quote.Reference.Accounts[0].Amount != nil {
				t.Fatalf("expected reference to be unavailable for %s: %+v %v", reason, quote, err)
			}
		}
		var id int64
		var work nativeWeeklyWork
		if err := db.QueryRow(ctx, `SELECT id,work FROM native_weekly_estimates WHERE account_id=$1 AND result IS NOT NULL ORDER BY id DESC LIMIT 1`, a.ID).Scan(&id, &work); err != nil {
			t.Fatal(err)
		}
		olderPeriod := *work.Plan.Period
		olderPeriod.StartAt = olderPeriod.StartAt.Add(-nativeWeek)
		changed := work
		changed.Plan.Period = &olderPeriod
		exec(t, db, `UPDATE native_weekly_estimates SET work=$1 WHERE id=$2`, changed, id)
		assertUnavailable("period_changed")
		changed = work
		changed.Plan.Binding = "previous-source"
		exec(t, db, `UPDATE native_weekly_estimates SET work=$1 WHERE id=$2`, changed, id)
		assertUnavailable("condition_changed")
		exec(t, db, `UPDATE native_weekly_estimates SET work=$1 WHERE id=$2`, work, id)
		exec(t, db, `UPDATE monitoring_config SET stale_after_seconds=interval_seconds WHERE singleton`)
		assertUnavailable("source_stale")
		exec(t, db, `UPDATE monitoring_config SET stale_after_seconds=$1 WHERE singleton`, policy.StaleAfterSeconds)
		if out := configure(false, true); out.Enabled || !out.AutoAllocate {
			t.Fatal("automatic allocation was not enabled in the fixture", out)
		}
		step()
		if len(status().Issuances) != 1 {
			t.Fatal("a read-only two-sample reference triggered automatic settlement")
		}
		configure(true, false)
	}
	mu.Lock()
	point, percent = start.Add(3*time.Hour), 24
	mu.Unlock()
	collectObservation(t, s, db, token, c, a)
	processEstimate()
	var estimateID string
	var qualified nativeWeeklyWork
	if err = db.QueryRow(ctx, `SELECT id::text,work FROM native_weekly_estimates WHERE account_id=$1
	 AND result #>> '{cumulative,confidence}'='trusted' ORDER BY id DESC LIMIT 1`, a.ID).Scan(&estimateID, &qualified); err != nil {
		t.Fatal(err)
	}
	if manual {
		exec(t, db, `UPDATE native_weekly_estimates SET result=jsonb_set(result,'{cumulative,confidence}','"unstable"'),
		 work=work-'wallet_capacity' WHERE id=$1`, estimateID)
		assertReference(3, "24", "unstable_estimate", start.Add(3*time.Hour))
		exec(t, db, `UPDATE native_weekly_estimates SET work=$2 WHERE id=$1`, estimateID, qualified)
		ref, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
		if err != nil || ref.Proposal == nil || ref.Proposal.Issuance == nil || !equalStringPtr(ref.Proposal.Issuance.Contributions[0].EstimateID, &estimateID) {
			t.Fatalf("independently trusted actual costs did not qualify: %+v %v", ref, err)
		}
		exec(t, db, `UPDATE native_weekly_estimates SET result=jsonb_set(result,'{cumulative,confidence}','"trusted"') WHERE id=$1`, estimateID)
	}
	mu.Lock()
	point, percent = point.Add(time.Minute), 26
	mu.Unlock()
	collectObservation(t, s, db, token, c, a)
	step()
	if len(status().Issuances) != 1 {
		t.Fatal("advance-only control performed a settlement")
	}
	automatic := !manual
	if out := configure(false, automatic); out.Enabled || out.AutoAllocate != automatic {
		t.Fatal("settlement depends on enabling prepay", out)
	}
	if automatic {
		exec(t, db, `UPDATE monitoring_config SET stale_after_seconds=interval_seconds WHERE singleton`)
		mu.Lock()
		blockAccount = true
		mu.Unlock()
		advanceCollectionFixture(t, db)
		queueAccount(t, s, token, c, a.ID)
		processQueued(t, s)
		step()
		view, err := s.GetEstimateCurrent(ctx, token, a.ID)
		if err != nil || len(status().Issuances) != 1 || view.NativeWeekly == nil || view.NativeWeekly.State != "quota_unavailable" {
			t.Fatalf("a current account read failure reused the stale published estimate: %+v %v", view.NativeWeekly, err)
		}
		mu.Lock()
		blockAccount = false
		mu.Unlock()
		collectObservation(t, s, db, token, c, a)
		view, err = s.GetEstimateCurrent(ctx, token, a.ID)
		if err != nil || view.NativeWeekly == nil || view.NativeWeekly.State != "source_stale" || view.NativeWeekly.Latest == nil ||
			view.NativeWeekly.Latest.ID != estimateID || view.NativeWeekly.Remaining != nil || view.NativeWeekly.Allocation == nil ||
			len(view.NativeWeekly.Allocation.Members) != 1 || view.NativeWeekly.Allocation.Members[0].Remaining != nil {
			t.Fatalf("idle success did not retain authorized capacity through the final projection: %+v %v", view.NativeWeekly, err)
		}
	}
	if _, err = s.GetWeeklyWalletRecommendation(ctx, "", member.ID); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("unauthenticated reader saw the recommendation", err)
	}
	if manual {
		setTestWriteGrant(t, s, token, writeWallet, false)
		preview, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
		if err != nil || preview.State != "ready" || preview.Proposal == nil || preview.Proposal.Basis != nil ||
			preview.Proposal.Issuance == nil || preview.Proposal.Issuance.Contributions[0].Gross != "30" || preview.Proposal.Delta != settlementAmount {
			t.Fatalf("ungranted preview lost allocation amounts: %+v %v", preview, err)
		}
		var requests int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM wallet_application_requests`).Scan(&requests); err != nil || requests != 1 || len(status().Issuances) != 1 {
			t.Fatal("preview changed the existing wallet history", requests, err)
		}
		mu.Lock()
		before, sent := balance, writes
		mu.Unlock()
		if before != 15 || sent != 1 {
			t.Fatal("preview dispatched a native adjustment", before, sent)
		}
		setTestWriteGrant(t, s, token, writeWallet, true)
	}
	quote, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || quote.State != "ready" || quote.Reference != nil || quote.Pool == nil || quote.Pool.ID != pool.PoolID || quote.Proposal == nil || quote.Proposal.Basis == nil || quote.Proposal.Delta != settlementAmount ||
		quote.Proposal.Issuance == nil || quote.Proposal.Issuance.Contributions[0].Gross != "30" || quote.Proposal.Issuance.Contributions[0].Share != share ||
		quote.Proposal.Issuance.Contributions[0].Prior != advanceAmount || !equalStringPtr(quote.Proposal.Issuance.Contributions[0].EstimateID, &estimateID) {
		t.Fatal("failed to generate the native recommendation across mixed billing types", quote, err)
	}
	request := WeeklyWalletApplyInput{PoolID: pool.PoolID, ParticipantID: member.ID, Basis: *quote.Proposal.Basis}
	mu.Lock()
	balance, frozen, hideProof, hideBalance = 14, 10, true, true
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
	last := now.Issuances[0]
	if len(now.Entries) != historical || len(now.Issuances) != 2 || last.State != "unknown" || last.Phase != "settlement" ||
		last.Issuance.Contributions[0].Gross != "30" || last.Issuance.Amount != settlementAmount || last.Issuance.Balance != nil {
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
	credit, _ := strconv.Atoi(settlementAmount)
	if gotBalance != 14+credit || sent != 2 {
		t.Fatal("restart re-dispatched a possibly applied credit", gotBalance, sent)
	}
	replacement := SaveInput{Revision: c.Revision, BaseURL: server.URL + "/replacement", CredentialAction: "clear", Confirmation: "更换地址"}
	if _, err = s.Save(ctx, token, replacement); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("source switch did not block on an unknown result", err)
	}
	c = save(t, s, token, server.URL)
	setTestWriteGrant(t, s, token, writeWallet, false)
	out, err := s.ReconcileWalletApplication(ctx, token, *last.ApplicationID)
	if err != nil || out.State != "applied" || out.Details.NativeAdjustment.BalanceAfter != nil ||
		out.Details.NativeAdjustment.LedgerID == "" || out.Details.Input.NativeDelta != settlementAmount {
		t.Fatal("credit was not confirmed by its original native record", out, err)
	}
	step()
	now = status()
	if len(now.Issuances) != 2 || now.Issuances[0].State != "applied" || now.Issuances[0].Issuance.Amount != settlementAmount {
		t.Fatal("lost the confirmed credit", now)
	}
	mu.Lock()
	sent = writes
	mu.Unlock()
	if sent != 2 {
		t.Fatalf("expected two credits, got %d adjustments", sent)
	}
	replacement.Revision = c.Revision
	switched, err := s.Save(ctx, token, replacement)
	if err != nil || !switched.SourceChanged || equalStringPtr(c.SourceID, switched.Configuration.SourceID) {
		t.Fatal("changed address did not allocate a separate source", switched, err)
	}
	if got := status(); got.State == "enabled" || len(got.Issuances) != 2 || !equalStringPtr(got.Issuances[0].ApplicationID, last.ApplicationID) {
		t.Fatal("source switch reused controls or lost an original receipt", got)
	}
	view, err := s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || view.NativeWeekly == nil || view.NativeWeekly.State != "condition_changed" || view.NativeWeekly.Latest != nil || view.NativeWeekly.Allocation != nil {
		t.Fatalf("old source supplied current allocation evidence after switching: %+v %v", view.NativeWeekly, err)
	}
}
