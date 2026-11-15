package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type TemporaryHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *TemporaryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fields := []string{}
	switch r.URL.Path {
	case "/api/v1/temporary/status", "/api/v1/temporary/preflight":
	case "/api/v1/temporary/history":
		fields = []string{"after", "limit"}
	case "/api/v1/temporary/round", "/api/v1/temporary/stop":
		fields = []string{"round_id"}
	case "/api/v1/temporary/rights":
		fields = []string{"round_id", "account_id"}
	case "/api/v1/temporary/start":
		fields = []string{"request_id", "scope_hash", "mode", "deadline", "caps", "acknowledged_participants", "informed", "confirmed"}
	case "/api/v1/temporary/rights-change":
		fields = []string{"request_id", "round_id", "account_id", "participant_id", "binding_id", "revision", "period", "after", "reason"}
	case "/api/v1/temporary/reminders":
		fields = []string{"round_id", "revision", "enabled", "threshold"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, fields, func(ctx context.Context, token string, values map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		body, err := json.Marshal(values)
		if err != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/temporary/history":
			var in struct {
				After string `json:"after"`
				Limit int    `json:"limit"`
			}
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListTemporaryHistory(ctx, token, in.After, in.Limit)
		case "/api/v1/temporary/status":
			return h.Service.GetTemporaryStatus(ctx, token)
		case "/api/v1/temporary/preflight":
			return h.Service.GetTemporaryPreflight(ctx, token)
		case "/api/v1/temporary/round", "/api/v1/temporary/stop":
			var in struct {
				RoundID string `json:"round_id"`
			}
			if json.Unmarshal(body, &in) != nil {
				return nil, auth.ErrInput
			}
			if r.URL.Path == "/api/v1/temporary/stop" {
				return h.Service.StopTemporary(ctx, token, in.RoundID)
			}
			return h.Service.GetTemporaryRound(ctx, token, in.RoundID)
		case "/api/v1/temporary/rights":
			var in struct {
				RoundID   string `json:"round_id"`
				AccountID string `json:"account_id"`
			}
			if json.Unmarshal(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.GetTemporaryRights(ctx, token, in.RoundID, in.AccountID)
		case "/api/v1/temporary/start":
			var in TemporaryStart
			var caps []json.RawMessage
			if json.Unmarshal(body, &in) != nil || !simulationTime(values["deadline"]) || json.Unmarshal(values["caps"], &caps) != nil {
				return nil, auth.ErrInput
			}
			for _, cap := range caps {
				var parsed TemporaryCap
				if !simulationObject(cap, "account_id participant_id amount", &parsed) {
					return nil, auth.ErrInput
				}
			}
			return h.Service.StartTemporary(ctx, token, in)
		case "/api/v1/temporary/rights-change":
			var in TemporaryRightsChange
			var period ObservationPeriod
			if json.Unmarshal(body, &in) != nil || !simulationObject(values["period"], "kind start_at end_at duration_seconds basis", &period) {
				return nil, auth.ErrInput
			}
			return h.Service.ChangeTemporaryRights(ctx, token, in)
		case "/api/v1/temporary/reminders":
			var in TemporaryRemindersInput
			if json.Unmarshal(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ConfigureTemporaryReminders(ctx, token, in)
		}
		return nil, auth.ErrNotFound
	})
}
