package connection

import (
	"context"

	"encoding/json"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/auth"
)

type TemporaryCap struct {
	AccountID     string `json:"account_id"`
	ParticipantID string `json:"participant_id"`
	Amount        string `json:"amount"`
}

type TemporaryExit struct {
	ExitedAt   time.Time `json:"exited_at"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
	ActorID    *string   `json:"actor_id"`
}
type TemporaryRound struct {
	ID                       string         `json:"id"`
	ActorID                  string         `json:"actor_id"`
	StartedAt                time.Time      `json:"started_at"`
	Deadline                 time.Time      `json:"deadline"`
	Mode                     string         `json:"mode"`
	Scope                    TemporaryScope `json:"scope"`
	Caps                     []TemporaryCap `json:"caps"`
	AcknowledgedParticipants []string       `json:"acknowledged_participants"`
	Informed                 bool           `json:"informed"`
	Exit                     *TemporaryExit `json:"exit"`
}
type temporarySaved struct {
	Mode                     string         `json:"mode"`
	Scope                    TemporaryScope `json:"scope"`
	Caps                     []TemporaryCap `json:"caps"`
	AcknowledgedParticipants []string       `json:"acknowledged_participants"`
	Informed                 bool           `json:"informed"`
}

func temporaryRound(ctx context.Context, tx pgx.Tx, id string) (TemporaryRound, error) {
	out := TemporaryRound{}
	var raw, exit []byte
	err := tx.QueryRow(ctx, `SELECT r.id,r.actor_id,r.started_at,r.deadline,r.snapshot,
 (SELECT jsonb_build_object('exited_at',e.exited_at,'reason',e.reason,'recorded_at',e.recorded_at,'actor_id',e.actor_id) FROM temporary_exits e WHERE e.round_id=r.id)
 FROM temporary_rounds r WHERE r.id=$1`, id).Scan(&out.ID, &out.ActorID, &out.StartedAt, &out.Deadline, &raw, &exit)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, auth.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var saved temporarySaved
	if err = json.Unmarshal(raw, &saved); err != nil {
		return out, err
	}
	out.Mode, out.Scope, out.Caps, out.AcknowledgedParticipants, out.Informed = saved.Mode, saved.Scope, saved.Caps, saved.AcknowledgedParticipants, saved.Informed
	if exit != nil {
		out.Exit = &TemporaryExit{}
		err = json.Unmarshal(exit, out.Exit)
		out.Exit.ExitedAt, out.Exit.RecordedAt = out.Exit.ExitedAt.UTC(), out.Exit.RecordedAt.UTC()
	}
	out.StartedAt, out.Deadline = out.StartedAt.UTC(), out.Deadline.UTC()
	return out, err
}

func temporaryLatest(ctx context.Context, tx pgx.Tx) (*TemporaryRound, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM temporary_rounds ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := temporaryRound(ctx, tx, id)
	return &out, err
}

// The logical exit is independent of worker liveness and is never later than the frozen deadline.
func temporaryEffectiveExit(ctx context.Context, tx pgx.Tx, r TemporaryRound, now time.Time) (*TemporaryExit, error) {
	if r.Exit != nil {
		return r.Exit, nil
	}
	at, reason := r.Deadline, "deadline"
	for _, a := range r.Scope.Accounts {
		rows, err := tx.Query(ctx, `SELECT snapshot FROM quota_observations WHERE account_id=$1
		 AND (snapshot->>'source_at')::timestamptz >= $2
		 AND (snapshot->>'source_at')::timestamptz <= $3
		 AND (snapshot->>'source_at')::timestamptz < $4
		 ORDER BY (snapshot->>'source_at')::timestamptz,id`, a.AccountID, r.StartedAt, now, at)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			var snapshot QuotaSnapshot
			if err = rows.Scan(&raw); err == nil {
				err = json.Unmarshal(raw, &snapshot)
			}
			if err != nil {
				break
			}
			if snapshot.Simulation == nil || *snapshot.Simulation != r.Scope.Simulation || snapshot.SourceID != r.Scope.SourceID || snapshot.AccountRevision != a.AccountRevision || snapshot.SourceAt == nil || snapshot.SourceAt.Before(r.StartedAt) || snapshot.SourceAt.After(now) {
				continue
			}
			for _, frozen := range a.Periods {
				period := nativeObservationPeriod(snapshot, frozen.Kind)
				if period != nil && (!period.StartAt.Equal(frozen.StartAt) || !period.EndAt.Equal(frozen.EndAt) || period.DurationSeconds != frozen.DurationSeconds) && snapshot.SourceAt.Before(at) {
					at, reason = *snapshot.SourceAt, "period_changed"
				}
			}
			if reason == "period_changed" && at.Equal(*snapshot.SourceAt) {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if now.Before(at) {
		return nil, nil
	}
	return &TemporaryExit{ExitedAt: at.UTC(), Reason: reason, RecordedAt: now.UTC()}, nil
}

func temporarySamePeriod(a, b ObservationPeriod) bool {
	return a.Kind == b.Kind && a.StartAt.Equal(b.StartAt) && a.EndAt.Equal(b.EndAt) && a.DurationSeconds == b.DurationSeconds && a.Basis == b.Basis
}

func temporaryWalletBasisHead(ctx context.Context, tx pgx.Tx, b *walletBasis) error {
	r, err := temporaryLatest(ctx, tx)
	if err != nil || r == nil {
		return err
	}
	relevant := false
	for _, w := range r.Scope.Wallets {
		if w.BindingID == b.BindingID {
			relevant = true
		}
	}
	if !relevant {
		return nil
	}
	exit, err := temporaryEffectiveExit(ctx, tx, *r, time.Now().UTC())
	if err != nil || exit == nil {
		return err
	}
	head := exit.ExitedAt.Format(time.RFC3339Nano) + ":" + exit.Reason
	b.TemporaryHead += ":" + head
	for i := range b.Sources {
		for _, a := range r.Scope.Accounts {
			if b.Sources[i].AccountID == a.AccountID {
				b.Sources[i].TemporaryHead += ":" + head
			}
		}
	}
	return nil
}
