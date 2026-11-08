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

func TestMaintenanceStrictSessionHTTPAndExplicitPlanConfirmation(t *testing.T) {
	s, db, token, c, calls := maintenanceFixture(t)
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &MaintenanceHandler{Service: s, Boundary: boundary}
	invoke := func(method, path, body, session, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:3000/api/v1/maintenance/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("maintenance leaked cache or renewed a session")
		}
		return w
	}
	in, _ := json.Marshal(maintenanceInputFor(a, fact, fact))
	before := calls.Load()
	for _, test := range []struct {
		name, method, path, body, session, origin string
		status                                    int
	}{
		{"wrong method", "GET", "plans", `{}`, token, "http://127.0.0.1:3000", 405},
		{"unknown route", "POST", "repair-upstream", `{}`, token, "http://127.0.0.1:3000", 404},
		{"no session", "POST", "preflight", string(in), "", "http://127.0.0.1:3000", 401},
		{"missing origin", "POST", "preflight", string(in), token, "", 403},
		{"foreign origin", "POST", "preflight", string(in), token, "https://attacker.example", 403},
		{"duplicate field", "POST", "plan", `{"id":"1","id":"2"}`, token, "http://127.0.0.1:3000", 400},
		{"case alias", "POST", "plan", `{"ID":"1"}`, token, "http://127.0.0.1:3000", 400},
		{"null field", "POST", "plan", `{"id":null}`, token, "http://127.0.0.1:3000", 400},
		{"trailing JSON", "POST", "plan", `{"id":"1"} {}`, token, "http://127.0.0.1:3000", 400},
		{"query overrides scope", "POST", "preflight?account_id=all", string(in), token, "http://127.0.0.1:3000", 400},
		{"malformed query", "POST", "preflight?x=%GG", string(in), token, "http://127.0.0.1:3000", 400},
		{"extra financial field", "POST", "preflight", strings.TrimSuffix(string(in), "}") + `,"wallet_target":"100"}`, token, "http://127.0.0.1:3000", 400},
		{"missing acknowledgement", "POST", "apply", `{"id":"1","basis":"` + strings.Repeat("a", 64) + `"}`, token, "http://127.0.0.1:3000", 400},
		{"false acknowledgement", "POST", "apply", `{"id":"1","basis":"` + strings.Repeat("a", 64) + `","confirmation":false}`, token, "http://127.0.0.1:3000", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := invoke(test.method, test.path, test.body, test.session, test.origin)
			if w.Code != test.status {
				t.Fatalf("strict boundary status %d, want %d: %s", w.Code, test.status, w.Body.String())
			}
		})
	}
	w := invoke("POST", "preflight", string(in), token, "http://127.0.0.1:3000")
	var p MaintenancePlan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.State != "ready" || p.ResultID != nil {
		t.Fatal("native HTTP preflight failed", w.Code, w.Body.String())
	}
	confirmation := `{"id":"` + p.ID + `","basis":"` + p.Basis.Fingerprint + `","confirmation":true}`
	w = invoke("POST", "apply", confirmation, token, "http://127.0.0.1:3000")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.State != "queued" || p.ResultID != nil {
		t.Fatal("HTTP acknowledgement was not a pending receipt", w.Code, w.Body.String())
	}
	processMaintenanceForTest(t, s)
	w = invoke("POST", "plan", `{"id":"`+p.ID+`"}`, token, "http://127.0.0.1:3000")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.State != "applied" || p.ResultID == nil {
		t.Fatal("HTTP read did not return the persisted result", w.Code, w.Body.String())
	}
	if calls.Load() != before {
		t.Fatal("maintenance HTTP contacted upstream")
	}
	if _, err := s.GetMaintenancePlan(context.Background(), token, "9223372036854775807"); err != auth.ErrNotFound {
		t.Fatal("absent plan did not return the declared failure", err)
	}
}
