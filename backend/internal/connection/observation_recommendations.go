package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type ObservationRecommendationReference struct {
	ID            string    `json:"id"`
	ParticipantID string    `json:"participant_id"`
	EstimateID    string    `json:"estimate_id"`
	GeneratedAt   time.Time `json:"generated_at"`
}

type ObservationRecommendationLink struct {
	ObservationID  string                              `json:"observation_id"`
	AccountID      string                              `json:"account_id"`
	Recommendation *ObservationRecommendationReference `json:"recommendation"`
}

func (s *Service) GetObservationRecommendationLink(ctx context.Context, token, id string) (ObservationRecommendationLink, error) {
	if !validRevision(id) {
		return ObservationRecommendationLink{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationRecommendationLink{}, err
	}
	defer tx.Rollback(ctx)
	for _, feature := range []string{"observations", "balances"} {
		access, e := s.auth.AuthorizeRead(ctx, tx, token, feature)
		if e != nil {
			return ObservationRecommendationLink{}, e
		}
		if !access.AllData {
			return ObservationRecommendationLink{}, auth.ErrForbidden
		}
	}
	item, err := readObservation(ctx, tx, id, "", auth.Access{AllData: true})
	if err != nil {
		return ObservationRecommendationLink{}, err
	}
	result := ObservationRecommendationLink{ObservationID: id, AccountID: item.AccountID}
	var reference ObservationRecommendationReference
	// Only the frozen contribution and its estimate's exact window references prove this chain.
	err = tx.QueryRow(ctx, `SELECT r.id::text,r.participant_id,e.id::text,r.generated_at
 FROM estimate_results e JOIN wallet_recommendations r ON r.snapshot->'contributions' @>
 jsonb_build_array(jsonb_build_object('account_id',$1::text,'estimate_id',e.id::text))
 WHERE e.account_id=$1 AND EXISTS (SELECT 1 FROM jsonb_array_elements(e.snapshot->'windows') w
 WHERE jsonb_typeof(w->'observation_ids')='array' AND w->'observation_ids' ? $2::text
 OR w->'current_observation_id'=to_jsonb($2::text))
 ORDER BY r.id DESC,e.id DESC LIMIT 1`, item.AccountID, id).Scan(&reference.ID, &reference.ParticipantID, &reference.EstimateID, &reference.GeneratedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ObservationRecommendationLink{}, err
	}
	if err == nil {
		reference.GeneratedAt = reference.GeneratedAt.UTC()
		result.Recommendation = &reference
	}
	for _, feature := range []string{"observations", "balances"} {
		access, e := s.auth.AuthorizeRead(ctx, tx, token, feature)
		if e != nil {
			return ObservationRecommendationLink{}, e
		}
		if !access.AllData {
			return ObservationRecommendationLink{}, auth.ErrForbidden
		}
		if e = access.CheckAccount(item.AccountID); e != nil {
			return ObservationRecommendationLink{}, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ObservationRecommendationLink{}, err
	}
	return result, nil
}
