package connection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestWalletStrictHTTPAndNoFinancialOverrides(t *testing.T) {
	f := newWalletFixture(t, true)
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &WalletsHandler{Service: f.s, Boundary: boundary}
	expected, _ := json.Marshal(walletExpected(f.p, f.c, "0"))
	get := `{"participant_id":"` + f.p.ID + `"}`
	for _, c := range []struct {
		method, path, body, token, origin string
		status                            int
	}{
		{"POST", "get", get, f.token, "http://127.0.0.1:3000", 200},
		{"GET", "get", get, f.token, "http://127.0.0.1:3000", 405},
		{"POST", "get", get, "", "http://127.0.0.1:3000", 401},
		{"POST", "refresh", string(expected), f.token, "https://attacker.test", 403},
		{"POST", "apply", `{}`, f.token, "http://127.0.0.1:3000", 404},
		{"POST", "get?extra=%GG", get, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "get", `{"participant_id":null}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "get", `{"participant_id":1}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "get", `{"participant_id":"` + f.p.ID + `","participant_id":"` + f.p.ID + `"}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "refresh", strings.TrimSuffix(string(expected), "}") + `,"complete":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "refresh", strings.TrimSuffix(string(expected), "}") + `,"target":"0"}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "list", `{"after":"","limit":101}`, f.token, "http://127.0.0.1:3000", 400},
	} {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:3000/api/v1/wallets/"+c.path, strings.NewReader(c.body))
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
