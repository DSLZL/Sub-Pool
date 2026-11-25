package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type UsageDay struct {
	Day         string     `json:"day"`
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	Records     string     `json:"records"`
	Total       *string    `json:"total"`
	NextAttempt *time.Time `json:"next_attempt_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type UsageDays struct {
	Days []UsageDay `json:"days"`
}

type usageDayJob struct {
	source, account, connection string
	fingerprint, digest         []byte
	actor, actorRevision        *string
	revision                    int64
	day                         time.Time
	scheduled                   bool
	workCapture                 *string
	checkpoint                  usageDayCheckpoint
}

const usageDayColumns = `j.source_id,j.account_id,j.connection_revision::text,j.fingerprint,j.session_digest,
	j.actor_id,j.actor_revision::text,j.revision,j.day,j.scheduled,j.work_capture_id::text,j.checkpoint`

func scanUsageDay(row pgx.Row) (usageDayJob, error) {
	var j usageDayJob
	err := row.Scan(&j.source, &j.account, &j.connection, &j.fingerprint, &j.digest,
		&j.actor, &j.actorRevision, &j.revision, &j.day, &j.scheduled, &j.workCapture, &j.checkpoint)
	return j, err
}

func usageDayAvailable(a storedAccount, c storedConfig) bool {
	return a.SourceCurrent && a.ConnectionAvailable && a.Monitoring && a.IdentityState == "verified" &&
		len(a.fingerprint) != 0 && a.ObservedConnectionRevision == c.Revision && a.Read.State == "available"
}

func (s *Service) UsageDayStatus(ctx context.Context, token string, input UsageCaptureInput) (UsageDays, error) {
	return s.usageDays(ctx, token, input, false)
}

func (s *Service) QueueUsageDays(ctx context.Context, token string, input UsageCaptureInput) (UsageDays, error) {
	return s.usageDays(ctx, token, input, true)
}

func (s *Service) usageDays(ctx context.Context, token string, input UsageCaptureInput, enqueue bool) (UsageDays, error) {
	result := UsageDays{Days: []UsageDay{}}
	start, end, valid := usageRange(input.StartAt, input.EndAt)
	first, last := start.Truncate(24*time.Hour), end.Add(-time.Nanosecond).Truncate(24*time.Hour)
	now := time.Now().UTC()
	if !valid || !validAccountRead(input.AccountReadInput) || last.After(now.Truncate(24*time.Hour)) {
		return result, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return result, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return result, err
	}
	a, err := readManaged(ctx, tx, input.ID, c, false)
	if err != nil {
		return result, err
	}
	if !accountReadMatches(input.AccountReadInput, a, c) || !a.SourceCurrent {
		return result, auth.ErrConflict
	}
	auto := policy.Enabled && policy.UsageEnabled
	if enqueue {
		if !usageDayAvailable(a, c) {
			return result, auth.ErrConflict
		}
		digest := sha256.Sum256([]byte(token))
		admitted, err := tx.Exec(ctx, `INSERT INTO usage_day_jobs AS j
			(source_id,account_id,day,connection_revision,fingerprint,scheduled,actor_id,actor_revision,session_digest)
			SELECT $1,$2,day::date,$5::bigint,$6,false,$7,$8::bigint,$9 FROM generate_series($3::date::timestamp,$4::date::timestamp,'1 day') day
			ON CONFLICT(source_id,account_id,day) DO UPDATE SET scheduled=false,actor_id=excluded.actor_id,
				actor_revision=excluded.actor_revision,session_digest=excluded.session_digest,revision=j.revision+1,
				state='queued',reason='QUEUED',retry_at=greatest(j.retry_at,now()),updated_at=now()
			WHERE j.state='blocked' OR j.scheduled AND NOT $10 AND j.state <> 'complete' OR
				j.state='complete' AND (j.connection_revision<>excluded.connection_revision OR j.fingerprint<>excluded.fingerprint)`,
			a.SourceID, a.ID, first, last, c.Revision, a.fingerprint, actor.ID, actor.Revision, digest[:], auto)
		if err != nil {
			return result, err
		}
		if admitted.RowsAffected() != 0 {
			if _, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "usage.days.queue", Outcome: "succeeded",
				TargetType: "account", TargetID: a.ID, SourceID: &a.SourceID, Revision: &c.Revision}); err != nil {
				return result, err
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT day::text,state,reason,retry_at,completed_at,checkpoint,connection_revision::text,fingerprint,scheduled
		FROM usage_day_jobs WHERE source_id=$1 AND account_id=$2 AND day BETWEEN $3 AND $4 ORDER BY day`, a.SourceID, a.ID, first, last)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var status UsageDay
		var checkpoint usageDayCheckpoint
		var revision string
		var fingerprint []byte
		var retry time.Time
		var scheduled bool
		if err = rows.Scan(&status.Day, &status.State, &status.Reason, &retry, &status.CompletedAt, &checkpoint, &revision, &fingerprint, &scheduled); err != nil {
			rows.Close()
			return result, err
		}
		status.Records = strconv.FormatInt(checkpoint.Stored, 10)
		if checkpoint.Anchored {
			total := strconv.FormatInt(checkpoint.Total, 10)
			status.Total = &total
		}
		status.CompletedAt = utc(status.CompletedAt)
		switch {
		case revision != c.Revision || !bytes.Equal(fingerprint, a.fingerprint):
			status.State, status.Reason = "blocked", "BINDING_CHANGED"
		case status.State != "complete" && (!usageDayAvailable(a, c) || scheduled && !auto):
			status.State, status.Reason = "blocked", "COLLECTION_DISABLED"
		case status.State != "complete" && status.State != "blocked":
			status.NextAttempt = utc(&retry)
		}
		result.Days = append(result.Days, status)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, s.finish(ctx, tx, token, true)
}

