package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSyntheticScenariosAreIndependentNonPersistentAndNonFinancial(t *testing.T) {
	for _, name := range []string{"overview", "wallet", "restore"} {
		first, ok := Example(name)
		if !ok || !first.Synthetic || first.Persistent || first.ProductionEffects || len(first.Facts) != 3 {
			t.Fatal("invalid isolated example", name)
		}
		if name != "overview" && first.Outcome != "blocked" {
			t.Fatal("unsafe scenario enabled")
		}
		first.Facts[0].Value = "local mutation"
		again, _ := Example(name)
		if reflect.DeepEqual(first, again) {
			t.Fatal("example state persisted across requests")
		}
	}
	if _, ok := Example("production"); ok {
		t.Fatal("unknown scenario selected production")
	}
}
func TestDemoHTTPRejectsCredentialsMalformedInputAndProductionRoutes(t *testing.T) {
	request := func(path, body, header string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if header != "" {
			r.Header.Set(header, "owned-credential-not-accepted")
		}
		w := httptest.NewRecorder()
		(Handler{}).ServeHTTP(w, r)
		return w
	}
	for _, header := range []string{"Cookie", "Authorization"} {
		if w := request("/api/v1/demo/scenario", "{\"scenario\":\"overview\"}", header); w.Code != 403 {
			t.Fatal("credential-bearing demo request accepted")
		}
	}
	for _, header := range []string{"Content-Encoding", "Content-Type"} {
		if w := request("/api/v1/demo/scenario", "{\"scenario\":\"overview\"}", header); w.Code != 400 {
			t.Fatal("unsupported representation accepted", w.Code)
		}
	}
	for _, body := range []string{"{}", "null", "[]", "{\"scenario\":null}", "{\"Scenario\":\"overview\"}", "{\"scenario\":\"overview\",\"scenario\":\"wallet\"}", "{\"scenario\":\"overview\",\"target\":1}", "{\"scenario\":\"overview\"}{}", "{\"scenario\":\"production\"}", "{\"scenario\":\"\\ud800\"}", strings.Repeat(" ", 4097) + "{}"} {
		if w := request("/api/v1/demo/scenario", body, ""); w.Code != 400 {
			t.Fatal("untrusted scenario accepted", w.Code)
		}
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/backups/apply", "/api/v1/demo/scenario?extra=%GG"} {
		w := request(path, "{}", "")
		if w.Code != 400 && w.Code != 404 {
			t.Fatal("production route reachable")
		}
	}
	for _, name := range []string{"overview", "wallet", "restore"} {
		w := request("/api/v1/demo/scenario", "{\"scenario\":\""+name+"\"}", "")
		var out Scenario
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Scenario != name || !out.Synthetic || out.ProductionEffects || out.Persistent {
			t.Fatal("native demo response invalid", name, w.Code)
		}
	}
}
func TestDemoServerRequiresLiteralLoopbackAndBoundedPort(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8082", "localhost:8082", "198.51.100.1:8082", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:-1", "invalid"} {
		if err := Serve(context.Background(), address); err == nil {
			t.Fatal("unsafe listen accepted", address)
		}
	}
}
