package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestConnectionHTTPUsesStrictTrustBoundaryAndSafeFailures(t *testing.T) {
	s, _, token := setup(t)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Service: s, Boundary: boundary}
	valid := `{"revision":"1","base_url":"https://target.example","credential_action":"replace","credential":"test-only-secret","confirmation":""}`
	for _, tc := range []struct {
		method, path, body, origin, cookie string
		status                             int
	}{
		{"GET", "/api/v1/connection", "", "", "", 401},
		{"GET", "/api/v1/connection?x=%GG", "", "", token, 400},
		{"POST", "/api/v1/connection/save", valid, "https://untrusted.example", token, 403},
		{"POST", "/api/v1/connection/save", valid, "", token, 403},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, `"revision":"1"`, `"revision":"1","revision":"1"`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, `"revision"`, `"Revision"`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, `"credential":"test-only-secret"`, `"credential":null`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, `"revision":"1"`, `"revision":1`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, `"revision":"1"`, `"extra":true,"revision":"1"`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, "test-only-secret", `test\ud800`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", strings.Replace(valid, "test-only-secret", strings.Repeat("x", 65536), 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "/api/v1/connection/save", valid, "http://127.0.0.1:3000", token + "; sub_pool_dev_session=" + token, 401},
		{"PUT", "/api/v1/connection/save", valid, "http://127.0.0.1:3000", token, 405},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.cookie != "" {
			r.Header.Set("Cookie", "sub_pool_dev_session="+tc.cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d, want %d; %s", tc.method, tc.path, w.Code, tc.status, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "test-only-secret") {
			t.Fatal("unsafe domain response")
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/connection/save", strings.NewReader(valid))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:3000")
	r.Header.Set("Cookie", "sub_pool_dev_session="+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-only-secret") {
		t.Fatalf("valid save failed: %d %s", w.Code, w.Body.String())
	}
	var result SaveResult
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Configuration.CredentialSet {
		t.Fatal("save did not return safe persisted configuration")
	}
	if err = s.auth.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("revoked session retained write authority")
	}
}

func TestAddressAndCredentialValidation(t *testing.T) {
	s, _ := New(nil, nil, nil, true)
	for _, value := range []string{"https://user:secret@host.test", "https://host.test?x=1", "https://host.test#part", "https://host.test/../api", "https://host.test/%2e%2e/api", "http://external.example", "file:///tmp/key", "https://host.test\\api", "https://host.test:70000"} {
		if _, err := s.normalizeURL(value); err == nil {
			t.Fatalf("accepted unsafe address %q", value)
		}
	}
	if got, err := s.normalizeURL("https://HOST.test/prefix/"); err != nil || got != "https://host.test/prefix" {
		t.Fatal("base path normalization failed")
	}
	for _, value := range []string{"", "Bearer ", "key\r\nx-api-key: leaked", "key with spaces", "密钥"} {
		if _, _, err := credential(value); err == nil {
			t.Fatal("invalid credential header accepted")
		}
	}
	if _, err := New(nil, nil, []byte("short"), true); err == nil {
		t.Fatal("invalid encryption key accepted")
	}
}