func scheduleUsageDays(ctx context.Context, tx pgx.Tx, policy MonitoringConfig, c storedConfig, now time.Time) error {
	if c.SourceID == nil || !c.CredentialSet || !policy.Enabled || !policy.UsageEnabled || policy.UsageEnabledAt == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO usage_day_jobs AS j
		(source_id,account_id,day,connection_revision,fingerprint,scheduled)
		SELECT a.source_id,a.id,d.day,$2::bigint,a.resource_fingerprint,true
		FROM managed_accounts a
		CROSS JOIN LATERAL (SELECT max(day) AS day FROM usage_day_jobs
			WHERE source_id=a.source_id AND account_id=a.id AND scheduled) prior
		CROSS JOIN LATERAL (SELECT greatest((a.created_at AT TIME ZONE 'UTC')::date,$3::date,prior.day+1) AS day) d
		WHERE a.source_id=$1 AND a.monitoring AND a.identity_state='verified' AND a.resource_fingerprint IS NOT NULL
			AND a.observed_connection_revision=$2::bigint AND d.day <= $4::date
		ORDER BY d.day,a.id LIMIT 32
		ON CONFLICT(source_id,account_id,day) DO UPDATE SET scheduled=true,actor_id=NULL,actor_revision=NULL,
			session_digest=NULL,revision=j.revision+1,
			state=CASE WHEN j.state='blocked' THEN 'queued' ELSE j.state END,
			reason=CASE WHEN j.state='blocked' THEN 'QUEUED' ELSE j.reason END WHERE NOT j.scheduled`,
		*c.SourceID, c.Revision, policy.UsageEnabledAt.UTC().Truncate(24*time.Hour), now.Truncate(24*time.Hour))
	return err
}

func (s *Service) nextUsageDay(ctx context.Context, leader *pgxpool.Conn) (usageDayJob, storedConfig, storedAccount, MonitoringConfig, error) {
	var j usageDayJob
	var c storedConfig
	var a storedAccount
	tx, err := leader.Begin(ctx)
	if err != nil {
		return j, c, a, MonitoringConfig{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return j, c, a, policy, err
	}
	c, err = s.config(ctx, tx, false)
	if err != nil {
		return j, c, a, policy, err
	}
	if c.SourceID == nil || !c.CredentialSet || !c.SecretStorageAvailable {
		return j, c, a, policy, pgx.ErrNoRows
	}
	now := time.Now().UTC()
	if err = scheduleUsageDays(ctx, tx, policy, c, now); err != nil {
		return j, c, a, policy, err
	}
	j, err = scanUsageDay(tx.QueryRow(ctx, `SELECT `+usageDayColumns+` FROM usage_day_jobs j
		JOIN managed_accounts a ON a.id=j.account_id
		WHERE j.source_id=$1 AND j.state NOT IN ('complete','blocked') AND (NOT j.scheduled OR $2)
			AND a.monitoring AND a.identity_state='verified' AND a.read_check->>'state'='available'
			AND a.observed_connection_revision=$3::bigint
			AND (j.retry_at<=clock_timestamp() OR j.state='partial' AND j.day<$4::date)
		ORDER BY j.updated_at,j.day,j.account_id LIMIT 1`, *c.SourceID, policy.Enabled && policy.UsageEnabled, c.Revision, now.Truncate(24*time.Hour)))
	if errors.Is(err, pgx.ErrNoRows) {
		if commit := tx.Commit(ctx); commit != nil {
			err = commit
		}
		return j, c, a, policy, err
	}
	if err != nil {
		return j, c, a, policy, err
	}
	a, err = readManaged(ctx, tx, j.account, c, false)
	if err != nil {
		return j, c, a, policy, err
	}
	if !usageDayAvailable(a, c) {
		return j, c, a, policy, pgx.ErrNoRows
	}
	if !j.scheduled {
		err = s.auth.AuthorizeQueuedAdmin(ctx, tx, j.digest, *j.actor, *j.actorRevision)
		if err != nil {
			if !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, auth.ErrUnauthorized) && !errors.Is(err, auth.ErrPasswordChangeRequired) {
				return j, c, a, policy, err
			}
			_, err = tx.Exec(ctx, `UPDATE usage_day_jobs SET state='blocked',reason='AUTHORITY_CHANGED',revision=revision+1,updated_at=now()
				WHERE source_id=$1 AND account_id=$2 AND day=$3 AND revision=$4`, j.source, j.account, j.day, j.revision)
			if err == nil {
				err = tx.Commit(ctx)
			}
			if err == nil {
				err = pgx.ErrNoRows
			}
			return j, c, a, policy, err
		}
	}
	return j, c, a, policy, tx.Commit(ctx)
}
