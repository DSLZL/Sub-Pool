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

func TestPricingControlHTTPValidatesNestedIntentAndKeepsMachineCredentialsClosed(t *testing.T) {
	f := newPricingFixture(t)
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &PricingHandler{Service: f.s, Boundary: boundary}
	in := pricingInput(t, f, "1", fastPricing())
	encoded, _ := json.Marshal(in)
	body := string(encoded)
	request := func(action, body, cookie, key, origin, peer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:3000/api/v1/pricing/"+action, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = peer
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: cookie})
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, bad := range []string{
		strings.Replace(body, `"kind":"fast"`, `"Kind":"fast"`, 1),
		strings.Replace(body, `"kind":"fast"`, `"kind":"fast","kind":"fast"`, 1),
		strings.Replace(body, `"value":null`, `"price_field":"","value":null`, 1),
		strings.Replace(body, `"force_fast":true`, `"force_fast":null`, 1),
		strings.Replace(body, `,"value":null`, "", 1),
		strings.Replace(body, `"acknowledge_shared_impact":true`, `"acknowledge_shared_impact":false`, 1),
		strings.Replace(body, `"changes":`, `"can_apply":true,"changes":`, 1),
	} {
		w := request("apply", bad, f.token, "", "http://127.0.0.1:3000", "127.0.0.1:5000")
		if w.Code != 400 {
			t.Fatal("malformed nested intent accepted", w.Code, w.Body.String())
		}
	}
	for _, mode := range []string{"read", "management"} {
		key, err := f.s.auth.IssueAPIKey(context.Background(), f.token, "pricing boundary", mode)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"control", "apply", "history", "retry", "revert", "review"} {
			w := request(action, body, "", key.Secret, "", "127.0.0.1:5000")
			if w.Code != 403 {
				t.Fatal("machine credential reached global pricing", action, w.Code)
			}
		}
	}
	for _, origin := range []string{"", "https://attacker.test"} {
		if w := request("apply", body, f.token, "", origin, "127.0.0.1:5000"); w.Code != 403 {
			t.Fatal("write accepted foreign origin", w.Code)
		}
	}
	exec(t, f.db, `INSERT INTO source_blocks(ip,revision,blocked,saved_at,actor_id) SELECT '127.0.0.2',1,true,clock_timestamp(),id FROM auth_users WHERE username='admin'`)
	if w := request("apply", body, f.token, "", "http://127.0.0.1:3000", "127.0.0.2:5000"); w.Code != 403 || !strings.Contains(w.Body.String(), "SOURCE_BLOCKED") {
		t.Fatal("blocked peer reached write", w.Code)
	}
	f.mu.Lock()
	writes, reads := f.writes, f.reads
	f.mu.Unlock()
	if writes != 0 || reads != 1 {
		t.Fatal("invalid or forbidden command reached native pricing", reads, writes)
	}
	w := request("apply", body, f.token, "", "http://127.0.0.1:3000", "127.0.0.1:5000")
	var p PricingOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.State != "applied" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("HTTP apply not confirmed", w.Code, w.Body.String())
	}
	get := request("operation", `{"id":"`+p.ID+`"}`, f.token, "", "http://127.0.0.1:3000", "127.0.0.1:5000")
	if get.Code != 200 || strings.Contains(get.Body.String(), "PRIVATE_NATIVE_BODY") {
		t.Fatal("unsafe operation read", get.Code)
	}
	if w = request("automatic-retry", `{}`, f.token, "", "http://127.0.0.1:3000", "127.0.0.1:5000"); w.Code != 404 {
		t.Fatal("undeclared native operation exposed")
	}
}
