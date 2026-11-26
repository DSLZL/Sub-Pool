package connection

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/estimation"
)

const weeklyDate = "2006-01-02"

type nativeWeeklyCost struct {
	Cost             *estimation.Amount `json:"cost"`
	Records          int                `json:"records"`
	Missing          bool               `json:"missing"`
	WalletCost       *estimation.Amount `json:"wallet_cost,omitempty"`
	WalletRows       int                `json:"wallet_rows,omitempty"`
	DiagnosedRows    int                `json:"diagnosed_rows,omitempty"`
	UnattributedRows int                `json:"unattributed_rows,omitempty"`
	CostIssueRows    int                `json:"cost_issue_rows,omitempty"`
}

func (c *nativeWeeklyCost) add(other nativeWeeklyCost) {
	if other.Cost != nil {
		sum, _ := other.Cost.Rational()
		if previous, ok := c.Cost.Rational(); ok {
			sum.Add(sum, previous)
		}
		c.Cost = estimation.Exact(sum)
	}
	c.Records += other.Records
	c.Missing = c.Missing || other.Missing
	if sum, ok := other.WalletCost.Rational(); ok {
		if previous, valid := c.WalletCost.Rational(); valid {
			sum.Add(sum, previous)
		}
		c.WalletCost = estimation.Exact(sum)
	}
	c.WalletRows += other.WalletRows
	c.DiagnosedRows += other.DiagnosedRows
	c.UnattributedRows += other.UnattributedRows
	c.CostIssueRows += other.CostIssueRows
}

type nativeWeeklyDayScan struct {
	Total int64  `json:"total"`
	Seen  int64  `json:"seen"`
	First string `json:"first,omitempty"`
}

type nativeWeeklyWork struct {
	Plan           nativeWeeklyPlan                `json:"plan"`
	State          string                          `json:"state"`
	Costs          []nativeWeeklyCost              `json:"costs"`
	Tail           []nativeWeeklyCost              `json:"tail,omitempty"`
	Day            time.Time                       `json:"day"`
	VerifiedAt     time.Time                       `json:"verified_at"`
	Scan           nativeWeeklyDayScan             `json:"scan"`
	WalletCapacity *estimation.Amount              `json:"wallet_capacity,omitempty"`
	Local          map[string]nativeWeeklyLocalDay `json:"local_days"`
}

type nativeWeeklyProgress struct {
	State      string    `json:"state"`
	Day        time.Time `json:"day"`
	Scanned    int64     `json:"scanned"`
	Total      int64     `json:"total"`
	Rechecking bool      `json:"rechecking"`
}

func (w *nativeWeeklyWork) progress() *nativeWeeklyProgress {
	return &nativeWeeklyProgress{w.State, w.Day, w.Scan.Seen, w.Scan.Total, w.Scan.First != ""}
}

func (w *nativeWeeklyWork) done() bool {
	return !w.Day.Before(w.Plan.through().At)
}

