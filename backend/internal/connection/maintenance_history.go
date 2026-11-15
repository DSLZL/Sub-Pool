package connection

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/estimation"
)

const maintenanceHistoryRules = "retained-estimates-v2"

type MaintenanceEstimateWindow struct {
	Kind           string   `json:"kind"`
	ObservationIDs []string `json:"observation_ids"`
	estimation.Result
}

type MaintenanceEstimate struct {
	EstimateID        string                      `json:"estimate_id"`
	GeneratedAt       time.Time                   `json:"generated_at"`
	OriginalRules     string                      `json:"original_rules"`
	OriginalState     string                      `json:"original_state"`
	OriginalRemaining *estimation.Amount          `json:"original_remaining"`
	State             string                      `json:"state"`
	Remaining         *estimation.Amount          `json:"remaining"`
	Windows           []MaintenanceEstimateWindow `json:"windows"`
}

type MaintenanceFinancial struct {
	CalculationRules string                `json:"calculation_rules"`
	Currency         string                `json:"currency"`
	ScanComplete     bool                  `json:"scan_complete"`
	Records          int                   `json:"records"`
	Results          []MaintenanceEstimate `json:"results"`
}

type MaintenanceFinancialDifference struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

type maintenanceEstimateFact struct {
	ID          string
	GeneratedAt time.Time
	Raw         json.RawMessage
	snapshot    EstimateSnapshot
}

