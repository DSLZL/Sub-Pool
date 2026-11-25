package connection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/estimation"
)

const nativeWeeklyDayLimit = 100000
const weeklyDate = "2006-01-02"

type nativeWeeklyCost struct {
	Cost       *estimation.Amount `json:"cost"`
	Records    int                `json:"records"`
	Missing    bool               `json:"missing"`
	WalletCost *estimation.Amount `json:"wallet_cost,omitempty"`
	WalletRows int                `json:"wallet_rows,omitempty"`
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
}

type nativeWeeklyDayScan struct {
	Anchor int64              `json:"anchor"`
	Total  int64              `json:"total"`
	Last   int64              `json:"last"`
	Seen   int64              `json:"seen"`
	Digest string             `json:"digest"`
	First  string             `json:"first"`
	Costs  []nativeWeeklyCost `json:"costs"`
}

type nativeWeeklyWork struct {
	Plan           nativeWeeklyPlan    `json:"plan"`
	State          string              `json:"state"`
	Costs          []nativeWeeklyCost  `json:"costs"`
	Tail           []nativeWeeklyCost  `json:"tail,omitempty"`
	Day            time.Time           `json:"day"`
	VerifiedAt     time.Time           `json:"verified_at"`
	Scan           nativeWeeklyDayScan `json:"scan"`
	WalletCapacity *estimation.Amount  `json:"wallet_capacity,omitempty"`
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

func newNativeWeeklyWork(plan nativeWeeklyPlan, previous *nativeWeeklyJob) nativeWeeklyWork {
	w := nativeWeeklyWork{
		Plan: plan, State: "collecting", Costs: make([]nativeWeeklyCost, len(plan.Points)),
		Day: plan.Period.StartAt.Truncate(24 * time.Hour),
	}
	if previous != nil && plan.CostVersion == previous.Work.Plan.CostVersion && len(previous.Work.Plan.Points) < len(plan.Points) && plan.includesCompleted(previous.Work.Plan) {
		// The UTC day of the prior cutoff can contain more usage for the next checkpoint.
		w.Day = previous.Work.Plan.through().At.Truncate(24 * time.Hour)
		copy(w.Costs, previous.Work.Costs)
	}
	return w
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
	_, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, now)
	if err != nil || len(plan.Points) == 0 {
		return nil, err
	}
	key := plan.key()
	pending, latest, err := readNativeWeeklyWork(ctx, tx, a.ID, key)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Work.Plan.CostVersion == plan.CostVersion && len(latest.Work.Plan.Points) == len(plan.Points) && plan.includesCompleted(latest.Work.Plan) {
		if pending != nil && !plan.includes(pending.Work.Plan) {
			_, err = tx.Exec(ctx, `DELETE FROM native_weekly_estimates WHERE id=$1 AND result IS NULL`, pending.ID)
		}
		return nil, err
	}
	if pending != nil {
		exhausted := pending.Work.State == "no_cost" || pending.Work.State == "cost_unavailable"
		if plan.CostVersion == pending.Work.Plan.CostVersion && plan.includes(pending.Work.Plan) && (!exhausted || len(plan.Points) == len(pending.Work.Plan.Points)) {
			if now.Before(pending.Retry) {
				return nil, nil
			}
			return pending, nil
		}
		if _, err = tx.Exec(ctx, `DELETE FROM native_weekly_estimates WHERE id=$1 AND result IS NULL`, pending.ID); err != nil {
			return nil, err
		}
	}
	job := &nativeWeeklyJob{Work: newNativeWeeklyWork(plan, latest)}
	err = tx.QueryRow(ctx, `INSERT INTO native_weekly_estimates(account_id,basis_key,retry_at,work) VALUES($1,$2,$3,$4) RETURNING id::text`,
		a.ID, key, now, job.Work).Scan(&job.ID)
	return job, err
}

