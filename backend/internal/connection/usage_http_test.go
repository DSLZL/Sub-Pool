package connection

import (
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestUsageHTTPUsesStrictCurrentTrustBoundary(t *testing.T) {
	s, _, token := setup(t)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &UsageHandler{Service: s, Boundary: boundary}
	valid := `{"account_id":"AAAAAAAAAAAAAAAAAAAAAA","after":"0","limit":100}`
	for _, tc := range []struct {
		method, action, body, origin, cookie string
		status                               int
	}{
		{"GET", "captures/list", "", "", token, 405},
		{"POST", "captures/list", valid, "", token, 403},
		{"POST", "captures/list", valid, "https://untrusted.example", token, 403},
		{"POST", "captures/list", valid, "http://127.0.0.1:3000", "", 401},
		{"POST", "captures/list", strings.Replace(valid, `"limit":100`, `"limit":null`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list", strings.Replace(valid, `"limit":100`, `"limit":100,"limit":1`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list", strings.Replace(valid, `"account_id"`, `"Account_id"`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list", strings.Replace(valid, `"after":"0"`, `"after":"01"`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list", strings.Replace(valid, `"limit":100`, `"limit":101`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list", `{"account_id":"AAAAAAAAAAAAAAAAAAAAAA","limit":1}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list?hidden=%GG", valid, "http://127.0.0.1:3000", token, 400},
		{"POST", "captures/list", valid, "http://127.0.0.1:3000", token + "; sub_pool_dev_session=" + token, 401},
		{"POST", "captures/list", valid, "http://127.0.0.1:3000", token, 200},
		{"POST", "capture", `{"id":"AAAAAAAAAAAAAAAAAAAAAA","revision":"1","source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","start_at":"2026-11-01T00:00:00Z","end_at":"2026-12-03T00:00:00Z"}`, "http://127.0.0.1:3000", token, 400},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/usage/"+tc.action, strings.NewReader(tc.body))
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
