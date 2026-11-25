package connection

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/notifications"
)

func (s *Service) FinancialNotificationSupport(ctx context.Context, tx pgx.Tx) (notifications.FinancialSupport, error) {
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return notifications.FinancialSupport{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil || s.simulationEpoch(c) != "" {
		return notifications.FinancialSupport{Simulation: err == nil}, err
	}
	support := notifications.FinancialSupport{}
	if policy.Enabled {
		support.NativeSourceID = nativeFinancialSource(c)
	}
	return support, nil
}

func nativeFinancialSource(c storedConfig) string {
	if c.SourceID == nil || !c.CredentialSet || !c.SecretStorageAvailable || c.reverificationRequired || c.Diagnostic == nil || c.Diagnostic.Version == nil {
		return ""
	}
	if !c.Diagnostic.Saved || c.Diagnostic.SourceID == nil || *c.Diagnostic.SourceID != *c.SourceID || c.Diagnostic.Revision != c.Revision {
		return ""
	}
	check := c.Diagnostic.capability("version.read")
	if check == nil || check.State != "available" || keyModeProbeForVersion(*c.Diagnostic.Version) == "" {
		return ""
	}
	return *c.SourceID
}

func financialEstimateChanged(previous, current *EstimateRecord) bool {
	if previous == nil || current == nil || !previous.Complete || !current.Complete || previous.State != "scenario" || current.State != "scenario" || previous.Currency != "USD" || current.Currency != "USD" || previous.AccountID != current.AccountID || previous.RulesVersion != current.RulesVersion || previous.PolicyRevision != current.PolicyRevision || previous.ManagementRevision != current.ManagementRevision || !reflect.DeepEqual(previous.Configuration, current.Configuration) || len(previous.Windows) == 0 || len(previous.Windows) != len(current.Windows) {
		return false
	}
	changed := false
	for i, old := range previous.Windows {
		next := current.Windows[i]
		a, b := old.CostProof, next.CostProof
		if old.State != "scenario" || next.State != "scenario" || old.Kind != next.Kind || old.Basis != next.Basis || old.Period == nil || !reflect.DeepEqual(old.Period, next.Period) || !reflect.DeepEqual(old.ManualStartAt, next.ManualStartAt) || !reflect.DeepEqual(old.ManualEndAt, next.ManualEndAt) || a == nil || b == nil || a.SimulationProvenance != b.SimulationProvenance || a.PriceVersion != b.PriceVersion || a.StandardRate != b.StandardRate || a.WalletMultiplier != b.WalletMultiplier || a.BillingType != b.BillingType || !a.PriceEffectiveAt.Equal(b.PriceEffectiveAt) {
			return false
		}
		before, okBefore := old.Capacity.Rational()
		after, okAfter := next.Capacity.Rational()
		if !okBefore || !okAfter || before.Sign() <= 0 || after.Sign() <= 0 {
			return false
		}
		// Independent notification policy: an exact relative capacity change of at least 20 percent.
		delta := new(big.Rat).Sub(after, before)
		delta.Abs(delta)
		delta.Mul(delta, big.NewRat(5, 1))
		changed = changed || delta.Cmp(before) >= 0
	}
	return changed
}

func financialWalletTarget(r *WalletRecommendation) (string, bool) {
	if r == nil || !r.Complete || r.Target == nil || r.Simulation == nil || r.Simulation.Protocol != simulationProtocol || r.Simulation.Environment != "simulated" || r.Simulation.Epoch == "" || r.ExpiresAt == nil || !time.Now().Before(*r.ExpiresAt) || len(r.Contributions) == 0 {
		return "", false
	}
	_, remainder, target, reason := simulationWalletTarget(r.Target, simulationWalletQuantum)
	if remainder == nil || reason != "" && reason != "ZERO_TARGET_PENDING" {
		return "", false
	}
	if target == "" {
		target = "0.00000000"
	}
	return target, true
}

func financialWalletChanged(previous, current *WalletRecommendation) bool {
	before, oldOK := financialWalletTarget(previous)
	after, newOK := financialWalletTarget(current)
	if !oldOK || !newOK || before == after || previous.ParticipantID != current.ParticipantID || previous.BindingID != current.BindingID || previous.ParticipantRevision != current.ParticipantRevision || previous.RulesVersion != current.RulesVersion || *previous.Simulation != *current.Simulation || len(previous.Contributions) != len(current.Contributions) {
		return false
	}
	for i, old := range previous.Contributions {
		next := current.Contributions[i]
		if old.AccountID != next.AccountID || old.AccountRevision != next.AccountRevision || old.SimulationProof == nil || next.SimulationProof == nil || !old.SimulationProof.StartAt.Equal(next.SimulationProof.StartAt) {
			return false
		}
	}
	return true
}

func financialBalanceLow(r *WalletRecommendation, read *WalletRead, b walletBasis) bool {
	target, valid := financialWalletTarget(r)
	if !valid || read == nil || read.ID != b.WalletHead || read.SourceID != b.SourceID || read.ConnectionRevision != b.ConnectionRevision || read.Read.State != "available" || read.FieldAuthority.State != "available" || read.Unit != "simulated_wallet_usd" || read.SimulationScope == nil || !simulationWalletClosed(*read.SimulationScope) || read.SimulationScope.SimulationProvenance != *r.Simulation || read.Balance.State != "reported" || read.Balance.Value == nil || read.ReadAt == nil || read.ReadAt.After(time.Now()) || time.Since(*read.ReadAt) > time.Duration(b.StaleAfterSeconds)*time.Second {
		return false
	}
	goal, _ := simulationDecimal(target)
	balance, valid := simulationDecimal(*read.Balance.Value)
	return valid && goal.Sign() > 0 && goal.Cmp(balance) > 0
}

func financialApplicationsCurrent(ctx context.Context, tx pgx.Tx, read *WalletRead) (bool, error) {
	if read == nil || read.ReadAt == nil {
		return false, nil
	}
	var current bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=$1 AND r.binding_id=$2 AND (NOT (`+walletApplicationResolvedSQL+`) OR r.attempted_at>$3 OR EXISTS(SELECT 1 FROM wallet_application_results x WHERE x.application_id=r.id AND x.recorded_at>$3)))`, read.ParticipantID, read.BindingID, *read.ReadAt).Scan(&current)
	return current, err
}

func (s *Service) recordWalletNotifications(ctx context.Context, tx pgx.Tx, b walletBasis, r WalletRecommendation, previous *WalletRecommendation) error {
	if _, complete := financialWalletTarget(&r); !complete {
		return nil
	}
	read, err := latestWalletRead(ctx, tx, r.ParticipantID, r.BindingID, false)
	if err != nil {
		return err
	}
	current, err := financialApplicationsCurrent(ctx, tx, read)
	if err != nil || !current {
		return err
	}
	accounts := make([]string, 0, len(r.Contributions))
	for _, row := range r.Contributions {
		accounts = append(accounts, row.AccountID)
	}
	e := notifications.FinancialEvidence{SubjectID: r.ParticipantID, SourceID: b.SourceID, ConnectionRevision: b.ConnectionRevision, SubjectRevision: b.ParticipantRevision, RecordID: r.ID, PreviousID: "0", AccountIDs: accounts, AsOf: r.GeneratedAt}
	target, _ := financialWalletTarget(&r)
	if financialBalanceLow(&r, read, b) {
		e.Kind = "balance_low"
		body := fmt.Sprintf("Conditional simulation only. Participant %s: independently read balance %s simulated wallet USD is below complete whole-wallet advice %s (q8). Recommendation %s, wallet read %s, %d sources, as of %s; trigger %s. This is advice, not a native qualification, and performs no financial operation.", r.ParticipantID, *read.Balance.Value, target, r.ID, read.ID, len(accounts), r.GeneratedAt.Format(time.RFC3339Nano), r.Trigger)
		if err = notifications.RecordFinancial(ctx, tx, e, body); err != nil {
			return err
		}
	}
	if financialWalletChanged(previous, &r) {
		e.Kind, e.PreviousID = "recommendation_changed", previous.ID
		before, _ := financialWalletTarget(previous)
		body := fmt.Sprintf("Conditional simulation only. Participant %s: comparable complete whole-wallet advice changed from %s to %s simulated wallet USD (q8), after %s. Previous recommendation %s at %s; current %s at %s; %d sources. No financial operation was performed by this notification.", r.ParticipantID, before, target, r.Trigger, previous.ID, previous.GeneratedAt.Format(time.RFC3339Nano), r.ID, r.GeneratedAt.Format(time.RFC3339Nano), len(accounts))
		return notifications.RecordFinancial(ctx, tx, e, body)
	}
	return nil
}

func (s *Service) recordEstimateNotification(ctx context.Context, tx pgx.Tx, r EstimateRecord, previous *EstimateRecord) error {
	if !financialEstimateChanged(previous, &r) {
		return nil
	}
	current, err := s.financialEstimateCurrent(ctx, tx, r)
	if err != nil || !current {
		return err
	}
	a := r.Configuration.Assumptions
	e := notifications.FinancialEvidence{Kind: "estimate_changed", SubjectID: r.AccountID, SourceID: a.SourceID, ConnectionRevision: a.ConnectionRevision, SubjectRevision: a.AccountRevision, RecordID: r.ID, PreviousID: previous.ID, AccountIDs: []string{r.AccountID}, AsOf: r.GeneratedAt}
	body := fmt.Sprintf("Conditional simulation only. Account %s: at least one comparable standard-cost capacity changed by 20 percent or more under the independent notification policy. Previous estimate %s at %s; current %s at %s; trigger %s. Source, configuration, official period, units and historical price are unchanged. Standard USD estimates are not wallet targets; the model is not calibrated.", r.AccountID, previous.ID, previous.GeneratedAt.Format(time.RFC3339Nano), r.ID, r.GeneratedAt.Format(time.RFC3339Nano), r.Trigger)
	return notifications.RecordFinancial(ctx, tx, e, body)
}

func (s *Service) financialEstimateCurrent(ctx context.Context, tx pgx.Tx, r EstimateRecord) (bool, error) {
	policy, c, account, err := s.estimateEnvironment(ctx, tx, r.AccountID)
	if errors.Is(err, auth.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	epoch := s.simulationEpoch(c)
	a := r.Configuration.Assumptions
	if epoch == "" || !r.Complete || r.State != "scenario" || r.Remaining == nil || r.Currency != "USD" || a == nil || !account.SourceCurrent || !account.ConnectionAvailable || account.IdentityState != "verified" || account.SourceMode != "active" || a.SourceID != account.SourceID || a.ConnectionRevision != c.Revision || a.AccountRevision != account.Revision || a.SourceMode != account.SourceMode || r.PolicyRevision != policy.Revision || len(r.Windows) == 0 {
		return false, nil
	}
	cfg, err := readEstimateConfiguration(ctx, tx, r.AccountID, false)
	if err != nil {
		return false, err
	}
	observation, capture, management, err := estimateHeads(ctx, tx, r.AccountID)
	if err != nil {
		return false, err
	}
	latest, err := latestEstimate(ctx, tx, r.AccountID)
	if err != nil {
		return false, err
	}
	if latest == nil || latest.ID != r.ID || !reflect.DeepEqual(cfg, r.Configuration) || observation != r.ObservationHead || capture != r.CaptureHead || management != r.ManagementRevision {
		return false, nil
	}
	var available bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT state='succeeded' AND read_check->>'state'='available' FROM collection_jobs WHERE account_id=$1 AND started_at IS NOT NULL ORDER BY collection_jobs.id DESC LIMIT 1),false)`, r.AccountID).Scan(&available); err != nil || !available {
		return false, err
	}
	for _, w := range r.Windows {
		if w.State != "scenario" || w.Capacity == nil || w.Period == nil || w.SourceAt == nil || w.OldestSampleAt == nil || staleEstimateWindow(w, policy, time.Now().UTC()) || w.CostProof == nil || w.CostProof.SimulationProvenance != (SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: epoch}) || len(w.CostProof.Intervals) != 2 {
			return false, nil
		}
	}
	return true, nil
}

