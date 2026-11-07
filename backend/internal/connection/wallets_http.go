package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type WalletsHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *WalletsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/wallets/list":
		names = []string{"after", "limit"}
	case "/api/v1/wallets/get":
		names = []string{"participant_id"}
	case "/api/v1/wallets/history":
		names = []string{"participant_id", "kind", "after", "limit"}
	case "/api/v1/wallets/refresh", "/api/v1/wallets/recompute":
		names = []string{"participant_id", "participant_revision", "binding_id", "connection_revision", "source_id", "latest_id"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var in struct {
			WalletExpected
			After string `json:"after"`
			Limit int    `json:"limit"`
			Kind  string `json:"kind"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &in) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/wallets/list":
			return h.Service.ListWallets(ctx, token, in.After, in.Limit)
		case "/api/v1/wallets/get":
			return h.Service.GetWallet(ctx, token, in.ParticipantID)
		case "/api/v1/wallets/history":
			return h.Service.ListWalletHistory(ctx, token, in.ParticipantID, in.Kind, in.After, in.Limit)
		case "/api/v1/wallets/refresh":
			return h.Service.RefreshWallet(ctx, token, in.WalletExpected)
		default:
			return h.Service.RecomputeWallet(ctx, token, in.WalletExpected)
		}
	})
}
