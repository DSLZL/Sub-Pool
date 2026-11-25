package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

// HandleJSON keeps domain routes on the same strict cookie, Origin and error boundary.
func (h *Handler) HandleJSON(w http.ResponseWriter, r *http.Request, method string, names []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	h.HandleJSONLimit(w, r, method, 65536, names, nil, run)
}

// HandleJSONLimit changes only the body bound for domains that transport protected files.
func (h *Handler) HandleJSONLimit(w http.ResponseWriter, r *http.Request, method string, limit int64, names, optional []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	h.handleJSON(w, r, method, limit, names, optional, run)
}

func (h *Handler) HandleJSONOptional(w http.ResponseWriter, r *http.Request, method string, names, optional []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	h.handleJSON(w, r, method, 65536, names, optional, run)
}

func (h *Handler) domainRequest(w http.ResponseWriter, r *http.Request, method string) (*http.Request, string, error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return r, "", ErrInput
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		return r, "", &Failure{Status: 405, Code: "METHOD_NOT_SUPPORTED"}
	}
	req, token, err := h.domainCredentials(r)
	if err != nil {
		return r, "", err
	}
	_, machine := req.Context().Value(machineKey{}).(machineCredential)
	if method == http.MethodPost && !machine && (len(req.Header.Values("Origin")) != 1 || req.Header.Get("Origin") != h.origin) {
		return req, "", ErrForbidden
	}
	return req, token, nil
}

// HandleBrowserTransfer lets a browser-only POST stream a request or response on the domain boundary.
func (h *Handler) HandleBrowserTransfer(w http.ResponseWriter, r *http.Request, run func(http.ResponseWriter, *http.Request, string) (any, error)) {
	req, token, err := h.domainRequest(w, r, http.MethodPost)
	if err == nil && len(req.Header.Values("Authorization")) != 0 {
		err = ErrForbidden
	}
	if err == nil {
		req, err = h.prepareSource(req)
	}
	if err != nil {
		failure(r.Context(), w, err)
		return
	}
	result, err := run(w, req, token)
	if err != nil {
		failure(req.Context(), w, err)
	} else if result != nil {
		reply(w, http.StatusOK, result)
	}
}

func (h *Handler) handleJSON(w http.ResponseWriter, r *http.Request, method string, limit int64, names, optional []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	ctx := r.Context()
	r, token, err := h.domainRequest(w, r, method)
	if err != nil {
		failure(ctx, w, err)
		return
	}
	var fields map[string]json.RawMessage
	if method == http.MethodPost {
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
