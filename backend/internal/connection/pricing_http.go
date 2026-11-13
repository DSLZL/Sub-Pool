package connection

import (
	"bytes"
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
	case "/api/v1/pricing/control":
		fields = []string{"revision", "source_id", "group_id"}
	case "/api/v1/pricing/apply":
		fields = []string{"revision", "source_id", "group_id", "request_id", "expected_fingerprint", "acknowledge_shared_impact", "changes"}
	case "/api/v1/pricing/operation":
		fields = []string{"id"}
	case "/api/v1/pricing/history":
		fields = []string{"source_id", "group_id", "after", "limit"}
	case "/api/v1/pricing/revert":
		fields = []string{"id", "revision", "acknowledge_shared_impact"}
	case "/api/v1/pricing/retry":
		fields = []string{"id", "revision", "request_id", "acknowledge_shared_impact"}
	case "/api/v1/pricing/review":
		fields = []string{"id", "revision", "acknowledge_result_unknown"}
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
		case "/api/v1/pricing/default":
			var input PricingDefaultInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ReadPricingDefault(ctx, token, input)
		case "/api/v1/pricing/control":
			var input PricingControlInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.GetPricingControl(ctx, token, input)
		case "/api/v1/pricing/apply":
			var items []json.RawMessage
			if decode(body["changes"], &items) != nil {
				return nil, auth.ErrInput
			}
			for _, item := range items {
				fields, ok := usageObject(item)
				var kind string
				if !ok || json.Unmarshal(fields["kind"], &kind) != nil {
					return nil, auth.ErrInput
				}
				keys := map[string][]string{"fast": {"kind", "force_fast", "free_fast", "value"}, "long_context": {"kind", "enabled", "value"}, "model_price": {"kind", "rule_index", "price_field", "value"}}[kind]
				if len(keys) == 0 || len(keys) != len(fields) {
					return nil, auth.ErrInput
				}
				for _, key := range keys {
					if fields[key] == nil {
						return nil, auth.ErrInput
					}
				}
			}
			var input PricingApplyInput
			d := json.NewDecoder(bytes.NewReader(encoded))
			d.DisallowUnknownFields()
			if d.Decode(&input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ApplyPricing(ctx, token, input)
		case "/api/v1/pricing/operation":
			var input struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.GetPricingOperation(ctx, token, input.ID)
		case "/api/v1/pricing/history":
			var input PricingHistoryInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ListPricingHistory(ctx, token, input)
		case "/api/v1/pricing/revert":
			var input PricingOperationInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.RevertPricing(ctx, token, input)
		case "/api/v1/pricing/retry":
			var input PricingRetryInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.RetryPricing(ctx, token, input)
		default:
			var input PricingReviewInput
			if json.Unmarshal(encoded, &input) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ReviewPricing(ctx, token, input)
		}
	})
}
