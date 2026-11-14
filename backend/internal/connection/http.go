package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type Handler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	var names []string
	switch r.URL.Path {
	case "/api/v1/connection", "/api/v1/connection/impact", "/api/v1/settings":
		method = http.MethodGet
	case "/api/v1/connection/save":
		names = []string{"revision", "base_url", "credential_action", "credential", "confirmation"}
	case "/api/v1/connection/test":
		names = []string{"revision", "base_url", "credential"}
	case "/api/v1/connection/discover":
		names = []string{"revision", "source_id", "page", "search"}
	case "/api/v1/settings/update":
		names = []string{"revision", "timezone"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, method, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var input struct {
			SaveInput
			SourceID string `json:"source_id"`
			Page     int    `json:"page"`
			Search   string `json:"search"`
			Timezone string `json:"timezone"`
		}
		if method == http.MethodPost {
			body, err := json.Marshal(fields)
			if err != nil || json.Unmarshal(body, &input) != nil {
				return nil, auth.ErrInput
			}
		}
		switch r.URL.Path {
		case "/api/v1/connection":
			return h.Service.Get(ctx, token)
		case "/api/v1/connection/impact":
			return h.Service.GetConnectionImpact(ctx, token)
		case "/api/v1/connection/save":
			return h.Service.Save(ctx, token, input.SaveInput)
		case "/api/v1/connection/test":
			return h.Service.Test(ctx, token, TestInput{Revision: input.Revision, BaseURL: input.BaseURL, Credential: input.Credential})
		case "/api/v1/connection/discover":
			return h.Service.Discover(ctx, token, DiscoverInput{Revision: input.Revision, SourceID: input.SourceID, Page: input.Page, Search: input.Search})
		case "/api/v1/settings":
			return h.Service.GetSettings(ctx, token)
		default:
			return h.Service.UpdateSettings(ctx, token, input.Revision, input.Timezone)
		}
	})
}