// ValidateFinancialNotification uses current local heads and frozen independent proofs; it never reads or writes an upstream.
func (s *Service) ValidateFinancialNotification(ctx context.Context, tx pgx.Tx, e notifications.FinancialEvidence) (bool, error) {
	if e.Native != nil {
		if e.Kind == "estimate_changed" {
			return s.validateNativeEstimateNotification(ctx, tx, e)
		}
		return s.validateNativeWalletNotification(ctx, tx, e)
	}
	if e.Kind == "estimate_changed" {
		r, err := scanEstimate(tx.QueryRow(ctx, `SELECT `+estimateColumns+` FROM estimate_results WHERE id=$1 AND account_id=$2`, e.RecordID, e.SubjectID))
		if errors.Is(err, auth.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		p, err := scanEstimate(tx.QueryRow(ctx, `SELECT `+estimateColumns+` FROM estimate_results WHERE id=$1 AND account_id=$2`, e.PreviousID, e.SubjectID))
		if errors.Is(err, auth.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		a := r.Configuration.Assumptions
		if a == nil || e.SourceID != a.SourceID || e.ConnectionRevision != a.ConnectionRevision || e.SubjectRevision != a.AccountRevision || !e.AsOf.Equal(r.GeneratedAt) || !slices.Equal(e.AccountIDs, []string{r.AccountID}) || !financialEstimateChanged(&p, &r) {
			return false, nil
		}
		return s.financialEstimateCurrent(ctx, tx, r)
	}
	if e.Kind != "balance_low" && e.Kind != "recommendation_changed" {
		return false, nil
	}
	if err := lockWallet(ctx, tx, e.SubjectID); err != nil {
		return false, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return false, err
	}
	b, err := s.walletBasis(ctx, tx, e.SubjectID)
	if errors.Is(err, auth.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if e.SourceID != b.SourceID || e.ConnectionRevision != b.ConnectionRevision || e.SubjectRevision != b.ParticipantRevision {
		return false, nil
	}
	r, err := latestWalletRecommendation(ctx, tx, e.SubjectID, b.BindingID)
	if err != nil || r == nil {
		return false, err
	}
	if r.ID != e.RecordID || !e.AsOf.Equal(r.GeneratedAt) {
		return false, nil
	}
	current := s.simulationWalletCurrent(c, b, e.SubjectID, time.Now().UTC(), r)
	if !current.Complete {
		return false, nil
	}
	accounts := make([]string, 0, len(r.Contributions))
	for _, row := range r.Contributions {
		accounts = append(accounts, row.AccountID)
	}
	if !slices.Equal(accounts, e.AccountIDs) {
		return false, nil
	}
	read, err := latestWalletRead(ctx, tx, e.SubjectID, b.BindingID, false)
	if err != nil {
		return false, err
	}
	applicationsCurrent, err := financialApplicationsCurrent(ctx, tx, read)
	if err != nil || !applicationsCurrent {
		return false, err
	}
	if e.Kind == "balance_low" {
		return err == nil && e.PreviousID == "0" && financialBalanceLow(r, read, b), err
	}
	p, err := scanWalletRecommendation(tx.QueryRow(ctx, `SELECT `+walletRecommendationColumns+` FROM wallet_recommendations WHERE id=$1 AND participant_id=$2 AND binding_id=$3`, e.PreviousID, e.SubjectID, b.BindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return financialWalletChanged(&p, r), nil
}
