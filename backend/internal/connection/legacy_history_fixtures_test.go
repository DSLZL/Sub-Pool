package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"sub-pool/backend/internal/estimation"
)

func seedArchivedEstimate(ctx context.Context, db *pgxpool.Pool, account ManagedAccount, config Configuration, windows []EstimateWindow) (EstimateRecord, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return EstimateRecord{}, err
	}
	defer tx.Rollback(ctx)
	at := time.Now().UTC().Truncate(time.Microsecond)
	if len(windows) == 0 {
		windows = []EstimateWindow{{Kind: "short", Basis: "official", ObservationIDs: []string{}, Result: estimation.Result{State: "insufficient_samples"}}}
	}
	kinds := make([]string, 0, len(windows))
	complete := true
	var remaining *estimation.Amount
	for _, window := range windows {
		kinds = append(kinds, window.Kind)
		value, ok := window.Remaining.Rational()
		if !ok || window.State != "scenario" {
			complete = false
			continue
		}
		if prior, exists := remaining.Rational(); !exists || value.Cmp(prior) < 0 {
			remaining = window.Remaining
		}
	}
	assumptions := &EstimateAssumptions{
		Plan: estimation.Plan, CostBasis: "standard_usd", KeepRatio: "0.8", Premise: "Retained estimate from before retirement",
		Windows: kinds, SourceID: account.SourceID, ConnectionRevision: config.Revision, AccountRevision: account.Revision, SourceMode: account.SourceMode,
	}
	saved := EstimateConfiguration{AccountID: account.ID, Assumptions: assumptions, SavedAt: &at}
	body, err := json.Marshal(assumptions)
	if err != nil {
		return EstimateRecord{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO estimate_config(account_id,revision,configuration,saved_at) VALUES($1,2,$2,$3)
	 ON CONFLICT(account_id) DO UPDATE SET revision=estimate_config.revision+1,configuration=EXCLUDED.configuration,saved_at=EXCLUDED.saved_at
	 RETURNING revision::text`, account.ID, body, at).Scan(&saved.Revision)
	if err != nil {
		return EstimateRecord{}, err
	}
	observation, capture, management, err := estimateHeads(ctx, tx, account.ID)
	if err != nil {
		return EstimateRecord{}, err
	}
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return EstimateRecord{}, err
	}
	state := windows[0].State
	if !complete {
		remaining = nil
	}
	out := EstimateRecord{AccountID: account.ID, GeneratedAt: at, EstimateSnapshot: EstimateSnapshot{
		RulesVersion: estimation.Rules, Configuration: saved, Trigger: "configuration", PolicyRevision: policy.Revision,
		ManagementRevision: management, ObservationHead: observation, CaptureHead: capture, State: state,
		Windows: windows, Remaining: remaining, Currency: "USD", Complete: complete,
		Limitations: []string{"INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"},
	}}
	body, err = json.Marshal(out.EstimateSnapshot)
	if err != nil {
		return EstimateRecord{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES($1,$2,$3) RETURNING id::text`, account.ID, at, body).Scan(&out.ID)
	if err != nil {
		return EstimateRecord{}, err
	}
	return out, tx.Commit(ctx)
}

var archivedSimulation = SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: "AgICAgICAgICAgICAgICAg"}

type archivedCostFixture struct {
	s      *Service
	db     *pgxpool.Pool
	token  string
	c      Configuration
	a      ManagedAccount
	points []ObservationRecord
	times  []time.Time
	index  atomic.Int32
	calls  atomic.Int32
}

