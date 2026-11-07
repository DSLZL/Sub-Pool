package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"sub-pool/backend/internal/auth"
)

type Handler struct {
	Service  *Service
	Boundary *auth.Handler
}

func objectFields(body []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, errors.New("invalid object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		t, e = d.Token()
		name, ok := t.(string)
		if e != nil || !ok || !slices.Contains(names, name) || fields[name] != nil {
			return nil, errors.New("invalid field")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("invalid value")
		}
		fields[name] = value
	}
	if t, e = d.Token(); e != nil || t != json.Delim('}') || len(fields) != len(names) || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("incomplete object")
	}
	return fields, nil
}
func strict(body []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var names []string
	switch r.URL.Path {
	case "/api/v1/notifications/config":
		names = []string{}
	case "/api/v1/notifications/config/update":
		names = []string{"revision", "enabled", "channel", "from", "recipient", "smtp", "preferences", "dedupe_seconds", "credential_action", "credential"}
	case "/api/v1/notifications/test":
		names = []string{"request_id", "revision"}
	case "/api/v1/notifications/get":
		names = []string{"id"}
	case "/api/v1/notifications/list":
		names = []string{"start_at", "end_at", "kind", "participant_id", "subject_type", "subject_id", "state", "after", "limit"}
	default:
		h.Boundary.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, auth.ErrNotFound })
		return
	}
	h.Boundary.HandleJSON(w, r, http.MethodPost, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if r.URL.Path == "/api/v1/notifications/config/update" {
			if _, e := objectFields(fields["smtp"], "host", "port", "tls", "username"); e != nil {
				return nil, auth.ErrInput
			}
			if _, e := objectFields(fields["preferences"], "balance_low", "recommendation_changed", "estimate_changed", "collection_failed"); e != nil {
				return nil, auth.ErrInput
			}
		}
		body, e := json.Marshal(fields)
		if e != nil {
			return nil, auth.ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/notifications/config":
			return h.Service.GetConfiguration(ctx, token)
		case "/api/v1/notifications/config/update":
			var in SaveInput
			if strict(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.SaveConfiguration(ctx, token, in)
		case "/api/v1/notifications/test":
			var in TestInput
			if strict(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.RequestTest(ctx, token, in)
		case "/api/v1/notifications/get":
			var in struct {
				ID string `json:"id"`
			}
			if strict(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.Get(ctx, token, in.ID)
		default:
			var in Query
			if strict(body, &in) != nil {
				return nil, auth.ErrInput
			}
			return h.Service.List(ctx, token, in)
		}
	})
}
