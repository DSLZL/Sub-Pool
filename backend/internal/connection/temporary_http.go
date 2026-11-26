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
	var fields []string
	switch r.URL.Path {
	case "/api/v1/temporary/history":
		fields = []string{"after", "limit"}
	case "/api/v1/temporary/round":
		fields = []string{"round_id"}
	case "/api/v1/temporary/rights":
		fields = []string{"round_id", "account_id"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, fields, func(ctx context.Context, token string, values map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var in struct {
			After     string `json:"after"`
			Limit     int    `json:"limit"`
			RoundID   string `json:"round_id"`
			AccountID string `json:"account_id"`
		}
		body, err := json.Marshal(values)
		if err != nil || decode(body, &in) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/temporary/history":
			return h.Service.ListTemporaryHistory(ctx, token, in.After, in.Limit)
		case "/api/v1/temporary/round":
			return h.Service.GetTemporaryRound(ctx, token, in.RoundID)
		default:
			return h.Service.GetTemporaryRights(ctx, token, in.RoundID, in.AccountID)
		}
	})
}
