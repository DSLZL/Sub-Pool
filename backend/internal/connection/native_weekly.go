package connection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

const nativeWeek = 7 * 24 * time.Hour
const nativeResetDrift = 5 * time.Minute
const nativeWeeklyObservationLimit = 12000
const nativeWeeklyPointLimit = 128

type NativeWeeklyPoint struct {
	ID          string    `json:"observation_id"`
	At          time.Time `json:"source_at"`
	UsedPercent string    `json:"used_percent"`
}

type nativeWeeklyInterval struct {
	Start NativeWeeklyPoint `json:"start"`
	End   NativeWeeklyPoint `json:"end"`
}

type NativeWeeklySample struct {
	nativeWeeklyInterval
	StandardCost *estimation.Amount `json:"standard_cost"`
	Records      int                `json:"records"`
	ReadAt       time.Time          `json:"read_at"`
}

type NativeWeeklyCheckpoint struct {
	Observation  NativeWeeklyPoint  `json:"observation"`
	StandardCost *estimation.Amount `json:"standard_cost"`
	Capacity     *estimation.Amount `json:"capacity"`
}

type NativeWeeklyCumulative struct {
	ReadAt      time.Time                `json:"read_at"`
	Confidence  string                   `json:"confidence"`
	Checkpoints []NativeWeeklyCheckpoint `json:"checkpoints"`
}

type NativeWeeklyRecord struct {
	ID                 string                  `json:"id"`
	AccountID          string                  `json:"account_id"`
	SourceID           string                  `json:"source_id"`
	ConnectionRevision string                  `json:"connection_revision"`
	AccountRevision    string                  `json:"account_revision"`
	GeneratedAt        time.Time               `json:"generated_at"`
	Observation        NativeWeeklyPoint       `json:"observation"`
	Period             ObservationPeriod       `json:"period"`
	Samples            []NativeWeeklySample    `json:"samples,omitempty"`
	Cumulative         *NativeWeeklyCumulative `json:"cumulative,omitempty"`
	StandardCost       *estimation.Amount      `json:"standard_cost"`
	Increase           *estimation.Amount      `json:"percentage_point_increase,omitempty"`
	Records            int                     `json:"records"`
	Capacity           *estimation.Amount      `json:"capacity"`
	Remaining          *estimation.Amount      `json:"remaining"`
}

type NativeWeeklyCurrent struct {
	State       string                  `json:"state"`
	Observation *NativeWeeklyPoint      `json:"observation"`
	Period      *ObservationPeriod      `json:"period"`
	Latest      *NativeWeeklyRecord     `json:"latest"`
	Remaining   *estimation.Amount      `json:"remaining"`
	Progress    *nativeWeeklyProgress   `json:"progress"`
	Allocation  *nativeWeeklyAllocation `json:"allocation,omitempty"`
}

type nativeWeeklyPlan struct {
	Binding     string              `json:"binding"`
	Period      *ObservationPeriod  `json:"period"`
	Points      []NativeWeeklyPoint `json:"points"`
	WalletBasis bool                `json:"wallet_basis,omitempty"`
	CostVersion int                 `json:"actual_cost_version,omitempty"`
}

