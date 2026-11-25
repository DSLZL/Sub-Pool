package connection

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type usageDayRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (r usageDayRange) contains(at time.Time) bool {
	return !at.Before(r.Start) && at.Before(r.End)
}

func usageDayRangesContain(ranges []usageDayRange, at time.Time) bool {
	return slices.ContainsFunc(ranges, func(r usageDayRange) bool { return r.contains(at) })
}

func usageDayRangesCover(ranges []usageDayRange, target usageDayRange) bool {
	return !target.Start.Before(target.End) || slices.ContainsFunc(ranges, func(r usageDayRange) bool {
		return !target.Start.Before(r.Start) && !target.End.After(r.End)
	})
}

func mergeUsageDayRanges(ranges ...usageDayRange) []usageDayRange {
	slices.SortFunc(ranges, func(a, b usageDayRange) int { return a.Start.Compare(b.Start) })
	var out []usageDayRange
	for _, r := range ranges {
		if !r.Start.Before(r.End) {
			continue
		}
		if last := len(out) - 1; last >= 0 && !r.Start.After(out[last].End) {
			out[last].End = maxTime(out[last].End, r.End)
		} else {
			out = append(out, r)
		}
	}
	return out
}

func (r usageDayRange) intersect(other usageDayRange) usageDayRange {
	end := r.End
	if other.End.Before(end) {
		end = other.End
	}
	return usageDayRange{Start: maxTime(r.Start, other.Start), End: end}
}

func usageCalendarDay(day time.Time) usageDayRange {
	return usageDayRange{Start: day, End: day.AddDate(0, 0, 1)}
}

func (j usageDayJob) proof() (*string, []usageDayRange) {
	if j.state == "complete" && j.published != nil {
		if j.checkpoint.Version == 0 {
			return j.published, []usageDayRange{usageCalendarDay(j.day)}
		}
		return j.published, j.checkpoint.Coverage
	}
	return j.workCapture, j.checkpoint.Coverage
}

type usageDayProof struct {
	Capture string
	Ranges  []usageDayRange
}

const usageLocalPageSize = 1000

func readUsageDayProof(ctx context.Context, tx pgx.Tx, c storedConfig, a storedAccount, day time.Time) (usageDayProof, error) {
	return usageDayCurrentProof(ctx, tx, c, a, day, false)
}

func usageDayCurrentProof(ctx context.Context, tx pgx.Tx, c storedConfig, a storedAccount, day time.Time, lock bool) (usageDayProof, error) {
	if c.SourceID == nil || a.SourceID != *c.SourceID || !a.SourceCurrent || a.IdentityState != "verified" || len(a.fingerprint) == 0 {
		return usageDayProof{}, nil
	}
	lockClause := ""
	if lock {
		lockClause = " FOR SHARE OF j"
	}
	j, err := scanUsageDay(tx.QueryRow(ctx, `SELECT `+usageDayColumns+`
		FROM usage_day_jobs j JOIN usage_captures u ON u.id=CASE WHEN j.state='complete' THEN j.published_capture_id ELSE j.work_capture_id END
		 AND u.account_id=j.account_id AND u.collection_day=j.day
		 AND u.snapshot->>'source_id'=j.source_id AND u.snapshot->>'connection_revision'=j.connection_revision::text
		WHERE j.source_id=$1 AND j.account_id=$2 AND j.day=$3 AND j.fingerprint=$4`+lockClause, a.SourceID, a.ID, day, a.fingerprint))
	if errors.Is(err, pgx.ErrNoRows) {
		return usageDayProof{}, nil
	}
	if err != nil {
		return usageDayProof{}, err
	}
	id, ranges := j.proof()
	if id == nil {
		return usageDayProof{}, nil
	}
	return usageDayProof{Capture: *id, Ranges: ranges}, nil
}

func readUsageDayFacts(ctx context.Context, tx pgx.Tx, capture, user string, after int64) ([]UsageFact, int64, error) {
	rows, err := tx.Query(ctx, `SELECT upstream_id,fact FROM usage_capture_facts
		WHERE capture_id=$1 AND upstream_id>$2
		AND ($3='' OR (fact->>'user_id'=$3 OR fact->>'user_id' IS NULL) AND fact->>'billing_type' IS DISTINCT FROM '1')
		ORDER BY upstream_id LIMIT $4`, capture, after, user, usageLocalPageSize)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	var facts []UsageFact
	for rows.Next() {
		var fact UsageFact
		if err = rows.Scan(&after, &fact); err != nil {
			return nil, after, err
		}
		facts = append(facts, fact)
	}
	return facts, after, rows.Err()
}
