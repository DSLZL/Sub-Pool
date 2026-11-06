package connection

import (
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestParticipantHTTPUsesExistingStrictCurrentBoundary(t *testing.T) {
	s, _, token := setup(t)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &ParticipantsHandler{Service: s, Boundary: boundary}
	valid := `{"after":"","search":"","limit":100}`
	for _, tc := range []struct {
		method, action, body, origin, cookie string
		status                               int
	}{
		{"GET", "list", "", "", token, 405},
		{"POST", "list", valid, "", token, 403},
		{"POST", "list", valid, "https://untrusted.example", token, 403},
		{"POST", "list", valid, "http://127.0.0.1:3000", "", 401},
		{"POST", "list", strings.Replace(valid, `"limit":100`, `"limit":null`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "list", strings.Replace(valid, `"limit":100`, `"limit":100,"limit":1`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "list", strings.Replace(valid, `"after"`, `"After"`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "list", `{"after":"","limit":100}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "list?hidden=%GG", valid, "http://127.0.0.1:3000", token, 400},
		{"POST", "list", valid, "http://127.0.0.1:3000", token + "; sub_pool_dev_session=" + token, 401},
		{"POST", "list", valid, "http://127.0.0.1:3000", token, 200},
		{"POST", "usage/view", `{"capture_ids":[],"participant_id":"","model":"","api_key_id":"","grain":"day","after":"0","limit":100}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "usage/view", `{"capture_ids":["1","1"],"participant_id":"","model":"","api_key_id":"","grain":"day","after":"0","limit":100}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "usage/view", `{"capture_ids":["1"],"participant_id":"","model":"","api_key_id":"","grain":"day","after":"1.bad","limit":100}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "create", `{"role":"admin"}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "update", `{"id":"","revision":"","source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","upstream_id":"21","name":"Member","contact":"","notes":"","enabled":true}`, "http://127.0.0.1:3000", token, 400},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/participants/"+tc.action, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.cookie != "" {
			r.Header.Set("Cookie", "sub_pool_dev_session="+tc.cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s %s: %d want %d; %s", tc.method, tc.action, w.Code, tc.status, w.Body.String())
		}
	}
}