func (w *nativeWeeklyWork) record(account storedAccount, c storedConfig, id string) *NativeWeeklyRecord {
	r := &NativeWeeklyRecord{
		ID: id, AccountID: account.ID, SourceID: account.SourceID, ConnectionRevision: c.Revision, AccountRevision: account.Revision,
		GeneratedAt: time.Now().UTC(), Observation: w.Plan.through(), Period: *w.Plan.Period,
		Cumulative: &NativeWeeklyCumulative{ReadAt: w.VerifiedAt},
	}
	cost := new(big.Rat)
	walletCost, walletRows := new(big.Rat), 0
	var walletPoints []NativeWeeklyCheckpoint
	for i, point := range w.Plan.Points {
		sample := w.Costs[i]
		if len(w.Tail) > 0 {
			sample.add(w.Tail[i])
		}
		if sample.Missing {
			w.State = "cost_unavailable"
			return nil
		}
		r.Records += sample.Records
		walletRows += sample.WalletRows
		if actual, ok := sample.WalletCost.Rational(); ok {
			walletCost.Add(walletCost, actual)
		}
		standard, ok := sample.Cost.Rational()
		if ok {
			cost.Add(cost, standard)
		}
		if cost.Sign() == 0 {
			continue
		}
		used, _ := new(big.Rat).SetString(point.UsedPercent)
		capacity := new(big.Rat).Quo(new(big.Rat).Mul(cost, big.NewRat(100, 1)), used)
		r.Cumulative.Checkpoints = append(r.Cumulative.Checkpoints, NativeWeeklyCheckpoint{point, estimation.Exact(cost), estimation.Exact(capacity)})
		if walletRows == r.Records && walletCost.Sign() > 0 {
			walletCap := new(big.Rat).Quo(new(big.Rat).Mul(walletCost, big.NewRat(100, 1)), used)
			walletPoints = append(walletPoints, NativeWeeklyCheckpoint{point, estimation.Exact(walletCost), estimation.Exact(walletCap)})
		}
	}
	if cost.Sign() == 0 {
		w.State = "no_cost"
		return nil
	}
	points := r.Cumulative.Checkpoints
	points = points[max(0, len(points)-3):]
	last := points[len(points)-1]
	r.Cumulative.Checkpoints = points
	r.Cumulative.Confidence = weeklyConfidence(points)
	if walletRows == r.Records && len(walletPoints) >= 3 && weeklyConfidence(walletPoints[len(walletPoints)-3:]) == "trusted" {
		w.WalletCapacity = walletPoints[len(walletPoints)-1].Capacity
	}
	r.StandardCost, r.Capacity = last.StandardCost, last.Capacity
	r.Remaining = nativeWeeklyRemaining(r.Capacity, r.Observation.UsedPercent)
	return r
}

func weeklyConfidence(points []NativeWeeklyCheckpoint) string {
	if len(points) < 3 {
		return "collecting"
	}
	last := points[len(points)-1]
	used, _ := new(big.Rat).SetString(last.Observation.UsedPercent)
	if used.Cmp(big.NewRat(20, 1)) < 0 {
		return "collecting"
	}
	current, _ := last.Capacity.Rational()
	low, high := current, current
	for _, point := range points {
		value, _ := point.Capacity.Rational()
		if value.Cmp(low) < 0 {
			low = value
		}
		if value.Cmp(high) > 0 {
			high = value
		}
	}
	if new(big.Rat).Sub(high, low).Cmp(new(big.Rat).Quo(current, big.NewRat(10, 1))) > 0 {
		return "unstable"
	}
	return "trusted"
}

type nativeWeeklyJob struct {
	ID     string
	Retry  time.Time
	Work   nativeWeeklyWork
	Result *NativeWeeklyRecord
}

func readNativeWeeklyWork(ctx context.Context, tx pgx.Tx, account, key string) (pending, latest *nativeWeeklyJob, err error) {
	rows, err := tx.Query(ctx, `SELECT id::text,retry_at,work,result FROM native_weekly_estimates WHERE account_id=$1 AND result IS NULL
	 UNION ALL (SELECT id::text,retry_at,work,result FROM native_weekly_estimates WHERE account_id=$1 AND result IS NOT NULL
	  ORDER BY (basis_key=$2) DESC,native_weekly_estimates.id DESC LIMIT 1)`, account, key)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var job nativeWeeklyJob
		if err = rows.Scan(&job.ID, &job.Retry, &job.Work, &job.Result); err != nil {
			return nil, nil, err
		}
		if job.Result == nil {
			pending = &job
		} else {
			latest = &job
		}
	}
	return pending, latest, rows.Err()
}