func archivedCosts(t *testing.T) *archivedCostFixture {
	t.Helper()
	s, db, token := setup(t)
	anchor := time.Now().UTC().Add(-6 * time.Minute).Truncate(time.Second)
	f := &archivedCostFixture{s: s, db: db, token: token, times: []time.Time{anchor, anchor.Add(2 * time.Minute), anchor.Add(4 * time.Minute), anchor.Add(5 * time.Minute)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/accounts/1" {
			http.NotFound(w, r)
			return
		}
		index := int(f.index.Load())
		reset := anchor.Add(2 * time.Hour)
		row := fullManagedAccount(1, "archived-estimate-account")
		row["extra"] = map[string]any{
			"codex_usage_updated_at": f.times[index].Format(time.RFC3339Nano),
			"codex_5h_used_percent":  []int{10, 20, 40, 100}[index], "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339Nano),
			"codex_7d_used_percent": []int{70, 80, 90, 100}[index], "codex_7d_window_minutes": 10080, "codex_7d_reset_at": reset.Format(time.RFC3339Nano),
		}
		respond(w, row)
	}))
	t.Cleanup(server.Close)
	f.c = save(t, s, token, server.URL)
	f.a = passiveManaged(t, s, token, f.c, 1)
	for index := range 3 {
		f.addPoint(t, index)
	}
	return f
}

func (f *archivedCostFixture) addPoint(t *testing.T, index int) {
	t.Helper()
	f.index.Store(int32(index))
	exec(t, f.db, `UPDATE managed_accounts SET source_mode='passive' WHERE id=$1`, f.a.ID)
	point := collectObservation(t, f.s, f.db, f.token, f.c, f.a)
	point.Snapshot.SourceMode, point.Snapshot.Simulation = "active", &archivedSimulation
	raw, err := json.Marshal(point.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, f.db, `UPDATE quota_observations SET snapshot=$2 WHERE id=$1`, point.ID, raw)
	exec(t, f.db, `UPDATE managed_accounts SET source_mode='active' WHERE id=$1`, f.a.ID)
	f.a.SourceMode = "active"
	f.points = append(f.points, point)
}

func (f *archivedCostFixture) store(ctx context.Context, kinds []string, manual bool) (EstimateRecord, error) {
	windows := []EstimateWindow{}
	start := len(f.points) - 3
	points := f.points[start:]
	for _, kind := range kinds {
		window := EstimateWindow{Kind: kind, Basis: "official", Period: nativeObservationPeriod(points[2].Snapshot, kind),
			ObservationIDs: []string{points[0].ID, points[1].ID, points[2].ID}, CurrentObservationID: &points[2].ID,
			SourceAt: points[2].Snapshot.SourceAt, OldestSampleAt: points[0].Snapshot.SourceAt, CurrentUsedPercent: quotaUsed(points[2].Snapshot, kind)}
		if window.Period == nil || window.CurrentUsedPercent == nil {
			return EstimateRecord{}, fmt.Errorf("archived test window %s unavailable", kind)
		}
		if manual {
			window.Basis, window.ManualStartAt, window.ManualEndAt = "manual", f.points[0].Snapshot.SourceAt, points[2].Snapshot.SourceAt
		}
		proof := &SimulationCostProof{SimulationProvenance: archivedSimulation, SourceStart: f.times[0].Add(-2 * time.Minute),
			ClosedThrough: f.times[3].Add(time.Second), LedgerRevision: "17", PriceVersion: "2", StandardRate: "1",
			WalletMultiplier: "0.5", BillingType: "standard_wallet", PriceEffectiveAt: f.times[0].Add(-time.Minute)}
		quotas := make([]string, 0, 3)
		costs := make([]string, 0, 2)
		for i, point := range points {
			quotas = append(quotas, *quotaUsed(point.Snapshot, kind))
			if i < 2 {
				standard := []string{"3", "7", "9"}[start+i]
				actual := []string{"1.5", "3.5", "4.5"}[start+i]
				costs = append(costs, standard)
				s, _ := estimation.Decimal(standard)
				a, _ := estimation.Decimal(actual)
				proof.Intervals = append(proof.Intervals, SimulationCostInterval{StartAt: f.times[start+i], EndAt: f.times[start+i+1],
					StandardCost: *estimation.Exact(s), ActualCost: *estimation.Exact(a), EntryCount: 1})
			}
		}
		window.CostProof = proof
		window.Result = estimation.Evaluate(estimation.Input{Keep: "0.8", Quota: quotas, Costs: costs, CurrentQuota: *window.CurrentUsedPercent, Complete: true})
		windows = append(windows, window)
	}
	return seedArchivedEstimate(ctx, f.db, f.a, f.c, windows)
}