// The collector leader owns the checkpoint. No row locks span the native usage reads.
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
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return account, err
	}
	network, cancelNetwork := context.WithTimeout(ctx, 4*time.Second)
	defer cancelNetwork()
	w := &job.Work
	w.State = "collecting"
	for i := 0; i < 4 && !w.done() && network.Err() == nil; i++ {
		state, retry := s.nativeWeeklyPage(network, c.BaseURL, kind, secret, a.UpstreamID, w)
		if state != "" {
			w.State, job.Retry = state, retry
			break
		}
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
	_, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, now)
	if err != nil {
		return err
	}
	w := &job.Work
	if plan.CostVersion != w.Plan.CostVersion || !plan.includes(w.Plan) {
		return tx.Commit(ctx)
	}
	retry := now
	if w.State != "collecting" {
		retry = maxTime(now.Add(time.Minute), job.Retry)
	}
	if w.done() {
		job.Result = w.record(a, c, job.ID)
		if job.Result == nil {
			work := newNativeWeeklyWork(w.Plan, nil)
			work.State = w.State
			*w, retry = work, now.Add(5*time.Minute)
		}
	}
	updated, err := tx.Exec(ctx, `UPDATE native_weekly_estimates SET retry_at=$2,work=$3,result=$4 WHERE id=$1 AND result IS NULL`,
		job.ID, retry, w, job.Result)
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

func (s *Service) nativeWeeklyPage(ctx context.Context, base, kind, secret, account string, work *nativeWeeklyWork) (string, time.Time) {
	scan := &work.Scan
	page, size, order := int(scan.Seen/usagePageSize)+1, usagePageSize, "asc"
	if scan.Anchor == 0 {
		page, size, order = 1, 1, "desc"
	}
	date := work.Day.Format(weeklyDate)
	query := url.Values{
		"account_id": {account}, "start_date": {date}, "end_date": {date}, "timezone": {"UTC"},
		"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}, "exact_total": {"true"},
		"sort_by": {"id"}, "sort_order": {order},
	}
	body, check := s.upstream.get(ctx, base, "/api/v1/admin/usage?"+query.Encode(), kind, secret, "usage.read", true)
	if check.State != "available" {
		if check.State == "denied" {
			return "usage_denied", time.Time{}
		}
		if check.retryAfter != nil {
			return "usage_rate_limited", *check.retryAfter
		}
		return "usage_unavailable", time.Time{}
	}
	var data pageData
	if decode(body, &data) != nil || data.Items == nil || data.Total == nil || *data.Total < 0 ||
		data.Page == nil || *data.Page != page || data.PageSize == nil || *data.PageSize != size || len(data.Items) > size {
		return "usage_invalid", time.Time{}
	}
	if scan.Anchor == 0 {
		if *data.Total > nativeWeeklyDayLimit {
			return "usage_limit", time.Time{}
		}
		if *data.Total == 0 && len(data.Items) == 0 {
			work.finishWeeklyDay(nil, time.Now().UTC())
			return "", time.Time{}
		}
		if *data.Total == 0 || len(data.Items) != 1 {
			return "pagination_changed", time.Time{}
		}
		row, valid := weeklyUsageIdentity(data.Items[0], account, work.Day)
		if !valid {
			return "usage_invalid", time.Time{}
		}
		*scan = nativeWeeklyDayScan{Anchor: row.ID, Total: *data.Total, Costs: make([]nativeWeeklyCost, len(work.Costs))}
		return "", time.Time{}
	}
	state := work.consumeWeeklyPage(data.Items, account, time.Now().UTC())
	if state == "pagination_changed" {
		work.Scan = nativeWeeklyDayScan{}
	}
	return state, time.Time{}
}

type weeklyUsageRow struct {
	ID     int64
	At     time.Time
	Fields map[string]json.RawMessage
}

