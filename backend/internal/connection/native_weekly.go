package connection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
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
	qualified   []NativeWeeklyPoint
}

func (p nativeWeeklyPlan) key() string {
	body, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (p nativeWeeklyPlan) includes(old nativeWeeklyPlan) bool {
	if p.Binding != old.Binding || p.WalletBasis != old.WalletBasis || p.Period == nil || old.Period == nil ||
		!p.Period.StartAt.Equal(old.Period.StartAt) || !p.Period.EndAt.Equal(old.Period.EndAt) ||
		p.Period.DurationSeconds != old.Period.DurationSeconds || len(old.Points) == 0 {
		return false
	}
	points := p.qualified
	if points == nil {
		points = p.Points
	}
	for _, point := range old.Points {
		i := sort.Search(len(points), func(i int) bool { return !points[i].At.Before(point.At) })
		if i == len(points) || !sameWeeklyPoint(points[i], point) {
			return false
		}
	}
	return true
}

func sameWeeklyPoint(a, b NativeWeeklyPoint) bool {
	return a.ID == b.ID && a.At.Equal(b.At) && a.UsedPercent == b.UsedPercent
}

func (p nativeWeeklyPlan) sameCheckpoints(old nativeWeeklyPlan) bool {
	return p.CostVersion == old.CostVersion && slices.EqualFunc(p.Points, old.Points, sameWeeklyPoint)
}

func (p nativeWeeklyPlan) includesCompleted(old nativeWeeklyPlan) bool {
	if p.Binding != old.Binding {
		// Persisted binding: source:connection revision:upstream account:fingerprint:management revision.
		current, previous := strings.SplitN(p.Binding, ":", 3), strings.SplitN(old.Binding, ":", 3)
		if len(current) != 3 || len(previous) != 3 || current[0] != previous[0] || current[2] != previous[2] || !validRevision(previous[1]) {
			return false
		}
		old.Binding = p.Binding
	}
	return p.includes(old)
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
	if text == nil {
		return weeklyPoint{}
	}
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
func weeklyCycle(points []weeklyPoint) (*ObservationPeriod, []NativeWeeklyPoint, *NativeWeeklyPoint) {
	if len(points) == 0 {
		return nil, nil, nil
	}
	// The input is in receipt order. Keep the earliest equivalent observation at a tie.
	sort.SliceStable(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	usable := points[:0]
	for i := 0; i < len(points); {
		point := points[i]
		i++
		conflict := false
		for i < len(points) && points[i].At.Equal(point.At) {
			drift := points[i].period.EndAt.Sub(point.period.EndAt)
			conflict = conflict || points[i].value.Cmp(point.value) != 0 || drift < -nativeResetDrift || drift > nativeResetDrift
			i++
		}
		if !conflict {
			usable = append(usable, point)
		}
	}
	if len(usable) == 0 {
		return nil, nil, nil
	}
	points = usable
	first := len(points) - 1
	latest := points[first]
	for first > 0 {
		previous := points[first-1]
		drift := previous.period.EndAt.Sub(latest.period.EndAt)
		if drift < -nativeResetDrift || drift > nativeResetDrift || !latest.At.Before(previous.period.EndAt) {
			break
		}
		first--
	}
	period := points[first].period
	var qualified []NativeWeeklyPoint
	var current *weeklyPoint
	for i := first; i < len(points); i++ {
		point := &points[i]
		if point.At.Before(period.StartAt) || !point.At.Before(period.EndAt) {
			continue
		}
		if current != nil && point.value.Cmp(current.value) < 0 {
			continue
		}
		if point.value.Sign() > 0 && point.At.After(period.StartAt) {
			qualified = append(qualified, point.NativeWeeklyPoint)
		}
		current = point
	}
	if current == nil {
		return period, qualified, nil
	}
	return period, qualified, &current.NativeWeeklyPoint
}

func sampleWeeklyPoints(qualified []NativeWeeklyPoint, start time.Time) []NativeWeeklyPoint {
	var points []NativeWeeklyPoint
	for _, point := range qualified {
		if len(points) == 0 || points[len(points)-1].UsedPercent != point.UsedPercent {
			points = append(points, point)
		}
	}
	if len(points) <= nativeWeeklyPointLimit {
		return points
	}
	const bucketCount = nativeWeeklyPointLimit - 4
	buckets := [bucketCount]int{}
	for i := 1; i < len(points)-3; i++ {
		bucket := int(points[i].At.Sub(start) * bucketCount / nativeWeek)
		buckets[bucket] = i
	}
	sampled := make([]NativeWeeklyPoint, 0, nativeWeeklyPointLimit)
	sampled = append(sampled, points[0])
	for _, i := range buckets {
		if i != 0 {
			sampled = append(sampled, points[i])
		}
	}
	return append(sampled, points[len(points)-3:]...)
}

func nativeWeeklyAccount(a storedAccount) bool {
	return a.SourceMode == "passive" && a.Snapshot.Account.Platform == "openai" && a.Snapshot.Account.Type == "oauth"
}

func currentNativePassiveSnapshot(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig) (bool, error) {
	var verified bool
	err := tx.QueryRow(ctx, `SELECT state='succeeded' AND reason='VERIFIED_PASSIVE_SNAPSHOT' FROM collection_jobs
	 WHERE account_id=$1 AND source_id=$2 AND connection_revision=$3 AND source_mode='passive' AND started_at IS NOT NULL
	 AND state NOT IN ('queued','running') AND (read_check IS NOT NULL OR state='interrupted' OR reason='CONNECTION_UNAVAILABLE')
	 ORDER BY id DESC LIMIT 1`, a.ID, a.SourceID, c.Revision).Scan(&verified)
	return verified, err
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
	verified, err := currentNativePassiveSnapshot(ctx, tx, a, c)
	if errors.Is(err, pgx.ErrNoRows) {
		out.State = "awaiting_observation"
		return out, plan, nil
	}
	if err != nil {
		return nil, plan, err
	}
	if !verified {
		out.State = "quota_unavailable"
		return out, plan, nil
	}
	var management string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM observation_management WHERE account_id=$1),1)::text`, a.ID).Scan(&management); err != nil {
		return nil, plan, err
	}
	points, scanned, err := readNativeWeeklyPoints(ctx, tx, a, now)
	if err != nil {
		return nil, plan, err
	}
	if !scanned {
		out.State = "awaiting_observation"
		return out, plan, nil
	}
	if len(points) == 0 {
		out.State = "period_unknown"
		return out, plan, nil
	}
	slices.Reverse(points)
	period, qualified, observation := weeklyCycle(points)
	if observation == nil {
		out.State = "invalid_evidence"
		return out, plan, nil
	}
	out.Observation, out.Period = observation, period
	switch {
	case out.stale(policy, now):
		out.State = "source_stale"
	case len(qualified) == 0:
		out.State = "awaiting_increase"
	default:
		out.State = "collecting"
	}
	if out.activeCycle(now) && len(qualified) != 0 {
		plan = nativeWeeklyPlan{
			Binding:     fmt.Sprintf("%s:%s:%s:%x:%s", a.SourceID, c.Revision, a.UpstreamID, a.fingerprint, management),
			Period:      period,
			Points:      sampleWeeklyPoints(qualified, period.StartAt),
			WalletBasis: true,
			CostVersion: 1,
			qualified:   qualified,
		}
	}
	return out, plan, nil
}

func readNativeWeeklyPoints(ctx context.Context, tx pgx.Tx, a storedAccount, now time.Time) ([]weeklyPoint, bool, error) {
	var points []weeklyPoint
	var before *int64
	scanned := false
	for {
		rows, err := tx.Query(ctx, `WITH page AS MATERIALIZED (
		 SELECT id,job_id,snapshot FROM quota_observations WHERE account_id=$1 AND received_at >= $3
		 AND ($4::bigint IS NULL OR id<$4) ORDER BY id DESC LIMIT $5
		 ) SELECT q.id,CASE WHEN j.source_id=$2 AND j.source_mode='passive' AND j.state='succeeded' AND j.reason='VERIFIED_PASSIVE_SNAPSHOT'
		  AND (SELECT action FROM observation_changes WHERE observation_id=q.id ORDER BY id DESC LIMIT 1) IS DISTINCT FROM 'exclude'
		  THEN q.snapshot END
		 FROM page q JOIN collection_jobs j ON j.id=q.job_id ORDER BY q.id DESC`,
			a.ID, a.SourceID, now.Add(-nativeWeek), before, nativeWeeklyObservationLimit)
		if err != nil {
			return nil, false, err
		}
		var id int64
		count := 0
		for rows.Next() {
			var snapshot *QuotaSnapshot
			if err = rows.Scan(&id, &snapshot); err != nil {
				rows.Close()
				return nil, false, err
			}
			count++
			if snapshot == nil {
				continue
			}
			scanned = true
			point := weeklyObservation(strconv.FormatInt(id, 10), *snapshot)
			if point.period != nil && !point.At.After(now) {
				points = append(points, point)
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, false, err
		}
		if count < nativeWeeklyObservationLimit {
			return points, scanned, nil
		}
		before = &id
	}
}

func (out *NativeWeeklyCurrent) activeCycle(now time.Time) bool {
	return out != nil && out.Observation != nil && out.Period != nil &&
		!now.Before(out.Period.StartAt) && now.Before(out.Period.EndAt)
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
	stale := out.State == "source_stale"
	if !stale {
		pending, _, err := readNativeWeeklyWork(ctx, tx, a.ID, plan.key())
		if err != nil {
			return nil, err
		}
		if pending != nil && plan.includes(pending.Work.Plan) && plan.CostVersion == pending.Work.Plan.CostVersion {
			out.State = pending.Work.State
			out.Progress = pending.Work.progress()
		}
	}
	latest, err := readNativeWeeklyResult(ctx, tx, a, c, plan, true, false)
	if err == nil && latest == nil && !stale {
		latest, err = readNativeWeeklyResult(ctx, tx, a, c, plan, false, false)
	}
	if err != nil {
		return nil, err
	}
	if latest != nil {
		out.Latest = latest.Result
		if !stale {
			out.State = "ready"
			out.Remaining = nativeWeeklyRemaining(latest.Result.Capacity, out.Observation.UsedPercent)
		}
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
