package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type PricingHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *PricingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var fields []string
	switch r.URL.Path {
	case "/api/v1/pricing/groups":
		fields = []string{"revision", "source_id", "page", "search"}
	case "/api/v1/pricing/group":
		fields = []string{"revision", "source_id", "group_id", "page"}
	case "/api/v1/pricing/default":
		fields = []string{"revision", "source_id", "model"}
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
		case "/api/v1/pricing/groups":
			var input PricingGroupsInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListPricingGroups(ctx, token, input)
		case "/api/v1/pricing/group":
			var input PricingGroupInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ReadPricingGroup(ctx, token, input)
		default:
			var input PricingDefaultInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ReadPricingDefault(ctx, token, input)
		}
	})
}
