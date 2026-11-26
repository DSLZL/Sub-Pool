package connection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestWalletApplicationStrictHTTPRejectsFinancialOverrides(t *testing.T) {
	f := newWalletFixture(t, true)
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &WalletApplicationsHandler{Service: f.s, Boundary: boundary}
	input, _ := json.Marshal(applicationInput(f, 1))
	list := `{"participant_id":"","start_at":"","end_at":"","after":"0","limit":100}`
	for _, c := range []struct {
		method, path, body, token, origin string
		status                            int
	}{
		{"POST", "request", string(input), f.token, "http://127.0.0.1:3000", 200},
		{"POST", "list", list, f.token, "http://127.0.0.1:3000", 200},
		{"POST", "automation/history", `{"after":"0","limit":25}`, f.token, "http://127.0.0.1:3000", 200},
		{"GET", "automation/history", `{}`, f.token, "http://127.0.0.1:3000", 405},
		{"POST", "automation/history", `{"after":"0","limit":25}`, "", "http://127.0.0.1:3000", 401},
		{"POST", "automation", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "automation/enable", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "automation/disable", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "simulate", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "automation/update", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "automation/update", `{"enabled":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"GET", "list", list, f.token, "http://127.0.0.1:3000", 405},
		{"POST", "request", string(input), "", "http://127.0.0.1:3000", 401},
		{"POST", "request", string(input), f.token, "https://attacker.test", 403},
		{"POST", "apply", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "reconcile", `{}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "request", strings.TrimSuffix(string(input), "}") + `,"target":"0"}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "request", strings.TrimSuffix(string(input), "}") + `,"confirmed":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "request", strings.TrimSuffix(string(input), "}") + `,"complete":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "request", strings.Replace(string(input), `"recommendation_id":"0"`, `"recommendation_id":null`, 1), f.token, "http://127.0.0.1:3000", 400},
		{"POST", "get", `{"id":"1","id":"2"}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "list", strings.Replace(list, `"limit":100`, `"limit":101`, 1), f.token, "http://127.0.0.1:3000", 400},
		{"POST", "list?bad=%GG", list, f.token, "http://127.0.0.1:3000", 400},
	} {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:3000/api/v1/wallet-applications/"+c.path, strings.NewReader(c.body))
		r.Header.Set("Origin", c.origin)
		r.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: c.token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d expected %d: %s", c.path, w.Code, c.status, w.Body.String())
		}
	}
}
