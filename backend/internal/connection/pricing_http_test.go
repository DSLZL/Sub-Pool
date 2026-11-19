package connection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestPricingHTTPUsesCurrentAdminStrictReadsAndRejectsIncompleteMutationInputs(t *testing.T) {
	s, _, token := setup(t)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/system/version" {
			validUpstream(w, r)
			return
		}
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/groups" {
			t.Error("unexpected native request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pricingTestPage(pricingTestGroup)))
	}))
	defer upstream.Close()
	c := save(t, s, token, upstream.URL)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &PricingHandler{Service: s, Boundary: boundary}
	encoded, _ := json.Marshal(PricingGroupsInput{Revision: c.Revision, SourceID: *c.SourceID, Page: 1})
	body := string(encoded)
	for _, tc := range []struct {
		method, path, body, origin, cookie string
		status                             int
	}{
		{"POST", "groups", body, "http://127.0.0.1:3000", token, 200},
		{"GET", "groups", body, "http://127.0.0.1:3000", token, 405},
		{"POST", "groups", body, "http://attacker.test", token, 403},
		{"POST", "groups", body, "http://127.0.0.1:3000", "", 401},
		{"POST", "groups", strings.Replace(body, `"page":1`, `"page":null`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "groups", strings.Replace(body, `"page":1`, `"page":0`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "groups", strings.Replace(body, `"page":1`, `"page":1,"page":1`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "groups", strings.Replace(body, `"page":1`, `"page":1,"can_apply":true`, 1), "http://127.0.0.1:3000", token, 400},
		{"POST", "groups", `{"revision":"1"}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "groups", `null`, "http://127.0.0.1:3000", token, 400},
		{"POST", "groups", body + `{}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "groups?bad=%GG", body, "http://127.0.0.1:3000", token, 400},
		{"POST", "group", `{"revision":"1","source_id":"x","group_id":"01","page":1}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "default", `{"revision":"1","source_id":"x","model":"bad\nmodel"}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "apply", body, "http://127.0.0.1:3000", token, 400},
		{"POST", "apply", `{}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "retry", `{}`, "http://127.0.0.1:3000", token, 400},
		{"POST", "revert", `{}`, "http://127.0.0.1:3000", token, 400},
	} {
		before := calls.Load()
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:3000/api/v1/pricing/"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		if tc.cookie != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: tc.cookie})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d expected %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
		if tc.status != 200 && calls.Load() != before {
			t.Fatal("invalid or undeclared request contacted upstream", tc.path)
		}
	}
}
