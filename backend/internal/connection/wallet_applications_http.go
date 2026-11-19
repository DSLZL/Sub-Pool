package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type WalletApplicationsHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *WalletApplicationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/wallet-applications/request":
		names = []string{"request_id", "participant_id", "participant_revision", "binding_id", "connection_revision", "source_id", "latest_id", "recommendation_id"}
	case "/api/v1/wallet-applications/adjust":
		names = []string{"request_id", "participant_id", "participant_revision", "binding_id", "connection_revision", "source_id", "latest_id", "recommendation_id", "native_delta"}
	case "/api/v1/wallet-applications/simulate":
		names = []string{"request_id", "participant_id", "participant_revision", "binding_id", "connection_revision", "source_id", "latest_id", "recommendation_id", "simulation"}
	case "/api/v1/wallet-applications/get", "/api/v1/wallet-applications/reconcile":
		names = []string{"id"}
	case "/api/v1/wallet-applications/list":
		names = []string{"participant_id", "start_at", "end_at", "after", "limit"}
	case "/api/v1/wallet-applications/automation":
		names = []string{}
	case "/api/v1/wallet-applications/automation/enable":
		names = []string{"revision", "source_id", "connection_revision", "simulation"}
	case "/api/v1/wallet-applications/automation/disable":
		names = []string{"revision"}
	case "/api/v1/wallet-applications/automation/history":
		names = []string{"after", "limit"}
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
		case "/api/v1/wallet-applications/automation":
			return h.Service.GetWalletAutomation(ctx, token)
		case "/api/v1/wallet-applications/automation/enable", "/api/v1/wallet-applications/automation/disable":
			var in WalletAutomationInput
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			enabled := r.URL.Path == "/api/v1/wallet-applications/automation/enable"
			if enabled {
				var marker SimulationProvenance
				if !simulationObject(fields["simulation"], "protocol environment epoch", &marker) {
					return nil, auth.ErrInput
				}
			}
			return h.Service.ConfigureWalletAutomation(ctx, token, in, enabled)
		case "/api/v1/wallet-applications/automation/history":
			var in struct {
				After string `json:"after"`
				Limit int    `json:"limit"`
			}
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListWalletAutomation(ctx, token, in.After, in.Limit)
		case "/api/v1/wallet-applications/request", "/api/v1/wallet-applications/adjust", "/api/v1/wallet-applications/simulate":
			var in WalletApplicationInput
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			if r.URL.Path == "/api/v1/wallet-applications/adjust" && !validNativeDelta(in.NativeDelta) {
				return nil, auth.ErrInput
			}
			if r.URL.Path == "/api/v1/wallet-applications/simulate" {
				var marker SimulationProvenance
				if !simulationObject(fields["simulation"], "protocol environment epoch", &marker) {
					return nil, auth.ErrInput
				}
			}
			return h.Service.RequestWalletApplication(ctx, token, in)
		case "/api/v1/wallet-applications/get", "/api/v1/wallet-applications/reconcile":
			var in struct {
				ID string `json:"id"`
			}
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			if r.URL.Path == "/api/v1/wallet-applications/reconcile" {
				return h.Service.ReconcileWalletApplication(ctx, token, in.ID)
			}
			return h.Service.GetWalletApplication(ctx, token, in.ID)
		default:
			var in WalletApplicationQuery
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListWalletApplications(ctx, token, in)
		}
	})
}
