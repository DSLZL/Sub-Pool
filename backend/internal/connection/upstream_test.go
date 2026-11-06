package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func respond(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func accountData(id int) map[string]any {
	return map[string]any{"id": id, "name": fmt.Sprintf("Account %d", id), "platform": "openai", "type": "oauth", "status": "active", "schedulable": true,
		"credentials": map[string]string{"access_token": "must-never-leave-upstream"}, "extra": map[string]any{"is_shadow": true}}
}

func validUpstream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "writes forbidden", 500)
		return
	}
	switch r.URL.Path {
	case "/health":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case "/api/v1/admin/system/version":
		respond(w, map[string]string{"version": "pinned-fixture"})
	case "/api/v1/auth/me":
		respond(w, map[string]string{"role": "admin", "run_mode": "standard"})
	default:
		page, size := 1, 1
		if r.URL.Query().Get("page_size") == "100" {
			size = 100
		}
		items := []any{accountData(1)}
		if r.URL.Path == "/api/v1/admin/users" {
			items = []any{map[string]any{"id": 1, "balance": 0}}
		} else if r.URL.Path == "/api/v1/admin/usage" {
			items = []any{map[string]any{"id": 1, "created_at": "2026-11-06T00:00:00Z", "total_cost": 0, "actual_cost": 0}}
		}
		respond(w, map[string]any{"items": items, "total": 1, "page": page, "page_size": size})
	}
}

func checkByID(t *testing.T, d Diagnostic, id string) Check {
	t.Helper()
	for _, check := range d.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("missing capability %s", id)
	return Check{}
}

func TestReadOnlyDiagnosisSeparatesHealthAuthenticationModeAndWriteGates(t *testing.T) {
	var bad atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "" && r.Header.Get("Authorization") != "" {
			bad.Store(true)
		}
		if r.URL.Path == "/health" && (r.Header.Get("x-api-key") != "" || r.Header.Get("Authorization") != "") {
			bad.Store(true)
		}
		validUpstream(w, r)
	}))
	defer server.Close()
	u := newUpstreamClient()
	key := u.diagnose(context.Background(), server.URL, "api_key", "adm_sc_fixture")
	if key.Version == nil || key.RunMode != nil || checkByID(t, key, "health").State != "available" || checkByID(t, key, "management_auth").State != "available" || checkByID(t, key, "run_mode").Reason != "MODE_REQUIRES_JWT" {
		t.Fatal("key diagnosis inferred a mode or lost separate management evidence")
	}
	jwt := u.diagnose(context.Background(), server.URL, "bearer", "fixture.jwt.token")
	if jwt.RunMode == nil || *jwt.RunMode != "standard" || bad.Load() {
		t.Fatal("wrong upstream authentication boundary")
	}
	for _, id := range []string{"wallet.write", "pricing.write", "scheduling.write", "quota.read", "pricing.read"} {
		if checkByID(t, jwt, id).State != "unverified" {
			t.Fatal("standard mode or a successful list enabled an unprobed capability")
		}
	}
	for _, id := range []string{"usage.read", "wallet.read"} {
		if checkByID(t, jwt, id).State != "available" {
			t.Fatal("valid zero-valued facts were treated as unknown")
		}
	}
	noCredential := u.diagnose(context.Background(), server.URL, "", "")
	if checkByID(t, noCredential, "health").State != "available" || checkByID(t, noCredential, "management_auth").State != "unverified" {
		t.Fatal("health was treated as management authentication")
	}
}

func TestDiagnosisRejectsMissingFactsAndReflectedSecrets(t *testing.T) {
	for _, tc := range []struct {
		id, path string
		data     any
	}{
		{"version.read", "/api/v1/admin/system/version", map[string]any{"version": "reflected-test-secret"}},
		{"run_mode", "/api/v1/auth/me", map[string]any{"role": "user", "run_mode": "standard"}},
		{"run_mode", "/api/v1/auth/me", map[string]any{"role": "admin"}},
		{"usage.read", "/api/v1/admin/usage", map[string]any{"id": 1, "created_at": "2026-11-06T00:00:00Z", "total_cost": 0}},
		{"usage.read", "/api/v1/admin/usage", map[string]any{"id": 1, "created_at": "invalid", "total_cost": 0, "actual_cost": 0}},
		{"wallet.read", "/api/v1/admin/users", map[string]any{"balance": 0}},
		{"wallet.read", "/api/v1/admin/users", map[string]any{"id": 1, "balance": "0"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					validUpstream(w, r)
					return
				}
				data := tc.data
				if tc.id == "usage.read" || tc.id == "wallet.read" {
					data = map[string]any{"items": []any{data}, "total": 1, "page": 1, "page_size": 1}
				}
				respond(w, data)
			}))
			defer server.Close()
			diagnostic := newUpstreamClient().diagnose(context.Background(), server.URL, "bearer", "test-secret")
			if checkByID(t, diagnostic, tc.id).Reason != "INVALID_RESPONSE" {
				t.Fatal("missing, wrongly typed or reflected fields were accepted")
			}
			body, _ := json.Marshal(diagnostic)
			if strings.Contains(string(body), "test-secret") {
				t.Fatal("upstream reflected a saved secret into the diagnostic")
			}
		})
	}
}

