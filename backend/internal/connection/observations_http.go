package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"sub-pool/backend/internal/auth"
	"time"
)

type ObservationsHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *ObservationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/observations/list":
		names = []string{"account_id", "source_id", "source_mode", "validity", "start_at", "end_at", "after", "limit"}
	case "/api/v1/observations/daily-history":
		names = []string{"account_id", "start_at", "end_at", "settings_revision"}
	case "/api/v1/observations/get", "/api/v1/observations/estimate-link", "/api/v1/observations/recommendation-link", "/api/v1/observations/usage-evidence":
		names = []string{"id"}
	case "/api/v1/observations/events":
		names = []string{"account_id", "after", "limit"}
	case "/api/v1/observations/state":
		names = []string{"account_id"}
	case "/api/v1/observations/review":
		names = []string{"account_id", "revision", "observation_id", "excluded", "reason"}
	case "/api/v1/observations/start":
		names = []string{"account_id", "revision", "start_observation_id", "end_observation_id", "window_kind", "reason"}
	case "/api/v1/observations/revoke":
		names = []string{"account_id", "revision", "reason"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var input struct {
			ObservationQuery
			ID                 string `json:"id"`
			Revision           string `json:"revision"`
			ObservationID      string `json:"observation_id"`
			Excluded           bool   `json:"excluded"`
			Reason             string `json:"reason"`
			StartObservationID string `json:"start_observation_id"`
			EndObservationID   string `json:"end_observation_id"`
			WindowKind         string `json:"window_kind"`
			SettingsRevision   string `json:"settings_revision"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/observations/list":
			return h.Service.ListObservations(ctx, token, input.ObservationQuery)
		case "/api/v1/observations/daily-history":
			return h.Service.GetDailyObservationHistory(ctx, token, DailyObservationQuery{AccountID: input.AccountID, StartAt: input.StartAt, EndAt: input.EndAt, SettingsRevision: input.SettingsRevision})
		case "/api/v1/observations/get":
			return h.Service.GetObservation(ctx, token, input.ID)
		case "/api/v1/observations/estimate-link":
			return h.Service.GetObservationEstimateLink(ctx, token, input.ID)
		case "/api/v1/observations/recommendation-link":
			return h.Service.GetObservationRecommendationLink(ctx, token, input.ID)
		case "/api/v1/observations/usage-evidence":
			return h.Service.GetObservationUsageEvidence(ctx, token, input.ID)
		case "/api/v1/observations/events":
			return h.Service.ListObservationEvents(ctx, token, input.AccountID, input.After, input.Limit)
		case "/api/v1/observations/state":
			return h.Service.GetObservationManagement(ctx, token, input.AccountID)
		case "/api/v1/observations/review":
			return h.Service.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: input.AccountID, Revision: input.Revision, ObservationID: input.ObservationID, Excluded: input.Excluded, Reason: input.Reason})
		default:
			return h.Service.ChangeObservationStart(ctx, token, ObservationStartInput{AccountID: input.AccountID, Revision: input.Revision, StartObservationID: input.StartObservationID, EndObservationID: input.EndObservationID, WindowKind: input.WindowKind, Reason: input.Reason}, r.URL.Path == "/api/v1/observations/revoke")
		}
	})
}
