package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/notifications"
)

const collectorLock int64 = 0x53503241434f4c4c

func collectorPause(ctx context.Context) bool {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// RunCollector owns one database session lock; browser lifetime never drives collection.
func (s *Service) RunCollector(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			if !collectorPause(ctx) {
				return
			}
			continue
		}
		var leader bool
		err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, collectorLock).Scan(&leader)
		if err == nil && leader {
			err = s.collectorLeader(ctx, conn)
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if leader {
			if _, unlockErr := conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, collectorLock); unlockErr != nil {
				_ = conn.Conn().Close(cleanup)
			}
		}
		cancel()
		conn.Release()
		if err != nil && ctx.Err() == nil {
			slog.Warn("collection worker cycle unavailable; inspect monitoring state")
		}
		if !collectorPause(ctx) {
			return
		}
	}
}

func (s *Service) collectorLeader(ctx context.Context, conn *pgxpool.Conn) error {
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return err
	}
	err = recoverCollections(ctx, conn)
	if err == nil {
		err = recoverMaintenance(ctx, conn)
	}
	release()
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		processed, err := s.collectorCycle(ctx, conn)
		if err != nil {
			return err
		}
		if !processed && !collectorPause(ctx) {
			return nil
		}
	}
	return nil
}

func (s *Service) collectorCycle(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if _, err = conn.Exec(ctx, `UPDATE monitoring_worker SET heartbeat_at=$1 WHERE singleton`, time.Now().UTC()); err != nil {
		return false, err
	}
	if err = s.scheduleCollections(ctx, conn); err != nil {
		return false, err
	}
	if _, err = s.processMaintenance(ctx, conn); err != nil {
		return false, err
	}
	return s.processCollection(ctx, conn)
}