func (p nativeWeeklyPlan) key() string {
	body, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (p nativeWeeklyPlan) includes(old nativeWeeklyPlan) bool {
	if p.Binding != old.Binding || p.WalletBasis != old.WalletBasis || p.Period == nil || old.Period == nil || !p.Period.StartAt.Equal(old.Period.StartAt) ||
		len(old.Points) == 0 || len(old.Points) > len(p.Points) {
		return false
	}
	for i, point := range old.Points {
		current := p.Points[i]
		if current.ID != point.ID || !current.At.Equal(point.At) || current.UsedPercent != point.UsedPercent {
			return false
		}
	}
	return true
}

func (p nativeWeeklyPlan) through() NativeWeeklyPoint {
	return p.Points[len(p.Points)-1]
}

type weeklyPoint struct {
	NativeWeeklyPoint
	period *ObservationPeriod
	value  *big.Rat
}

func weeklyObservation(id string, snapshot QuotaSnapshot) weeklyPoint {
	period := nativeObservationPeriod(snapshot, "seven_day")
	if period == nil || period.DurationSeconds != "604800" {
		return weeklyPoint{}
	}
	text := quotaUsed(snapshot, "seven_day")
	percent, ok := usageNumber(json.RawMessage(*text), false)
	if !ok || percent == nil {
		return weeklyPoint{}
	}
	value, _ := new(big.Rat).SetString(*percent)
	if value.Cmp(big.NewRat(100, 1)) > 0 {
		return weeklyPoint{}
	}
	return weeklyPoint{NativeWeeklyPoint{id, snapshot.SourceAt.UTC(), *percent}, period, value}
}

// Keep the first reported reset for a cycle while the provider's reset estimates drift.
func weeklyCycle(points []weeklyPoint) (*ObservationPeriod, []NativeWeeklyPoint, string) {
	// Ties keep the earliest receipt for stable evidence IDs across repeated reads.
	sort.SliceStable(points, func(i, j int) bool { return points[i].At.After(points[j].At) })
	latest, prior := points[0], points[0]
	levels := []weeklyPoint{latest}
	period := latest.period
	for _, point := range points[1:] {
		drift := point.period.EndAt.Sub(latest.period.EndAt)
		if drift < -nativeResetDrift || drift > nativeResetDrift || !latest.At.Before(point.period.EndAt) {
			break
		}
		delta := prior.value.Cmp(point.value)
		if point.At.Equal(prior.At) && delta != 0 {
			return period, nil, "invalid_evidence"
		}
		if delta < 0 {
			return period, nil, "quota_decreased"
		}
		if point.At.Equal(prior.At) {
			continue
		}
		if delta > 0 {
			levels = append(levels, point)
		} else {
			levels[len(levels)-1] = point
		}
		period = point.period
		prior = point
	}
	var checkpoints []NativeWeeklyPoint
	for i := len(levels) - 1; i >= 0; i-- {
		if levels[i].value.Sign() > 0 && levels[i].At.After(period.StartAt) {
			checkpoints = append(checkpoints, levels[i].NativeWeeklyPoint)
		}
	}
	return period, checkpoints, ""
}

func nativeWeeklyAccount(a storedAccount) bool {
	return a.SourceMode == "passive" && a.Snapshot.Account.Platform == "openai" && a.Snapshot.Account.Type == "oauth"
}

func readNativeWeeklyPlan(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, policy MonitoringConfig, now time.Time) (*NativeWeeklyCurrent, nativeWeeklyPlan, error) {
	var plan nativeWeeklyPlan
	if !nativeWeeklyAccount(a) {
		return nil, plan, nil
	}
	out := &NativeWeeklyCurrent{}
	switch {
	case !a.SourceCurrent || a.IdentityState != "verified" || len(a.fingerprint) == 0:
		out.State = "condition_changed"
	case !a.ConnectionAvailable || c.reverificationRequired:
		out.State = "connection_unavailable"
	case !a.Monitoring || !policy.Enabled:
		out.State = "monitoring_paused"
	}
	if out.State != "" {
		return out, plan, nil
	}

	// A successful passive job already checked the immutable managed account binding.
	var state, reason string
	err := tx.QueryRow(ctx, `SELECT state,reason FROM collection_jobs WHERE account_id=$1 AND source_id=$2
	 AND connection_revision=$3 AND source_mode='passive' AND started_at IS NOT NULL
	 AND state NOT IN ('queued','running') AND (read_check IS NOT NULL OR state='interrupted' OR reason='CONNECTION_UNAVAILABLE')
	 ORDER BY id DESC LIMIT 1`, a.ID, a.SourceID, c.Revision).Scan(&state, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		out.State = "awaiting_observation"
		return out, plan, nil
	}
	if err != nil {
		return nil, plan, err
	}
	if state != "succeeded" || reason != "VERIFIED_PASSIVE_SNAPSHOT" {
		out.State = "quota_unavailable"
		return out, plan, nil
	}
	var management string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM observation_management WHERE account_id=$1),1)::text`, a.ID).Scan(&management); err != nil {
		return nil, plan, err
	}
	rows, err := tx.Query(ctx, `SELECT q.id::text,q.snapshot FROM quota_observations q JOIN collection_jobs j ON j.id=q.job_id
	 WHERE q.account_id=$1 AND q.received_at >= $4 AND j.source_id=$2 AND j.connection_revision=$3
	 AND j.state='succeeded' AND j.reason='VERIFIED_PASSIVE_SNAPSHOT'
	 AND (SELECT action FROM observation_changes WHERE observation_id=q.id ORDER BY id DESC LIMIT 1) IS DISTINCT FROM 'exclude'
	 ORDER BY q.id DESC LIMIT $5`, a.ID, a.SourceID, c.Revision, now.Add(-nativeWeek), nativeWeeklyObservationLimit+1)
	if err != nil {
		return nil, plan, err
	}
	defer rows.Close()
	points := []weeklyPoint{}
	var current weeklyPoint
	scanned := 0
	for rows.Next() {
		var id string
		var snapshot QuotaSnapshot
		if err := rows.Scan(&id, &snapshot); err != nil {
			return nil, plan, err
		}
		scanned++
		point := weeklyObservation(id, snapshot)
		if scanned == 1 {
			current = point
			if current.period == nil {
				out.State = "period_unknown"
				return out, plan, nil
			}
			out.Observation, out.Period = &current.NativeWeeklyPoint, current.period
			if out.stale(policy, now) {
				out.State = "source_stale"
				return out, plan, nil
			}
		}
		if point.period != nil {
			if point.At.After(current.At) {
				out.State = "invalid_evidence"
				return out, plan, nil
			}
			points = append(points, point)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, plan, err
	}
	switch {
	case scanned == 0:
		out.State = "awaiting_observation"
	case scanned > nativeWeeklyObservationLimit:
		out.State = "observation_limit"
	default:
		// SQL emits newest receipts first. Reverse ties before sorting by source time.
		for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
			points[i], points[j] = points[j], points[i]
		}
		period, checkpoints, cycleState := weeklyCycle(points)
		out.Period = period
		switch {
		case cycleState != "":
			out.State = cycleState
		case out.stale(policy, now):
			out.State = "source_stale"
		case len(checkpoints) == 0:
			out.State = "awaiting_increase"
		case len(checkpoints) > nativeWeeklyPointLimit:
			out.State = "observation_limit"
		default:
			out.State = "collecting"
			plan = nativeWeeklyPlan{
				Binding:     fmt.Sprintf("%s:%s:%s:%x:%s", a.SourceID, c.Revision, a.UpstreamID, a.fingerprint, management),
				Period:      period,
				Points:      checkpoints,
				WalletBasis: true,
				CostVersion: 1,
			}
		}
	}
	return out, plan, nil
}

func (out *NativeWeeklyCurrent) stale(policy MonitoringConfig, now time.Time) bool {
	return out.Observation != nil && out.Period != nil && (out.Observation.At.After(now) ||
		now.Sub(out.Observation.At) > time.Duration(policy.StaleAfterSeconds)*time.Second || !now.Before(out.Period.EndAt))
}

func nativeWeeklyRemaining(capacity *estimation.Amount, used string) *estimation.Amount {
	total, _ := capacity.Rational()
	percent, _ := new(big.Rat).SetString(used)
	fraction := new(big.Rat).Sub(big.NewRat(100, 1), percent)
	return estimation.Exact(total.Mul(total, fraction.Quo(fraction, big.NewRat(100, 1))))
}

func (s *Service) nativeWeeklyCurrent(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, policy MonitoringConfig, now time.Time) (*NativeWeeklyCurrent, error) {
	out, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, now)
	if err != nil || len(plan.Points) == 0 {
		return out, err
	}
	pending, latest, err := readNativeWeeklyWork(ctx, tx, a.ID, plan.key())
	if err != nil {
		return nil, err
	}
	if pending != nil && plan.includes(pending.Work.Plan) {
		out.State = pending.Work.State
		out.Progress = pending.Work.progress()
	}
	if latest != nil && latest.Result.Cumulative != nil && plan.includes(latest.Work.Plan) {
		out.State, out.Latest = "ready", latest.Result
		out.Remaining = nativeWeeklyRemaining(latest.Result.Capacity, out.Observation.UsedPercent)
	}
	return out, nil
}

type NativeWeeklyHistory struct {
	AccountID  string               `json:"account_id"`
	Records    []NativeWeeklyRecord `json:"records"`
	NextBefore *string              `json:"next_before"`
}

func (s *Service) ListNativeWeeklyHistory(ctx context.Context, token, account, before string) (NativeWeeklyHistory, error) {
	if !managedID(account) || !validUsageAfter(before) {
		return NativeWeeklyHistory{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NativeWeeklyHistory{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "estimates")
	if err == nil {
		err = access.CheckAccount(account)
	}
	if err != nil {
		return NativeWeeklyHistory{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE id=$1)`, account).Scan(&exists); err != nil {
		return NativeWeeklyHistory{}, err
	}
	if !exists {
		return NativeWeeklyHistory{}, auth.ErrNotFound
	}
	const limit = 10
	out := NativeWeeklyHistory{AccountID: account, Records: []NativeWeeklyRecord{}}
	rows, err := tx.Query(ctx, `SELECT result FROM native_weekly_estimates WHERE account_id=$1 AND result IS NOT NULL
	 AND ($2::bigint=0 OR id < $2::bigint) ORDER BY id DESC LIMIT $3`, account, before, limit+1)
	if err != nil {
		return NativeWeeklyHistory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var record NativeWeeklyRecord
		if err = rows.Scan(&record); err != nil {
			return NativeWeeklyHistory{}, err
		}
		if len(out.Records) == limit {
			out.NextBefore = &out.Records[limit-1].ID
			break
		}
		out.Records = append(out.Records, record)
	}
	if err = rows.Err(); err != nil {
		return NativeWeeklyHistory{}, err
	}
	rows.Close()
	return out, s.finishEstimateRead(ctx, tx, token)
}
