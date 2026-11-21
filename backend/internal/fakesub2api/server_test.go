package fakesub2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func adminRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, adminPath+path, strings.NewReader(body))
	r.Header.Set("x-api-key", ManagementKey)
	return r
}

func call(t *testing.T, h http.Handler, r *http.Request, status int) (map[string]any, http.Header) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%s %s: got HTTP %d, want %d: %s", r.Method, r.URL, w.Code, status, w.Body)
	}
	d := json.NewDecoder(w.Body)
	d.UseNumber()
	var body map[string]any
	if err := d.Decode(&body); err != nil {
		t.Fatal(err)
	}
	if r.URL.Path == "/health" && status == http.StatusOK {
		return body, w.Header()
	}
	if status != http.StatusOK {
		if body["code"] != json.Number(strconv.Itoa(status)) || body["message"] == nil {
			t.Fatalf("invalid error envelope: %v", body)
		}
		return body, w.Header()
	}
	data, ok := body["data"].(map[string]any)
	if body["code"] != json.Number("0") || !ok {
		t.Fatalf("invalid success envelope: %v", body)
	}
	return data, w.Header()
}

func TestRoutesAndAuth(t *testing.T) {
	h := New(Options{})
	health, _ := call(t, h, httptest.NewRequest("GET", "/health", nil), 200)
	if health["status"] != "ok" {
		t.Fatal(health)
	}
	for _, tc := range []struct {
		path  string
		field string
	}{
		{"/system/version", "version"},
		{"/groups/0/model-allowlist-candidates?platform=openai", "models"},
		{"/accounts", "items"},
		{"/accounts/1", "credentials"},
		{"/usage", "items"},
		{"/users", "items"},
		{"/users/21", "balance"},
		{"/users/21/balance-history", "items"},
		{"/groups", "items"},
		{"/groups/1", "model_pricing"},
		{"/channels/model-pricing?model=requested-model", "input_price"},
		{"/settings", "affiliate_enabled"},
	} {
		data, _ := call(t, h, adminRequest("GET", tc.path, ""), 200)
		if data[tc.field] == nil {
			t.Errorf("%s missing %s: %v", tc.path, tc.field, data)
		}
	}
	for _, key := range []string{"", "wrong-key"} {
		r := adminRequest("GET", "/accounts", "")
		r.Header.Set("x-api-key", key)
		call(t, h, r, 401)
	}
	call(t, h, adminRequest("POST", "/system/version", ""), 404)
	r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	r.Header.Set("x-api-key", ManagementKey)
	call(t, h, r, 401)
	r.Header.Del("x-api-key")
	r.Header.Set("Authorization", "Bearer "+BearerToken)
	simple := New(Options{Mode: "simple"})
	admin, _ := call(t, simple, r, 200)
	if admin["role"] != "admin" || admin["run_mode"] != "simple" {
		t.Fatal(admin)
	}
	r = httptest.NewRequest("GET", adminPath+"/system/version", nil)
	r.Header.Set("Authorization", "Bearer "+BearerToken)
	call(t, simple, r, 200)
	body, _ := call(t, simple, adminRequest("GET", "/groups/0/model-allowlist-candidates?platform=openai", ""), 403)
	if body["reason"] != "SIMPLE_MODE_OPERATION_UNSUPPORTED" {
		t.Fatal(body)
	}
}

