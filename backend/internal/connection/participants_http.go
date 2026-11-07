package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"sub-pool/backend/internal/auth"
)

type ParticipantsHandler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *ParticipantsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/participants/candidates":
		names = []string{"source_id", "revision", "page", "search"}
	case "/api/v1/participants/create":
		names = []string{"source_id", "connection_revision", "upstream_id", "name", "contact", "notes", "enabled"}
	case "/api/v1/participants/update":
		names = []string{"id", "revision", "source_id", "connection_revision", "upstream_id", "name", "contact", "notes", "enabled"}
	case "/api/v1/participants/list":
		names = []string{"after", "search", "limit"}
	case "/api/v1/participants/get":
		names = []string{"id"}
	case "/api/v1/participants/delete":
		names = []string{"id", "revision", "confirmation"}
	case "/api/v1/participants/bindings":
		names = []string{"id", "after", "limit"}
	case "/api/v1/participants/usage/captures":
		names = []string{"account_id", "after", "limit"}
	case "/api/v1/participants/usage/view", "/api/v1/participants/usage/window", "/api/v1/participants/usage/history":
		names = []string{"capture_ids", "participant_id", "model", "api_key_id", "grain", "after", "limit"}
		if r.URL.Path != "/api/v1/participants/usage/view" {
			names = append(names, "start_at", "end_at")
		}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var input struct {
			ParticipantUpdate
			After         string   `json:"after"`
			Search        string   `json:"search"`
			Limit         int      `json:"limit"`
			Page          int      `json:"page"`
			Confirmation  string   `json:"confirmation"`
			AccountID     string   `json:"account_id"`
			CaptureIDs    []string `json:"capture_ids"`
			ParticipantID string   `json:"participant_id"`
			Model         string   `json:"model"`
			APIKeyID      string   `json:"api_key_id"`
			Grain         string   `json:"grain"`
			StartAt       string   `json:"start_at"`
			EndAt         string   `json:"end_at"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/participants/candidates":
			return h.Service.DiscoverUsers(ctx, token, DiscoverInput{SourceID: input.SourceID, Revision: input.Revision, Page: input.Page, Search: input.Search})
		case "/api/v1/participants/create", "/api/v1/participants/update":
			if r.URL.Path == "/api/v1/participants/update" && (!managedID(input.ID) || !validRevision(input.Revision)) {
				return nil, auth.ErrInput
			}
			return h.Service.SaveParticipant(ctx, token, input.ParticipantUpdate)
		case "/api/v1/participants/list":
			return h.Service.ListParticipants(ctx, token, input.After, input.Search, input.Limit)
		case "/api/v1/participants/get":
			return h.Service.GetParticipant(ctx, token, input.ID)
		case "/api/v1/participants/delete":
			err := h.Service.DeleteParticipant(ctx, token, input.ID, input.Revision, input.Confirmation)
			return map[string]bool{"deleted": err == nil}, err
		case "/api/v1/participants/bindings":
			return h.Service.ListParticipantBindings(ctx, token, input.ID, input.After, input.Limit)
		case "/api/v1/participants/usage/captures":
			return h.Service.ListAttributedCaptures(ctx, token, input.AccountID, input.After, input.Limit)
		default:
			if r.URL.Path != "/api/v1/participants/usage/view" && (input.StartAt == "" || input.EndAt == "") {
				return nil, auth.ErrInput
			}
			query := AttributedUsageQuery{CaptureIDs: input.CaptureIDs, ParticipantID: input.ParticipantID, Model: input.Model, APIKeyID: input.APIKeyID, Grain: input.Grain, After: input.After, Limit: input.Limit, StartAt: input.StartAt, EndAt: input.EndAt}
			if r.URL.Path == "/api/v1/participants/usage/history" {
				return h.Service.GetUsageHistory(ctx, token, query)
			}
			return h.Service.GetAttributedUsage(ctx, token, query)
		}
	})
}
