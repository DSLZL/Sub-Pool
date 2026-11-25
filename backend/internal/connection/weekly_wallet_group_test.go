package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestWeeklyWalletGroupNativeSettlement(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	startA := time.Now().UTC().Truncate(time.Minute).Add(-48 * time.Hour)
	starts := map[int]time.Time{8: startA, 9: startA.Add(-24 * time.Hour), 10: startA.Add(-12 * time.Hour)}
	points := map[int]time.Time{8: startA.Add(time.Hour), 9: startA.Add(time.Hour), 10: startA.Add(time.Hour)}
	percent := map[int]int{8: 10, 9: 10, 10: 10}
	type use struct {
		id, account, user, billing int
		cost                       json.Number
		at                         time.Time
	}
	uses := []use{
		{1, 8, 22, 0, "12", startA.Add(30 * time.Minute)},
		{2, 8, 23, 0, "12", startA.Add(90 * time.Minute)},
		{3, 8, 21, 0, "35.0000000001", startA.Add(150 * time.Minute)},
		{4, 8, 21, 1, "1", startA.Add(160 * time.Minute)},
		{5, 10, 21, 0, "2", startA.Add(160 * time.Minute)},
	}
	var mu sync.Mutex
	balance, writes := 45, 0
	hideProof, blockB := false, false
	filters := map[int]int{}
	ledger := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path, q := r.URL.Path, r.URL.Query()
		switch {
		case path == "/api/v1/admin/system/version":
			respond(w, map[string]string{"version": "0.2.23"})
		case path == "/api/v1/admin/groups/0/model-allowlist-candidates":
			respond(w, map[string][]string{"models": {"gpt-5"}})
		case path == "/api/v1/admin/settings":
			respond(w, map[string]bool{"affiliate_admin_recharge_enabled": false})
		case strings.HasPrefix(path, "/api/v1/admin/accounts/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(path, "/api/v1/admin/accounts/"))
			a := fullManagedAccount(id, "weekly-group-"+strconv.Itoa(id))
			a["extra"] = map[string]any{"codex_usage_updated_at": points[id], "codex_5h_window_minutes": 0,
				"codex_7d_window_minutes": 10080, "codex_7d_used_percent": percent[id], "codex_7d_reset_at": starts[id].Add(nativeWeek)}
			respond(w, a)
		case path == "/api/v1/admin/usage":
			id, _ := strconv.Atoi(q.Get("account_id"))
			user, _ := strconv.Atoi(q.Get("user_id"))
			if q.Get("user_id") != "" {
				if user != 21 && user != 22 || q.Get("billing_type") != "0" {
					t.Error("bound wallet query omitted a native filter")
				}
				filters[id]++
				if id == 9 && blockB {
					http.Error(w, "read unavailable", http.StatusForbidden)
					return
				}
			}
			size, _ := strconv.Atoi(q.Get("page_size"))
			page, _ := strconv.Atoi(q.Get("page"))
			items := []map[string]any{}
			for _, item := range uses {
				if item.account != id || item.at.Format(weeklyDate) != q.Get("start_date") ||
					(user != 0 && (item.user != user || item.billing != 0)) {
					continue
				}
				items = append(items, map[string]any{"id": item.id, "account_id": id, "user_id": item.user, "model": "gpt-5",
					"created_at": item.at, "billing_type": item.billing, "total_cost": item.cost, "actual_cost": item.cost})
			}
			total := len(items)
			if q.Get("sort_order") == "desc" {
				sort.Slice(items, func(i, j int) bool { return items[i]["id"].(int) > items[j]["id"].(int) })
			}
			from := min(total, (page-1)*size)
			respond(w, map[string]any{"items": items[from:min(total, from+size)], "total": total, "page": page, "page_size": size})
		case path == "/api/v1/admin/users/21/balance":
			var in struct {
				Balance   int    `json:"balance"`
				Operation string `json:"operation"`
				Notes     string `json:"notes"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&in) != nil || in.Balance != 20 || in.Operation != "subtract" || in.Notes == "" {
				t.Error("wanted a single atomic deduction of 20")
				http.Error(w, "bad adjustment", http.StatusBadRequest)
				return
			}
			balance -= in.Balance
			writes++
			ledger = append(ledger, map[string]any{"id": writes, "notes": in.Notes, "type": "admin_balance", "status": "used", "used_by": 21, "value": -in.Balance})
			if hideProof {
				http.Error(w, "acknowledgement lost", http.StatusBadGateway)
				return
			}
			respond(w, map[string]int{"id": 21})
		case path == "/api/v1/admin/users/21/balance-history":
			items := ledger
			if hideProof {
				items = []map[string]any{}
			}
			respond(w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100})
		case strings.HasPrefix(path, "/api/v1/admin/users/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(path, "/api/v1/admin/users/"))
			row := nativeUserRow(id)
			row["balance"], row["frozen_balance"] = 0, 0
			if id == 21 {
				row["balance"] = balance
			}
			respond(w, row)
		default:
			t.Errorf("unexpected upstream request: %s", path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 8)
	b := passiveManaged(t, s, token, c, 9)
	accountC := passiveManaged(t, s, token, c, 10)
	member := createParticipant(t, s, token, c, 21, "Across two pools")
	other := createParticipant(t, s, token, c, 22, "Second member")
	third := createParticipant(t, s, token, c, 23, "Third member")
	exec(t, db, `UPDATE participant_bindings SET started_at=$2 WHERE id=$1`, member.Binding.ID, starts[9])

	pool := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{member, other, third}, []string{"25", "0", "40"})})
	second := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{accountC}, []Participant{member}, []string{"100"})})
	aCycle := walletTestCycle(pool.PoolID, a.ID, starts[8], "50")
	bCycle := walletTestCycle(pool.PoolID, b.ID, starts[9], "30")
	cCycle := walletTestCycle(second.PoolID, accountC.ID, starts[10], "2")
	aCycle.Phase, aCycle.ContractID, aCycle.LastFormalGross = "advance", pool.ID, "100"
	bCycle.Phase, bCycle.ContractID, bCycle.EstimateID, bCycle.LastFormalGross = "settlement", pool.ID, &pool.ID, "30"
	cCycle.Phase, cCycle.ContractID, cCycle.EstimateID, cCycle.LastFormalGross = "settlement", second.ID, &second.ID, "2"
	cut := startA.Add(135 * time.Minute)
	exec(t, db, `INSERT INTO weekly_wallet_groups(participant_id,binding_id,pool_ids,phase,period_end,recorded_at,basis,snapshot)
	 VALUES($1,$2,$3,'advance',$4,$5,$6,$7)`, member.ID, member.Binding.ID, []string{pool.PoolID, second.PoolID}, bCycle.End,
		cut, strings.Repeat("0", 64), weeklyWalletGroupPlan{
			Cutoff: cut, Target: "82", Contributions: []weeklyWalletContribution{aCycle, bCycle, cCycle},
		})
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled, policy.StaleAfterSeconds = true, 3*24*60*60
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	collectObservation(t, s, db, token, c, a)
	collectObservation(t, s, db, token, c, b)
	collectObservation(t, s, db, token, c, accountC)
	for _, point := range []struct{ hours, used int }{{2, 20}, {3, 50}} {
		mu.Lock()
		points[8], percent[8] = startA.Add(time.Duration(point.hours)*time.Hour), point.used
		mu.Unlock()
		collectObservation(t, s, db, token, c, a)
	}
	var cursor string
	for i := 0; i < 20; i++ {
		if cursor, err = s.processNativeWeekly(ctx, cursor); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	blockB = true
	mu.Unlock()
	blocked, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || blocked.State != "waiting_evidence" || len(blocked.Issues) != 1 || blocked.Issues[0].AccountID != b.ID || blocked.Proposal != nil {
		t.Fatalf("missing-usage state: %+v %v", blocked, err)
	}
	if blocked.Reference == nil || blocked.Reference.Amount != nil || len(blocked.Reference.Accounts) != 3 {
		t.Fatalf("cross-pool reference must omit a total when any required basis is missing: %+v", blocked.Reference)
	}
	for _, row := range blocked.Reference.Accounts {
		if row.AccountID == a.ID {
			if row.Amount == nil || *row.Amount != "30" || row.Samples != 3 {
				t.Fatalf("complete account contribution was unavailable: %+v", row)
			}
		} else if row.Amount != nil || row.Reason != "waiting_estimate" {
			t.Fatalf("missing costs became an eligible contribution: %+v", row)
		}
	}
	mu.Lock()
	blockB = false
	mu.Unlock()
	preview, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || preview.State != "ready" || preview.Proposal == nil || preview.Proposal.Basis != nil ||
		preview.Proposal.Group == nil || preview.Proposal.Group.Target != "25" || preview.Proposal.Delta != "-20" {
		t.Fatalf("ungranted cross-pool preview: %+v %v", preview, err)
	}
	history, err := s.GetWeeklyWallet(ctx, token, pool.PoolID)
	if err != nil || len(history.Groups) != 1 || history.Enabled || history.AutoAllocate {
		t.Fatalf("preview changed the existing disabled control or history: %+v %v", history, err)
	}
	var requests int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM wallet_application_requests`).Scan(&requests); err != nil || requests != 0 {
		t.Fatal("preview created an application request", requests, err)
	}
	mu.Lock()
	before, sent := balance, writes
	mu.Unlock()
	if before != 45 || sent != 0 {
		t.Fatal("preview dispatched a native adjustment", before, sent)
	}
	setTestWriteGrant(t, s, token, writeWallet, true)
	quote, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || quote.Proposal == nil || quote.Proposal.Basis == nil || quote.Proposal.Group == nil {
		t.Fatalf("weekly recommendation: %+v %v", quote, err)
	}
	plan := quote.Proposal.Group
	if plan.Target != "25" || plan.Delta != "-20" || len(plan.Contributions) != 3 || len(plan.Offsets) != 1 ||
		plan.Offsets[0].From.AccountID != b.ID || plan.Offsets[0].To.AccountID != a.ID || plan.Offsets[0].Amount != "5" {
		t.Fatalf("cross-pool recommendation: %+v", plan)
	}
	if plan.Contributions[1].Spent != "2" || plan.Contributions[2].Spent != "35" {
		t.Fatalf("wallet spending was assigned to the wrong source: %+v", plan.Contributions)
	}
	in := WeeklyWalletApplyInput{PoolID: quote.Pool.ID, ParticipantID: member.ID, Basis: *quote.Proposal.Basis, Cutoff: &plan.Cutoff}
	setTestWriteGrant(t, s, token, writeWallet, false)
	if _, err = s.ApplyWeeklyWallet(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("revoked wallet grant accepted a saved group proposal", err)
	}
	setTestWriteGrant(t, s, token, writeWallet, true)
	if _, err = s.ApplyWeeklyWallet(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("replacement wallet grant revived an old group proposal", err)
	}
	fresh, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || fresh.Proposal == nil || fresh.Proposal.Basis == nil || fresh.Proposal.Group == nil {
		t.Fatalf("fresh proposal for the current grant: %+v %v", fresh, err)
	}
	in.Basis, in.Cutoff = *fresh.Proposal.Basis, &fresh.Proposal.Group.Cutoff
	s.UpstreamWritesDisabled = true
	if _, err = s.ApplyWeeklyWallet(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("emergency stop accepted a saved group proposal", err)
	}
	s.UpstreamWritesDisabled = false
	c = save(t, s, token, server.URL)
	for _, account := range []ManagedAccount{a, b, accountC} {
		collectObservation(t, s, db, token, c, account)
	}
	if _, err = s.ApplyWeeklyWallet(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("a group proposal from the old connection revision was accepted", err)
	}
	refreshed, err := s.GetWeeklyWalletRecommendation(ctx, token, member.ID)
	if err != nil || refreshed.Proposal == nil || refreshed.Proposal.Basis == nil || refreshed.Proposal.Group == nil ||
		refreshed.Proposal.Group.Target != plan.Target || refreshed.Proposal.Delta != quote.Proposal.Delta ||
		!equalStringPtr(refreshed.Proposal.Group.Contributions[0].EstimateID, plan.Contributions[0].EstimateID) {
		t.Fatalf("same-source save lost the existing group settlement evidence: %+v %v", refreshed, err)
	}
	in.Basis, in.Cutoff = *refreshed.Proposal.Basis, &refreshed.Proposal.Group.Cutoff
	mu.Lock()
	balance++
	mu.Unlock()
	if _, err = s.ApplyWeeklyWallet(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("changed native balance accepted an old proposal", err)
	}
	mu.Lock()
	balance--
	hideProof = true
	mu.Unlock()
	const origin = "http://127.0.0.1:3000"
	boundary, err := auth.NewHandler(s.auth, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, origin+"/api/v1/pools/weekly-wallet/apply", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: token})
	response := httptest.NewRecorder()
	(&PoolsHandler{Service: s, Boundary: boundary}).ServeHTTP(response, request)
	var receipt weeklyWalletEntry
	if err = json.Unmarshal(response.Body.Bytes(), &receipt); response.Code != http.StatusOK || err != nil ||
		receipt.State != "unknown" || receipt.ApplicationID == nil || receipt.Group == nil {
		t.Fatalf("missing original intent: status=%d body=%s err=%v", response.Code, response.Body, err)
	}
	s, err = New(db, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyWeeklyWallet(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("retry while outcome is unknown", err)
	}
	mu.Lock()
	hideProof = false
	mu.Unlock()
	closed, err := s.ReconcileWalletApplication(ctx, token, *receipt.ApplicationID)
	if err != nil || closed.State != "applied" {
		t.Fatalf("original request could not be reconciled: %+v %v", closed, err)
	}
	for _, poolID := range []string{pool.PoolID, second.PoolID} {
		history, err := s.GetWeeklyWallet(ctx, token, poolID)
		if err != nil || len(history.Groups) != 2 || history.Groups[0].State != "applied" || history.Groups[0].Group.Target != "25" {
			t.Fatalf("wrong pool history: %+v %v", history, err)
		}
		status, err := s.ConfigureWeeklyWallet(ctx, token, WeeklyWalletInput{ID: poolID, PoolRevision: history.PoolRevision, Revision: history.Revision, AutoAllocate: true})
		if err != nil || status.State != "enabled" {
			t.Fatalf("automatic control: %+v %v", status, err)
		}
	}
	// The worker orders its cursor using the database collation.
	var poolOrder []string
	if err := db.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM pools WHERE id IN ($1,$2)`, pool.PoolID, second.PoolID).Scan(&poolOrder); err != nil || len(poolOrder) != 2 {
		t.Fatalf("order the wallet's candidate pools: %v %v", poolOrder, err)
	}
	after, err := s.processWeeklyWallet(ctx, member.ID+"/"+poolOrder[0])
	if err != nil || after != member.ID+"/"+poolOrder[1] {
		t.Fatalf("scheduler missed the second pool for this wallet: %q %v", after, err)
	}
	zeroA, zeroB := aCycle, bCycle
	zeroA.Gross, zeroB.Gross = "0", "0"
	zeroCutoff := time.Now().UTC().Truncate(time.Microsecond)
	exec(t, db, `INSERT INTO weekly_wallet_groups(participant_id,binding_id,pool_ids,phase,period_end,recorded_at,basis,snapshot)
	 VALUES($1,$2,$3,'advance',$4,$5,$6,$7)`, other.ID, other.Binding.ID, []string{pool.PoolID}, bCycle.End,
		zeroCutoff, strings.Repeat("0", 64), weeklyWalletGroupPlan{
			Cutoff: zeroCutoff, Target: "0", Contributions: []weeklyWalletContribution{zeroA, zeroB},
		})
	zero, err := s.GetWeeklyWalletRecommendation(ctx, token, other.ID)
	if err != nil || zero.Proposal == nil || zero.Proposal.Basis == nil || zero.Proposal.Group == nil || zero.Proposal.Delta != "0" {
		t.Fatalf("zero-share proposal: %+v %v", zero, err)
	}
	zeroInput := WeeklyWalletApplyInput{PoolID: zero.Pool.ID, ParticipantID: other.ID, Basis: *zero.Proposal.Basis, Cutoff: &zero.Proposal.Group.Cutoff}
	noWrite, err := s.ApplyWeeklyWallet(ctx, token, zeroInput)
	if err != nil || noWrite.State != "applied" || noWrite.ApplicationID != nil || noWrite.Group.Target != "0" {
		t.Fatalf("zero-delta confirmation: %+v %v", noWrite, err)
	}
	if _, err = s.ApplyWeeklyWallet(ctx, token, zeroInput); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("zero-delta plan was accepted twice", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || balance != 25 || len(filters) != 3 {
		t.Fatalf("native adjustments=%d balance=%d accounts queried=%d", writes, balance, len(filters))
	}
}
