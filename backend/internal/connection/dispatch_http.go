package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type DispatchHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *DispatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var fields []string
	switch r.URL.Path {
	case "/api/v1/dispatch/list":
		fields = []string{"after", "search", "limit"}
	case "/api/v1/dispatch/account":
		fields = []string{"id", "revision", "source_id", "connection_revision"}
	case "/api/v1/dispatch/control":
		fields = []string{}
	case "/api/v1/dispatch/pause":
		fields = []string{"id", "revision", "source_id", "connection_revision", "request_id", "restore_at", "acknowledge_account_wide"}
	case "/api/v1/dispatch/get":
		fields = []string{"id"}
	case "/api/v1/dispatch/history":
		fields = []string{"account_id", "after", "limit"}
	case "/api/v1/dispatch/reschedule":
		fields = []string{"id", "revision", "restore_at"}
	case "/api/v1/dispatch/restore":
		fields = []string{"id", "revision"}
	case "/api/v1/dispatch/review":
		fields = []string{"id", "revision", "acknowledge_result_unknown"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, fields, func(ctx context.Context, token string, body map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/dispatch/control":
			return h.Service.GetDispatchControl(ctx, token)
		case "/api/v1/dispatch/list":
			var input DispatchListInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListDispatchAccounts(ctx, token, input)
		case "/api/v1/dispatch/pause":
			var input DispatchPauseInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.PauseDispatch(ctx, token, input)
		case "/api/v1/dispatch/get":
			var input struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.GetDispatchPause(ctx, token, input.ID)
		case "/api/v1/dispatch/history":
			var input DispatchHistoryInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListDispatchHistory(ctx, token, input)
		case "/api/v1/dispatch/reschedule":
			var input DispatchRescheduleInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.RescheduleDispatch(ctx, token, input)
		case "/api/v1/dispatch/restore":
			var input DispatchChangeInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.RestoreDispatch(ctx, token, input)
		case "/api/v1/dispatch/review":
			var input DispatchReviewInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ReviewDispatch(ctx, token, input)
		}
		var input AccountReadInput
		if json.Unmarshal(encoded, &input) != nil {
			return nil, auth.ErrInput
		}
		return h.Service.ReadDispatchAccount(ctx, token, input)
	})
}
