package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

// HandleJSON keeps domain routes on the same strict cookie, Origin and error boundary.
func (h *Handler) HandleJSON(w http.ResponseWriter, r *http.Request, method string, names []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	h.HandleJSONLimit(w, r, method, 65536, names, run)
}

// HandleJSONLimit changes only the body bound for domains that transport protected files.
func (h *Handler) HandleJSONLimit(w http.ResponseWriter, r *http.Request, method string, limit int64, names []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	h.handleJSON(w, r, method, limit, names, nil, run)
}

func (h *Handler) HandleJSONOptional(w http.ResponseWriter, r *http.Request, method string, names, optional []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	h.handleJSON(w, r, method, 65536, names, optional, run)
}

func (h *Handler) handleJSON(w http.ResponseWriter, r *http.Request, method string, limit int64, names, optional []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(ctx, w, ErrInput)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		failure(ctx, w, &Failure{Status: 405, Code: "METHOD_NOT_SUPPORTED"})
		return
	}
	r, token, err := h.domainCredentials(r)
	if err != nil {
		failure(ctx, w, err)
		return
	}
	_, machine := r.Context().Value(machineKey{}).(machineCredential)
	var fields map[string]json.RawMessage
	if method == http.MethodPost {
		if !machine && (len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.origin) {
			failure(ctx, w, ErrForbidden)
			return
		}
		var err error
		fields, err = readFieldsWithOptional(w, r, limit, names, optional)
		if err != nil {
			failure(ctx, w, err)
			return
		}
	} else if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		failure(ctx, w, ErrInput)
		return
	}
	r, err = h.prepareSource(r)
	if err != nil {
		failure(ctx, w, err)
		return
	}
	result, err := run(r.Context(), token, fields)
	if err != nil {
		failure(ctx, w, err)
		return
	}
	reply(w, http.StatusOK, result)
}
