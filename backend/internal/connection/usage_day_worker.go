package connection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

func (s *Service) processUsageDay(ctx context.Context, leader *pgxpool.Conn) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	job, config, account, policy, err := s.nextUsageDay(ctx, leader)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	kind, secret, err := s.decrypt(config)
	if err != nil {
		return false, err
	}
	rebind := job.connection != config.Revision || !bytes.Equal(job.fingerprint, account.fingerprint)
	now := time.Now().UTC()
	read, stop := context.WithTimeout(ctx, 7*time.Second)
	batch, err := s.scanUsageDayBatch(read, job, config, account, kind, secret, time.Duration(policy.UsageIntervalSeconds)*time.Second, now, rebind)
	stop()
	if err != nil {
		return false, err
	}
	return true, s.saveUsageDayBatch(ctx, leader, job, config, account, batch, rebind, now)
}

func (s *Service) saveUsageDayBatch(ctx context.Context, leader *pgxpool.Conn, job usageDayJob, expected storedConfig, prior storedAccount,
	batch usageDayBatch, rebind bool, started time.Time) error {
	tx, err := leader.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return err
	}
	config, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	if config.SourceID == nil || *config.SourceID != job.source || config.Revision != expected.Revision {
		return nil
	}
	account, err := readManaged(ctx, tx, job.account, config, false)
	if err != nil {
		return err
	}
	if account.Revision != prior.Revision || !bytes.Equal(account.fingerprint, prior.fingerprint) {
		return nil
	}
	ranges, err := usageDayScanRanges(ctx, tx, policy, config, account, job, time.Now().UTC())
	if err != nil {
		return err
	}
	if !slices.Equal(ranges, job.ranges) {
		return nil
	}
	var authorityErr error
	if !job.scheduled {
		authorityErr = s.auth.AuthorizeQueuedAdmin(ctx, tx, job.digest, *job.actor, *job.actorRevision)
		if authorityErr != nil && !errors.Is(authorityErr, auth.ErrForbidden) && !errors.Is(authorityErr, auth.ErrUnauthorized) && !errors.Is(authorityErr, auth.ErrPasswordChangeRequired) {
			return authorityErr
		}
	}
	current, err := scanUsageDay(tx.QueryRow(ctx, `SELECT `+usageDayColumns+` FROM usage_day_jobs j
		WHERE j.source_id=$1 AND j.account_id=$2 AND j.day=$3 AND j.revision=$4 FOR UPDATE`, job.source, job.account, job.day, job.revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if authorityErr != nil {
		_, err = tx.Exec(ctx, `UPDATE usage_day_jobs SET state='blocked',reason='AUTHORITY_CHANGED',revision=revision+1,updated_at=now()
			WHERE source_id=$1 AND account_id=$2 AND day=$3`, job.source, job.account, job.day)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if batch.reset && current.workCapture != nil {
		if err := discardUsageWork(ctx, tx, current); err != nil {
			return err
		}
		current.workCapture = nil
	}
	now := time.Now().UTC()
	complete := batch.state == "complete"
	if current.workCapture != nil || len(batch.facts) != 0 || len(batch.checkpoint.Coverage) != 0 || complete {
		if err := saveUsageDayCapture(ctx, tx, &current, account, config, batch, started, now); err != nil {
			return err
		}
	}
	var published *string
	var completed *time.Time
	if complete {
		published, completed = current.workCapture, &now
		current.workCapture = nil
		event := audit.Entry{ActorID: current.actor, Action: "usage.days.complete", Outcome: "succeeded",
			TargetType: "account", TargetID: account.ID, SourceID: &account.SourceID, Revision: &config.Revision}
		if current.scheduled {
			task := "usage.days.worker"
			event.TaskID = &task
		}
		if _, err = audit.Record(ctx, tx, event); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE usage_day_jobs SET connection_revision=$4::bigint,fingerprint=$5,state=$6,reason=$7,retry_at=$8,
		updated_at=$9,checkpoint=$10,work_capture_id=$11::bigint,
		published_capture_id=CASE WHEN $12 THEN $13::bigint WHEN $15 THEN NULL ELSE published_capture_id END,
		completed_at=CASE WHEN $12 THEN $14 WHEN $15 THEN NULL ELSE completed_at END,revision=revision+1
		WHERE source_id=$1 AND account_id=$2 AND day=$3`,
		job.source, job.account, job.day, config.Revision, account.fingerprint, batch.state, batch.reason, batch.retryAt,
		now, batch.checkpoint, current.workCapture, complete, published, completed, rebind)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func discardUsageWork(ctx context.Context, tx pgx.Tx, job usageDayJob) error {
	var guardedID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM usage_captures
		WHERE id=$1 AND account_id=$2 AND collection_day=$3 AND staging FOR UPDATE`,
		*job.workCapture, job.account, job.day).Scan(&guardedID); err != nil {
		return fmt.Errorf("invalid working usage capture: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE usage_day_jobs SET work_capture_id=NULL WHERE source_id=$1 AND account_id=$2 AND day=$3`, job.source, job.account, job.day); err != nil {
		return err
	}
	if len(job.checkpoint.Coverage) != 0 || job.checkpoint.Version == 0 && job.state == "partial" {
		_, err := tx.Exec(ctx, `UPDATE usage_captures SET staging=false WHERE id=$1 AND staging`, guardedID)
		return err
	}
	for _, query := range []string{
		`DELETE FROM usage_attributions WHERE capture_id=$1::bigint`,
		`DELETE FROM usage_capture_facts WHERE capture_id=$1::bigint`,
		`DELETE FROM usage_captures WHERE id=$1::bigint AND staging`,
	} {
		if _, err := tx.Exec(ctx, query, guardedID); err != nil {
			return err
		}
	}
	return nil
}

func saveUsageDayCapture(ctx context.Context, tx pgx.Tx, job *usageDayJob, account storedAccount, config storedConfig,
	batch usageDayBatch, started, now time.Time) error {
	checkpoint := batch.checkpoint
	total := strconv.FormatInt(checkpoint.Total, 10)
	count := strconv.FormatInt(checkpoint.Stored, 10)
	state := "partial"
	if batch.state == "complete" {
		state = "exhausted"
	}
	capture := UsageCapture{
		AccountID: account.ID, SourceID: account.SourceID, ConnectionRevision: config.Revision,
		AccountRevision: account.Revision, UpstreamAccountID: account.UpstreamID, CollectionDay: job.day.Format(weeklyDate), StartAt: job.day,
		EndAt: job.day.AddDate(0, 0, 1), StartedAt: started, CapturedAt: now, SourceCurrent: true,
		ConnectionCurrent: true, State: state, Read: batch.read, TimestampBasis: "native_created_at", Currency: "USD",
		ScannedCount: strconv.FormatInt(checkpoint.Seen, 10), ObservedCount: count, UpstreamDayTotal: &total, PagesRead: int(max(1, (checkpoint.Seen+usagePageSize-1)/usagePageSize)),
		Limitations: []string{"RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "PARTICIPANT_MAPPING_UNAVAILABLE", "REQUEST_START_TIME_UNAVAILABLE"},
	}
	if job.workCapture == nil {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot,collection_day,staging)
			VALUES($1,$2,$3,$4,$5,true) RETURNING id::text`, account.ID, job.actor, now, capture, job.day).Scan(&id)
		if err != nil {
			return err
		}
		job.workCapture = &id
	}
	id, err := strconv.ParseInt(*job.workCapture, 10, 64)
	if err != nil {
		return err
	}
	if len(batch.facts) > 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"usage_capture_facts"}, []string{"capture_id", "upstream_id", "fact"},
			pgx.CopyFromSlice(len(batch.facts), func(i int) ([]any, error) {
				logID, err := strconv.ParseInt(batch.facts[i].ID, 10, 64)
				return []any{id, logID, batch.facts[i]}, err
			}))
		if err != nil {
			return err
		}
		if err := attributeUsage(ctx, tx, *job.workCapture, account.SourceID, batch.facts); err != nil {
			return err
		}
	}
	update, err := tx.Exec(ctx, `UPDATE usage_captures SET snapshot=$2,captured_at=$3,staging=$4
		WHERE id=$1 AND staging AND account_id=$5 AND collection_day=$6`,
		id, capture, now, batch.state != "complete", account.ID, job.day)
	if err == nil && update.RowsAffected() != 1 {
		return errors.New("working usage capture is no longer valid")
	}
	return err
}
