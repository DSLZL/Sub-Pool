package connection

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestMonitoringStrictBoundaryAndProcessOnlyHealth(t *testing.T) {
	boundary, err := auth.NewHandler(auth.New(nil), "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &MonitoringHandler{Boundary: boundary}
	for _, test := range []struct {
		path, body, origin string
		status             int
	}{
		{"collect", `{"source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","all":true,"account_ids":null}`, "", 400},
		{"collect", `{"source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","all":true,"all":false,"account_ids":[]}`, "", 400},
		{"collect", `{"source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","account_ids":[]}`, "", 400},
		{"config/update", `{"revision":"1","enabled":true}`, "", 400},
		{"collect", `{"source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","all":true,"account_ids":[]}`, "https://attacker.example", 403},
		{"collect?override=1", `{}`, "", 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3000/api/v1/monitoring/"+test.path, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:3000")
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("unsafe monitoring boundary %s: %d %s", test.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	ProcessHealth(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil))
	if w.Code != 200 || w.Body.String() != `{"status":"ok","scope":"process"}` || strings.Contains(w.Body.String(), "database") {
		t.Fatal("public health claimed business/database health")
	}
}
