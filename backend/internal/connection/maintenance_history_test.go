package connection

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMaintenanceRetainedEstimateUpgradeUsesExactOfflineFactsAndPreservesOriginals(t *testing.T) {
	f := archivedCosts(t)
	ctx := context.Background()
	original, err := f.store(ctx, []string{"short"}, false)
	if err != nil || original.State != "scenario" {
		t.Fatal(original, err)
	}
	// An owned old derived-cache error must be visible beside the independently recomputed value.
	exec(t, f.db, `UPDATE estimate_results SET snapshot=jsonb_set(snapshot,'{remaining}','{"numerator":"1","denominator":"1"}'::jsonb) WHERE id=$1`, original.ID)
	exec(t, f.db, `UPDATE connection_config SET secret=NULL,credential_kind=NULL`)
	in := maintenanceInputFor(f.a, f.points[0], f.points[2])
	in.Until = original.GeneratedAt.Add(time.Microsecond).Format(time.RFC3339Nano)
	prior, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || prior.State != "ready" {
		t.Fatal(prior, err)
	}
	if _, err = f.s.ApplyMaintenancePlan(ctx, f.token, prior.ID, prior.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	processMaintenanceForTest(t, f.s)
	before, calls := maintenanceFactSnapshot(t, f.db), f.calls.Load()
	in.Kind, in.RulesVersion = "upgrade", "retained-estimates-v2"
	plan, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || plan.State != "ready" || plan.Basis.PreviousAppliedPlanID == nil || *plan.Basis.PreviousAppliedPlanID != prior.ID {
		t.Fatal("reviewed retained-estimate rule could not upgrade the real old projection offline", plan, err)
	}
	var wire struct {
		Preview struct {
			Financial *struct {
				ScanComplete bool `json:"scan_complete"`
				Records      int  `json:"records"`
				Results      []struct {
					EstimateID        string                                  `json:"estimate_id"`
					OriginalRemaining struct{ Numerator, Denominator string } `json:"original_remaining"`
					Remaining         struct{ Numerator, Denominator string } `json:"remaining"`
					State             string                                  `json:"state"`
				} `json:"results"`
			} `json:"financial"`
		} `json:"preview"`
	}
	body, _ := json.Marshal(plan)
	if err = json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	financial := wire.Preview.Financial
	if financial == nil || !financial.ScanComplete || financial.Records != 1 || len(financial.Results) != 1 {
		t.Fatal("retained estimate scan was missing or partial", string(body))
	}
	result := financial.Results[0]
	// 100*min(3/10,7/20)*0.8*(100-40)/100 = 72/5; never substitute actual charges.
	if result.EstimateID != original.ID || result.OriginalRemaining.Numerator != "1" || result.State != "scenario" || result.Remaining.Numerator != "72" || result.Remaining.Denominator != "5" {
		t.Fatal("old/new exact conditional values were not independently reconstructed", result)
	}
	queued, err := f.s.ApplyMaintenancePlan(ctx, f.token, plan.ID, plan.Basis.Fingerprint, true)
	if err != nil || queued.State != "queued" || queued.ResultID != nil {
		t.Fatal(queued, err)
	}
	processMaintenanceForTest(t, f.s)
	applied, err := f.s.GetMaintenancePlan(ctx, f.token, plan.ID)
	if err != nil || applied.State != "applied" || applied.ResultID == nil || *applied.ResultID != plan.ID {
		t.Fatal(applied, err)
	}
	if before != maintenanceFactSnapshot(t, f.db) || calls != f.calls.Load() {
		t.Fatal("offline maintenance rewrote facts, notified or contacted source")
	}
	old, err := f.s.GetMaintenancePlan(ctx, f.token, prior.ID)
	if err != nil || old.RulesVersion != maintenanceRules || old.State != "applied" {
		t.Fatal("upgrade changed the old result", old, err)
	}
	next, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || next.Basis.PreviousAppliedPlanID == nil || *next.Basis.PreviousAppliedPlanID != plan.ID || next.Difference.Financial == nil || *next.Difference.Financial != (MaintenanceFinancialDifference{}) {
		t.Fatal("same-rule rebuilding lost the previous version or invented a difference", next, err)
	}
}

func TestMaintenanceRetainedProofRejectsMissingChangedAndUnclosedEvidence(t *testing.T) {
	f := archivedCosts(t)
	original, err := f.store(context.Background(), []string{"short"}, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(original.EstimateSnapshot)
	makeInput := func() (maintenanceEstimateFact, []maintenanceFact) {
		record := maintenanceEstimateFact{ID: original.ID, GeneratedAt: original.GeneratedAt, Raw: body}
		if err := json.Unmarshal(body, &record.snapshot); err != nil {
			t.Fatal(err)
		}
		facts := []maintenanceFact{}
		for _, point := range f.points {
			raw, _ := json.Marshal(point.Snapshot)
			var snapshot QuotaSnapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			facts = append(facts, maintenanceFact{ID: point.ID, JobID: point.JobID, ReceivedAt: snapshot.ReceivedAt, Raw: raw, snapshot: snapshot})
		}
		return record, facts
	}
	cases := []struct {
		name   string
		change func(*maintenanceEstimateFact, []maintenanceFact)
	}{
		{"missing-proof", func(r *maintenanceEstimateFact, _ []maintenanceFact) { r.snapshot.Windows[0].CostProof = nil }},
		{"missing-sample", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].ObservationIDs = r.snapshot.Windows[0].ObservationIDs[:2]
		}},
		{"duplicate-sample", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].ObservationIDs[1] = r.snapshot.Windows[0].ObservationIDs[0]
		}},
		{"missing-current", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CurrentObservationID = nil
		}},
		{"missing-fact", func(_ *maintenanceEstimateFact, facts []maintenanceFact) { facts[0].ID = "999999" }},
		{"excluded", func(_ *maintenanceEstimateFact, facts []maintenanceFact) { facts[0].Excluded = true }},
		{"receipt", func(_ *maintenanceEstimateFact, facts []maintenanceFact) {
			facts[0].ReceivedAt = facts[0].ReceivedAt.Add(time.Microsecond)
		}},
		{"source", func(_ *maintenanceEstimateFact, facts []maintenanceFact) {
			facts[0].snapshot.SourceID = "AAAAAAAAAAAAAAAAAAAAAA"
		}},
		{"revision", func(_ *maintenanceEstimateFact, facts []maintenanceFact) { facts[0].snapshot.AccountRevision = "999" }},
		{"mode", func(_ *maintenanceEstimateFact, facts []maintenanceFact) { facts[0].snapshot.SourceMode = "passive" }},
		{"epoch", func(_ *maintenanceEstimateFact, facts []maintenanceFact) {
			facts[0].snapshot.Simulation.Epoch = "AAAAAAAAAAAAAAAAAAAAAA"
		}},
		{"nil-quota", func(_ *maintenanceEstimateFact, facts []maintenanceFact) {
			facts[0].snapshot.Windows[0].UsedPercent = nil
		}},
		{"period", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].Period.EndAt = r.snapshot.Windows[0].Period.EndAt.Add(time.Second)
		}},
		{"current-quota", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			v := "41"
			r.snapshot.Windows[0].CurrentUsedPercent = &v
		}},
		{"interval", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.Intervals[0].EndAt = r.GeneratedAt
		}},
		{"negative-standard", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.Intervals[0].StandardCost.Numerator = "-1"
		}},
		{"fraction-no-rounding", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.Intervals[0].StandardCost.Numerator = "1"
			r.snapshot.Windows[0].CostProof.Intervals[0].StandardCost.Denominator = "3"
		}},
		{"negative-actual", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.Intervals[0].ActualCost.Numerator = "-1"
		}},
		{"missing-entries", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.Intervals[0].EntryCount = 0
		}},
		{"late-price", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.PriceEffectiveAt = r.GeneratedAt
		}},
		{"unclosed", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.ClosedThrough = *r.snapshot.Windows[0].OldestSampleAt
		}},
		{"future-close", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.ClosedThrough = r.GeneratedAt.Add(time.Second)
		}},
		{"zero-rate", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Windows[0].CostProof.StandardRate = "0"
		}},
		{"unverified-math", func(r *maintenanceEstimateFact, _ []maintenanceFact) { r.snapshot.RulesVersion = "unreviewed-v9" }},
		{"old-stale", func(r *maintenanceEstimateFact, _ []maintenanceFact) { r.snapshot.State = "source_stale" }},
		{"old-window-stale", func(r *maintenanceEstimateFact, _ []maintenanceFact) { r.snapshot.Windows[0].State = "source_stale" }},
		{"incomplete-history", func(r *maintenanceEstimateFact, _ []maintenanceFact) { r.snapshot.Complete = false }},
		{"assumption-source", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			r.snapshot.Configuration.Assumptions.SourceID = "AAAAAAAAAAAAAAAAAAAAAA"
		}},
		{"missing-manual-endpoint", func(r *maintenanceEstimateFact, _ []maintenanceFact) {
			w := &r.snapshot.Windows[0]
			w.Basis = "manual"
			start := w.OldestSampleAt.Add(-time.Second)
			w.ManualStartAt = &start
			w.ManualEndAt = w.SourceAt
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record, facts := makeInput()
			tc.change(&record, facts)
			out, issues := projectMaintenanceHistory([]maintenanceEstimateFact{record}, facts, f.a.ID, *f.c.SourceID)
			if len(out.Results) != 1 || out.Results[0].Remaining != nil || out.Results[0].State != "evidence_incomplete" || len(issues) != 1 || !issues[0].Blocking || issues[0].EstimateID == nil || *issues[0].EstimateID != original.ID {
				t.Fatal("unqualified retained input became a monetary result", out, issues)
			}
		})
	}
	record, facts := makeInput()
	w := &record.snapshot.Windows[0]
	w.Basis = "manual"
	w.ManualStartAt = w.OldestSampleAt
	w.ManualEndAt = w.SourceAt
	out, issues := projectMaintenanceHistory([]maintenanceEstimateFact{record}, facts, f.a.ID, *f.c.SourceID)
	if len(issues) != 0 || out.Results[0].Remaining == nil || out.Results[0].Remaining.Numerator != "72" {
		t.Fatal("complete retained manual range was rejected", out, issues)
	}
}

