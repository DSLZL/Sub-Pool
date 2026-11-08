package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type MaintenanceHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *MaintenanceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/maintenance/preflight":
		names = []string{"account_id", "from", "until", "kind", "rules_version"}
	case "/api/v1/maintenance/plan":
		names = []string{"id"}
	case "/api/v1/maintenance/plans":
		names = []string{"account_id", "after", "limit"}
	case "/api/v1/maintenance/apply":
		names = []string{"id", "basis", "confirmation"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var in struct {
			MaintenanceInput
			ID           string `json:"id"`
			Basis        string `json:"basis"`
			Confirmation bool   `json:"confirmation"`
			After        string `json:"after"`
			Limit        int    `json:"limit"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &in) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/maintenance/preflight":
			return h.Service.PreflightMaintenance(ctx, token, in.MaintenanceInput)
		case "/api/v1/maintenance/plan":
			return h.Service.GetMaintenancePlan(ctx, token, in.ID)
		case "/api/v1/maintenance/plans":
			return h.Service.ListMaintenancePlans(ctx, token, in.AccountID, in.After, in.Limit)
		default:
			return h.Service.ApplyMaintenancePlan(ctx, token, in.ID, in.Basis, in.Confirmation)
		}
	})
}