func TestUpstreamDenialsAndMalformedSuccessAreSafe(t *testing.T) {
	for _, tc := range []struct {
		status        int
		state, reason string
	}{
		{401, "denied", "UNAUTHORIZED"}, {403, "denied", "FORBIDDEN"}, {423, "denied", "COMPLIANCE_REQUIRED"},
		{404, "unsupported", "ROUTE_UNAVAILABLE"}, {429, "failed", "RATE_LIMITED"}, {500, "failed", "UPSTREAM_ERROR"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret upstream exception", tc.status) }))
			defer server.Close()
			_, got := newUpstreamClient().get(context.Background(), server.URL, "/read", "api_key", "test-key", "accounts.read", true)
			if got.State != tc.state || got.Reason != tc.reason || got.HTTPStatus == nil || *got.HTTPStatus != tc.status {
				t.Fatalf("unexpected safe classification: %+v", got)
			}
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "secret") {
				t.Fatal("raw error leaked")
			}
		})
	}
	for _, body := range []string{`{}`, `{"code":1,"data":{}}`, `{"code":0}`, `{"code":0,"data":null}`, `{"code":0,"data":{}} {}`, `null`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, check := newUpstreamClient().get(context.Background(), server.URL, "/read", "api_key", "test-key", "accounts.read", true)
			if check.Reason != "INVALID_RESPONSE" {
				t.Fatal("malformed success accepted")
			}
		})
	}
	for _, body := range []string{`{}`, `{"items":[],"total":1,"page":1,"page_size":100}`, `{"items":null,"total":0,"page":1,"page_size":100}`, `{"items":[],"total":0,"page":2,"page_size":100}`} {
		if _, ok := parsePage([]byte(body), 1, 100); ok {
			t.Fatal("missing fields, wrong page or hidden partial coverage accepted")
		}
	}
}

func TestUpstreamRedirectAndTimeoutDoNotReplayOrForwardSecrets(t *testing.T) {
	var destinationCalls, requests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, destination.URL, 302)
	}))
	defer server.Close()
	u := newUpstreamClient()
	_, check := u.get(context.Background(), server.URL, "/read", "api_key", "sensitive-test-key", "accounts.read", true)
	if check.Reason != "REDIRECT_REFUSED" || destinationCalls.Load() != 0 || requests.Load() != 1 {
		t.Fatal("redirect followed or request replayed")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		respond(w, map[string]string{})
	}))
	defer slow.Close()
	u.http.Timeout = 20 * time.Millisecond
	_, check = u.get(context.Background(), slow.URL, "/read", "api_key", "test-key", "accounts.read", true)
	if check.Reason != "TIMEOUT" {
		t.Fatal("timeout was not distinct")
	}
}

func TestAccountProjectionDoesNotExposeCredentialsOrConfirmShadowResources(t *testing.T) {
	body, _ := json.Marshal(accountData(922))
	account, ok := projectAccount(body)
	if !ok || account.Eligibility != "unverified" || account.UpstreamID != "922" {
		t.Fatal("OAuth list entry incorrectly accepted as an independent quota resource")
	}
	encoded, _ := json.Marshal(account)
	if strings.Contains(string(encoded), "credentials") || strings.Contains(string(encoded), "must-never") || strings.Contains(string(encoded), "is_shadow") {
		t.Fatal("upstream private fields leaked")
	}
	if _, ok = projectAccount([]byte(`{"id":1,"name":"a","type":"oauth","platform":"openai","status":"active"}`)); ok {
		t.Fatal("missing schedulable became false")
	}
	if _, ok = projectAccount([]byte(`{"id":9223372036854775808,"name":"a"}`)); ok {
		t.Fatal("out-of-range upstream identity accepted")
	}
}
