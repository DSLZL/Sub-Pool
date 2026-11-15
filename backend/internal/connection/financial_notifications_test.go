package connection

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"sub-pool/backend/internal/estimation"
	"sub-pool/backend/internal/notifications"
)

func TestFinancialWalletChangeUsesQ8AndComparableCompleteScope(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	provenance := &SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: simulationTestEpoch}
	previous := WalletRecommendation{ParticipantID: "member", BindingID: "7", WalletRecommendationSnapshot: WalletRecommendationSnapshot{Complete: true, Target: estimation.Exact(big.NewRat(1, 1)), Simulation: provenance, ExpiresAt: &expires, Contributions: []WalletContribution{{AccountID: "account", AccountRevision: "1", SimulationProof: &simulationWalletSourceProof{StartAt: time.Now().Truncate(time.Second)}}}}}
	clone := func() WalletRecommendation {
		var r WalletRecommendation
		body, err := json.Marshal(previous)
		if err == nil {
			err = json.Unmarshal(body, &r)
		}
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	next := clone()
	next.Target = estimation.Exact(big.NewRat(1000000009, 1000000000))
	if financialWalletChanged(&previous, &next) {
		t.Fatal("sub-quantum change notified")
	}
	next.Target = estimation.Exact(big.NewRat(100000001, 100000000))
	if !financialWalletChanged(&previous, &next) {
		t.Fatal("one quantum change missed")
	}
	next.Target = estimation.Exact(new(big.Rat))
	if !financialWalletChanged(&previous, &next) {
		t.Fatal("known zero change missed")
	}
	for _, change := range []func(*WalletRecommendation){func(r *WalletRecommendation) { r.Complete = false }, func(r *WalletRecommendation) { r.BindingID = "8" }, func(r *WalletRecommendation) { r.Simulation.Epoch = "other" }, func(r *WalletRecommendation) { r.Contributions[0].AccountID = "hidden-source" }, func(r *WalletRecommendation) {
		r.Contributions[0].SimulationProof.StartAt = r.Contributions[0].SimulationProof.StartAt.Add(time.Second)
	}} {
		next := clone()
		next.Target = estimation.Exact(big.NewRat(2, 1))
		change(&next)
		if financialWalletChanged(&previous, &next) {
			t.Fatal("incomparable wallet change notified")
		}
	}
}

func TestFinancialEstimateChangeUsesExactComparableCapacity(t *testing.T) {
	f := simulatedCostFixture(t)
	first, err := f.s.SaveEstimateConfiguration(context.Background(), f.token, f.input())
	if err != nil || !first.Complete {
		t.Fatal("complete estimator prerequisite", err)
	}
	clone := func() EstimateRecord {
		var r EstimateRecord
		body, e := json.Marshal(first)
		if e == nil {
			e = json.Unmarshal(body, &r)
		}
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	for _, tc := range []struct {
		name, numerator, denominator string
		want                         bool
	}{
		{"below", "119999999999999999999", "100000000000000000000", false},
		{"exact", "6", "5", true}, {"decrease", "4", "5", true}, {"same", "1", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := clone()
			value, _ := next.Windows[0].Capacity.Rational()
			ratio, _ := new(big.Rat).SetString(tc.numerator + "/" + tc.denominator)
			next.Windows[0].Capacity = estimation.Exact(value.Mul(value, ratio))
			if financialEstimateChanged(&first, &next) != tc.want {
				t.Fatal("wrong exact significance")
			}
		})
	}
	for _, change := range []func(*EstimateRecord){
		func(r *EstimateRecord) { r.Complete = false },
		func(r *EstimateRecord) { r.Currency = "native_wallet" },
		func(r *EstimateRecord) { r.Configuration.Revision = "999" },
		func(r *EstimateRecord) { r.Windows[0].CostProof.PriceVersion = "999" },
		func(r *EstimateRecord) { r.Windows[0].Period.EndAt = r.Windows[0].Period.EndAt.Add(time.Second) },
	} {
		next := clone()
		value, _ := next.Windows[0].Capacity.Rational()
		next.Windows[0].Capacity = estimation.Exact(value.Mul(value, big.NewRat(2, 1)))
		change(&next)
		if financialEstimateChanged(&first, &next) {
			t.Fatal("incomparable estimate generated a change")
		}
	}
	zero := clone()
	zero.Windows[0].Capacity = estimation.Exact(new(big.Rat))
	if financialEstimateChanged(nil, &first) || financialEstimateChanged(&zero, &first) {
		t.Fatal("first or zero baseline generated a change")
	}
}