func TestMaintenanceRetainedBasisBindsSameIDHistoryAndOutOfScopeReferences(t *testing.T) {
	for _, change := range []string{"estimate-content", "reference-content"} {
		t.Run(change, func(t *testing.T) {
			f := archivedCosts(t)
			ctx := context.Background()
			original, err := f.store(ctx, []string{"short"}, false)
			if err != nil {
				t.Fatal(err)
			}
			in := MaintenanceInput{MaintenanceScope: MaintenanceScope{AccountID: f.a.ID, From: original.GeneratedAt.Format(time.RFC3339Nano), Until: original.GeneratedAt.Add(time.Microsecond).Format(time.RFC3339Nano)}, Kind: "rebuild", RulesVersion: maintenanceHistoryRules}
			plan, err := f.s.PreflightMaintenance(ctx, f.token, in)
			if err != nil || plan.State != "ready" || plan.Preview.Observations != 3 || plan.Preview.Financial == nil || plan.Preview.Financial.Records != 1 {
				t.Fatal("out-of-range retained references were not loaded completely", plan, err)
			}
			if _, err = f.s.ApplyMaintenancePlan(ctx, f.token, plan.ID, plan.Basis.Fingerprint, true); err != nil {
				t.Fatal(err)
			}
			beforeCalls := f.calls.Load()
			if change == "estimate-content" {
				exec(t, f.db, `UPDATE estimate_results SET snapshot=jsonb_set(snapshot,'{remaining,numerator}','"99"'::jsonb) WHERE id=$1`, original.ID)
			} else {
				exec(t, f.db, `UPDATE quota_observations SET snapshot=jsonb_set(snapshot,'{windows,0,used_percent}','"11"'::jsonb) WHERE id=$1`, f.points[0].ID)
			}
			before := maintenanceFactSnapshot(t, f.db)
			processMaintenanceForTest(t, f.s)
			expired, err := f.s.GetMaintenancePlan(ctx, f.token, plan.ID)
			if err != nil || expired.State != "expired" || expired.Reason != "BASIS_CHANGED" || expired.ResultID != nil || before != maintenanceFactSnapshot(t, f.db) || beforeCalls != f.calls.Load() {
				t.Fatal("same-ID changed historical basis was applied or fetched", expired, err)
			}
		})
	}
}

