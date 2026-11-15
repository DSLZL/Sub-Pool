package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func keyRequest(h http.Handler, method, path, body, secret, cookie, origin, peer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAPIKeyCredentialParserIsClosedAndUnambiguous(t *testing.T) {
	h, err := NewHandler(New(nil), "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	secret := apiKeyPrefix + strings.Repeat("A", 22) + "_" + strings.Repeat("A", 43)
	for _, c := range []struct {
		path, method, authorization, cookie string
		status                              int
	}{
		{"/api/v1/api-keys/access", "GET", "Bearer " + secret, "", 0},
		{"/api/v1/wallet-applications/request", "POST", "Bearer " + secret, "", 0},
		{"/api/v1/api-keys/create", "POST", "Bearer " + secret, "", 403},
		{"/api/v1/accounts/refresh", "POST", "Bearer " + secret, "", 403},
		{"/api/v1/connection", "GET", "Bearer " + secret, "", 403},
		{"/api/v1/future/read", "POST", "Bearer " + secret, "", 403},
		{"/api/v1/api-keys/access", "GET", secret, "", 401},
		{"/api/v1/api-keys/access", "GET", "bearer " + secret, "", 401},
		{"/api/v1/api-keys/access", "GET", "Bearer " + secret + " ", "", 401},
		{"/api/v1/api-keys/access", "GET", "Bearer " + secret, "malformed", 401},
		{"/api/v1/api-keys/access", "GET", "Bearer " + secret, "sub_pool_dev_session=valid", 401},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Authorization", c.authorization)
		if c.cookie != "" {
			r.Header.Set("Cookie", c.cookie)
		}
		parsed, token, err := h.domainCredentials(r)
		if c.status == 0 {
			credential, ok := parsed.Context().Value(machineKey{}).(machineCredential)
			if err != nil || token != secret || !ok || credential.Management != (c.path == "/api/v1/wallet-applications/request") {
				t.Fatal("valid closed operation failed", err)
			}
		} else {
			known, ok := err.(*Failure)
			if !ok || known.Status != c.status {
				t.Fatal("ambiguous credential accepted", c.path, err)
			}
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/api-keys/access", nil)
	r.Header.Add("Authorization", "Bearer "+secret)
	r.Header.Add("Authorization", "Bearer "+secret)
	if _, _, err = h.domainCredentials(r); err == nil {
		t.Fatal("duplicate authorization accepted")
	}
}

func TestAPIKeyHTTPStrictLifecycleSourceAndNoCookieConversion(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	origin := "http://127.0.0.1:3000"
	h, err := NewHandler(s, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	cookie := "sub_pool_dev_session=" + admin
	created := keyRequest(h, "POST", "/api/v1/api-keys/create", `{"name":"actual client","mode":"read"}`, "", cookie, origin, "127.0.0.1:5000")
	var key APIKeyIssuance
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &key) != nil || key.Secret == "" || created.Header().Get("Set-Cookie") != "" {
		t.Fatal("HTTP issue failed")
	}
	for _, body := range []string{
		`{"name":"x","mode":"read","user_id":"hidden"}`,
		`{"name":"x","mode":"read","mode":"management"}`,
		`{"Name":"x","mode":"read"}`, `{"name":"x","mode":null}`,
		`{"name":"x","mode":"readonly"}`, `{"name":" x ","mode":"read"}`,
		`{"name":"x\n","mode":"read"}`, `{"name":"\ud800","mode":"read"}`,
		`{"name":"x","mode":"read"} {}`,
	} {
		w := keyRequest(h, "POST", "/api/v1/api-keys/create", body, "", cookie, origin, "127.0.0.1:5000")
		if w.Code != 400 {
			t.Fatal("malformed lifecycle input accepted", w.Code)
		}
	}
	for _, c := range []struct {
		method, path, body, secret, cookie, origin string
		status                                     int
	}{
		{"GET", "/api/v1/api-keys/access", "", key.Secret, "", "", 200},
		{"GET", "/api/v1/api-keys/access", "", "", "sub_pool_dev_session=" + key.Secret, "", 401},
		{"GET", "/api/v1/auth/session", "", key.Secret, "", "", 403},
		{"POST", "/api/v1/api-keys/list", `{"after":"","limit":100}`, key.Secret, "", "", 403},
		{"POST", "/api/v1/api-keys/create", `{"name":"nested","mode":"read"}`, key.Secret, "", "", 403},
		{"POST", "/api/v1/api-keys/list", `{"after":"","limit":100}`, "", cookie, "", 403},
		{"GET", "/api/v1/api-keys/list", "", "", cookie, "", 405},
		{"GET", "/api/v1/api-keys/access?bad=%GG", "", key.Secret, "", "", 400},
		{"GET", "/api/v1/api-keys/access", "{}", key.Secret, "", "", 400},
		{"GET", "/api/v1/api-keys/access", "", key.Secret, cookie, "", 401},
	} {
		w := keyRequest(h, c.method, c.path, c.body, c.secret, c.cookie, c.origin, "127.0.0.1:5000")
		if w.Code != c.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("credential boundary status", c.path, w.Code, c.status)
		}
	}
	rule, err := s.UpdateSource(managementContext(), admin, "127.0.0.2", "0", true)
	if err != nil || !rule.Blocked {
		t.Fatal("source fixture failed", err)
	}
	blocked := keyRequest(h, "GET", "/api/v1/api-keys/access", "", key.Secret, "", "", "127.0.0.2:5000")
	if blocked.Code != 403 || !strings.Contains(blocked.Body.String(), "SOURCE_BLOCKED") {
		t.Fatal("machine bypassed shared source rules")
	}
	var used bool
	if err = pool.QueryRow(context.Background(), "SELECT last_used_at IS NOT NULL FROM auth_api_keys WHERE id=$1", key.Key.ID).Scan(&used); err != nil || !used {
		t.Fatal("successful machine use was not recorded", err)
	}
}

func TestAPIKeyHTTPCatalogTracksCurrentUserPermissions(t *testing.T) {
	s, _ := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "key.catalog")
	key := issueKey(t, s, member, "read")
	h, err := NewHandler(s, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	read := func() APICatalog {
		t.Helper()
		w := keyRequest(h, "GET", "/api/v1/api-keys/catalog", "", key.Secret, "", "", "127.0.0.1:5000")
		var out APICatalog
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("machine catalog failed", w.Code)
		}
		return out
	}
	if len(read().Operations) != 2 {
		t.Fatal("empty grants exposed operations")
	}
	input := userInput(user)
	input.Features = []string{"accounts", "notifications"}
	user, err = s.UpdateUser(context.Background(), admin, input)
	if err != nil {
		t.Fatal(err)
	}
	out := read()
	foundReverification := false
	for _, failure := range out.Errors {
		if failure.Code == "CONNECTION_REVERIFICATION_REQUIRED" {
			foundReverification = failure.Status == 409
		}
	}
	if !foundReverification {
		t.Fatal("catalog omitted the restored connection conflict")
	}
	if len(out.Operations) != 7 || out.Access.User.Revision != user.Revision {
		t.Fatal("catalog ignored current grants")
	}
	for _, operation := range out.Operations {
		if operation.Feature == "notifications" && operation.Contract != "notifications-v3" {
			t.Fatal("notification catalog contract is stale", operation.Name, operation.Contract)
		}
	}
}
