package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type ObservationEstimateReference struct {
	ID          string    `json:"id"`
	GeneratedAt time.Time `json:"generated_at"`
}

type ObservationEstimateLink struct {
	ObservationID string                        `json:"observation_id"`
	AccountID     string                        `json:"account_id"`
	Estimate      *ObservationEstimateReference `json:"estimate"`
}

func (s *Service) GetObservationEstimateLink(ctx context.Context, token, id string) (ObservationEstimateLink, error) {
	if !validRevision(id) {
		return ObservationEstimateLink{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationEstimateLink{}, err
	}
	defer tx.Rollback(ctx)
	observations, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err != nil {
		return ObservationEstimateLink{}, err
	}
	estimates, err := s.auth.AuthorizeRead(ctx, tx, token, "estimates")
	if err != nil {
		return ObservationEstimateLink{}, err
	}
	item, err := readObservation(ctx, tx, id, "", observations)
	if err != nil {
		return ObservationEstimateLink{}, err
	}
	if err = estimates.CheckAccount(item.AccountID); err != nil {
		return ObservationEstimateLink{}, err
	}
	result := ObservationEstimateLink{ObservationID: id, AccountID: item.AccountID}
	var reference ObservationEstimateReference
	// Only a frozen window reference proves use; a head pointer merely tracks collection progress.
	err = tx.QueryRow(ctx, `SELECT r.id::text,r.generated_at FROM estimate_results r
 WHERE r.account_id=$1 AND EXISTS (SELECT 1 FROM jsonb_array_elements(r.snapshot->'windows') w
 WHERE jsonb_typeof(w->'observation_ids')='array' AND w->'observation_ids' ? $2::text
 OR w->'current_observation_id'=to_jsonb($2::text))
 ORDER BY r.id DESC LIMIT 1`, item.AccountID, id).Scan(&reference.ID, &reference.GeneratedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ObservationEstimateLink{}, err
	}
	if err == nil {
		reference.GeneratedAt = reference.GeneratedAt.UTC()
		result.Estimate = &reference
	}
	for _, feature := range []string{"observations", "estimates"} {
		access, e := s.auth.AuthorizeRead(ctx, tx, token, feature)
		if e != nil {
			return ObservationEstimateLink{}, e
		}
		if e = access.CheckAccount(item.AccountID); e != nil {
			return ObservationEstimateLink{}, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ObservationEstimateLink{}, err
	}
	return result, nil
}