func TestWritesAndReadback(t *testing.T) {
	h := New(Options{})
	call(t, h, adminRequest("POST", "/accounts/1/schedulable", `{"schedulable":false}`), 200)
	account, _ := call(t, h, adminRequest("GET", "/accounts/1", ""), 200)
	if account["schedulable"] != false {
		t.Fatal(account)
	}
	const pricing = `{"force_openai_fast":true,"free_openai_fast":true,"long_context_pricing_enabled":false,"model_pricing":[{"models":["fixture-price"],"input_price":0.35}]}`
	call(t, h, adminRequest("PUT", "/groups/1", pricing), 200)
	group, _ := call(t, h, adminRequest("GET", "/groups/1", ""), 200)
	if group["long_context_pricing_enabled"] != false || group["force_openai_fast"] != true || group["free_openai_fast"] != true {
		t.Fatal(group)
	}
	entries, _ := group["model_pricing"].([]any)
	if len(entries) != 1 {
		t.Fatal(group)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["input_price"] != json.Number("0.35") {
		t.Fatal(entry)
	}

	changeBalance := func(body, key string, status int) map[string]any {
		t.Helper()
		r := adminRequest("POST", "/users/21/balance", body)
		r.Header.Set("Idempotency-Key", key)
		user, _ := call(t, h, r, status)
		return user
	}
	const credit = `{"balance":12.05,"operation":"add","notes":"fixture credit"}`
	if user := changeBalance(credit, "credit", 200); user["balance"] != json.Number("112.05") {
		t.Fatal(user)
	}
	changeBalance(`{"balance":1,"operation":"subtract","notes":"fixture debit"}`, "debit", 200)
	if user := changeBalance(credit, "credit", 200); user["balance"] != json.Number("112.05") {
		t.Fatalf("replay changed the original response: %v", user)
	}
	changeBalance(`{"balance":99,"operation":"add"}`, "credit", 409)
	user, _ := call(t, h, adminRequest("GET", "/users/21", ""), 200)
	history, _ := call(t, h, adminRequest("GET", "/users/21/balance-history?type=admin_balance", ""), 200)
	rows, _ := history["items"].([]any)
	if user["balance"] != json.Number("111.05") || history["total"] != json.Number("2") || len(rows) != 2 {
		t.Fatalf("unexpected balance or ledger: %v %v", user, history)
	}
	debit, _ := rows[0].(map[string]any)
	posted, _ := rows[1].(map[string]any)
	if debit["value"] != json.Number("-1") || posted["value"] != json.Number("12.05") || posted["notes"] != "fixture credit" || posted["status"] != "used" {
		t.Fatal(rows)
	}
	fresh, _ := call(t, New(Options{}), adminRequest("GET", "/users/21", ""), 200)
	if fresh["balance"] != json.Number("100") {
		t.Fatal(fresh)
	}
}

func TestFaultsAndLargeUsage(t *testing.T) {
	h := New(Options{
		At: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), UsageRows: 1000,
		Faults: []Fault{
			{Method: "GET", Path: adminPath + "/accounts", Request: 1, Status: 503},
			{Method: "GET", Path: adminPath + "/users", Status: 429, RetryAfter: 7},
			{Method: "GET", Path: adminPath + "/usage", Page: 2, Request: 2, TotalDelta: 1},
		},
	})
	call(t, h, adminRequest("GET", "/accounts", ""), 503)
	call(t, h, adminRequest("GET", "/accounts", ""), 200)
	_, header := call(t, h, adminRequest("GET", "/users", ""), 429)
	if header.Get("Retry-After") != "7" {
		t.Fatal(header)
	}
	pageRequest := func(page int) *http.Request {
		return adminRequest("GET", "/usage?account_id=1&start_date=2026-11-01&end_date=2026-11-01&sort_by=id&sort_order=asc&page_size=100&page="+strconv.Itoa(page), "")
	}
	for page := 1; page <= 10; page++ {
		data, _ := call(t, h, pageRequest(page), 200)
		rows, _ := data["items"].([]any)
		if data["total"] != json.Number("1000") || data["page"] != json.Number(strconv.Itoa(page)) || data["page_size"] != json.Number("100") || len(rows) != 100 {
			t.Fatalf("unexpected page metadata or count for %d: %v / %d", page, data["total"], len(rows))
		}
		for i, row := range rows {
			item, _ := row.(map[string]any)
			if item["id"] != json.Number(strconv.Itoa((page-1)*100+i+1)) || item["total_cost"] != json.Number("0.1") || item["actual_cost"] != json.Number("0.2") {
				t.Fatalf("incorrect usage row for page %d, index %d: %v", page, i, item)
			}
		}
	}
	drift, _ := call(t, h, pageRequest(2), 200)
	recovered, _ := call(t, h, pageRequest(2), 200)
	if drift["total"] != json.Number("1001") || recovered["total"] != json.Number("1000") {
		t.Fatalf("unexpected drift and recovery totals: %v / %v", drift["total"], recovered["total"])
	}
}

func TestDelayCancels(t *testing.T) {
	h := New(Options{Faults: []Fault{{Method: "GET", Path: adminPath + "/usage", Delay: time.Minute}}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := adminRequest("GET", "/usage", "").WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, r)
	}()
	select {
	case <-done:
		if ctx.Err() != context.DeadlineExceeded || w.Body.Len() != 0 {
			t.Fatalf("delayed request was not canceled: %v %s", ctx.Err(), w.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after cancellation")
	}
}
