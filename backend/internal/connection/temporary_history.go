package connection

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"time"
)

type TemporaryRoundDetails struct {
	Round       TemporaryRound        `json:"round"`
	Settlements []TemporarySettlement `json:"settlements"`
	Reminders   TemporaryReminders    `json:"reminders"`
}
type TemporaryRoundSummary struct {
	ID          string               `json:"id"`
	SourceID    string               `json:"source_id"`
	Simulation  SimulationProvenance `json:"simulation"`
	StartedAt   time.Time            `json:"started_at"`
	Deadline    time.Time            `json:"deadline"`
	Mode        string               `json:"mode"`
	Exit        *TemporaryExit       `json:"exit"`
	Accounts    int                  `json:"accounts"`
	RightsState string               `json:"rights_state"`
}
type TemporaryHistory struct {
	Rounds    []TemporaryRoundSummary `json:"rounds"`
	NextAfter *string                 `json:"next_after"`
}

func temporaryRoundDetails(ctx context.Context, tx pgx.Tx, r TemporaryRound) (TemporaryRoundDetails, error) {
	out := TemporaryRoundDetails{Round: r, Settlements: []TemporarySettlement{}}
	var err error
	out.Reminders, err = temporaryReminders(ctx, tx, r.ID)
	if err != nil {
		return out, err
	}
	for _, account := range r.Scope.Accounts {
		settlement, err := temporarySettlement(ctx, tx, r.ID, account.AccountID)
		if err != nil {
			return out, err
		}
		if settlement != nil {
			out.Settlements = append(out.Settlements, *settlement)
		}
	}
	return out, nil
}

func (s *Service) ListTemporaryHistory(ctx context.Context, token, after string, limit int) (TemporaryHistory, error) {
	out := TemporaryHistory{Rounds: []TemporaryRoundSummary{}}
	if after != "" && !managedID(after) || limit < 1 || limit > 100 {
		return out, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if after != "" {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM temporary_rounds WHERE id=$1)", after).Scan(&exists); err != nil {
			return out, err
		}
		if !exists {
			return out, auth.ErrNotFound
		}
	}
	rows, err := tx.Query(ctx, `SELECT id FROM temporary_rounds WHERE $1='' OR (started_at,id)<(SELECT started_at,id FROM temporary_rounds WHERE id=$1) ORDER BY started_at DESC,id DESC LIMIT $2`, after, limit+1)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	for _, id := range ids {
		r, err := temporaryRound(ctx, tx, id)
		if err != nil {
			return out, err
		}
		r.Exit, err = temporaryEffectiveExit(ctx, tx, r, time.Now().UTC())
		if err != nil {
			return out, err
		}
		state := "none"
		if r.Exit != nil {
			ready, err := temporarySettlementsFinished(ctx, tx, r)
			if err != nil {
				return out, err
			}
			state = "pending_evidence"
			if ready {
				state = "processed"
			}
		}
		out.Rounds = append(out.Rounds, TemporaryRoundSummary{ID: r.ID, SourceID: r.Scope.SourceID, Simulation: r.Scope.Simulation, StartedAt: r.StartedAt, Deadline: r.Deadline, Mode: r.Mode, Exit: r.Exit, Accounts: len(r.Scope.Accounts), RightsState: state})
	}
	if more {
		last := ids[len(ids)-1]
		out.NextAfter = &last
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return TemporaryHistory{}, err
	}
	return out, nil
}
