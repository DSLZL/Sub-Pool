package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type ParticipantDeletion struct {
	ParticipantID    string    `json:"participant_id"`
	PreviousRevision string    `json:"previous_revision"`
	Revision         string    `json:"revision"`
	BindingID        string    `json:"binding_id"`
	Name             string    `json:"name"`
	EffectiveAt      time.Time `json:"effective_at"`
}

func (s *Service) GetParticipantDeletion(ctx context.Context, token, id, previousRevision string) (ParticipantDeletion, error) {
	if !managedID(id) || !validRevision(previousRevision) {
		return ParticipantDeletion{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return ParticipantDeletion{}, err
	}
	defer tx.Rollback(ctx)
	out := ParticipantDeletion{PreviousRevision: previousRevision}
	// A missing active row alone cannot prove that this version's deletion committed.
	err = tx.QueryRow(ctx, `SELECT p.id,p.revision::text,b.id::text,p.name,p.deleted_at
		FROM participants p JOIN LATERAL (SELECT id,ended_at FROM participant_bindings
			WHERE participant_id=p.id ORDER BY id DESC LIMIT 1) b ON b.ended_at=p.deleted_at
		WHERE p.id=$1 AND p.deleted_at IS NOT NULL AND NOT p.enabled AND p.revision-1=$2::bigint
		AND EXISTS(SELECT 1 FROM participant_events e WHERE e.participant_id=p.id
			AND e.revision=p.revision AND e.action='participant.delete') FOR SHARE OF p`,
		id, previousRevision).Scan(&out.ParticipantID, &out.Revision, &out.BindingID, &out.Name, &out.EffectiveAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return ParticipantDeletion{}, err
	}
	out.EffectiveAt = out.EffectiveAt.UTC()
	return out, nil
}
