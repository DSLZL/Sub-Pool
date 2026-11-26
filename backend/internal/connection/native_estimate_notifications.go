package connection

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/notifications"
)

func nativeEstimateChanged(previous, current *nativeWeeklyJob) bool {
	if previous == nil || current == nil || previous.Result == nil || current.Result == nil {
		return false
	}
	p, r := previous.Result, current.Result
	a, b := previous.Work.Plan, current.Work.Plan
	if p.Cumulative == nil || r.Cumulative == nil || p.Cumulative.Confidence != "trusted" || r.Cumulative.Confidence != "trusted" ||
		p.AccountID != r.AccountID || p.SourceID != r.SourceID || p.ConnectionRevision != r.ConnectionRevision ||
		p.AccountRevision != r.AccountRevision || !p.GeneratedAt.Before(r.GeneratedAt) ||
		!p.Period.StartAt.Equal(r.Period.StartAt) || !p.Period.EndAt.Equal(r.Period.EndAt) ||
		a.Binding == "" || a.Binding != b.Binding || a.CostVersion <= 0 || a.CostVersion != b.CostVersion || a.WalletBasis != b.WalletBasis {
		return false
	}
	old, oldOK := p.Capacity.Rational()
	next, nextOK := r.Capacity.Rational()
	if !oldOK || !nextOK || old.Sign() <= 0 || next.Sign() <= 0 {
		return false
	}
	diff := new(big.Rat).Sub(next, old)
	return diff.Abs(diff).Mul(diff, big.NewRat(5, 1)).Cmp(old) >= 0
}

func readPublishedNativeEstimate(row pgx.Row) (*nativeWeeklyJob, error) {
	var job nativeWeeklyJob
	err := row.Scan(&job.ID, &job.Work, &job.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &job, err
}

func (s *Service) recordNativeEstimateNotification(ctx context.Context, tx pgx.Tx, c storedConfig, current *nativeWeeklyJob) error {
	source := nativeFinancialSource(c)
	if source == "" {
		return nil
	}
	revision, preferences, err := notifications.ReadNativePolicy(ctx, tx, source)
	if err != nil || !preferences.EstimateChanged {
		return err
	}
	r := current.Result
	previous, err := readPublishedNativeEstimate(tx.QueryRow(ctx, `SELECT id::text,work,result FROM native_weekly_estimates
	 WHERE account_id=$1 AND id<$2 AND result IS NOT NULL ORDER BY native_weekly_estimates.id DESC LIMIT 1`, r.AccountID, current.ID))
	if err != nil || !nativeEstimateChanged(previous, current) {
		return err
	}
	old, _ := previous.Result.Capacity.Rational()
	next, _ := r.Capacity.Rational()
	body := fmt.Sprintf("Native seven-day standard-cost capacity changed from %s to %s for account %s. UTC week %s to %s. Previous estimate %s at %s, current estimate %s at %s.",
		usageDecimal(old), usageDecimal(next), r.AccountID, r.Period.StartAt.Format(time.RFC3339), r.Period.EndAt.Format(time.RFC3339),
		previous.ID, previous.Result.GeneratedAt.Format(time.RFC3339), current.ID, r.GeneratedAt.Format(time.RFC3339))
	return notifications.RecordFinancial(ctx, tx, notifications.FinancialEvidence{
		Kind: "estimate_changed", SubjectID: r.AccountID, SourceID: source, ConnectionRevision: r.ConnectionRevision,
		SubjectRevision: r.AccountRevision, RecordID: current.ID, PreviousID: previous.ID, AccountIDs: []string{r.AccountID}, AsOf: r.GeneratedAt,
		Native: &notifications.NativeFinancialEvidence{PolicyRevision: revision},
	}, body)
}

func (s *Service) validateNativeEstimateNotification(ctx context.Context, tx pgx.Tx, e notifications.FinancialEvidence) (bool, error) {
	policy, c, account, err := s.estimateEnvironment(ctx, tx, e.SubjectID)
	if errors.Is(err, auth.ErrNotFound) {
		return false, nil
	}
	if err != nil || !policy.Enabled || nativeFinancialSource(c) != e.SourceID ||
		c.Revision != e.ConnectionRevision || account.Revision != e.SubjectRevision || !slices.Equal(e.AccountIDs, []string{account.ID}) {
		return false, err
	}
	now := time.Now().UTC()
	view, plan, err := readNativeWeeklyPlan(ctx, tx, account, c, policy, now)
	if err != nil || view == nil || view.stale(policy, now) || len(plan.Points) == 0 {
		return false, err
	}
	_, current, err := readVerifiedNativeWeeklyWork(ctx, tx, account, c, plan.key())
	if err != nil || current == nil || current.ID != e.RecordID || current.Result.AccountID != e.SubjectID ||
		current.Result.SourceID != e.SourceID || current.Result.ConnectionRevision != e.ConnectionRevision ||
		current.Result.AccountRevision != e.SubjectRevision || !e.AsOf.Equal(current.Result.GeneratedAt) ||
		!plan.includes(current.Work.Plan) || plan.CostVersion != current.Work.Plan.CostVersion {
		return false, err
	}
	previous, err := readPublishedNativeEstimate(tx.QueryRow(ctx, `SELECT id::text,work,result FROM native_weekly_estimates
	 WHERE account_id=$1 AND id=$2 AND id<$3 AND result IS NOT NULL`, account.ID, e.PreviousID, e.RecordID))
	return err == nil && nativeEstimateChanged(previous, current), err
}
