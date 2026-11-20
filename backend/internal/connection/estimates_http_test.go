package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sub-pool/backend/internal/auth"
	"testing"
)

func TestEstimateStrictHTTPRoutesAndNoFinancialInputs(t *testing.T) {
	s, _, token, c, calls := estimateFixture(t)
	a := passiveManaged(t, s, token, c, 1)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &EstimatesHandler{Service: s, Boundary: boundary}
	invoke := func(method, path, body, session, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:3000/api/v1/estimates/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("estimate boundary cached data or altered cookie")
		}
		return w
	}
	config := estimateInput(a, c)
	raw, _ := json.Marshal(map[string]any{"account_id": a.ID, "revision": config.Revision, "plan": config.Plan, "cost_basis": config.CostBasis, "keep_ratio": config.KeepRatio, "premise": config.Premise, "windows": config.Windows, "source_id": config.SourceID, "connection_revision": config.ConnectionRevision, "account_revision": config.AccountRevision})
	current := `{"account_id":"` + a.ID + `"}`
	before := calls.Load()
	for _, c := range []struct {
		name, method, path, body, session, origin string
		status                                    int
	}{
		{"unconfigured", "POST", "current", current, token, "http://127.0.0.1:3000", 200},
		{"weekly history first page", "POST", "weekly-history", strings.TrimSuffix(current, "}") + `,"before":"0"}`, token, "http://127.0.0.1:3000", 200},
		{"weekly history null cursor", "POST", "weekly-history", strings.TrimSuffix(current, "}") + `,"before":null}`, token, "http://127.0.0.1:3000", 400},
		{"wrong method", "GET", "current", current, token, "http://127.0.0.1:3000", 405},
		{"no session", "POST", "current", current, "", "http://127.0.0.1:3000", 401},
		{"cross origin", "POST", "configure", string(raw), token, "https://attacker.test", 403},
		{"unknown route", "POST", "apply", `{}`, token, "http://127.0.0.1:3000", 404},
		{"query override", "POST", "current?account_id=all", current, token, "http://127.0.0.1:3000", 400},
		{"duplicate key", "POST", "record", `{"id":"1","id":"2"}`, token, "http://127.0.0.1:3000", 400},
		{"null id", "POST", "record", `{"id":null}`, token, "http://127.0.0.1:3000", 400},
		{"number id", "POST", "record", `{"id":1}`, token, "http://127.0.0.1:3000", 400},
		{"manufactured complete", "POST", "configure", strings.TrimSuffix(string(raw), "}") + `,"complete":true}`, token, "http://127.0.0.1:3000", 400},
		{"wallet amount", "POST", "configure", strings.TrimSuffix(string(raw), "}") + `,"target":"100"}`, token, "http://127.0.0.1:3000", 400},
		{"save", "POST", "configure", string(raw), token, "http://127.0.0.1:3000", 200},
		{"stale config", "POST", "configure", string(raw), token, "http://127.0.0.1:3000", 409},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := invoke(c.method, c.path, c.body, c.session, c.origin)
			if w.Code != c.status {
				t.Fatalf("status %d expected %d: %s", w.Code, c.status, w.Body.String())
			}
		})
	}
	if calls.Load() != before {
		t.Fatal("local configuration sent remote write/read")
	}
	result, err := s.GetEstimateCurrent(context.Background(), token, a.ID)
	if err != nil || result.Latest == nil {
		t.Fatal(err)
	}
	if w := invoke("POST", "record", `{"id":"`+result.Latest.ID+`"}`, token, "http://127.0.0.1:3000"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
