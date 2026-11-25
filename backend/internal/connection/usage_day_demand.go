package connection

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type usageDayDemand struct {
	ConnectionRevision string          `json:"connection_revision"`
	AccountRevision    string          `json:"account_revision"`
	ExpiresAt          time.Time       `json:"expires_at"`
	Ranges             []usageDayRange `json:"ranges"`
}

func (d *usageDayDemand) current(a storedAccount, c storedConfig, now time.Time) bool {
	return d != nil && d.ConnectionRevision == c.Revision && d.AccountRevision == a.Revision && now.Before(d.ExpiresAt)
}

func usageDayScanRanges(ctx context.Context, tx pgx.Tx, policy MonitoringConfig, c storedConfig, a storedAccount, j usageDayJob, now time.Time) ([]usageDayRange, error) {
	if (!j.scheduled || policy.Enabled && policy.UsageEnabled && !j.checkpoint.WeeklyOnly) && usageDayAvailable(a, c) {
		return []usageDayRange{usageCalendarDay(j.day)}, nil
	}
	demand := j.checkpoint.Weekly
	if !j.scheduled || !policy.Enabled || !a.SourceCurrent || !a.ConnectionAvailable || c.reverificationRequired || !a.Monitoring ||
		a.IdentityState != "verified" || len(a.fingerprint) == 0 || !nativeWeeklyAccount(a) || !demand.current(a, c, now) {
		return nil, nil
	}
	verified, err := currentNativePassiveSnapshot(ctx, tx, a, c)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil || !verified {
		return nil, err
	}
	var ranges []usageDayRange
	day := usageCalendarDay(j.day)
	for _, r := range demand.Ranges {
		ranges = append(ranges, r.intersect(day))
	}
	return mergeUsageDayRanges(ranges...), nil
}

func queueWeeklyUsage(ctx context.Context, tx pgx.Tx, c storedConfig, a storedAccount, window usageDayRange, now, expires time.Time, wake bool) error {
	if !window.Start.Before(window.End) || window.End.After(now) || !now.Before(expires) || window.End.Sub(window.Start) > 31*24*time.Hour {
		return nil
	}
	for day := window.Start.UTC().Truncate(24 * time.Hour); day.Before(window.End); day = day.AddDate(0, 0, 1) {
		part := window.intersect(usageCalendarDay(day))
		j, err := scanUsageDay(tx.QueryRow(ctx, `SELECT `+usageDayColumns+`
			FROM usage_day_jobs j WHERE j.source_id=$1 AND j.account_id=$2 AND j.day=$3 FOR UPDATE`, a.SourceID, a.ID, day))
		demand := usageDayDemand{ConnectionRevision: c.Revision, AccountRevision: a.Revision, ExpiresAt: expires, Ranges: []usageDayRange{part}}
		if errors.Is(err, pgx.ErrNoRows) {
			p := usageDayCheckpoint{Weekly: &demand, WeeklyOnly: true}
			_, err = tx.Exec(ctx, `INSERT INTO usage_day_jobs(source_id,account_id,day,connection_revision,fingerprint,scheduled,checkpoint)
				VALUES($1,$2,$3,$4,$5,true,$6) ON CONFLICT DO NOTHING`, a.SourceID, a.ID, day, c.Revision, a.fingerprint, p)
			if err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if bytes.Equal(j.fingerprint, a.fingerprint) {
			capture, verified := j.proof()
			if capture != nil && usageDayRangesCover(verified, part) {
				continue
			}
		}
		if previous := j.checkpoint.Weekly; previous.current(a, c, now) {
			demand.Ranges = mergeUsageDayRanges(append(slices.Clone(previous.Ranges), part)...)
			demand.ExpiresAt = maxTime(previous.ExpiresAt, expires)
			if j.state != "blocked" && j.reason != "SCOPED_READ" &&
				slices.Equal(demand.Ranges, previous.Ranges) && demand.ExpiresAt.Equal(previous.ExpiresAt) {
				continue
			}
		}
		j.checkpoint.Weekly = &demand
		requeue := j.state == "complete" || j.state == "blocked"
		if requeue && !j.scheduled {
			j.checkpoint.WeeklyOnly = true
		}
		_, err = tx.Exec(ctx, `UPDATE usage_day_jobs SET checkpoint=$4,revision=revision+1,
			state=CASE WHEN $5 OR reason='SCOPED_READ' THEN 'queued' ELSE state END,
			reason=CASE WHEN $5 OR reason='SCOPED_READ' THEN 'QUEUED' ELSE reason END,
			scheduled=scheduled OR $5,actor_id=CASE WHEN $5 THEN NULL ELSE actor_id END,
			actor_revision=CASE WHEN $5 THEN NULL ELSE actor_revision END,
			session_digest=CASE WHEN $5 THEN NULL ELSE session_digest END,
			retry_at=CASE WHEN $5 OR reason='SCOPED_READ' OR $6 AND state='partial' THEN LEAST(retry_at,now()) ELSE retry_at END
			WHERE source_id=$1 AND account_id=$2 AND day=$3`, a.SourceID, a.ID, day, j.checkpoint, requeue, wake)
		if err != nil {
			return err
		}
	}
	return nil
}
