package help

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
	var fields []string
	switch r.URL.Path {
	case "/api/v1/help/catalog":
		fields = []string{}
	case "/api/v1/help/read":
		fields = []string{"id", "content_sha256"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, fields, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if r.URL.Path == "/api/v1/help/catalog" {
			return h.Service.Catalog(ctx, token)
		}
		var id, digest string
		if json.Unmarshal(fields["id"], &id) != nil || json.Unmarshal(fields["content_sha256"], &digest) != nil {
			return nil, auth.ErrInput
		}
		return h.Service.Read(ctx, token, id, digest)
	})
}
