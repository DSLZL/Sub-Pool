package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/fakesub2api"
)

func TestSub2APIFixtureConnectionAndUsage(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	// Keep page 2 full so both totals describe a valid page.
	fixture := fakesub2api.New(fakesub2api.Options{
		At: at, UsageRows: 202,
		Faults: []fakesub2api.Fault{
			{Method: http.MethodGet, Path: "/api/v1/admin/system/version", Request: 1, Delay: 3 * time.Second},
			{Method: http.MethodGet, Path: "/api/v1/admin/usage", Request: 1, Status: http.StatusBadGateway},
			{Method: http.MethodGet, Path: "/api/v1/admin/usage", Page: 2, Request: 2, TotalDelta: 1},
		},
	})
	var versionReads, usageReads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/system/version":
			versionReads.Add(1)
		case "/api/v1/admin/usage":
			usageReads.Add(1)
		}
		fixture.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	c := save(t, s, token, upstream.URL)
	if c.SourceID == nil {
		t.Fatal("saved connection has no source")
	}
	d, err := s.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: upstream.URL})
	if err != nil || !d.Saved || d.SourceID == nil || *d.SourceID != *c.SourceID ||
		d.Version == nil || *d.Version != "0.2.23" || d.RunMode == nil || *d.RunMode != "standard" {
		t.Fatalf("fixture diagnostic failed: %+v %v", d, err)
	}
	if versionReads.Load() != 1 || usageReads.Load() != 2 {
		t.Fatalf("unexpected diagnostic request counts: version %d, usage %d", versionReads.Load(), usageReads.Load())
	}
	states := make(map[string]string)
	for _, check := range d.Checks {
		states[check.ID] = check.State
	}
	for _, id := range []string{"health", "management_auth", "version.read", "run_mode", "accounts.read", "usage.read", "wallet.read"} {
		if states[id] != "available" {
			t.Errorf("%s is not available: %+v", id, d.Checks)
		}
	}

	discover := DiscoverInput{Revision: c.Revision, SourceID: *c.SourceID, Page: 1}
	accounts, err := s.Discover(ctx, token, discover)
	if err != nil || accounts.Read.State != "available" || len(accounts.Accounts) != 1 ||
		accounts.Accounts[0].UpstreamID != "1" || accounts.Total == nil || *accounts.Total != "1" {
		t.Fatalf("fixture discovery failed: %+v %v", accounts, err)
	}
	a := adoptManaged(t, s, token, c, 1, "Fixture account")
	input := UsageCaptureInput{
		AccountReadInput: readInput(a, c),
		StartAt:          at.Format(time.RFC3339), EndAt: at.Add(time.Hour).Format(time.RFC3339),
	}
	capture, err := s.CaptureUsage(ctx, token, input)
	if err != nil || capture.State != "exhausted" || capture.PagesRead != 3 ||
		capture.ScannedCount != "202" || capture.ObservedCount != "202" || capture.SettlementEligible {
		t.Fatalf("fixture capture failed: %+v %v", capture, err)
	}
	view, err := s.GetUsageCapture(ctx, token, UsageQuery{ID: capture.ID, After: "0", Limit: 2, Grain: "hour"})
	total, actual := view.Summary.Metrics["total_cost"], view.Summary.Metrics["actual_cost"]
	if err != nil || view.Summary.RequestCount != "202" || total == nil || *total != "20.2" ||
		actual == nil || *actual != "40.4" || len(view.Records) != 2 || view.Records[1].BillingCandidate != "subscription" {
		t.Fatalf("fixture usage facts failed: %+v %v", view, err)
	}
	safe, err := json.Marshal(view)
	if err != nil || strings.Contains(string(safe), "private-") || strings.Contains(string(safe), fakesub2api.ManagementKey) {
		t.Fatal("usage view exposed an upstream secret")
	}

	drift, err := s.CaptureUsage(ctx, token, input)
	if err != nil || drift.Read.Reason != "PAGINATION_CHANGED" || drift.Complete {
		t.Fatalf("fixture pagination change was not detected: %+v %v", drift, err)
	}
	upstream.Close()
	offline, err := s.Discover(ctx, token, discover)
	if err != nil || offline.Read.Reason != "NETWORK_ERROR" || len(offline.Accounts) != 0 {
		t.Fatalf("fixture disconnect was not detected: %+v %v", offline, err)
	}
}
