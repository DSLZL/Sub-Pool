package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type PoolsHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func validPoolMemberJSON(fields map[string]json.RawMessage) bool {
	for _, name := range []string{"accounts", "participants"} {
		body, exists := fields[name]
		if !exists {
			continue
		}
		var members []json.RawMessage
		if json.Unmarshal(body, &members) != nil || members == nil {
			return false
		}
		maximum, count := 100, 2
		names := []string{"id", "revision"}
		if name == "participants" {
			maximum, count, names = 500, 3, []string{"id", "revision", "share_percent"}
		}
		if len(members) > maximum {
			return false
		}
		for _, raw := range members {
			object, ok := usageObject(raw)
			if !ok || len(object) != count {
				return false
			}
			for _, key := range names {
				value, exists := object[key]
				if !exists || string(value) == "null" {
					return false
				}
			}
		}
	}
	return true
}

func (h *PoolsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names, optional []string
	switch r.URL.Path {
	case "/api/v1/pools/create":
		names = []string{"source_id", "connection_revision", "name", "enabled", "accounts", "participants"}
	case "/api/v1/pools/update":
		names = []string{"id", "revision", "source_id", "connection_revision", "name", "enabled", "accounts", "participants"}
	case "/api/v1/pools/disable":
		names = []string{"id", "revision"}
	case "/api/v1/pools/list":
		names = []string{"after", "search", "limit"}
	case "/api/v1/pools/get", "/api/v1/pools/contract":
		names = []string{"id"}
	case "/api/v1/pools/history":
		names = []string{"id", "after", "limit"}
	case "/api/v1/pools/usage-evidence":
		names = []string{"capture_id", "after", "limit"}
	case "/api/v1/pools/weekly-wallet":
		names = []string{"id"}
	case "/api/v1/pools/weekly-wallet/configure":
		names = []string{"id", "pool_revision", "revision", "enabled", "auto_allocate"}
	case "/api/v1/pools/weekly-wallet/recommendation":
		names = []string{"id"}
	case "/api/v1/pools/weekly-wallet/apply":
		names = []string{"pool_id", "participant_id", "basis"}
		optional = []string{"cutoff"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSONOptional(w, r, http.MethodPost, names, optional, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if !validPoolMemberJSON(fields) {
			return nil, auth.ErrInput
		}
		var input struct {
			PoolUpdate
			After     string `json:"after"`
			Search    string `json:"search"`
			Limit     int    `json:"limit"`
			CaptureID string `json:"capture_id"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/pools/weekly-wallet":
			return h.Service.GetWeeklyWallet(ctx, token, input.ID)
		case "/api/v1/pools/weekly-wallet/recommendation":
			return h.Service.GetWeeklyWalletRecommendation(ctx, token, input.ID)
		case "/api/v1/pools/weekly-wallet/apply":
			var application WeeklyWalletApplyInput
			if json.Unmarshal(body, &application) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ApplyWeeklyWallet(ctx, token, application)
		case "/api/v1/pools/weekly-wallet/configure":
			var control WeeklyWalletInput
			if json.Unmarshal(body, &control) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.ConfigureWeeklyWallet(ctx, token, control)
		case "/api/v1/pools/create", "/api/v1/pools/update":
			if r.URL.Path == "/api/v1/pools/update" && (!managedID(input.ID) || !validRevision(input.Revision)) {
				return nil, auth.ErrInput
			}
			return h.Service.SavePool(ctx, token, input.PoolUpdate)
		case "/api/v1/pools/disable":
			return h.Service.DisablePool(ctx, token, input.ID, input.Revision)
		case "/api/v1/pools/list":
			return h.Service.ListPools(ctx, token, input.After, input.Search, input.Limit)
		case "/api/v1/pools/get":
			return h.Service.GetPool(ctx, token, input.ID)
		case "/api/v1/pools/contract":
			return h.Service.GetPoolContract(ctx, token, input.ID)
		case "/api/v1/pools/history":
			return h.Service.ListPoolHistory(ctx, token, input.ID, input.After, input.Limit)
		default:
			return h.Service.GetPoolUsageEvidence(ctx, token, input.CaptureID, input.After, input.Limit)
		}
	})
}
