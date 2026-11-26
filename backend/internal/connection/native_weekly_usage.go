package connection

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/estimation"
)

type nativeWeeklyLocalDay struct {
	Capture string             `json:"capture"`
	Cursor  int64              `json:"cursor"`
	Scanned int64              `json:"scanned"`
	Costs   []nativeWeeklyCost `json:"costs"`
	Done    bool               `json:"done"`
}

func newNativeLocalWeeklyWork(plan nativeWeeklyPlan, latest *nativeWeeklyJob) nativeWeeklyWork {
	w := nativeWeeklyWork{
		Plan: plan, State: "collecting", Day: plan.Period.StartAt.Truncate(24 * time.Hour),
		Costs: make([]nativeWeeklyCost, len(plan.Points)), Local: map[string]nativeWeeklyLocalDay{},
	}
	if latest == nil || latest.Work.Plan.CostVersion != plan.CostVersion || !plan.includesCompleted(latest.Work.Plan) {
		return w
	}
	old := latest.Work.Plan.Points
	if len(old) > len(plan.Points) || !slices.EqualFunc(plan.Points[:len(old)], old, sameWeeklyPoint) {
		return w
	}
	// A later checkpoint can add facts to the day containing the previous cutoff.
	oldCutoff := latest.Work.Plan.through().At
	oldEnd := oldCutoff.Truncate(24 * time.Hour).Format(weeklyDate)
	sameCutoff := plan.through().At.Equal(oldCutoff)
	for day, prior := range latest.Work.Local {
		if prior.Done && (day < oldEnd || sameCutoff) {
			costs := make([]nativeWeeklyCost, len(plan.Points))
			copy(costs, prior.Costs)
			prior.Costs = costs
			w.Local[day] = prior
		}
	}
	return w
}

func readVerifiedNativeWeeklyWork(ctx context.Context, tx pgx.Tx, account storedAccount, c storedConfig, key string) (*nativeWeeklyJob, *nativeWeeklyJob, error) {
	pending, latest, err := readNativeWeeklyWork(ctx, tx, account.ID, key)
	if err != nil || latest == nil {
		return pending, latest, err
	}
	current, err := nativeWeeklyLocalCurrent(ctx, tx, account, c, latest.Work)
	if !current {
		latest = nil
	}
	return pending, latest, err
}

func readNativeWeeklyResult(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, plan nativeWeeklyPlan, trusted, wallet bool) (*nativeWeeklyJob, error) {
	if len(plan.Points) == 0 {
		return nil, nil
	}
	// Weekly evidence stores UTC RFC3339Nano. The sort key omits Z so subsecond points sort after the whole second.
	const observed = "rtrim(result #>> '{observation,source_at}','Z') COLLATE \"C\""
	var before, id *string
	for {
		candidate, err := readPublishedNativeEstimate(tx.QueryRow(ctx, `SELECT id::text,work,result FROM native_weekly_estimates
		 WHERE account_id=$1 AND result->>'source_id'=$2 AND result->'cumulative' IS NOT NULL
		 AND result #>> '{period,start_at}'=$3 AND result #>> '{period,end_at}'=$4
		 AND ($5::text IS NULL OR (`+observed+`,native_weekly_estimates.id)<($5,$6::bigint))
		 AND (NOT $7 OR result #>> '{cumulative,confidence}'='trusted')
		 AND (NOT $8 OR work->>'wallet_capacity' IS NOT NULL)
		 ORDER BY `+observed+` DESC,native_weekly_estimates.id DESC LIMIT 1`,
			a.ID, a.SourceID, plan.Period.StartAt.UTC().Format(time.RFC3339Nano), plan.Period.EndAt.UTC().Format(time.RFC3339Nano),
			before, id, trusted, wallet))
		if err != nil || candidate == nil {
			return candidate, err
		}
		result, work := candidate.Result, candidate.Work
		cursor := strings.TrimSuffix(result.Observation.At.UTC().Format(time.RFC3339Nano), "Z")
		before, id = &cursor, &candidate.ID
		if work.Plan.CostVersion != plan.CostVersion || !plan.includesCompleted(work.Plan) ||
			result.Cumulative == nil || !sameWeeklyPoint(result.Observation, work.Plan.through()) {
			continue
		}
		if wallet {
			capacity, valid := work.WalletCapacity.Rational()
			if !valid || capacity.Sign() <= 0 {
				continue
			}
		}
		current, err := nativeWeeklyLocalCurrent(ctx, tx, a, c, work)
		if err != nil {
			return nil, err
		}
		if current {
			return candidate, nil
		}
	}
}