func TestMaintenanceRetainedManualEndpointsOutsideLastThreeAreBound(t *testing.T) {
	f := archivedCosts(t)
	ctx := context.Background()
	f.addPoint(t, 3)
	_, err := f.s.ChangeObservationStart(ctx, f.token, ObservationStartInput{AccountID: f.a.ID, Revision: "1", StartObservationID: f.points[0].ID, EndObservationID: f.points[3].ID, WindowKind: "short", Reason: "Owned retained manual range"}, false)
	if err != nil {
		t.Fatal(err)
	}
	original, err := f.store(ctx, []string{"short"}, true)
	if err != nil || original.State != "scenario" || original.Windows[0].Basis != "manual" || original.Windows[0].ObservationIDs[0] == f.points[0].ID {
		t.Fatal(original, err)
	}
	in := MaintenanceInput{MaintenanceScope: MaintenanceScope{AccountID: f.a.ID, From: original.GeneratedAt.Format(time.RFC3339Nano), Until: original.GeneratedAt.Add(time.Microsecond).Format(time.RFC3339Nano)}, Kind: "upgrade", RulesVersion: maintenanceHistoryRules}
	plan, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || plan.State != "ready" || plan.Preview.Observations != 4 || plan.Preview.Financial.Results[0].Remaining.Numerator != "0" {
		t.Fatal("manual anchor outside selected samples was omitted or zero was lost", plan, err)
	}
	exec(t, f.db, `UPDATE quota_observations SET snapshot=jsonb_set(snapshot,'{windows,0,used_percent}','"11"'::jsonb) WHERE id=$1`, f.points[0].ID)
	current, err := f.s.GetMaintenancePlan(ctx, f.token, plan.ID)
	if err != nil || current.Validity != "basis_changed" {
		t.Fatal("retained manual endpoint was not part of the immutable basis", current, err)
	}
}

