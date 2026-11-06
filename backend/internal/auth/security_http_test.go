package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHTTPRetainsStrictInputAndOptionalPublicBoundary(t *testing.T) {
	s, _ := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	h, _ := NewHandler(s, "http://127.0.0.1:8081", true)
	if w := sourceRequest(h, "/api/v1/security/public", "", "", "127.0.0.1", ""); w.Code != 200 || strings.Contains(w.Body.String(), "revision") {
		t.Fatal("public diagnostic flag requires a session or exposes administrator metadata")
	}
	for _, body := range []string{
		`{}`, `{"revision":"1"}`, `{"revision":"1","network_diagnostics_enabled":null}`,
		`{"revision":"1","network_diagnostics_enabled":"true"}`,
		`{"revision":"1","network_diagnostics_enabled":true,"network_diagnostics_enabled":false}`,
		`{"revision":"1","network_diagnostics_enabled":true,"source_ip":"203.0.113.1"}`,
	} {
		if w := sourceRequest(h, "/api/v1/security/config/update", body, token, "127.0.0.1", ""); w.Code != 400 {
			t.Fatal("invalid required boolean or client source override accepted")
		}
	}
	if w := sourceRequest(h, "/api/v1/security/config?extra=1", "", token, "127.0.0.1", ""); w.Code != 400 {
		t.Fatal("unexpected query accepted")
	}
	if w := sourceRequest(h, "/api/v1/security/config/update", "", token, "127.0.0.1", ""); w.Code != 405 {
		t.Fatal("management write accepted GET")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/security/config/update", strings.NewReader(`{"revision":"1","network_diagnostics_enabled":true}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://attacker.invalid")
	r.Header.Set("Cookie", "sub_pool_dev_session="+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted Origin accepted")
	}
	if w := sourceRequest(h, "/api/v1/security/config", "", token, "127.0.0.1", ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || len(w.Result().Cookies()) != 0 {
		t.Fatal("security reads changed the strict cookie/cache boundary")
	}
}
