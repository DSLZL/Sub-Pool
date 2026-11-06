package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

// HandleJSON keeps domain routes on the same strict cookie, Origin and error boundary.
func (h *Handler) HandleJSON(w http.ResponseWriter, r *http.Request, method string, names []string, run func(context.Context, string, map[string]json.RawMessage) (any, error)) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, ErrInput)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		failure(w, &Failure{Status: 405, Code: "METHOD_NOT_SUPPORTED"})
		return
	}
	var fields map[string]json.RawMessage
	if method == http.MethodPost {
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.origin {
			failure(w, ErrForbidden)
			return
		}
		var err error
		fields, err = readFields(w, r, 65536, names...)
		if err != nil {
			failure(w, err)
			return
		}
	} else if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		failure(w, ErrInput)
		return
	}
	result, err := run(r.Context(), h.token(r), fields)
	if err != nil {
		failure(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}
