package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(h http.Handler, method, path, body, origin, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPTrustAndStrictJSON(t *testing.T) {
	origin := "http://127.0.0.1:8081"
	h, err := NewHandler(New(nil), origin, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "null", "https://attacker.invalid", origin + "/", origin + ", https://attacker.invalid"} {
		if w := request(h, "POST", "/api/v1/auth/login", `{"username":"admin","password":"TestPassword123"}`, bad, ""); w.Code != 403 {
			t.Fatal("accepted untrusted Origin")
		}
	}
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"username":"admin","password":null}`,
		`{"username":"admin","password":123}`, `{"username":"admin","password":{}}`,
		`{"username":"admin","password":"x","password":"y"}`,
		`{"username":"admin","Password":"x"}`, `{"username":"admin","password":"x","extra":"x"}`,
		`{"username":"admin","password":"x"} {}`, `{"username":"admin","password":"\ud800"}`,
		`{"username":"admin","password":"\udfff"}`, strings.Repeat(" ", 4097),
		string([]byte{'{', '"', 0xff, '"', ':', '"', 'x', '"', '}'}),
	} {
		if w := request(h, "POST", "/api/v1/auth/login", body, origin, ""); w.Code != 400 {
			t.Fatalf("strict input accepted malformed body: status %d", w.Code)
		}
	}
	for _, contentType := range []string{"", "text/plain", "application/json; charset=latin1", "application/json; other=utf-8"} {
		r := httptest.NewRequest("POST", "/api/v1/auth/logout", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("accepted undeclared media type")
		}
	}
	for _, path := range []string{"/api/v1/auth/session?extra=%GG", "/api/v1/auth/session?%=hidden", "/api/v1/auth/session?"} {
		if w := request(h, "GET", path, "", "", ""); w.Code != 400 {
			t.Fatal("ignored unexpected query")
		}
	}
	if w := request(h, "GET", "/api/v1/auth/session", "", "", ""); w.Code != 401 {
		t.Fatal("missing cookie authenticated")
	}
	if w := request(h, "GET", "/api/v1/auth/session", "", "", "sub_pool_dev_session=forged"); w.Code != 401 {
		t.Fatal("forged cookie authenticated")
	}
	if w := request(h, "POST", "/api/v1/auth/logout", `{}`, origin, ""); w.Code != 200 {
		t.Fatal("absent logout failed")
	}
	if w := request(h, "GET", "/api/v1/auth/login", "", "", ""); w.Code != 405 {
		t.Fatal("unsafe action accepted GET")
	}
	if w := request(h, "POST", "/api/v1/auth/bootstrap", `{}`, origin, ""); w.Code != 404 {
		t.Fatal("bootstrap exposed publicly")
	}
	for _, origin := range []string{"https://example.com/", "https://user:password@example.com", "https://example.com?x=1", "https://example.com#x"} {
		if _, err := NewHandler(New(nil), origin, true); err == nil {
			t.Fatal("accepted invalid origin configuration")
		}
	}
	for _, body := range []string{`{"password":"\ud83d\ude00"}`, `{"password":"literal\\ud800"}`} {
		if !validJSONUnicode([]byte(body)) {
			t.Fatal("valid Unicode or literal backslash rejected")
		}
	}
}

func TestHTTPOriginModes(t *testing.T) {
	service := New(nil)
	for _, test := range []struct {
		origin       string
		allowLocal   bool
		cookieName   string
		secureCookie bool
	}{
		{"https://example.test", false, "__Host-sub_pool_session", true},
		{"https://example.test", true, "__Host-sub_pool_session", true},
		{"http://127.0.0.1:8081", true, "sub_pool_dev_session", false},
		{"http://[::1]:8081", true, "sub_pool_dev_session", false},
		{"http://127.0.0.1:8081", false, "", false},
		{"http://[::1]:8081", false, "", false},
		{"http://example.com", false, "", false},
		{"http://example.com", true, "", false},
	} {
		handler, err := NewHandler(service, test.origin, test.allowLocal)
		if test.cookieName == "" {
			if err == nil {
				t.Errorf("accepted %s with loopback HTTP set to %t", test.origin, test.allowLocal)
			}
			continue
		}
		if err != nil {
			t.Errorf("rejected %s with loopback HTTP set to %t: %v", test.origin, test.allowLocal, err)
			continue
		}
		if handler.cookieName != test.cookieName || handler.secure != test.secureCookie {
			t.Errorf("wrong cookie mode for %s with loopback HTTP set to %t", test.origin, test.allowLocal)
		}
	}
}

func TestHTTPRealSessionCookieAndPasswordFlow(t *testing.T) {
	s, pool := setup(t)
	user := bootstrap(t, s)
	origin := "https://console.example.test"
	h, err := NewHandler(s, origin, false)
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/api/v1/auth/login", `{"username":" ADMIN ","password":"OriginalPassword123"}`, origin, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("login failed")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Domain != "" || c.Path != "/" || c.Name != "__Host-sub_pool_session" {
		t.Fatal("unsafe production cookie")
	}
	if strings.Contains(w.Body.String(), c.Value) || strings.Contains(w.Body.String(), testPassword) || strings.Contains(w.Body.String(), "digest") {
		t.Fatal("login exposed secret")
	}
	var view Session
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.User.ID != user.ID {
		t.Fatal("invalid session response")
	}
	cookie := c.Name + "=" + c.Value
	if w := request(h, "GET", "/api/v1/auth/session", "", "", cookie+"; "+cookie); w.Code != 401 {
		t.Fatal("ambiguous cookie accepted")
	}
	for _, duplicate := range []string{c.Name + "=invalid value", c.Name + " =bad", c.Name} {
		if w := request(h, "GET", "/api/v1/auth/session", "", "", cookie+"; "+duplicate); w.Code != 401 {
			t.Fatal("malformed duplicate cookie was silently ignored")
		}
	}
	if w := request(h, "GET", "/api/v1/auth/session", "", "", cookie); w.Code != 200 {
		t.Fatal("session restore failed")
	}
	if w := request(h, "POST", "/api/v1/auth/renew", `{}`, origin, cookie); w.Code != 200 {
		t.Fatal("renew failed")
	}
	if w := request(h, "POST", "/api/v1/auth/password", `{"current_password":"OriginalPassword123","new_password":"NewPassword45678"}`, origin, cookie); w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("password flow did not clear cookie")
	}
	if w := request(h, "GET", "/api/v1/auth/session", "", "", cookie); w.Code != 401 {
		t.Fatal("old cookie authenticated after password change")
	}
	// Forwarded headers cannot choose the persistent source-IP bucket.
	r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"WrongPassword123"}`))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	r.RemoteAddr = "127.0.0.1:1234"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM auth_login_limits WHERE bucket=$1)`, bucketKey("source", "203.0.113.1")).Scan(&exists); err != nil || exists {
		t.Fatal("untrusted proxy header controlled rate limit")
	}
	secretError := httptest.NewRecorder()
	failure(context.Background(), secretError, io.ErrUnexpectedEOF)
	if secretError.Code != 500 || strings.Contains(secretError.Body.String(), "EOF") {
		t.Fatal("internal error leaked")
	}
	if err := s.Logout(context.Background(), c.Value); err != nil {
		t.Fatal(err)
	}
}