// References outside the receipt range are retained inputs, never fetched from upstream.
func maintenanceHistoryInputs(ctx context.Context, tx pgx.Tx, in MaintenanceInput, facts []maintenanceFact) ([]maintenanceEstimateFact, []maintenanceFact, error) {
	from, _ := maintenanceTime(in.From)
	until, _ := maintenanceTime(in.Until)
	rows, err := tx.Query(ctx, `SELECT id::text,generated_at,snapshot FROM estimate_results WHERE account_id=$1 AND generated_at>=$2 AND generated_at<$3 ORDER BY id LIMIT $4`, in.AccountID, ceilMicrosecond(from), ceilMicrosecond(until), maintenanceObservationLimit+1)
	if err != nil {
		return nil, nil, err
	}
	history := []maintenanceEstimateFact{}
	for rows.Next() {
		var fact maintenanceEstimateFact
		if err = rows.Scan(&fact.ID, &fact.GeneratedAt, &fact.Raw); err != nil {
			break
		}
		if err = json.Unmarshal(fact.Raw, &fact.snapshot); err != nil {
			break
		}
		fact.GeneratedAt = fact.GeneratedAt.UTC()
		history = append(history, fact)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(history) > maintenanceObservationLimit || len(facts) > maintenanceObservationLimit {
		return history, facts, err
	}
	known := map[string]bool{}
	loaded := map[string]bool{}
	for _, fact := range facts {
		known[fact.ID] = true
		loaded[fact.ID] = true
	}
	ids := []int64{}
	manualTimes := []string{}
	for _, fact := range history {
		if len(fact.snapshot.Windows) > 2 {
			continue
		}
		for _, window := range fact.snapshot.Windows {
			if window.Basis == "manual" {
				for _, at := range []*time.Time{window.ManualStartAt, window.ManualEndAt} {
					if at != nil {
						manualTimes = append(manualTimes, at.UTC().Format(time.RFC3339Nano))
					}
				}
			}
			if len(window.ObservationIDs) > 3 {
				continue
			}
			references := append([]string{}, window.ObservationIDs...)
			if window.CurrentObservationID != nil {
				references = append(references, *window.CurrentObservationID)
			}
			for _, id := range references {
				if known[id] || !validRevision(id) {
					continue
				}
				known[id] = true
				n, _ := strconv.ParseInt(id, 10, 64)
				ids = append(ids, n)
			}
		}
	}
	if len(ids) == 0 && len(manualTimes) == 0 {
		return history, facts, nil
	}
	rows, err = tx.Query(ctx, `SELECT q.id::text,q.job_id::text,q.received_at,q.snapshot,COALESCE(v.action='exclude',false) FROM quota_observations q
 LEFT JOIN LATERAL (SELECT action FROM observation_changes WHERE observation_id=q.id ORDER BY observation_changes.id DESC LIMIT 1) v ON true
 WHERE q.account_id=$1 AND (q.id=ANY($2::bigint[]) OR q.snapshot->>'source_at'=ANY($3::text[])) ORDER BY q.id LIMIT $4`, in.AccountID, ids, manualTimes, maintenanceObservationLimit+1)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var fact maintenanceFact
		if err = rows.Scan(&fact.ID, &fact.JobID, &fact.ReceivedAt, &fact.Raw, &fact.Excluded); err != nil {
			break
		}
		if err = json.Unmarshal(fact.Raw, &fact.snapshot); err != nil {
			break
		}
		fact.ReceivedAt = fact.ReceivedAt.UTC()
		if loaded[fact.ID] {
			continue
		}
		loaded[fact.ID] = true
		facts = append(facts, fact)
		if len(facts) > maintenanceObservationLimit {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	sort.Slice(facts, func(i, j int) bool {
		a, _ := strconv.ParseInt(facts[i].ID, 10, 64)
		b, _ := strconv.ParseInt(facts[j].ID, 10, 64)
		return a < b
	})
	return history, facts, err
}

func maintenanceCost(value *estimation.Amount) (string, bool) {
	if value == nil || len(value.Numerator) > 256 || len(value.Denominator) > 256 {
		return "", false
	}
	amount, ok := value.Rational()
	if !ok {
		return "", false
	}
	decimal := amount.FloatString(18)
	parsed, valid := estimation.Decimal(decimal)
	return decimal, valid && parsed.Cmp(amount) == 0
}

func retainedEstimateFactValid(fact maintenanceFact, record maintenanceEstimateFact, window EstimateWindow) bool {
	snap, assumptions, proof := fact.snapshot, record.snapshot.Configuration.Assumptions, window.CostProof
	period := nativeObservationPeriod(snap, window.Kind)
	return !fact.Excluded && snap.SourceID == assumptions.SourceID && snap.ConnectionRevision == assumptions.ConnectionRevision && snap.AccountRevision == assumptions.AccountRevision && snap.SourceMode == "active" && snap.Simulation != nil && *snap.Simulation == proof.SimulationProvenance && fact.ReceivedAt.Equal(snap.ReceivedAt) && !fact.ReceivedAt.After(record.GeneratedAt) && quotaUsed(snap, window.Kind) != nil && period != nil && period.Kind == window.Period.Kind && period.DurationSeconds == window.Period.DurationSeconds && period.Basis == window.Period.Basis && period.StartAt.Equal(window.Period.StartAt) && period.EndAt.Equal(window.Period.EndAt)
}

func retainedEstimateWindow(record maintenanceEstimateFact, window EstimateWindow, facts map[string]maintenanceFact) (estimation.Result, string) {
	fail := func(code string) (estimation.Result, string) {
		return estimation.Result{State: "evidence_incomplete"}, code
	}
	assumptions := record.snapshot.Configuration.Assumptions
	proof := window.CostProof
	if proof == nil || len(window.ObservationIDs) != 3 || window.CurrentObservationID == nil || window.SourceAt == nil || window.OldestSampleAt == nil || window.CurrentUsedPercent == nil || window.Period == nil {
		return fail("ESTIMATE_EVIDENCE_MISSING")
	}
	// Historic freshness policy is not retained; a formerly invalid window cannot be promoted.
	if window.State != "scenario" && window.State != "non_identifying_delta" {
		return fail("ESTIMATE_EVIDENCE_MISSING")
	}
	if proof.Protocol != simulationProtocol || proof.Environment != "simulated" || !managedID(proof.Epoch) || !validRevision(proof.LedgerRevision) || !validRevision(proof.PriceVersion) || len(proof.Intervals) != 2 || proof.SourceStart.IsZero() || proof.PriceEffectiveAt.IsZero() || proof.ClosedThrough.After(record.GeneratedAt) {
		return fail("ESTIMATE_EVIDENCE_INVALID")
	}
	rate, rateOK := simulationDecimal(proof.StandardRate)
	_, multiplierOK := simulationDecimal(proof.WalletMultiplier)
	if !rateOK || rate.Sign() <= 0 || !multiplierOK || proof.BillingType != "standard_wallet" && proof.BillingType != "subscription" {
		return fail("ESTIMATE_EVIDENCE_INVALID")
	}
	ids := append(append([]string{}, window.ObservationIDs...), *window.CurrentObservationID)
	points := []QuotaSnapshot{}
	for i, id := range ids {
		fact, exists := facts[id]
		if !exists {
			return fail("ESTIMATE_EVIDENCE_MISSING")
		}
		snap := fact.snapshot
		if !retainedEstimateFactValid(fact, record, window) {
			return fail("ESTIMATE_EVIDENCE_INVALID")
		}
		if i < 3 && window.Basis == "manual" && (window.ManualStartAt == nil || window.ManualEndAt == nil || snap.SourceAt.Before(*window.ManualStartAt) || snap.SourceAt.After(*window.ManualEndAt)) {
			return fail("ESTIMATE_EVIDENCE_INVALID")
		}
		points = append(points, snap)
	}
	if window.Basis == "manual" {
		for _, at := range []*time.Time{window.ManualStartAt, window.ManualEndAt} {
			found := false
			for _, fact := range facts {
				if at != nil && fact.snapshot.SourceAt != nil && at.Equal(*fact.snapshot.SourceAt) && retainedEstimateFactValid(fact, record, window) {
					found = true
					break
				}
			}
			if !found {
				return fail("ESTIMATE_EVIDENCE_MISSING")
			}
		}
	}
	first, middle, last, current := points[0], points[1], points[2], points[3]
	if !middle.SourceAt.After(*first.SourceAt) || !last.SourceAt.After(*middle.SourceAt) || current.SourceAt.Before(*last.SourceAt) || !window.SourceAt.Equal(*current.SourceAt) || !window.OldestSampleAt.Equal(*first.SourceAt) || *window.CurrentUsedPercent != *quotaUsed(current, window.Kind) || proof.SourceStart.After(*first.SourceAt) || proof.PriceEffectiveAt.After(*first.SourceAt) || proof.ClosedThrough.Before(*last.SourceAt) {
		return fail("ESTIMATE_EVIDENCE_INVALID")
	}
	if window.Basis != "official" && window.Basis != "manual" || window.Basis == "official" && (window.ManualStartAt != nil || window.ManualEndAt != nil) {
		return fail("ESTIMATE_EVIDENCE_INVALID")
	}
	quotas, costs := []string{}, []string{}
	for i, point := range points[:3] {
		quotas = append(quotas, *quotaUsed(point, window.Kind))
		if i == 2 {
			break
		}
		interval := proof.Intervals[i]
		cost, ok := maintenanceCost(&interval.StandardCost)
		_, actualOK := maintenanceCost(&interval.ActualCost)
		if !ok || !actualOK || interval.EntryCount < 1 || interval.EntryCount > 10000 || !interval.StartAt.Equal(*point.SourceAt) || !interval.EndAt.Equal(*points[i+1].SourceAt) {
			return fail("ESTIMATE_EVIDENCE_INVALID")
		}
		costs = append(costs, cost)
	}
	result := estimation.Evaluate(estimation.Input{Keep: assumptions.KeepRatio, Quota: quotas, Costs: costs, CurrentQuota: *window.CurrentUsedPercent, Complete: true})
	if result.State != "scenario" && result.State != "non_identifying_delta" {
		return fail("ESTIMATE_EVIDENCE_INVALID")
	}
	return result, ""
}

func projectMaintenanceHistory(history []maintenanceEstimateFact, facts []maintenanceFact, account, source string) (*MaintenanceFinancial, []MaintenanceIssue) {
	out := &MaintenanceFinancial{CalculationRules: estimation.Rules, Currency: "USD", ScanComplete: len(history) <= maintenanceObservationLimit, Records: len(history), Results: []MaintenanceEstimate{}}
	issues := []MaintenanceIssue{}
	if !out.ScanComplete {
		return out, []MaintenanceIssue{{Code: "ESTIMATE_HISTORY_LIMIT", Blocking: true}}
	}
	if len(history) == 0 {
		return out, []MaintenanceIssue{{Code: "NO_ESTIMATE_HISTORY", Blocking: true}}
	}
	indexed := map[string]maintenanceFact{}
	for _, fact := range facts {
		indexed[fact.ID] = fact
	}
	for _, record := range history {
		old := record.snapshot
		result := MaintenanceEstimate{EstimateID: record.ID, GeneratedAt: record.GeneratedAt, OriginalRules: old.RulesVersion, OriginalState: old.State, OriginalRemaining: old.Remaining, State: "evidence_incomplete", Windows: []MaintenanceEstimateWindow{}}
		cfg, assumptions := old.Configuration, old.Configuration.Assumptions
		code := ""
		if assumptions == nil || !old.Complete || old.State != "scenario" && old.State != "non_identifying_delta" {
			code = "ESTIMATE_EVIDENCE_MISSING"
		}
		if assumptions != nil && (cfg.AccountID != account || !validRevision(cfg.Revision) || cfg.SavedAt == nil || cfg.SavedAt.After(record.GeneratedAt) || old.RulesVersion != estimation.Rules || old.Currency != "USD" || old.SettlementEligible || old.ModelCalibrated || assumptions.Plan != estimation.Plan || assumptions.CostBasis != "standard_usd" || !estimation.ValidKeep(assumptions.KeepRatio) || !observationReason(assumptions.Premise) || assumptions.SourceID != source || assumptions.SourceMode != "active" || !validRevision(assumptions.ConnectionRevision) || !validRevision(assumptions.AccountRevision) || len(assumptions.Windows) == 0 || len(assumptions.Windows) > 2 || len(old.Windows) != len(assumptions.Windows)) {
			code = "ESTIMATE_EVIDENCE_INVALID"
		}
		evaluated := []estimation.Result{}
		var prior *SimulationCostProof
		if code == "" {
			seen := map[string]bool{}
			for i, window := range old.Windows {
				if (window.Kind != "short" && window.Kind != "seven_day") || window.Kind != assumptions.Windows[i] || seen[window.Kind] {
					code = "ESTIMATE_EVIDENCE_INVALID"
					break
				}
				seen[window.Kind] = true
				value, reason := retainedEstimateWindow(record, window, indexed)
				if reason != "" {
					code = reason
				}
				if window.CostProof != nil && prior != nil {
					left, right := *prior, *window.CostProof
					left.Intervals, right.Intervals = nil, nil
					a, _ := json.Marshal(left)
					b, _ := json.Marshal(right)
					if string(a) != string(b) {
						code = "ESTIMATE_EVIDENCE_INVALID"
						value = estimation.Result{State: "evidence_incomplete"}
					}
				}
				if window.CostProof != nil {
					prior = window.CostProof
				}
				ids := append([]string{}, window.ObservationIDs...)
				if reason != "" {
					ids = []string{}
				}
				result.Windows = append(result.Windows, MaintenanceEstimateWindow{Kind: window.Kind, ObservationIDs: ids, Result: value})
				evaluated = append(evaluated, value)
			}
		}
		if code != "" {
			issues = append(issues, MaintenanceIssue{Code: code, Blocking: true, EstimateID: &record.ID})
		} else {
			result.Remaining = estimation.MinimumRemaining(evaluated)
			result.State = "scenario"
			for _, value := range evaluated {
				if value.State != "scenario" {
					result.State = value.State
					break
				}
			}
		}
		out.Results = append(out.Results, result)
	}
	return out, issues
}

func maintenanceFinancialDifference(previous, current *MaintenanceFinancial) *MaintenanceFinancialDifference {
	if current == nil {
		return nil
	}
	out := &MaintenanceFinancialDifference{}
	before, after := map[string]string{}, map[string]string{}
	if previous != nil {
		for _, result := range previous.Results {
			raw, _ := json.Marshal(result)
			before[result.EstimateID] = string(raw)
		}
	}
	for _, result := range current.Results {
		raw, _ := json.Marshal(result)
		after[result.EstimateID] = string(raw)
	}
	for id, value := range after {
		if old, ok := before[id]; !ok {
			out.Added++
		} else if old != value {
			out.Changed++
		}
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			out.Removed++
		}
	}
	return out
}
