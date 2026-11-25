package backups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"sub-pool/backend/internal/auth"
)

type Handler struct {
	Service  *Service
	Boundary *auth.Handler
}

func LongOperation(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.EscapedPath() != r.URL.Path {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/backups/export", "/api/v1/backups/preflight", "/api/v1/backups/preflight-file",
		"/api/v1/backups/download", "/api/v1/backups/apply":
		return true
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/backups/preflight-file" || r.URL.Path == "/api/v1/backups/download" {
		h.Boundary.HandleBrowserTransfer(w, r, h.transfer)
		return
	}
	var fields, optional []string
	limit := int64(65536)
	switch r.URL.Path {
	case "/api/v1/backups/export":
		fields = []string{"request_id", "include_secrets"}
		optional = []string{"scope"}
	case "/api/v1/backups/get":
		fields = []string{"id"}
	case "/api/v1/backups/list":
		fields = []string{"after", "limit"}
	case "/api/v1/backups/preflight":
		fields = []string{"request_id"}
		optional = []string{"content", "archive_id"}
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
	run := func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		if r.URL.EscapedPath() != r.URL.Path {
			return nil, auth.ErrInput
		}
		if !LongOperation(r) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
		}
		body, err := json.Marshal(fields)
		if err != nil {
			return nil, auth.ErrInput
		}
		var in struct {
			RequestID      string  `json:"request_id"`
			IncludeSecrets bool    `json:"include_secrets"`
			Scope          *string `json:"scope"`
			ID             string  `json:"id"`
			After          string  `json:"after"`
			Limit          int     `json:"limit"`
			Content        string  `json:"content"`
			ArchiveID      string  `json:"archive_id"`
			TargetBasis    string  `json:"target_basis"`
			Acknowledged   bool    `json:"acknowledged"`
		}
		if json.Unmarshal(body, &in) != nil {
			return nil, auth.ErrInput
		}
		var out any
		switch r.URL.Path {
		case "/api/v1/backups/export":
			if in.Scope == nil {
				out, err = h.Service.Export(ctx, token, in.RequestID, in.IncludeSecrets)
			} else if *in.Scope == fullScope || *in.Scope == lightScope {
				out, err = h.Service.export(ctx, token, in.RequestID, in.IncludeSecrets, *in.Scope)
			} else {
				err = auth.ErrInput
			}
		case "/api/v1/backups/get":
			out, err = h.Service.GetFile(ctx, token, in.ID)
		case "/api/v1/backups/list":
			out, err = h.Service.List(ctx, token, in.After, in.Limit)
		case "/api/v1/backups/preflight":
			if _, stored := fields["archive_id"]; stored {
				if _, uploaded := fields["content"]; uploaded || !objectID.MatchString(in.ArchiveID) {
					return nil, auth.ErrInput
				}
				out, err = h.Service.preflightStream(ctx, token, in.RequestID, nil, in.ArchiveID)
			} else if _, uploaded := fields["content"]; uploaded {
				out, err = h.Service.Preflight(ctx, token, in.RequestID, in.Content)
			} else {
				err = auth.ErrInput
			}
		case "/api/v1/backups/plan":
			out, err = h.Service.GetPlan(ctx, token, in.ID)
		case "/api/v1/backups/apply":
			out, err = h.Service.Apply(ctx, token, ApplyInput{ID: in.ID, TargetBasis: in.TargetBasis, Acknowledged: in.Acknowledged})
		case "/api/v1/backups/checks":
			out, err = h.Service.Checks(ctx, token, in.ID)
		case "/api/v1/backups/review":
			out, err = h.Service.Review(ctx, token, in.ID, in.Acknowledged)
		}
		return out, backupRequestError(err)
	}
	h.Boundary.HandleJSONLimit(w, r, http.MethodPost, limit, fields, optional, run)
}

func backupRequestError(err error) error {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return ErrLimit
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "40001" || pg.Code == "40P01") {
		return auth.ErrConflict
	}
	return err
}

func (h *Handler) transfer(w http.ResponseWriter, r *http.Request, token string) (any, error) {
	if r.URL.EscapedPath() != r.URL.Path || len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		return nil, auth.ErrInput
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, auth.ErrInput
	}
	if r.URL.Path == "/api/v1/backups/preflight-file" {
		if media != "application/octet-stream" || len(params) != 0 || len(r.Header.Values("X-Backup-Request-ID")) != 1 {
			return nil, auth.ErrInput
		}
		if r.ContentLength > h.Service.MaxBytes {
			return nil, ErrLimit
		}
		plan, err := h.Service.preflightFile(r.Context(), token, r.Header.Get("X-Backup-Request-ID"), http.MaxBytesReader(w, r.Body, h.Service.MaxBytes))
		return plan, backupRequestError(err)
	}
	if media != "application/x-www-form-urlencoded" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		return nil, auth.ErrInput
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return nil, auth.ErrInput
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || len(form["id"]) != 1 || !objectID.MatchString(form.Get("id")) {
		return nil, auth.ErrInput
	}
	planID := form.Get("plan_id")
	for key, values := range form {
		if key != "id" && (key != "plan_id" || len(values) != 1 || !objectID.MatchString(planID)) {
			return nil, auth.ErrInput
		}
	}
	return nil, backupRequestError(h.Service.download(r.Context(), w, token, form.Get("id"), planID))
}
