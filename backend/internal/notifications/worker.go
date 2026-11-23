package notifications

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/telemetry"
)

const workerLock int64 = 0x535032414d41494c

func pause(ctx context.Context) bool {
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (s *Service) Run(ctx context.Context) {
	slog.Info("background worker started", "worker", "notifications")
	defer slog.Info("background worker stopped", "worker", "notifications")
	defer s.Heartbeat.Clear()
	for ctx.Err() == nil {
		conn, e := s.pool.Acquire(ctx)
		if e != nil {
			s.Heartbeat.Clear()
			if ctx.Err() == nil {
				telemetry.Error(ctx, "notification_database", e)
			}
			if !pause(ctx) {
				return
			}
			continue
		}
		var leader bool
		e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, workerLock).Scan(&leader)
		if e == nil && leader {
			e = s.lead(ctx, conn)
		} else if e == nil {
			s.Heartbeat.Beat()
			slog.Debug("background worker waiting for leadership", "worker", "notifications")
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if leader {
			if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, workerLock); err != nil {
				_ = conn.Conn().Close(cleanup)
			}
		}
		cancel()
		conn.Release()
		if e != nil && ctx.Err() == nil {
			s.Heartbeat.Clear()
			telemetry.Error(ctx, "notification_cycle", e)
		}
		if !pause(ctx) {
			return
		}
	}
}
func (s *Service) lead(ctx context.Context, conn *pgxpool.Conn) error {
	release, e := s.Runtime.Enter(ctx)
	if e != nil {
		return e
	}
	e = telemetry.RunStep(ctx, "notification_recovery", func() error { return recoverDispatches(ctx, conn) })
	release()
	if e != nil {
		return e
	}
	for ctx.Err() == nil {
		release, e = s.Runtime.Enter(ctx)
		if e != nil {
			return e
		}
		processed, e := telemetry.RunJob(ctx, "notifications", func() (bool, error) { return s.process(ctx, conn) })
		release()
		if e != nil {
			return e
		}
		s.Heartbeat.Beat()
		if !processed && !pause(ctx) {
			return nil
		}
	}
	return nil
}
func recoverDispatches(ctx context.Context, conn *pgxpool.Conn) error {
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `UPDATE notifications n SET state='unknown',reason='WORKER_INTERRUPTED',finished_at=$1 WHERE state='dispatching' RETURNING `+columns, time.Now().UTC())
	if e != nil {
		return e
	}
	var records []storedNotification
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			e = err
			break
		}
		records = append(records, n)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for _, n := range records {
		if e = recordAudit(ctx, tx, n, "notification.result"); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func finish(ctx context.Context, tx pgx.Tx, n storedNotification, result delivery) error {
	tag, e := tx.Exec(ctx, `UPDATE notifications SET state=$2,reason=$3,provider_id=$4,finished_at=$5 WHERE id=$1 AND state='dispatching'`, n.ID, result.state, result.reason, result.providerID, time.Now().UTC())
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return auth.ErrConflict
	}
	n.State, n.Reason = result.state, result.reason
	if e = recordAudit(ctx, tx, n, "notification.result"); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	level := slog.LevelDebug
	if result.state == "failed" || result.state == "unknown" {
		level = slog.LevelWarn
	}
	telemetry.Log(ctx, level, "notification result", slog.String("task", "notifications"),
		slog.String("state", result.state), slog.String("reason", result.reason))
	return nil
}
func (s *Service) process(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	tx, e := conn.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var id string
	e = tx.QueryRow(ctx, `SELECT id::text FROM notifications WHERE state='queued' ORDER BY notifications.id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if e != nil {
		return false, e
	}
	_, e = tx.Exec(ctx, `UPDATE notifications SET state='dispatching',reason='DISPATCHING',started_at=$2 WHERE id=$1`, id, time.Now().UTC())
	if e == nil {
		n, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM notifications n WHERE id=$1`, id))
		e = err
		if e == nil {
			e = recordAudit(ctx, tx, n, "notification.dispatch")
		}
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return false, e
	}
	return true, s.dispatch(ctx, conn, id)
}
func (s *Service) dispatch(ctx context.Context, conn *pgxpool.Conn, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	n, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM notifications n WHERE id=$1`, id))
	if e != nil {
		return e
	}
	if n.State != "dispatching" {
		return auth.ErrConflict
	}
	if financialKind(n.Kind) {
		eligible, err := s.financialEligible(ctx, tx, n)
		if err != nil {
			return err
		}
		if !eligible {
			return finish(ctx, tx, n, delivery{state: "skipped", reason: "FINANCIAL_EVENT_UNAVAILABLE"})
		}
	}
	if n.Kind == "temporary_near" || n.Kind == "temporary_exhausted" {
		eligible, err := temporaryDispatchEligible(ctx, tx, n)
		if err != nil {
			return err
		}
		if !eligible {
			return finish(ctx, tx, n, delivery{state: "skipped", reason: "TEMPORARY_EVENT_UNAVAILABLE"})
		}
	}
	authorize := func() error {
		if n.Kind != "test" {
			return nil
		}
		if n.details.ActorID == nil || n.actorRevision == nil {
			return auth.ErrUnauthorized
		}
		return s.auth.AuthorizeQueuedAdmin(ctx, tx, n.digest, *n.details.ActorID, *n.actorRevision)
	}
	if e = authorize(); e != nil {
		if errors.Is(e, auth.ErrUnauthorized) || errors.Is(e, auth.ErrForbidden) {
			return finish(ctx, tx, n, delivery{state: "skipped", reason: "ACTOR_UNAVAILABLE"})
		}
		return e
	}
	c, e := readConfig(ctx, tx, false)
	if e != nil {
		return e
	}
	why := ""
	switch {
	case c.Revision != n.details.ConfigRevision:
		why = "CONFIGURATION_CHANGED"
	case n.Kind != "test" && !c.Enabled:
		why = "CHANNEL_DISABLED"
	case n.Kind == "collection_failed" && !c.Preferences.CollectionFailed:
		why = "EVENT_DISABLED"
	case financialKind(n.Kind) && (!c.FinancialEnabled || n.financial != nil && n.financial.Native != nil && !nativeFinancialEnabled(c, n.financial.SourceID)):
		why = "FINANCIAL_NOT_ACTIVATED"
	case financialKind(n.Kind) && !financialPreference(c, n.Kind):
		why = "EVENT_DISABLED"
	case !ready(c):
		why = "CHANNEL_INCOMPLETE"
	}
	if why == "" && n.Kind == "collection_failed" {
		var source *string
		var connectionRevision, accountSource, accountRevision string
		e = tx.QueryRow(ctx, `SELECT source_id,revision::text FROM connection_config WHERE singleton FOR SHARE`).Scan(&source, &connectionRevision)
		if e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `SELECT source_id,revision::text FROM managed_accounts WHERE id=$1 FOR SHARE`, n.SubjectID).Scan(&accountSource, &accountRevision)
		if errors.Is(e, pgx.ErrNoRows) {
			why = "SUBJECT_CHANGED"
		} else if e != nil {
			return e
		} else if source == nil || n.sourceID == nil || n.connectionRevision == nil || n.subjectRevision == nil || *source != *n.sourceID || accountSource != *n.sourceID || connectionRevision != *n.connectionRevision || accountRevision != *n.subjectRevision {
			why = "SUBJECT_CHANGED"
		}
	}
	if why != "" {
		return finish(ctx, tx, n, delivery{state: "skipped", reason: why})
	}
	secret, e := s.decrypt(c)
	if e != nil {
		return finish(ctx, tx, n, delivery{state: "skipped", reason: "SECRET_STORAGE_UNAVAILABLE"})
	}
	if e = authorize(); e != nil {
		if errors.Is(e, auth.ErrUnauthorized) || errors.Is(e, auth.ErrForbidden) {
			return finish(ctx, tx, n, delivery{state: "skipped", reason: "ACTOR_UNAVAILABLE"})
		}
		return e
	}
	if financialKind(n.Kind) {
		eligible, err := s.financialEligible(ctx, tx, n)
		if err != nil {
			return err
		}
		if !eligible {
			return finish(ctx, tx, n, delivery{state: "skipped", reason: "FINANCIAL_EVENT_UNAVAILABLE"})
		}
	}
	result := s.send(ctx, c, secret, n)
	// Once dispatched, keep the factual receipt even if the browser session expires or cancels.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return finish(ctx, tx, n, result)
}