func recoverCollections(ctx context.Context, conn *pgxpool.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE collection_jobs SET state='interrupted',reason='WORKER_INTERRUPTED',finished_at=$1 WHERE state='running' RETURNING `+jobColumns, time.Now().UTC())
	if err != nil {
		return err
	}
	jobs := []CollectionJob{}
	for rows.Next() {
		j, scanErr := scanJob(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		jobs = append(jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err = collectionAudit(ctx, tx, j, nil, "unknown", "collection.result"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) scheduleCollections(ctx context.Context, conn *pgxpool.Conn) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return tx.Commit(ctx)
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	accounts, err := loadCollectionAccounts(ctx, tx, c, true, []string{})
	if err != nil {
		return err
	}
	due := []storedAccount{}
	now := time.Now().UTC()
	for _, a := range accounts {
		if !a.Monitoring || !a.SourceCurrent || a.SourceMode != "passive" || a.IdentityState != "verified" {
			continue
		}
		h, err := readCollectionHistory(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		next := nextCollection(now, policy, a, c, h)
		if next != nil && !next.After(now) {
			due = append(due, a)
		}
		if len(due) == 100 {
			break
		}
	}
	if len(due) > 0 {
		_, err = queueRun(ctx, tx, "scheduled", nil, nil, false, policy, c, due)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return err
}

func (s *Service) finishCollection(ctx context.Context, tx pgx.Tx, j CollectionJob, state, reason string, check *Check, snapshot *QuotaSnapshot, authorize func() error) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	var body []byte
	var err error
	if check != nil {
		body, err = json.Marshal(check)
		if err != nil {
			return err
		}
	}
	var retryAfter *time.Time
	if j.StartedAt != nil {
		until := j.StartedAt.Add(60 * time.Second)
		retryAfter = &until
	}
	if check != nil && check.retryAfter != nil && (retryAfter == nil || check.retryAfter.After(*retryAfter)) {
		retryAfter = check.retryAfter
	}
	result, err := tx.Exec(ctx, `UPDATE collection_jobs SET state=$2,reason=$3,read_check=$4,finished_at=$5,retry_after=$6 WHERE id=$1 AND state='running'`, j.ID, state, reason, body, now, retryAfter)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return auth.ErrConflict
	}
	if snapshot != nil && state == "succeeded" {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO quota_observations(job_id,account_id,received_at,snapshot) VALUES ($1,$2,$3,$4)`, j.ID, j.AccountID, snapshot.ReceivedAt, encoded); err != nil {
			return err
		}
		task := "collection:" + j.ID
		if _, err = s.appendAccountEstimate(ctx, tx, j.AccountID, "observation", nil, &task); err != nil {
			return err
		}
	}
	outcome := "failed"
	switch state {
	case "succeeded":
		outcome = "succeeded"
	case "skipped":
		outcome = "denied"
	case "conflict":
		outcome = "conflict"
	case "interrupted":
		outcome = "unknown"
	}
	if err = collectionAudit(ctx, tx, j, nil, outcome, "collection.result"); err != nil {
		return err
	}
	if state == "failed" {
		if err = notifications.RecordCollectionFailure(ctx, tx, j.ID); err != nil {
			return err
		}
	}
	if authorize != nil {
		if err = authorize(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) processCollection(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM collection_jobs WHERE state='queued' ORDER BY collection_jobs.id LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	started := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = tx.Exec(ctx, `UPDATE collection_jobs SET state='running',reason='RUNNING',started_at=$2 WHERE id=$1`, j.ID, started); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	j.StartedAt = &started
	err = s.executeCollection(ctx, conn, j)
	return true, err
}

func (s *Service) executeCollection(ctx context.Context, conn *pgxpool.Conn, j CollectionJob) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var reason string
	var selected bool
	var actor, revision *string
	var digest []byte
	err = tx.QueryRow(ctx, `SELECT reason,selected,actor_id,actor_revision::text,actor_digest FROM collection_runs WHERE id=$1`, j.RunID).Scan(&reason, &selected, &actor, &revision, &digest)
	if err != nil {
		return err
	}
	authorize := func() error {
		if reason == "scheduled" {
			return nil
		}
		if actor == nil || revision == nil {
			return auth.ErrUnauthorized
		}
		return s.auth.AuthorizeQueuedAdmin(ctx, tx, digest, *actor, *revision)
	}
	if err = authorize(); err != nil {
		if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) {
			return s.finishCollection(ctx, tx, j, "skipped", "ACTOR_UNAVAILABLE", nil, nil, nil)
		}
		return err
	}
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	a, err := readManaged(ctx, tx, j.AccountID, c, true)
	if err != nil {
		return err
	}
	state, why := "", ""
	switch {
	case c.SourceID == nil || *c.SourceID != j.SourceID || !a.SourceCurrent:
		state, why = "conflict", "SOURCE_CHANGED"
	case j.ConnectionRevision != c.Revision:
		state, why = "conflict", "CONNECTION_CHANGED"
	case j.AccountRevision != a.Revision:
		state, why = "conflict", "ACCOUNT_CHANGED"
	case j.PolicyRevision != policy.Revision:
		state, why = "conflict", "POLICY_CHANGED"
	case reason == "scheduled" && !policy.Enabled || !selected && !a.Monitoring:
		state, why = "skipped", "PAUSED"
	case a.IdentityState != "verified":
		state, why = "skipped", "IDENTITY_UNVERIFIED"
	case a.SourceMode != "passive":
		state, why = "skipped", "ACTIVE_QUERY_SIDE_EFFECT"
	}
	if state != "" {
		return s.finishCollection(ctx, tx, j, state, why, nil, nil, authorize)
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return s.finishCollection(ctx, tx, j, "failed", "CONNECTION_UNAVAILABLE", nil, nil, authorize)
	}
	if err = authorize(); err != nil {
		return s.finishCollection(ctx, tx, j, "skipped", "ACTOR_UNAVAILABLE", nil, nil, nil)
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID, kind, secret, "quota.read", true)
	var snapshot *QuotaSnapshot
	state, why = "failed", check.Reason
	if check.State == "available" {
		observed, valid := inspectAccount(body, a.UpstreamID, secret)
		if !valid {
			check = invalid(check)
			why = check.Reason
		} else if observed.identity != "verified" || !bytes.Equal(a.fingerprint, observed.fingerprint) {
			check.State, check.Reason = "unverified", "RESOURCE_IDENTITY_CHANGED"
			state, why = "conflict", check.Reason
			if _, err = tx.Exec(ctx, `UPDATE managed_accounts SET identity_state='conflict',revision=revision+1 WHERE id=$1`, a.ID); err != nil {
				return err
			}
		} else {
			value, valid := passiveQuota(body)
			if !valid {
				check = invalid(check)
				why = check.Reason
			} else if len(value.Windows) == 0 {
				check.State, check.Reason = "unverified", "SNAPSHOT_MISSING"
				state, why = "skipped", check.Reason
			} else {
				value.SourceID, value.ConnectionRevision, value.AccountRevision = a.SourceID, c.Revision, a.Revision
				value.ReceivedAt = ceilMicrosecond(time.Now().UTC())
				snapshot = &value
				check.Reason = "VERIFIED_PASSIVE_SNAPSHOT"
				state, why = "succeeded", check.Reason
			}
		}
	}
	if err = authorize(); err != nil {
		if !errors.Is(err, auth.ErrUnauthorized) && !errors.Is(err, auth.ErrForbidden) {
			return err
		}
		// A session that expired during the read cannot persist protected observations.
		if err = tx.Rollback(ctx); err != nil {
			return err
		}
		tx, err = conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		return s.finishCollection(ctx, tx, j, "skipped", "ACTOR_UNAVAILABLE", nil, nil, nil)
	}
	return s.finishCollection(ctx, tx, j, state, why, &check, snapshot, authorize)
}
