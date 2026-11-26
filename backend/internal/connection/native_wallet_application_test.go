package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestNativeWalletAdjustmentReconcilesWithoutRepeatingPost(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	var mu sync.Mutex
	var requests int
	var ledgerShown bool
	balance, marker := json.Number("0"), ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		const user = "/api/v1/admin/users/21"
		if r.Header.Get("Authorization") != "Bearer wallet-native-test" || r.Header.Get("Cookie") != "" {
			t.Error("native credential or destination boundary changed")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			respond(w, map[string]any{"role": "admin", "run_mode": "standard"})
		case r.Method == http.MethodGet && r.URL.Path == user:
			row := nativeUserRow(21)
			row["balance"], row["frozen_balance"] = balance, 0
			respond(w, row)
		case r.Method == http.MethodPost && r.URL.Path == user+"/balance":
			var in struct {
				Balance   json.Number
				Operation string
				Notes     string
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Balance != "0.25" || in.Operation != "add" || in.Notes == "" || in.Notes != r.Header.Get("Idempotency-Key") {
				t.Error("unexpected original Sub2API adjustment request")
			}
			requests++
			balance, marker = json.Number("0.25"), in.Notes
			http.Error(w, "upstream reply lost", http.StatusBadGateway)
		case r.Method == http.MethodGet && r.URL.Path == user+"/balance-history":
			rows := []map[string]any{}
			if ledgerShown {
				rows = append(rows, map[string]any{"id": 7, "used_by": 21, "type": "admin_balance", "status": "used", "notes": marker, "value": balance})
			}
			respond(w, map[string]any{"items": rows, "total": len(rows), "page": 1, "page_size": 100})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	initial := save(t, s, token, server.URL)
	saved, err := s.Save(ctx, token, SaveInput{Revision: initial.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "Bearer wallet-native-test"})
	if err != nil {
		t.Fatal(err)
	}
	c := saved.Configuration
	p := createParticipant(t, s, token, c, 21, "Native test user")
	read, err := s.RefreshWallet(ctx, token, walletExpected(p, c, "0"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.GetWallet(ctx, token, p.ID)
	if err != nil || view.LatestRead == nil || view.LatestRead.ID != read.ID || view.LatestRecommendation != nil {
		t.Fatal("native read lost its head or produced a legacy recommendation", view, err)
	}
	input := WalletApplicationInput{WalletExpected: walletExpected(p, c, read.ID), RequestID: "AAAAAAAAAAAAAAAAAAAAAQ",
		RecommendationID: "0", NativeDelta: "0.25"}
	blocked := input
	blocked.RequestID = "BBBBBBBBBBBBBBBBBBBBBA"
	denied, err := s.RequestWalletApplication(ctx, token, blocked)
	if err != nil || denied.DispatchState != "not_dispatched" || denied.Reason != writeGrantNeeded {
		t.Fatal("an ungranted native adjustment was eligible", denied, err)
	}
	setTestWriteGrant(t, s, token, writeWallet, true)
	repeatDenied, err := s.RequestWalletApplication(ctx, token, blocked)
	if err != nil || repeatDenied.ID != denied.ID || repeatDenied.DispatchState != "not_dispatched" {
		t.Fatal("grant reactivated a denied request", repeatDenied, err)
	}
	out, err := s.RequestWalletApplication(ctx, token, input)
	if err != nil || out.State != "unknown" || out.Details.NativeAdjustment.LedgerID != "" {
		t.Fatal("lost reply must remain unknown", out, err)
	}
	repeat, err := s.RequestWalletApplication(ctx, token, input)
	if err != nil || repeat.ID != out.ID || repeat.State != "unknown" {
		t.Fatal("same key did not read back the unknown adjustment", repeat, err)
	}
	another := input
	another.RequestID = "AAAAAAAAAAAAAAAAAAAAAA"
	if _, err = s.RequestWalletApplication(ctx, token, another); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("an unresolved adjustment accepted a new request", err)
	}
	mu.Lock()
	ledgerShown = true
	mu.Unlock()
	setTestWriteGrant(t, s, token, writeWallet, false)
	reconciled, err := s.ReconcileWalletApplication(ctx, token, out.ID)
	if err != nil || reconciled.State != "applied" || reconciled.Details.NativeAdjustment.LedgerID != "7" ||
		reconciled.Details.NativeAdjustment.BalanceAfter == nil || *reconciled.Details.NativeAdjustment.BalanceAfter != "0.25" {
		t.Fatal("stock history and user read did not prove the exact request", reconciled, err)
	}
	another.NativeDelta = "-0.26"
	setTestWriteGrant(t, s, token, writeWallet, true)
	rejected, err := s.RequestWalletApplication(ctx, token, another)
	if err != nil || rejected.DispatchState != "not_dispatched" || rejected.Reason != "NATIVE_WALLET_INSUFFICIENT_BALANCE" {
		t.Fatal("insufficient debit must stop before dispatch", rejected, err)
	}
	mu.Lock()
	count := requests
	mu.Unlock()
	if count != 1 {
		t.Fatal("native adjustment was dispatched more than once", count)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	config, err := s.config(ctx, tx, false)
	if err != nil {
		t.Fatal(err)
	}
	mode, since := "standard", time.Now().Add(-time.Minute)
	diagnostic := Diagnostic{RunMode: &mode, VersionSince: &since, Checks: []Check{
		{ID: "management_auth", State: "available"},
		{ID: "run_mode", State: "available"},
		unknown("wallet.write", "WRITE_NOT_PROBED"),
	}}
	if err = s.projectWriteCapabilities(ctx, tx, config, &diagnostic); err != nil {
		t.Fatal(err)
	}
	check := diagnostic.capability("wallet.write")
	if check.Reason != "VERIFIED_WRITE" || check.Evidence == nil || check.Evidence.UpstreamID != "21" {
		t.Fatal("diagnostics did not bind the native write to the verified user", check)
	}
}
