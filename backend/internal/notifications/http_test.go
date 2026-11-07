package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestNotificationHTTPEnforcesExactNestedInputOriginAndCurrentRoles(t *testing.T) {
	s, token := setup(t)
	boundary, e := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if e != nil {
		t.Fatal(e)
	}
	h := &Handler{Service: s, Boundary: boundary}
	u, e := s.auth.CreateUser(context.Background(), token, auth.UserCreate{Username: "reader", Password: "OwnedTestPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"notifications"}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if e != nil {
		t.Fatal(e)
	}
	_, reader, e := s.auth.Login(context.Background(), u.Username, "OwnedTestPassword123", netip.MustParseAddr("127.0.0.2"))
	if e != nil {
		t.Fatal(e)
	}
	configBody, _ := json.Marshal(input(config(t, s, token)))
	testBody, _ := json.Marshal(TestInput{id(1), "1"})
	queryBody, _ := json.Marshal(Query{After: "0", Limit: 100})
	for _, tc := range []struct {
		method, path, body, token, origin string
		status                            int
	}{
		{"POST", "config", "{}", token, "http://127.0.0.1:3000", 200},
		{"POST", "config", "{}", reader, "http://127.0.0.1:3000", 403},
		{"POST", "test", string(testBody), reader, "http://127.0.0.1:3000", 403},
		{"POST", "config/update", string(configBody), reader, "http://127.0.0.1:3000", 403},
		{"POST", "list", string(queryBody), reader, "http://127.0.0.1:3000", 200},
		{"POST", "config", "{}", "", "http://127.0.0.1:3000", 401},
		{"POST", "config", "{}", token, "https://attacker.invalid", 403},
		{"GET", "config", "{}", token, "http://127.0.0.1:3000", 405},
		{"POST", "config", "{\"enabled\":true}", token, "http://127.0.0.1:3000", 400},
		{"POST", "test", strings.TrimSuffix(string(testBody), "}") + `,"recipient":"victim@owned.test"}`, token, "http://127.0.0.1:3000", 400},
		{"POST", "test", `{"request_id":"` + id(1) + `","revision":"1","Revision":"1"}`, token, "http://127.0.0.1:3000", 400},
		{"POST", "config/update", strings.Replace(string(configBody), `"host":""`, `"host":"","host":"evil.invalid"`, 1), token, "http://127.0.0.1:3000", 400},
		{"POST", "config/update", strings.Replace(string(configBody), `"port":587`, `"Port":587`, 1), token, "http://127.0.0.1:3000", 400},
		{"POST", "config/update", strings.Replace(string(configBody), `"collection_failed":false`, `"collection_failed":null`, 1), token, "http://127.0.0.1:3000", 400},
		{"POST", "config/update", strings.Replace(string(configBody), `"collection_failed":false`, `"collection_failed":false,"admin":true`, 1), token, "http://127.0.0.1:3000", 400},
		{"POST", "config?x=%GG", "{}", token, "http://127.0.0.1:3000", 400},
		{"POST", "send", "{}", token, "http://127.0.0.1:3000", 404},
		{"POST", "retry", "{}", token, "http://127.0.0.1:3000", 404},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:3000/api/v1/notifications/"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: tc.token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s returned %d expected %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
	}
}
