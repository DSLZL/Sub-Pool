package connection

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

const estimateObservationLimit = 2000

func quotaUsed(snapshot QuotaSnapshot, kind string) *string {
	for _, w := range snapshot.Windows {
		if w.Kind == kind {
			return w.UsedPercent
		}
	}
	return nil
}

func staleEstimateWindow(w EstimateWindow, policy MonitoringConfig, now time.Time) bool {
	for _, at := range []*time.Time{w.SourceAt, w.OldestSampleAt} {
		if at != nil && (at.After(now) || now.Sub(*at) > time.Duration(policy.StaleAfterSeconds)*time.Second) {
			return true
		}
	}
	return w.Period != nil && !now.Before(w.Period.EndAt)
}

func (s *Service) estimateWindow(ctx context.Context, tx pgx.Tx, cfg EstimateConfiguration, a storedAccount, c storedConfig, policy MonitoringConfig, management ObservationManagement, kind string, now time.Time) (EstimateWindow, error) {
	out := EstimateWindow{Kind: kind, Basis: "official", ObservationIDs: []string{}, Result: estimation.Result{State: "insufficient_samples"}}
	latest, err := scanObservation(tx.QueryRow(ctx, `SELECT `+observationColumns+` FROM `+observationTables+` WHERE q.account_id=$1 AND NOT COALESCE(v.action='exclude',false) ORDER BY q.id DESC LIMIT 1`, a.ID))
	if errors.Is(err, auth.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.CurrentObservationID = &latest.ID
	out.SourceAt = latest.Snapshot.SourceAt
	out.CurrentUsedPercent = quotaUsed(latest.Snapshot, kind)
	out.Period = nativeObservationPeriod(latest.Snapshot, kind)
	if out.Period == nil {
		out.State = "period_unknown"
		return out, nil
	}
	if !a.SourceCurrent || !a.ConnectionAvailable || a.IdentityState != "verified" || cfg.Assumptions.SourceID != a.SourceID || cfg.Assumptions.ConnectionRevision != c.Revision || cfg.Assumptions.AccountRevision != a.Revision || cfg.Assumptions.SourceMode != a.SourceMode {
		out.State = "condition_changed"
		return out, nil
	}
	if latest.Snapshot.SourceAt.After(now) || now.Sub(*latest.Snapshot.SourceAt) > time.Duration(policy.StaleAfterSeconds)*time.Second || !now.Before(out.Period.EndAt) {
		out.State = "source_stale"
		return out, nil
	}
	var manual *ObservationStart
	if management.ManualStart != nil && management.ManualStart.Event.WindowKind != nil && *management.ManualStart.Event.WindowKind == kind {
		manual = management.ManualStart
		out.Basis = "manual"
		out.ManualStartAt = manual.Start.Snapshot.SourceAt
		out.ManualEndAt = manual.End.Snapshot.SourceAt
		if !manual.Usable || manual.Period == nil || !manual.Period.StartAt.Equal(out.Period.StartAt) || !manual.Period.EndAt.Equal(out.Period.EndAt) {
			out.State = "manual_range_unusable"
			return out, nil
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+observationColumns+` FROM `+observationTables+` WHERE q.account_id=$1 AND q.received_at>=$2 AND NOT COALESCE(v.action='exclude',false) ORDER BY q.id DESC LIMIT $3`, a.ID, ceilMicrosecond(out.Period.StartAt), estimateObservationLimit+1)
	if err != nil {
		return out, err
	}
	points := []ObservationRecord{}
	scanned := 0
	for rows.Next() {
		point, e := scanObservation(rows)
		if e != nil {
			err = e
			break
		}
		scanned++
		period := nativeObservationPeriod(point.Snapshot, kind)
		if period == nil || !period.StartAt.Equal(out.Period.StartAt) || !period.EndAt.Equal(out.Period.EndAt) {
			continue
		}
		if manual != nil && (point.Snapshot.SourceAt.Before(*out.ManualStartAt) || point.Snapshot.SourceAt.After(*out.ManualEndAt)) {
			continue
		}
		points = append(points, point)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	if scanned > estimateObservationLimit {
		out.State = "evidence_limit"
		return out, nil
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Snapshot.SourceAt.Equal(*points[j].Snapshot.SourceAt) {
			left, _ := strconv.ParseInt(points[i].ID, 10, 64)
			right, _ := strconv.ParseInt(points[j].ID, 10, 64)
			return left < right
		}
		return points[i].Snapshot.SourceAt.Before(*points[j].Snapshot.SourceAt)
	})
	unique := []ObservationRecord{}
	for _, p := range points {
		snap := p.Snapshot
		if snap.SourceID != a.SourceID || snap.ConnectionRevision != c.Revision || snap.AccountRevision != a.Revision || snap.SourceMode != a.SourceMode {
			out.State = "condition_changed"
			return out, nil
		}
		if len(unique) > 0 && snap.SourceAt.Equal(*unique[len(unique)-1].Snapshot.SourceAt) {
			left, leftOK := new(big.Rat).SetString(*quotaUsed(snap, kind))
			right, rightOK := new(big.Rat).SetString(*quotaUsed(unique[len(unique)-1].Snapshot, kind))
			if !leftOK || !rightOK || left.Cmp(right) != 0 {
				out.State = "condition_changed"
				return out, nil
			}
			unique[len(unique)-1] = p
			continue
		}
		unique = append(unique, p)
	}
	if len(unique) > 3 {
		unique = unique[len(unique)-3:]
	}
	if len(unique) > 0 {
		out.OldestSampleAt = unique[0].Snapshot.SourceAt
	}
	quotas := []string{}
	for _, p := range unique {
		out.ObservationIDs = append(out.ObservationIDs, p.ID)
		quotas = append(quotas, *quotaUsed(p.Snapshot, kind))
	}
	for _, p := range unique {
		if p.Snapshot.SourceAt.After(now) || now.Sub(*p.Snapshot.SourceAt) > time.Duration(policy.StaleAfterSeconds)*time.Second {
			out.State = "source_stale"
			return out, nil
		}
	}
	if len(unique) > 0 && unique[len(unique)-1].Snapshot.SourceAt.After(*latest.Snapshot.SourceAt) {
		out.State = "condition_changed"
		return out, nil
	}
	out.Result = estimation.Evaluate(estimation.Input{Keep: cfg.Assumptions.KeepRatio, Quota: quotas, CurrentQuota: *out.CurrentUsedPercent, Complete: false})
	if len(unique) == 3 && a.SourceMode == "active" && s.simulationEpoch(c) != "" {
		proof, state := s.simulationCosts(ctx, c, a, unique, latest.Snapshot)
		if proof == nil {
			out.State = state
			return out, nil
		}
		out.CostProof = proof
		costs := []string{}
		for _, interval := range proof.Intervals {
			cost, _ := interval.StandardCost.Rational()
			costs = append(costs, usageDecimal(cost))
		}
		out.Result = estimation.Evaluate(estimation.Input{Keep: cfg.Assumptions.KeepRatio, Quota: quotas, Costs: costs, CurrentQuota: *out.CurrentUsedPercent, Complete: true})
		checked := time.Now().UTC()
		if !checked.Before(out.Period.EndAt) || checked.Sub(*out.OldestSampleAt) > time.Duration(policy.StaleAfterSeconds)*time.Second || checked.Sub(*out.SourceAt) > time.Duration(policy.StaleAfterSeconds)*time.Second {
			out.Result = estimation.Result{State: "source_stale"}
		}
	}
	return out, nil
}

func (s *Service) appendAccountEstimate(ctx context.Context, tx pgx.Tx, account, trigger string, actor, task *string) (*EstimateRecord, error) {
	var configured bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM estimate_config WHERE account_id=$1 AND configuration IS NOT NULL)`, account).Scan(&configured); err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}
	policy, c, a, err := s.estimateEnvironment(ctx, tx, account)
	if err != nil {
		return nil, err
	}
	cfg, err := readEstimateConfiguration(ctx, tx, account, true)
	if err != nil {
		return nil, err
	}
	if cfg.Assumptions == nil {
		return nil, nil
	}
	management, err := readObservationManagement(ctx, tx, a, c, policy, true)
	if err != nil {
		return nil, err
	}
	observation, capture, revision, err := estimateHeads(ctx, tx, account)
	if err != nil {
		return nil, err
	}
	record := EstimateRecord{AccountID: account, GeneratedAt: time.Now().UTC().Truncate(time.Microsecond), EstimateSnapshot: EstimateSnapshot{RulesVersion: estimation.Rules, Configuration: cfg, Trigger: trigger, PolicyRevision: policy.Revision, ManagementRevision: revision, ObservationHead: observation, CaptureHead: capture, State: "evidence_incomplete", Windows: []EstimateWindow{}, Currency: "USD", Limitations: []string{"INTERVAL_COST_COVERAGE_UNVERIFIED", "HISTORICAL_PRICING_UNVERIFIED", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"}}}
	for _, kind := range cfg.Assumptions.Windows {
		window, e := s.estimateWindow(ctx, tx, cfg, a, c, policy, management, kind, record.GeneratedAt)
		if e != nil {
			return nil, e
		}
		record.Windows = append(record.Windows, window)
	}
	record.GeneratedAt = time.Now().UTC().Truncate(time.Microsecond)
	var firstProof *SimulationCostProof
	consistent := true
	for _, w := range record.Windows {
		if p := w.CostProof; p != nil {
			if firstProof == nil {
				firstProof = p
			} else {
				consistent = consistent && p.SimulationProvenance == firstProof.SimulationProvenance && p.LedgerRevision == firstProof.LedgerRevision && p.ClosedThrough.Equal(firstProof.ClosedThrough) && p.PriceVersion == firstProof.PriceVersion && p.StandardRate == firstProof.StandardRate && p.WalletMultiplier == firstProof.WalletMultiplier && p.BillingType == firstProof.BillingType && p.PriceEffectiveAt.Equal(firstProof.PriceEffectiveAt)
			}
		}
	}
	results := []estimation.Result{}
	for i := range record.Windows {
		w := &record.Windows[i]
		if !consistent {
			w.Result = estimation.Result{State: "condition_changed"}
		} else if w.State == "scenario" && staleEstimateWindow(*w, policy, record.GeneratedAt) {
			w.Result = estimation.Result{State: "source_stale"}
		}
		results = append(results, w.Result)
	}
	record.State, record.Complete = "scenario", true
	simulated := false
	for _, w := range record.Windows {
		record.Complete = record.Complete && w.CostProof != nil
		simulated = simulated || w.CostProof != nil
		if w.State != "scenario" {
			if record.State == "scenario" {
				record.State = w.State
			}
		}
	}
	if record.Complete {
		record.Limitations = []string{"SIMULATED_PROTOCOL_ONLY", "MODEL_NOT_CALIBRATED", "NOT_A_WALLET_TARGET"}
	} else if simulated {
		record.Limitations = append(record.Limitations, "SIMULATED_PROTOCOL_ONLY")
	}
	record.Remaining = estimation.MinimumRemaining(results)
	body, err := json.Marshal(record.EstimateSnapshot)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES($1,$2,$3) RETURNING id::text`, account, record.GeneratedAt, body).Scan(&record.ID); err != nil {
		return nil, err
	}
	if err = estimateAudit(ctx, tx, record, actor, task); err != nil {
		return nil, err
	}
	return &record, nil
}
