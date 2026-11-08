package backups

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"sub-pool/backend/internal/auth"
)

type Handler struct {
	Service  *Service
	Boundary *auth.Handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var fields []string
	limit := int64(65536)
	switch r.URL.Path {
	case "/api/v1/backups/export":
		fields = []string{"request_id", "include_secrets"}
	case "/api/v1/backups/get":
		fields = []string{"id"}
	case "/api/v1/backups/list":
		fields = []string{"after", "limit"}
	case "/api/v1/backups/preflight":
		fields = []string{"request_id", "content"}
		limit = maxJSON
	case "/api/v1/backups/plan", "/api/v1/backups/checks":
		fields = []string{"id"}
	case "/api/v1/backups/apply":
		fields = []string{"id", "target_basis", "acknowledged"}
	case "/api/v1/backups/review":
		fields = []string{"id", "acknowledged"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSONLimit(w, r, http.MethodPost, limit, fields, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		body, err := json.Marshal(fields)
		if err != nil {
			return nil, auth.ErrInput
		}
		var in struct {
			RequestID      string `json:"request_id"`
			IncludeSecrets bool   `json:"include_secrets"`
			ID             string `json:"id"`
			After          string `json:"after"`
			Limit          int    `json:"limit"`
			Content        string `json:"content"`
			TargetBasis    string `json:"target_basis"`
			Acknowledged   bool   `json:"acknowledged"`
		}
		if json.Unmarshal(body, &in) != nil {
			return nil, auth.ErrInput
		}
		var out any
		switch r.URL.Path {
		case "/api/v1/backups/export":
			out, err = h.Service.Export(ctx, token, in.RequestID, in.IncludeSecrets)
		case "/api/v1/backups/get":
			out, err = h.Service.GetFile(ctx, token, in.ID)
		case "/api/v1/backups/list":
			out, err = h.Service.List(ctx, token, in.After, in.Limit)
		case "/api/v1/backups/preflight":
			out, err = h.Service.Preflight(ctx, token, in.RequestID, in.Content)
		case "/api/v1/backups/plan":
			out, err = h.Service.GetPlan(ctx, token, in.ID)
		case "/api/v1/backups/apply":
			out, err = h.Service.Apply(ctx, token, ApplyInput{ID: in.ID, TargetBasis: in.TargetBasis, Acknowledged: in.Acknowledged})
		case "/api/v1/backups/checks":
			out, err = h.Service.Checks(ctx, token, in.ID)
		case "/api/v1/backups/review":
			out, err = h.Service.Review(ctx, token, in.ID, in.Acknowledged)
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "40001" || pg.Code == "40P01") {
			err = auth.ErrConflict
		}
		return out, err
	})
}