func TestMaintenanceRetainedWindowsUseBottleneckAndUnknownNeverMeansZero(t *testing.T) {
	f := archivedCosts(t)
	ctx := context.Background()
	original, err := f.store(ctx, []string{"short", "seven_day"}, false)
	if err != nil {
		t.Fatal(err)
	}
	in := maintenanceInputFor(f.a, f.points[0], f.points[2])
	in.Until = original.GeneratedAt.Add(time.Microsecond).Format(time.RFC3339Nano)
	in.RulesVersion = maintenanceHistoryRules
	plan, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || plan.State != "ready" || len(plan.Preview.Financial.Results[0].Windows) != 2 || plan.Preview.Financial.Results[0].Remaining.Numerator != "12" || plan.Preview.Financial.Results[0].Remaining.Denominator != "5" {
		t.Fatal("overlapping windows were summed or lost", plan, err)
	}
	exec(t, f.db, `UPDATE estimate_results SET snapshot=jsonb_set(snapshot,'{windows,1,cost_proof,intervals,0,standard_cost}','{"numerator":"0","denominator":"1"}'::jsonb) WHERE id=$1`, original.ID)
	unknown, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || unknown.State != "ready" || unknown.Preview.Financial.Results[0].State != "non_identifying_delta" || unknown.Preview.Financial.Results[0].Remaining != nil {
		t.Fatal("complete non-identifying evidence became zero or a partial monetary value", unknown, err)
	}
	if _, err = f.s.ApplyMaintenancePlan(ctx, f.token, unknown.ID, unknown.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	processMaintenanceForTest(t, f.s)
	applied, err := f.s.GetMaintenancePlan(ctx, f.token, unknown.ID)
	if err != nil || applied.State != "applied" || applied.Preview.Financial.Results[0].Remaining != nil {
		t.Fatal("explicit unknown result was not preserved", applied, err)
	}
}

func TestMaintenanceRetainedScanLimitAndNativeConstraintFailClosed(t *testing.T) {
	f := archivedCosts(t)
	ctx := context.Background()
	original, err := f.store(ctx, []string{"short"}, false)
	if err != nil {
		t.Fatal(err)
	}
	in := maintenanceInputFor(f.a, f.points[0], f.points[2])
	in.Until = original.GeneratedAt.Add(time.Microsecond).Format(time.RFC3339Nano)
	legacy, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || legacy.State != "ready" {
		t.Fatal(legacy, err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE maintenance_plans SET snapshot=jsonb_set(snapshot,'{rules_version}','"retained-estimates-v2"'::jsonb) WHERE id=$1`, legacy.ID); err == nil {
		t.Fatal("native schema accepted an executable v2 plan without historical projection")
	}
	exec(t, f.db, `INSERT INTO estimate_results(account_id,generated_at,snapshot) SELECT account_id,generated_at,snapshot FROM estimate_results CROSS JOIN generate_series(1,2000) WHERE id=$1`, original.ID)
	in.RulesVersion = maintenanceHistoryRules
	blocked, err := f.s.PreflightMaintenance(ctx, f.token, in)
	if err != nil || blocked.State != "blocked" || blocked.Preview.Financial.ScanComplete || blocked.Preview.Financial.Records != 2001 || len(blocked.Preview.Financial.Results) != 0 {
		t.Fatal("bounded history returned a fabricated partial result", blocked, err)
	}
}
