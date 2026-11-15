package connection

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/notifications"
)

type TemporaryReminders struct {
	Revision  string  `json:"revision"`
	Enabled   bool    `json:"enabled"`
	Threshold *string `json:"threshold"`
}
type TemporaryRemindersInput struct {
	RoundID   string `json:"round_id"`
	Revision  string `json:"revision"`
	Enabled   bool   `json:"enabled"`
	Threshold string `json:"threshold"`
}

func temporaryReminders(ctx context.Context, tx pgx.Tx, round string) (TemporaryReminders, error) {
	out := TemporaryReminders{Revision: "0"}
	var raw []byte
	var revision string
	err := tx.QueryRow(ctx, `SELECT id::text,snapshot FROM temporary_events WHERE round_id=$1 AND kind='reminders' ORDER BY temporary_events.id DESC LIMIT 1`, round).Scan(&revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	out.Revision = revision
	return out, err
}
func (s *Service) ConfigureTemporaryReminders(ctx context.Context, token string, in TemporaryRemindersInput) (TemporaryReminders, error) {
	threshold, valid := simulationDecimal(in.Threshold)
	if !managedID(in.RoundID) || !validUsageAfter(in.Revision) || in.Enabled && (!valid || threshold.Sign() <= 0 || threshold.Cmp(big.NewRat(100, 1)) >= 0) || !in.Enabled && in.Threshold != "" {
		return TemporaryReminders{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryReminders{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, temporaryLock); err != nil {
		return TemporaryReminders{}, err
	}
	r, err := temporaryRound(ctx, tx, in.RoundID)
	if err != nil {
		return TemporaryReminders{}, err
	}
	old, err := temporaryReminders(ctx, tx, r.ID)
	if err != nil {
		return TemporaryReminders{}, err
	}
	if old.Revision != in.Revision {
		return TemporaryReminders{}, auth.ErrConflict
	}
	if in.Enabled {
		c, err := s.config(ctx, tx, false)
		if err != nil {
			return TemporaryReminders{}, err
		}
		exit, err := temporaryEffectiveExit(ctx, tx, r, time.Now().UTC())
		if err != nil {
			return TemporaryReminders{}, err
		}
		if exit != nil || s.simulationEpoch(c) != r.Scope.Simulation.Epoch || c.Revision != r.Scope.ConnectionRevision {
			return TemporaryReminders{}, auth.ErrConflict
		}
	}
	out := TemporaryReminders{Enabled: in.Enabled}
	if in.Enabled {
		out.Threshold = &in.Threshold
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO temporary_events(round_id,kind,request_key,recorded_at,snapshot) VALUES($1,'reminders',$2,$3,$4) RETURNING id::text`, r.ID, "temporary-reminders:"+r.ID+":"+in.Revision, time.Now().UTC(), raw).Scan(&out.Revision)
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "temporary.reminders", Outcome: "succeeded", TargetType: "temporary_round", TargetID: r.ID})
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryReminders{}, err
	}
	return out, nil
}

func temporaryRecordReminders(ctx context.Context, tx pgx.Tx, r TemporaryRound) error {
	control, err := temporaryReminders(ctx, tx, r.ID)
	if err != nil || !control.Enabled || control.Threshold == nil {
		return err
	}
	threshold, ok := simulationDecimal(*control.Threshold)
	if !ok {
		return auth.ErrConflict
	}
	var stale int
	if err = tx.QueryRow(ctx, `SELECT stale_after_seconds FROM monitoring_config WHERE singleton`).Scan(&stale); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, a := range r.Scope.Accounts {
		var raw []byte
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text,snapshot FROM quota_observations WHERE account_id=$1 ORDER BY quota_observations.id DESC LIMIT 1`, a.AccountID).Scan(&id, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var snapshot QuotaSnapshot
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		if snapshot.SourceAt == nil || snapshot.SourceAt.After(now) || snapshot.Simulation == nil || *snapshot.Simulation != r.Scope.Simulation || snapshot.SourceID != r.Scope.SourceID || snapshot.AccountRevision != a.AccountRevision {
			continue
		}
		expires := snapshot.SourceAt.Add(time.Duration(stale) * time.Second)
		if r.Deadline.Before(expires) {
			expires = r.Deadline
		}
		if !now.Before(expires) {
			continue
		}
		kind := ""
		for _, window := range snapshot.Windows {
			if window.UsedPercent == nil {
				continue
			}
			relevant := false
			for _, period := range a.Periods {
				if window.Kind == period.Kind {
					relevant = true
				}
			}
			if !relevant {
				continue
			}
			value, ok := simulationDecimal(*window.UsedPercent)
			if !ok || value.Cmp(big.NewRat(100, 1)) > 0 {
				continue
			}
			if value.Cmp(big.NewRat(100, 1)) == 0 {
				kind = "temporary_exhausted"
				break
			}
			if value.Cmp(threshold) >= 0 {
				kind = "temporary_near"
			}
		}
		if kind != "" {
			for _, member := range a.Members {
				err = notifications.RecordTemporary(ctx, tx, notifications.TemporaryNotice{RoundID: r.ID, ReminderRevision: control.Revision, AccountID: a.AccountID, ParticipantID: member.ParticipantID, SourceID: r.Scope.SourceID, ConnectionRevision: r.Scope.ConnectionRevision, AccountRevision: a.AccountRevision, ObservationID: id, ExpiresAt: expires, Kind: kind})
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}
