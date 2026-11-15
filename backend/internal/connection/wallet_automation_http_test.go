package connection

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

func TestWalletAutomationStrictHTTPRequiresCurrentSessionAdminAndExplicitBinding(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &WalletApplicationsHandler{Service: f.s, Boundary: boundary}
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "automation-http-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(WalletAutomationInput{Revision: "1", SourceID: *f.c.SourceID, ConnectionRevision: f.c.Revision, Simulation: f.input.Simulation})
	for _, tc := range []struct {
		method, path, body, token, origin string
		status                            int
	}{
		{"POST", "automation/enable", string(input), f.token, "https://attacker.test", 403},
		{"POST", "automation/enable", string(input), reader, "http://127.0.0.1:3000", 403},
		{"POST", "automation/enable", string(input), "", "http://127.0.0.1:3000", 401},
		{"POST", "automation/history", `{"after":"0","limit":10}`, reader, "http://127.0.0.1:3000", 403},
		{"POST", "automation/disable", `{"revision":"1"}`, reader, "http://127.0.0.1:3000", 403},
		{"GET", "automation/enable", string(input), f.token, "http://127.0.0.1:3000", 405},
		{"POST", "automation/enable", strings.TrimSuffix(string(input), "}") + `,"enabled":true}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/enable", strings.Replace(string(input), `"revision":"1"`, `"revision":"1","revision":"1"`, 1), f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/enable", strings.Replace(string(input), `"environment":"simulated"`, `"environment":"production"`, 1), f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/enable", strings.Replace(string(input), `"environment":"simulated"`, `"environment":"simulated","unexpected":true`, 1), f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/enable", strings.Replace(string(input), simulationTestEpoch, "AwMDAwMDAwMDAwMDAwMDAw", 1), f.token, "http://127.0.0.1:3000", 409},
		{"POST", "automation/history", `{"after":"01","limit":10}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/history", `{"after":"0","limit":101}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/disable", `{"revision":"1","enabled":false}`, f.token, "http://127.0.0.1:3000", 400},
		{"POST", "automation/enable", string(input), f.token, "http://127.0.0.1:3000", 200},
		{"POST", "automation/enable", string(input), f.token, "http://127.0.0.1:3000", 409},
		{"POST", "automation/history", `{"after":"0","limit":10}`, f.token, "http://127.0.0.1:3000", 200},
		{"POST", "automation/disable", `{"revision":"2"}`, f.token, "http://127.0.0.1:3000", 200},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:3000/api/v1/wallet-applications/"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: tc.token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s: %d expected %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
	}
	var applications, events int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM management_events WHERE action LIKE 'wallet.automation.%')`).Scan(&applications, &events); err != nil || applications != 0 || events != 2 || f.writes.Load() != 0 {
		t.Fatal("control-only calls dispatched or fabricated runs", applications, events, err)
	}
}
