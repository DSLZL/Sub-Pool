package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type AccountsHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *AccountsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/accounts/list":
		names = []string{"after", "search", "limit"}
	case "/api/v1/accounts/get":
		names = []string{"id"}
	case "/api/v1/accounts/adopt":
		names = []string{"source_id", "connection_revision", "upstream_id", "name", "monitoring", "source_mode"}
	case "/api/v1/accounts/update":
		names = []string{"id", "revision", "name", "monitoring", "source_mode"}
	case "/api/v1/accounts/refresh", "/api/v1/accounts/models":
		names = []string{"id", "revision", "source_id", "connection_revision"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var input struct {
			AdoptInput
			ID       string `json:"id"`
			Revision string `json:"revision"`
			After    string `json:"after"`
			Search   string `json:"search"`
			Limit    int    `json:"limit"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/accounts/list":
			return h.Service.ListManaged(ctx, token, input.After, input.Search, input.Limit)
		case "/api/v1/accounts/get":
			return h.Service.GetManaged(ctx, token, input.ID)
		case "/api/v1/accounts/adopt":
			return h.Service.Adopt(ctx, token, input.AdoptInput)
		case "/api/v1/accounts/update":
			return h.Service.UpdateManaged(ctx, token, ManagedUpdate{ID: input.ID, Revision: input.Revision, Name: input.Name, Monitoring: input.Monitoring, SourceMode: input.SourceMode})
		case "/api/v1/accounts/refresh":
			return h.Service.RefreshManaged(ctx, token, AccountReadInput{ID: input.ID, Revision: input.Revision, SourceID: input.SourceID, ConnectionRevision: input.ConnectionRevision})
		default:
			return h.Service.ManagedModels(ctx, token, AccountReadInput{ID: input.ID, Revision: input.Revision, SourceID: input.SourceID, ConnectionRevision: input.ConnectionRevision})
		}
	})
}