func (s *Service) prepareNativeWeekly(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, policy MonitoringConfig) (*nativeWeeklyJob, error) {
	now := time.Now().UTC()
	view, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, now)
	if err != nil || len(plan.Points) == 0 || view.State == "source_stale" {
		return nil, err
	}
	window := usageDayRange{Start: plan.Period.StartAt, End: plan.through().At}
	if err = queueWeeklyUsage(ctx, tx, c, a, window, now, view.Observation.At.Add(time.Duration(policy.StaleAfterSeconds)*time.Second), true, false); err != nil {
		return nil, err
	}
	key := plan.key()
	pending, latest, err := readNativeWeeklyWork(ctx, tx, a.ID, key)
	if err != nil {
		return nil, err
	}
	if latest != nil && plan.sameCheckpoints(latest.Work.Plan) && plan.includesCompleted(latest.Work.Plan) {
		current, err := nativeWeeklyLocalCurrent(ctx, tx, a, c, latest.Work)
		if err != nil || current {
			if err == nil && pending != nil && !plan.includes(pending.Work.Plan) {
				_, err = tx.Exec(ctx, `DELETE FROM native_weekly_estimates WHERE id=$1 AND result IS NULL`, pending.ID)
			}
			return nil, err
		}
	}
	if pending != nil {
		exhausted := pending.Work.State == "no_cost" || pending.Work.State == "cost_unavailable"
		if pending.Work.Local != nil && plan.CostVersion == pending.Work.Plan.CostVersion &&
			plan.includes(pending.Work.Plan) && (!exhausted || plan.sameCheckpoints(pending.Work.Plan)) {
			if exhausted {
				current, err := nativeWeeklyLocalCurrent(ctx, tx, a, c, pending.Work)
				if err != nil || current {
					return nil, err
				}
				pending.Retry = time.Time{}
			}
			if now.Before(pending.Retry) {
				return nil, nil
			}
			return pending, nil
		}
		if _, err = tx.Exec(ctx, `DELETE FROM native_weekly_estimates WHERE id=$1 AND result IS NULL`, pending.ID); err != nil {
			return nil, err
		}
	}
	job := &nativeWeeklyJob{Work: newNativeLocalWeeklyWork(plan, latest)}
	// Release the plan's queue key when its published usage proof has been replaced.
	if _, err = tx.Exec(ctx, `UPDATE native_weekly_estimates SET
		basis_key=encode(sha256(convert_to(basis_key||':'||id::text,'UTF8')),'hex')
		WHERE account_id=$1 AND basis_key=$2 AND result IS NOT NULL`, a.ID, key); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO native_weekly_estimates(account_id,basis_key,retry_at,work) VALUES($1,$2,$3,$4) RETURNING id::text`,
		a.ID, key, now, job.Work).Scan(&job.ID)
	return job, err
}

// The collector leader owns the estimate checkpoint. Only the daily collector reads upstream usage.
func (s *Service) processNativeWeekly(ctx context.Context, after string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return after, err
	}
	defer release()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return after, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil || !policy.Enabled {
		return after, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil || c.SourceID == nil || !c.CredentialSet || c.reverificationRequired {
		return after, err
	}
	var account string
	err = tx.QueryRow(ctx, `SELECT id FROM managed_accounts
	 WHERE source_id=$1 AND monitoring AND source_mode='passive' AND identity_state='verified'
	 AND snapshot #>> '{account,platform}'='openai' AND snapshot #>> '{account,type}'='oauth'
	 ORDER BY (id <= $2),id LIMIT 1`, c.SourceID, after).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return after, err
	}
	a, err := readManaged(ctx, tx, account, c, false)
	if err != nil {
		return account, err
	}
	job, err := s.prepareNativeWeekly(ctx, tx, a, c, policy)
	if err != nil {
		return account, err
	}
	if err = tx.Commit(ctx); err != nil || job == nil {
		return account, err
	}
	local, err := s.pool.Begin(ctx)
	if err != nil {
		return account, err
	}
	defer local.Rollback(ctx)
	if err = collectNativeWeeklyLocal(ctx, local, a, c, &job.Work); err != nil {
		return account, err
	}
	if err = local.Commit(ctx); err != nil {
		return account, err
	}
	return account, s.saveNativeWeeklyWork(ctx, account, job)
}

func (s *Service) saveNativeWeeklyWork(ctx context.Context, account string, job *nativeWeeklyJob) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	policy, c, a, err := s.estimateEnvironment(ctx, tx, account)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	view, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, now)
	if err != nil {
		return err
	}
	w := &job.Work
	if plan.CostVersion != w.Plan.CostVersion || !plan.includes(w.Plan) || view.State == "source_stale" {
		return tx.Commit(ctx)
	}
	if w.done() {
		current, err := nativeWeeklyLocalCurrent(ctx, tx, a, c, *w)
		if err != nil || !current {
			return err
		}
		job.Result = w.record(a, c, job.ID)
	}
	updated, err := tx.Exec(ctx, `UPDATE native_weekly_estimates SET retry_at=$2,work=$3,result=$4 WHERE id=$1 AND result IS NULL`,
		job.ID, now, w, job.Result)
	if err != nil {
		return err
	}
	if updated.RowsAffected() == 1 && job.Result != nil {
		if err = s.recordNativeEstimateNotification(ctx, tx, c, job); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
