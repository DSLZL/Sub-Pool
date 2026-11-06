package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type MonitoringHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func ProcessHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.ContentLength > 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte(`{"status":"ok","scope":"process"}`))
}

func (h *MonitoringHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	var names []string
	switch r.URL.Path {
	case "/api/v1/monitoring/config", "/api/v1/monitoring/status":
		method = http.MethodGet
	case "/api/v1/monitoring/config/update":
		names = []string{"revision", "enabled", "interval_seconds", "stale_after_seconds", "refresh_on_change", "near_reset_enabled", "near_reset_lead_seconds", "near_reset_interval_seconds", "stale_warnings_enabled"}
	case "/api/v1/monitoring/collect":
		names = []string{"source_id", "connection_revision", "all", "account_ids"}
	case "/api/v1/monitoring/runs/list":
		names = []string{"after", "limit"}
	case "/api/v1/monitoring/runs/get":
		names = []string{"id", "after", "limit"}
	case "/api/v1/monitoring/accounts/get":
		names = []string{"id"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, method, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var input struct {
			MonitoringConfig
			CollectInput
			ID    string `json:"id"`
			After string `json:"after"`
			Limit int    `json:"limit"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/monitoring/config":
			return h.Service.GetMonitoringConfig(ctx, token)
		case "/api/v1/monitoring/config/update":
			return h.Service.UpdateMonitoringConfig(ctx, token, input.MonitoringConfig)
		case "/api/v1/monitoring/status":
			return h.Service.GetMonitoringStatus(ctx, token)
		case "/api/v1/monitoring/collect":
			return h.Service.QueueCollection(ctx, token, input.CollectInput)
		case "/api/v1/monitoring/runs/list":
			return h.Service.ListCollectionRuns(ctx, token, input.After, input.Limit)
		case "/api/v1/monitoring/runs/get":
			return h.Service.GetCollectionRun(ctx, token, input.ID, input.After, input.Limit)
		default:
			return h.Service.GetAccountObservation(ctx, token, input.ID)
		}
	})
}