func TestFinancialWalletProducerRequiresActivationAndCurrentCompleteWholeWallet(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	mail, err := notifications.New(f.db, f.s.auth, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	mail.Financial = f.s
	configuration, err := mail.GetConfiguration(ctx, f.token)
	if err != nil || configuration.FinancialEnabled || !configuration.Capabilities[0].Available {
		t.Fatal("unsafe activation default", configuration, err)
	}
	f.mu.Lock()
	f.scope["balance"] = "0"
	f.mu.Unlock()
	first := f.refresh(t, "0")
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count); err != nil || count != 0 {
		t.Fatal("upgrade enabled saved preferences", count, err)
	}
	in := notifications.SaveInput{Revision: configuration.Revision, Enabled: true, FinancialEnabled: true, Channel: "smtp", From: "sender@owned.test", Recipient: "admin@owned.test", SMTP: notifications.SMTP{Host: "127.0.0.1", Port: 2525, TLS: "loopback_plain"}, Preferences: notifications.Preferences{BalanceLow: true, RecommendationChanged: true, EstimateChanged: true}, DedupeSeconds: 60, CredentialAction: "keep"}
	if _, err = mail.SaveConfiguration(ctx, f.token, in); err != nil {
		t.Fatal(err)
	}
	f.refresh(t, first.ID)
	page, err := mail.List(ctx, f.token, notifications.Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 1 || page.Notifications[0].Kind != "balance_low" || page.Notifications[0].State != "queued" {
		t.Fatal("complete low balance missing", page, err)
	}
	var evidence notifications.FinancialEvidence
	if err = f.db.QueryRow(ctx, `SELECT financial_evidence FROM notifications WHERE id=$1`, page.Notifications[0].ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := f.s.ValidateFinancialNotification(ctx, tx, evidence)
	_ = tx.Rollback(ctx)
	if err != nil || !eligible || f.calls.Load() != before {
		t.Fatal("dispatch validation called source or lost current proof", eligible, err)
	}
	exec(t, f.db, `UPDATE estimate_config SET revision=revision+1 WHERE account_id=$1`, f.a.ID)
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eligible, err = f.s.ValidateFinancialNotification(ctx, tx, evidence)
	_ = tx.Rollback(ctx)
	if err != nil || eligible || f.calls.Load() != before {
		t.Fatal("changed estimate retained notification eligibility", eligible, err)
	}
}

func TestFinancialEstimateProducerUsesActualObservationAndRejectsUnboundReplacement(t *testing.T) {
	f := simulatedCostFixture(t)
	ctx := context.Background()
	first, err := f.s.SaveEstimateConfiguration(ctx, f.token, f.input())
	if err != nil || !first.Complete {
		t.Fatal("estimate prerequisite", err)
	}
	mail, err := notifications.New(f.db, f.s.auth, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	mail.Financial = f.s
	c, err := mail.GetConfiguration(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	in := notifications.SaveInput{Revision: c.Revision, Enabled: true, FinancialEnabled: true, Channel: "smtp", From: "sender@owned.test", Recipient: "admin@owned.test", SMTP: notifications.SMTP{Host: "127.0.0.1", Port: 2525, TLS: "loopback_plain"}, Preferences: notifications.Preferences{EstimateChanged: true}, DedupeSeconds: 60, CredentialAction: "keep"}
	if _, err = mail.SaveConfiguration(ctx, f.token, in); err != nil {
		t.Fatal(err)
	}
	f.index.Store(3)
	collectObservation(t, f.s, f.db, f.token, f.c, f.a)
	page, err := mail.List(ctx, f.token, notifications.Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 1 || page.Notifications[0].Kind != "estimate_changed" || page.Notifications[0].State != "queued" {
		t.Fatal("actual observation did not publish significant change", page, err)
	}
	var evidence notifications.FinancialEvidence
	if err = f.db.QueryRow(ctx, `SELECT financial_evidence FROM notifications`).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := f.s.ValidateFinancialNotification(ctx, tx, evidence)
	_ = tx.Rollback(ctx)
	if err != nil || !valid {
		t.Fatal("current estimate event unavailable", valid, err)
	}
	if err = f.s.ConfigureSimulation(""); err != nil {
		t.Fatal(err)
	}
	c, err = mail.GetConfiguration(ctx, f.token)
	if err != nil || !c.FinancialEnabled || c.Capabilities[2].Available || c.Capabilities[2].Effective {
		t.Fatal("unbound runtime claimed financial capability", c, err)
	}
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	valid, err = f.s.ValidateFinancialNotification(ctx, tx, evidence)
	_ = tx.Rollback(ctx)
	if err != nil || valid {
		t.Fatal("unbound replacement retained event eligibility", valid, err)
	}
}

func TestFinancialWalletReadMustFollowLocalApplicationReceipts(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	read := f.refresh(t, "0")
	check := func(want bool) {
		t.Helper()
		tx, err := f.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := financialApplicationsCurrent(ctx, tx, &read)
		_ = tx.Rollback(ctx)
		if err != nil || actual != want {
			t.Fatal("local application freshness", actual, want, err)
		}
	}
	check(true)
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.RequestWalletApplication(ctx, f.token, WalletApplicationInput{WalletExpected: walletExpected(f.p, f.c, read.ID), RequestID: "AgICAgICAgICAgICAgICAg", RecommendationID: view.LatestRecommendation.ID})
	if err != nil || result.State != "rejected" {
		t.Fatal("native unavailable intent prerequisite", result, err)
	}
	check(false)
	read = f.refresh(t, read.ID)
	check(true)
	exec(t, f.db, `UPDATE wallet_application_requests SET snapshot=snapshot||'{"state":"pending","dispatch_state":"pending"}'::jsonb WHERE id=$1`, result.ID)
	check(false)
}

func TestFinancialWalletChangeSurvivesAnIncompleteIntermediateRead(t *testing.T) {
	f := simulatedWallet(t)
	ctx := context.Background()
	first := f.refresh(t, "0")
	mail, err := notifications.New(f.db, f.s.auth, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	mail.Financial = f.s
	c, err := mail.GetConfiguration(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	in := notifications.SaveInput{Revision: c.Revision, Enabled: true, FinancialEnabled: true, Channel: "smtp", From: "sender@owned.test", Recipient: "admin@owned.test", SMTP: notifications.SMTP{Host: "127.0.0.1", Port: 2525, TLS: "loopback_plain"}, Preferences: notifications.Preferences{RecommendationChanged: true}, DedupeSeconds: 60, CredentialAction: "keep"}
	if _, err = mail.SaveConfiguration(ctx, f.token, in); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.scope["scope_complete"] = false
	f.mu.Unlock()
	intermediate := f.refresh(t, first.ID)
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.LatestRecommendation.Complete {
		t.Fatal("incomplete intermediate prerequisite", view, err)
	}
	f.mu.Lock()
	f.scope["scope_complete"] = true
	f.mu.Unlock()
	estimate, err := f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	change := f.input()
	change.Revision, change.KeepRatio = estimate.Configuration.Revision, "0.6"
	if _, err = f.s.SaveEstimateConfiguration(ctx, f.token, change); err != nil {
		t.Fatal(err)
	}
	f.refresh(t, intermediate.ID)
	page, err := mail.List(ctx, f.token, notifications.Query{After: "0", Limit: 100})
	var changed []notifications.Notification
	for _, n := range page.Notifications {
		if n.Kind == "recommendation_changed" {
			changed = append(changed, n)
		}
	}
	if err != nil || len(changed) != 1 || changed[0].State != "queued" {
		t.Fatal("complete advice change lost after incomplete read", page, err)
	}
	var evidence notifications.FinancialEvidence
	if err = f.db.QueryRow(ctx, `SELECT financial_evidence FROM notifications WHERE id=$1`, changed[0].ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if current, err := f.s.ValidateFinancialNotification(ctx, tx, evidence); err != nil || !current {
		t.Fatal("historical complete baseline lost at dispatch", current, err)
	}
}
