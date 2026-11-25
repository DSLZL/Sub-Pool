package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type UsageHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *UsageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/usage/capture", "/api/v1/usage/days/queue", "/api/v1/usage/days/status":
		names = []string{"id", "revision", "source_id", "connection_revision", "start_at", "end_at"}
	case "/api/v1/usage/captures/list":
		names = []string{"account_id", "after", "limit"}
	case "/api/v1/usage/captures/get":
		names = []string{"id", "after", "limit", "model", "user_id", "api_key_id", "grain"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		body, err := json.Marshal(fields)
		if err != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/usage/capture", "/api/v1/usage/days/queue", "/api/v1/usage/days/status":
			var input UsageCaptureInput
			if json.Unmarshal(body, &input) != nil {
				return nil, auth.ErrInput
			}
			switch r.URL.Path {
			case "/api/v1/usage/days/queue":
				return h.Service.QueueUsageDays(ctx, token, input)
			case "/api/v1/usage/days/status":
				return h.Service.UsageDayStatus(ctx, token, input)
			default:
				return h.Service.CaptureUsage(ctx, token, input)
			}
		case "/api/v1/usage/captures/list":
			var input struct {
				AccountID string `json:"account_id"`
				After     string `json:"after"`
				Limit     int    `json:"limit"`
			}
			if json.Unmarshal(body, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListUsageCaptures(ctx, token, input.AccountID, input.After, input.Limit)
		default:
			var input UsageQuery
			if json.Unmarshal(body, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.GetUsageCapture(ctx, token, input)
		}
	})
}
