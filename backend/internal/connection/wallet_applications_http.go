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
	case "/api/v1/wallet-applications/get":
		names = []string{"id"}
	case "/api/v1/wallet-applications/list":
		names = []string{"participant_id", "start_at", "end_at", "after", "limit"}
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
		case "/api/v1/wallet-applications/request":
			var in WalletApplicationInput
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.RequestWalletApplication(ctx, token, in)
		case "/api/v1/wallet-applications/get":
			var in struct {
				ID string `json:"id"`
			}
			if decode(body, &in) != nil {
				return nil, auth.ErrInput
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
