package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestPricingGlobalHistoryRetainsOldSourcesAndNeverContactsNative(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	first := applyPricing(t, f, "1")
	other := newPricingFixture(t)
	saved, err := f.s.Save(ctx, f.token, SaveInput{Revision: f.c.Revision, BaseURL: other.c.BaseURL, CredentialAction: "replace", Credential: "Bearer controlled-pricing-jwt", Confirmation: "更换地址"})
	if err != nil || !saved.SourceChanged {
		t.Fatal("source replacement failed", err)
	}
	other.s, other.db, other.token, other.c = f.s, f.db, f.token, saved.Configuration
	if err = other.s.ConfigurePricingControl(*other.c.SourceID + ":" + other.c.Revision); err != nil {
		t.Fatal(err)
	}
	second := applyPricing(t, other, "1")
	f.mu.Lock()
	firstReads, firstWrites := f.reads, f.writes
	f.mu.Unlock()
	other.mu.Lock()
	secondReads, secondWrites := other.reads, other.writes
	other.mu.Unlock()
	after := "0"
	for i, want := range []PricingOperation{first, second} {
		page, e := f.s.ListPricingHistory(ctx, f.token, PricingHistoryInput{After: after, Limit: 1})
		if e != nil || len(page.Operations) != 1 || !reflect.DeepEqual(page.Operations[0], want) {
			t.Fatal("global history omitted retained source", page, e)
		}
		if i == 0 {
			if page.NextAfter == nil || *page.NextAfter != want.ID {
				t.Fatal("global cursor lost next source", page)
			}
			after = *page.NextAfter
		} else if page.NextAfter != nil {
			t.Fatal("unexpected history continuation", page)
		}
	}
	for _, want := range []PricingOperation{first, second} {
		page, e := f.s.ListPricingHistory(ctx, f.token, PricingHistoryInput{SourceID: want.SourceID, GroupID: "1", After: "0", Limit: 100})
		if e != nil || len(page.Operations) != 1 || !reflect.DeepEqual(page.Operations[0], want) {
			t.Fatal("source filter changed", page, e)
		}
	}
	empty, err := f.s.ListPricingHistory(ctx, f.token, PricingHistoryInput{GroupID: "2", After: "0", Limit: 100})
	if err != nil || len(empty.Operations) != 0 || empty.NextAfter != nil {
		t.Fatal("global group filter changed", empty, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	other.mu.Lock()
	defer other.mu.Unlock()
	if f.reads != firstReads || f.writes != firstWrites || other.reads != secondReads || other.writes != secondWrites {
		t.Fatal("local history contacted native source")
	}
	exec(t, f.db, `UPDATE auth_users SET role='user',revision=revision+1 WHERE username='admin'`)
	if _, err = f.s.ListPricingHistory(ctx, f.token, PricingHistoryInput{After: "0", Limit: 100}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("role contraction exposed global pricing history", err)
	}
}

func TestPricingGlobalHistoryHTTPKeepsCurrentAdminSessionBoundary(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &PricingHandler{Service: s, Boundary: boundary}
	body := `{"source_id":"","group_id":"","after":"0","limit":100}`
	key, err := s.auth.IssueAPIKey(ctx, token, "local history exclusion", "management")
	if err != nil {
		t.Fatal(err)
	}
	if w := machineCall(h, "/api/v1/pricing/history", body, key.Secret); w.Code != 403 {
		t.Fatal("management key reached browser-only pricing history", w.Code)
	}
	request := func(body, origin, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3000/api/v1/pricing/history", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		body, origin, cookie string
		status               int
	}{
		{body, "http://127.0.0.1:3000", token, 200},
		{body, "http://127.0.0.1:3000", "", 401},
		{body, "http://attacker.test", token, 403},
		{`{"group_id":"","after":"0","limit":100}`, "http://127.0.0.1:3000", token, 400},
		{strings.Replace(body, `"source_id":""`, `"source_id":null`, 1), "http://127.0.0.1:3000", token, 400},
		{strings.Replace(body, `"source_id":""`, `"source_id":"invalid"`, 1), "http://127.0.0.1:3000", token, 400},
		{strings.Replace(body, `"limit":100`, `"limit":101`, 1), "http://127.0.0.1:3000", token, 400},
	} {
		w := request(tc.body, tc.origin, tc.cookie)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("global history boundary changed", w.Code, tc.status, w.Body.String())
		}
		if w.Code == 200 {
			var out PricingHistory
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Operations == nil || len(out.Operations) != 0 {
				t.Fatal("empty history was not a complete zero", w.Body.String())
			}
		}
	}
	exec(t, db, `UPDATE auth_users SET role='user',revision=revision+1 WHERE username='admin'`)
	if w := request(body, "http://127.0.0.1:3000", token); w.Code != 403 {
		t.Fatal("ordinary session exposed global history", w.Code)
	}
}