func weeklyUsageIdentity(raw json.RawMessage, account string, day time.Time) (weeklyUsageRow, bool) {
	fields, valid := usageObject(raw)
	if !valid {
		return weeklyUsageRow{}, false
	}
	native, valid := usageID(fields["account_id"], true)
	if !valid || *native != account {
		return weeklyUsageRow{}, false
	}
	id, valid := usageID(fields["id"], true)
	if !valid {
		return weeklyUsageRow{}, false
	}
	number, err := strconv.ParseInt(*id, 10, 64)
	at, valid := snapshotTime(fields["created_at"])
	if err != nil || number <= 0 || !valid || at == nil || at.Before(day) || !at.Before(day.AddDate(0, 0, 1)) {
		return weeklyUsageRow{}, false
	}
	return weeklyUsageRow{number, *at, fields}, true
}

func (w *nativeWeeklyWork) consumeWeeklyPage(items []json.RawMessage, account string, now time.Time) string {
	scan := &w.Scan
	seen, last := scan.Seen, scan.Last
	costs := make([]nativeWeeklyCost, len(w.Costs))
	digest := sha256.New()
	fmt.Fprintln(digest, scan.Digest)
	points := w.Plan.Points
	for _, item := range items {
		row, valid := weeklyUsageIdentity(item, account, w.Day)
		if !valid {
			return "usage_invalid"
		}
		if row.ID <= last || row.ID > scan.Anchor {
			return "pagination_changed"
		}
		seen, last = seen+1, row.ID
		i := sort.Search(len(points), func(i int) bool { return points[i].At.After(row.At) })
		value, actual := "-", "-"
		if i < len(points) && !row.At.Before(w.Plan.Period.StartAt) {
			part := &costs[i]
			part.Records++
			standard, ok := usageNumber(row.Fields["total_cost"], false)
			if ok && standard != nil {
				value = *standard
				cost, _ := new(big.Rat).SetString(value)
				part.add(nativeWeeklyCost{Cost: estimation.Exact(cost)})
			} else {
				part.Missing = true
			}
			var billingType int8
			// Native balance and subscription billing both consume the recorded actual cost.
			if raw := row.Fields["billing_type"]; decode(raw, &billingType) == nil && (billingType == 0 || billingType == 1) {
				if amount, ok := usageNumber(row.Fields["actual_cost"], false); ok && amount != nil {
					actual = *amount
					cost, _ := new(big.Rat).SetString(actual)
					part.add(nativeWeeklyCost{WalletCost: estimation.Exact(cost), WalletRows: 1})
				}
			}
		}
		fmt.Fprintln(digest, row.ID, row.At.Format(time.RFC3339Nano), value, actual)
		if last == scan.Anchor {
			break
		}
	}
	if seen > scan.Total || last == scan.Anchor && seen != scan.Total ||
		last != scan.Anchor && (len(items) != usagePageSize || seen >= scan.Total) {
		return "pagination_changed"
	}
	scan.Seen, scan.Last, scan.Digest = seen, last, fmt.Sprintf("%x", digest.Sum(nil))
	for i, cost := range costs {
		scan.Costs[i].add(cost)
	}
	if last != scan.Anchor {
		return ""
	}
	if scan.First == "" {
		// An independent pass also checks offset changes that preserve the boundary count.
		*scan = nativeWeeklyDayScan{Anchor: scan.Anchor, Total: scan.Total, First: scan.Digest, Costs: make([]nativeWeeklyCost, len(costs))}
		return ""
	}
	if scan.Digest != scan.First {
		return "pagination_changed"
	}
	w.finishWeeklyDay(scan.Costs, now)
	return ""
}

func (w *nativeWeeklyWork) finishWeeklyDay(costs []nativeWeeklyCost, now time.Time) {
	nextDay := w.Day.AddDate(0, 0, 1)
	if w.Plan.through().At.Before(nextDay) {
		w.Tail = costs
	} else {
		for i, cost := range costs {
			w.Costs[i].add(cost)
		}
	}
	w.Day = nextDay
	w.VerifiedAt = now
	w.Scan = nativeWeeklyDayScan{}
}
