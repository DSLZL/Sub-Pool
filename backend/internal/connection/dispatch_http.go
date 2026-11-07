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
		if r.URL.Path == "/api/v1/dispatch/list" {
			var input DispatchListInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListDispatchAccounts(ctx, token, input)
		}
		var input AccountReadInput
		if json.Unmarshal(encoded, &input) != nil {
			return nil, auth.ErrInput
		}
		return h.Service.ReadDispatchAccount(ctx, token, input)
	})
}
