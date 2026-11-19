package connection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSavedDiagnosticReadsPassiveQuotaAndInvalidatesStaleEvidence(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	account := fullManagedAccount(1, "resource")
	account["extra"] = map[string]any{"codex_usage_updated_at": at, "codex_5h_used_percent": 0}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path == "/api/v1/admin/openai/accounts/1/quota" {
			t.Error("connection diagnostics invoked an active quota query or write")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/api/v1/admin/accounts/1" {
			respond(w, account)
			return
		}
		validUpstream(w, r)
	}))
	t.Cleanup(server.Close)
	c := save(t, s, token, server.URL)
	managed := adoptManaged(t, s, token, c, 1, "Quota account")
	d, err := s.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: c.BaseURL})
	if err != nil {
		t.Fatal(err)
	}
	got := checkByID(t, d, "quota.read")
	if got.State != "available" || got.Reason != "VERIFIED_PASSIVE_QUOTA" || got.Evidence == nil ||
		got.Evidence.UpstreamID != "1" || got.Evidence.AccountRevision != managed.Revision || !got.Evidence.VerifiedAt.Equal(at) {
		t.Fatalf("unexpected quota evidence: %+v", got)
	}
	if checkByID(t, d, "pricing.write").Reason != "STANDARD_MODE_REQUIRED" ||
		checkByID(t, d, "wallet.write").Reason != "STANDARD_MODE_REQUIRED" {
		t.Fatal("API Key produced a false write qualification")
	}
	assertQuotaStatus := func(want string) {
		t.Helper()
		read, err := s.Get(ctx, token)
		if err != nil || read.Diagnostic == nil {
			t.Fatal("saved diagnostic unavailable", err)
		}
		if got := checkByID(t, *read.Diagnostic, "quota.read"); got.Reason != want || (got.Evidence != nil) != (want == "VERIFIED_PASSIVE_QUOTA") {
			t.Fatalf("expected %s, got %+v", want, got)
		}
	}
	assertQuotaStatus("VERIFIED_PASSIVE_QUOTA")
	exec(t, db, `UPDATE monitoring_config SET interval_seconds=60,stale_after_seconds=60`)
	assertQuotaStatus("QUOTA_SNAPSHOT_STALE")
	_, err = s.UpdateManaged(ctx, token, ManagedUpdate{ID: managed.ID, Revision: managed.Revision, Name: "Updated quota account", Monitoring: true, SourceMode: "passive"})
	if err != nil {
		t.Fatal(err)
	}
	assertQuotaStatus("QUOTA_IDENTITY_UNVERIFIED")
}

func TestControlledDiagnosticOnlyProjectsVersionBoundVerifiedReceipts(t *testing.T) {
	ctx := context.Background()
	standard := "standard"
	checkReceipt := func(t *testing.T, s *Service, db *pgxpool.Pool, c Configuration, id string, since time.Time, want string) {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		d := Diagnostic{RunMode: &standard, VersionSince: &since, Checks: []Check{
			{ID: "management_auth", State: "available", Reason: "VERIFIED_READ"},
			{ID: "run_mode", State: "available", Reason: "VERIFIED_READ"},
			unknown(id, "WRITE_NOT_PROBED"),
		}}
		if err := s.projectWriteCapabilities(ctx, tx, storedConfig{Configuration: c}, &d); err != nil {
			t.Fatal(err)
		}
		got := checkByID(t, d, id)
		if got.Reason != want || (got.Evidence != nil) != (want == "VERIFIED_WRITE") || got.Evidence != nil && got.Evidence.UpstreamID != "1" {
			t.Fatalf("expected %s, got %+v", want, got)
		}
	}
	t.Run("dispatch", func(t *testing.T) {
		f := newDispatchFixture(t, true)
		since := time.Now().UTC()
		checkReceipt(t, f.s, f.db, f.c, "scheduling.write", since, "WRITE_EVIDENCE_REQUIRED")
		pauseDispatch(t, f)
		checkReceipt(t, f.s, f.db, f.c, "scheduling.write", since, "VERIFIED_WRITE")
		checkReceipt(t, f.s, f.db, f.c, "scheduling.write", time.Now().Add(time.Minute), "WRITE_EVIDENCE_REQUIRED")
		if err := f.s.ConfigureDispatchControl(""); err != nil {
			t.Fatal(err)
		}
		checkReceipt(t, f.s, f.db, f.c, "scheduling.write", since, "CONTROL_BINDING_REQUIRED")
	})
	t.Run("pricing", func(t *testing.T) {
		f := newPricingFixture(t)
		since := time.Now().UTC()
		checkReceipt(t, f.s, f.db, f.c, "pricing.write", since, "WRITE_EVIDENCE_REQUIRED")
		applyPricing(t, f, "1")
		checkReceipt(t, f.s, f.db, f.c, "pricing.write", since, "VERIFIED_WRITE")
		checkReceipt(t, f.s, f.db, f.c, "pricing.write", time.Now().Add(time.Minute), "WRITE_EVIDENCE_REQUIRED")
	})
}