func nativeWeeklyLocalCurrent(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, w nativeWeeklyWork) (bool, error) {
	if len(w.Local) == 0 || w.Plan.Period == nil || len(w.Plan.Points) == 0 || !w.done() {
		return false, nil
	}
	window := usageDayRange{Start: w.Plan.Period.StartAt, End: w.Plan.through().At}
	for day := window.Start.Truncate(24 * time.Hour); day.Before(window.End); day = day.AddDate(0, 0, 1) {
		part := window.intersect(usageCalendarDay(day))
		input := w.Local[day.Format(weeklyDate)]
		if !input.Done {
			return false, nil
		}
		proof, err := readUsageDayProof(ctx, tx, c, a, day)
		if err != nil || proof.Capture != input.Capture || !usageDayRangesCover(proof.Ranges, part) {
			return false, err
		}
	}
	return true, nil
}

func collectNativeWeeklyLocal(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, w *nativeWeeklyWork) error {
	w.State = "collecting"
	window := usageDayRange{Start: w.Plan.Period.StartAt, End: w.Plan.through().At}
	pages := 4
	for day := window.Start.Truncate(24 * time.Hour); day.Before(window.End); day = day.AddDate(0, 0, 1) {
		w.Day = day
		date := day.Format(weeklyDate)
		part := window.intersect(usageCalendarDay(day))
		proof, err := readUsageDayProof(ctx, tx, c, a, day)
		if err != nil {
			return err
		}
		if proof.Capture == "" || !usageDayRangesCover(proof.Ranges, part) {
			return readNativeWeeklyUsageProgress(ctx, tx, a, day, w)
		}
		input := w.Local[date]
		if input.Capture != proof.Capture {
			input = nativeWeeklyLocalDay{Capture: proof.Capture, Costs: make([]nativeWeeklyCost, len(w.Plan.Points))}
		}
		for !input.Done && pages > 0 {
			facts, after, err := readUsageDayFacts(ctx, tx, proof.Capture, "", input.Cursor)
			if err != nil {
				return err
			}
			for _, fact := range facts {
				if part.contains(fact.CreatedAt) {
					i := sort.Search(len(w.Plan.Points), func(i int) bool { return fact.CreatedAt.Before(w.Plan.Points[i].At) })
					if i < len(input.Costs) {
						input.Costs[i].add(nativeWeeklyFactCost(fact))
					}
				}
			}
			input.Cursor = after
			input.Scanned += int64(len(facts))
			input.Done = len(facts) < usageLocalPageSize
			pages--
		}
		w.Local[date] = input
		w.Scan = nativeWeeklyDayScan{Seen: input.Scanned, Total: input.Scanned}
		if !input.Done {
			return nil
		}
	}
	w.Day = window.End
	w.VerifiedAt = time.Now().UTC()
	clear(w.Costs)
	w.Tail = nil
	for _, input := range w.Local {
		for i, cost := range input.Costs {
			w.Costs[i].add(cost)
		}
	}
	return nil
}

func readNativeWeeklyUsageProgress(ctx context.Context, tx pgx.Tx, a storedAccount, day time.Time, w *nativeWeeklyWork) error {
	w.Scan = nativeWeeklyDayScan{}
	j, err := scanUsageDay(tx.QueryRow(ctx, `SELECT `+usageDayColumns+`
		FROM usage_day_jobs j WHERE j.source_id=$1 AND j.account_id=$2 AND j.day=$3 AND j.fingerprint=$4`,
		a.SourceID, a.ID, day, a.fingerprint))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	w.Scan.Seen, w.Scan.Total = j.checkpoint.Seen, j.checkpoint.Total
	switch j.reason {
	case "UNAUTHORIZED", "FORBIDDEN":
		w.State = "usage_denied"
	case "RATE_LIMITED":
		w.State = "usage_rate_limited"
	case "PAGINATION_CHANGED":
		w.State = "pagination_changed"
	case "INVALID_RESPONSE":
		w.State = "usage_invalid"
	}
	return nil
}

func nativeWeeklyFactCost(fact UsageFact) nativeWeeklyCost {
	out := nativeWeeklyCost{Records: 1, Missing: true}
	if text := fact.Metrics["total_cost"]; text != nil {
		if total, ok := new(big.Rat).SetString(*text); ok && total.Sign() >= 0 {
			out.Cost, out.Missing = estimation.Exact(total), false
		}
	}
	if text := fact.Metrics["actual_cost"]; text != nil && (fact.BillingCandidate == "wallet" || fact.BillingCandidate == "subscription") {
		if actual, ok := new(big.Rat).SetString(*text); ok && actual.Sign() >= 0 {
			out.WalletCost, out.WalletRows = estimation.Exact(actual), 1
		}
	}
	return out
}
