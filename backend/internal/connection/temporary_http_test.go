package connection

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestTemporaryHTTPRejectsEnablingOverridesAndUndeclaredMutations(t *testing.T) {
	f := newWalletFixture(t, true)
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &TemporaryHandler{Service: f.s, Boundary: boundary}
	for _, c := range []struct {
		method, path, body, token, origin string
		status                            int
	}{
		{"POST", "status", `{}`, f.token, "http://127.0.0.1:3000", 200},
		{"GET", "status", `{}`, f.token, "http://127.0.0.1:3000", 405},
		{"POST", "status", `{}`, "", "http://127.0.0.1:3000", 401},
		{"POST", "status", `{}`, f.token, "https://attacker.test", 403},
		{"POST", "status", `{"enabled":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status", `{"can_start":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status", `{"mode":"carry"}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status", `{"mode":"no_carry","notified":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status", `{"target":"100","complete":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status", `null`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status", `{} {}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "status?bad=%GG", `{}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "start", `{}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "stop", `{}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "history", `{"after":"","limit":1}`, f.token, "http://127.0.0.1:3000", 200},
		{"POST", "history", `{"after":"","limit":0}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "history", `{"after":"","limit":1,"complete":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "history", `{"after":"","limit":1}`, "", "http://127.0.0.1:3000", 401},
		{"POST", "history", `{"after":"","limit":1}`, f.token, "https://attacker.test", 403},
		{"POST", "rights/update", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "reminders/update", `{}`, f.token, "http://127.0.0.1:3000", 404},
	} {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:3000/api/v1/temporary/"+c.path, strings.NewReader(c.body))
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
