package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type TemporaryNotice struct {
	RoundID, ReminderRevision, AccountID, ParticipantID, SourceID, ConnectionRevision, AccountRevision, ObservationID, Kind string
	ExpiresAt                                                                                                               time.Time
}

func RecordTemporary(ctx context.Context, tx pgx.Tx, in TemporaryNotice) error {
	if !validID(in.RoundID) || !validID(in.AccountID) || !validID(in.ParticipantID) || !validID(in.SourceID) || !validRevision(in.ReminderRevision) || !validRevision(in.ConnectionRevision) || !validRevision(in.AccountRevision) || !validRevision(in.ObservationID) || in.Kind != "temporary_near" && in.Kind != "temporary_exhausted" || !time.Now().Before(in.ExpiresAt) {
		return auth.ErrInput
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, enqueueLock); err != nil {
		return err
	}
	event := "temporary:" + in.RoundID + ":" + in.AccountID + ":" + in.ParticipantID + ":" + in.Kind + ":" + in.ReminderRevision
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE event_key=$1)`, event).Scan(&exists); err != nil || exists {
		return err
	}
	c, err := readConfig(ctx, tx, false)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	state, reason := "queued", "QUEUED"
	var finished *time.Time
	if !c.Enabled {
		state, reason = "skipped", "CHANNEL_DISABLED"
	} else if !ready(c) {
		state, reason = "skipped", "CHANNEL_INCOMPLETE"
	}
	if state == "skipped" {
		finished = &now
	}
	subject := "Sub-Pool temporary quota nearing exhaustion"
	eventReason := "TEMPORARY_QUOTA_NEAR"
	if in.Kind == "temporary_exhausted" {
		subject, eventReason = "Sub-Pool temporary quota exhausted", "TEMPORARY_QUOTA_EXHAUSTED"
	}
	body := fmt.Sprintf("Fresh quota observation %s for local account %s reached the explicitly configured temporary-round threshold (%s). This is a delayed observation notification; provider acceptance does not prove delivery or reading.", in.ObservationID, in.AccountID, eventReason)
	n, err := scan(tx.QueryRow(ctx, `INSERT INTO notifications(event_key,kind,subject_type,subject_id,participant_id,source_id,connection_revision,subject_revision,config_revision,channel,from_address,recipient_address,subject,body,event_reason,created_at,finished_at,state,reason,temporary_round_id,temporary_revision,temporary_observation_id,temporary_expires_at)
 VALUES($1,$2,'account',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) RETURNING `+strings.ReplaceAll(columns, "n.", ""), event, in.Kind, in.AccountID, in.ParticipantID, in.SourceID, in.ConnectionRevision, in.AccountRevision, c.Revision, c.Channel, c.From, c.Recipient, subject, body, eventReason, now, finished, state, reason, in.RoundID, in.ReminderRevision, in.ObservationID, in.ExpiresAt))
	if err == nil {
		err = recordAudit(ctx, tx, n, "notification.request")
	}
	return err
}

// It shares the round/wallet transition lock, so stopping waits for an already active send and fences queued work.
func temporaryDispatchEligible(ctx context.Context, tx pgx.Tx, n storedNotification) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, int64(0x5350324154454d50)); err != nil {
		return false, err
	}
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications n
 JOIN temporary_rounds r ON r.id=n.temporary_round_id
 JOIN connection_config c ON c.singleton
 JOIN managed_accounts a ON a.id=n.subject_id
 JOIN monitoring_config m ON m.singleton
 JOIN quota_observations q ON q.id=n.temporary_observation_id
 JOIN participant_bindings b ON b.participant_id=n.participant_id AND b.ended_at IS NULL
 JOIN participants p ON p.id=b.participant_id
 WHERE n.id=$1 AND r.deadline>clock_timestamp() AND n.temporary_expires_at>clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM temporary_exits e WHERE e.round_id=r.id)
 AND c.source_id=n.source_id AND c.revision=n.connection_revision AND a.source_id=n.source_id AND a.revision=n.subject_revision AND a.identity_state='verified'
 AND p.enabled AND p.deleted_at IS NULL AND b.source_id=n.source_id
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(r.snapshot->'scope'->'wallets') w WHERE w->>'participant_id'=p.id AND w->>'binding_id'=b.id::text AND w->>'participant_revision'=p.revision::text)
 AND n.temporary_revision=(SELECT id FROM temporary_events WHERE round_id=r.id AND kind='reminders' AND snapshot->>'enabled'='true' ORDER BY id DESC LIMIT 1)
 AND n.temporary_revision=(SELECT max(id) FROM temporary_events WHERE round_id=r.id AND kind='reminders')
 AND q.id=(SELECT max(id) FROM quota_observations WHERE account_id=a.id)
 AND q.snapshot->'simulation'=r.snapshot->'scope'->'simulation'
 AND (q.snapshot->>'source_at')::timestamptz<=clock_timestamp()
 AND (q.snapshot->>'source_at')::timestamptz+make_interval(secs=>m.stale_after_seconds)>clock_timestamp()
 AND NOT EXISTS(
  SELECT 1 FROM jsonb_array_elements(r.snapshot->'scope'->'accounts') frozen
  WHERE NOT EXISTS(
   SELECT 1 FROM quota_observations latest JOIN managed_accounts current ON current.id=latest.account_id
   WHERE latest.account_id=frozen->>'account_id' AND latest.id=(SELECT max(id) FROM quota_observations WHERE account_id=current.id)
   AND current.source_id=n.source_id AND current.revision::text=frozen->>'account_revision' AND current.identity_state='verified'
   AND latest.snapshot->'simulation'=r.snapshot->'scope'->'simulation'
   AND latest.snapshot->>'source_id'=n.source_id AND latest.snapshot->>'account_revision'=current.revision::text
   AND (latest.snapshot->>'source_at')::timestamptz<=clock_timestamp()
   AND (latest.snapshot->>'source_at')::timestamptz+make_interval(secs=>m.stale_after_seconds)>clock_timestamp()
   AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(frozen->'periods') period
    WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(latest.snapshot->'windows') w
     WHERE w->>'kind'=period->>'kind' AND w->'used_percent'<>'null'::jsonb
      AND (w->>'reset_at')::timestamptz=(period->>'end_at')::timestamptz AND w->>'duration_seconds'=period->>'duration_seconds'))
  )
 )
 )`, n.ID).Scan(&eligible)
	return eligible, err
}
