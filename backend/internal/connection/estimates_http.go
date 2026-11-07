package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"sub-pool/backend/internal/auth"
	"time"
)

type EstimatesHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *EstimatesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/estimates/current":
		names = []string{"account_id"}
	case "/api/v1/estimates/record":
		names = []string{"id"}
	case "/api/v1/estimates/history":
		names = []string{"account_id", "after", "limit", "window_kind", "period_end_at"}
	case "/api/v1/estimates/configure":
		names = []string{"account_id", "revision", "plan", "cost_basis", "keep_ratio", "premise", "windows", "source_id", "connection_revision", "account_revision"}
	case "/api/v1/estimates/recompute":
		names = []string{"account_id", "revision", "latest_id"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var in struct {
			EstimateSaveInput
			After       string `json:"after"`
			Limit       int    `json:"limit"`
			WindowKind  string `json:"window_kind"`
			PeriodEndAt string `json:"period_end_at"`
			ID          string `json:"id"`
			LatestID    string `json:"latest_id"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &in) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/estimates/current":
			return h.Service.GetEstimateCurrent(ctx, token, in.AccountID)
		case "/api/v1/estimates/record":
			return h.Service.GetEstimateRecord(ctx, token, in.ID)
		case "/api/v1/estimates/history":
			return h.Service.ListEstimateHistory(ctx, token, EstimateHistoryInput{AccountID: in.AccountID, After: in.After, Limit: in.Limit, WindowKind: in.WindowKind, PeriodEndAt: in.PeriodEndAt})
		case "/api/v1/estimates/configure":
			return h.Service.SaveEstimateConfiguration(ctx, token, in.EstimateSaveInput)
		default:
			return h.Service.RecomputeEstimate(ctx, token, in.AccountID, in.Revision, in.LatestID)
		}
	})
}
