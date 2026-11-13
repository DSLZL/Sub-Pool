package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
)

func scanWalletInputs(t *testing.T, s *Service) {
	t.Helper()
	after := ""
	for {
		next, err := s.processWalletRecommendations(context.Background(), after)
		if err != nil {
			t.Fatal(err)
		}
		if next == "" {
			return
		}
		if next <= after {
			t.Fatal("wallet scan cursor did not advance")
		}
		after = next
	}
}

func currentWalletRecommendation(t *testing.T, s *Service, token, id string) WalletRecommendation {
	t.Helper()
	v, err := s.GetWallet(context.Background(), token, id)
	if err != nil || v.LatestRecommendation == nil || v.NeedsRecalculation == nil || *v.NeedsRecalculation || v.CurrentTarget != nil || v.Applicable {
		t.Fatal("automatic explanation is missing, stale or became money", v, err)
	}
	return *v.LatestRecommendation
}

func TestWalletInputScanTracksRecordedProducersAndRetainsHistoricalSources(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	p := createParticipant(t, s, token, c, 21, "Wallet input member")
	q := createParticipant(t, s, token, c, 22, "Other member")
	a, b := passiveManaged(t, s, token, c, 1), passiveManaged(t, s, token, c, 2)
	pool := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{p, q}, []string{"25", "50"})})
	scanWalletInputs(t, s)
	first := currentWalletRecommendation(t, s, token, p.ID)
	if first.Trigger != "input_change" || first.Global.SourceCount != 2 {
		t.Fatal("initial global input explanation was not recorded", first)
	}
	old, _ := json.Marshal(first)
	previous := first
	check := func() {
		t.Helper()
		before := calls.Load()
		scanWalletInputs(t, s)
		next := currentWalletRecommendation(t, s, token, p.ID)
		if next.ID == previous.ID || next.BasisHash == previous.BasisHash || next.Trigger != "input_change" || len(next.Contributions) != 2 || calls.Load() != before {
			t.Fatal("recorded input did not create one local full-source explanation", next, previous.ID)
		}
		scanWalletInputs(t, s)
		if same := currentWalletRecommendation(t, s, token, p.ID); same.ID != next.ID {
			t.Fatal("unchanged input appended duplicate history")
		}
		previous = next
	}
	fact := collectObservation(t, s, db, token, c, a)
	check()
	if _, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c)); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: fact.ID, Excluded: true, Reason: "Retain source but exclude the sample"}); err != nil {
		t.Fatal(err)
	}
	check()
	savePool(t, s, token, PoolUpdate{ID: pool.PoolID, Revision: pool.Revision, PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{q}, []string{"50"})})
	check()
	if previous.Contributions[0].State != "contract_unavailable" || previous.Contributions[1].State != "contract_unavailable" {
		t.Fatal("removed member's historical sources were hidden")
	}
	exec(t, db, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) SELECT $1,id,clock_timestamp(),'{}'::jsonb FROM auth_users WHERE role='admin'`, a.ID)
	check()
	exec(t, db, `UPDATE monitoring_config SET revision=revision+1 WHERE singleton`)
	check()
	if _, err := s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "New recorded identity revision", Monitoring: a.Monitoring, SourceMode: a.SourceMode}); err != nil {
		t.Fatal(err)
	}
	check()
	edit := participantEdit(p, c)
	edit.Name = "Current wallet member name"
	if _, err := s.SaveParticipant(ctx, token, edit); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: c.BaseURL, CredentialAction: "keep"}); err != nil {
		t.Fatal(err)
	}
	check()
	history, err := s.ListWalletHistory(ctx, token, p.ID, "recommendation", "0", 100)
	if err != nil || len(history.Recommendations) != 10 {
		t.Fatal("producer transitions missing from retained history", len(history.Recommendations), err)
	}
	retained, _ := json.Marshal(history.Recommendations[0])
	if !bytes.Equal(old, retained) {
		t.Fatal("automatic input changes rewrote original history")
	}
}

func TestWalletInputScanPagesReplacementAndBindingLifecycle(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	for i := 0; i < walletScanSize+2; i++ {
		createParticipant(t, f.s, f.token, f.c, 100+i, fmt.Sprintf("Scanned member %d", i))
	}
	after, err := f.s.processWalletRecommendations(ctx, "")
	if err != nil || after == "" {
		t.Fatal("first bounded wallet page did not return a cursor", after, err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != walletScanSize {
		t.Fatal("wallet batch exceeded or truncated its fixed bound", count, err)
	}
	if next, err := f.s.processWalletRecommendations(ctx, after); err != nil || next != "" {
		t.Fatal("second wallet page did not finish", next, err)
	}
	replacement, err := New(f.db, auth.New(f.db), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	scanWalletInputs(t, replacement)
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != walletScanSize+3 || f.calls.Load() != before {
		t.Fatal("replacement scan duplicated history or called native", count, err)
	}
	old := currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	edit := participantEdit(f.p, f.c)
	edit.UpstreamID = "22"
	changed, err := f.s.SaveParticipant(ctx, f.token, edit)
	if err != nil || changed.Participant == nil {
		t.Fatal("read-only explanation created a binding obligation", err)
	}
	scanWalletInputs(t, f.s)
	current := currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	if current.BindingID == old.BindingID || current.ParticipantRevision != changed.Participant.Revision {
		t.Fatal("new binding reused another native user's explanation")
	}
	if err = f.s.DeleteParticipant(ctx, f.token, f.p.ID, changed.Participant.Revision, changed.Participant.Name); err != nil {
		t.Fatal("automatic explanation prevented unused participant deletion", err)
	}
	scanWalletInputs(t, f.s)
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations WHERE participant_id=$1`, f.p.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("deleted participant was scanned or old binding history lost", count, err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE action='wallet.recommendation' AND (actor_id IS NOT NULL OR task_id IS DISTINCT FROM 'wallet-inputs')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("automatic recommendations were attributed to a user", count, err)
	}
}

func TestWalletInputScanSerializesWithManualRecompute(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if err = lockWallet(ctx, blocker, f.p.ID); err != nil {
		t.Fatal(err)
	}
	automatic, manual := make(chan error, 1), make(chan error, 1)
	go func() { automatic <- f.s.refreshWalletRecommendation(ctx, f.p.ID) }()
	observePoolWait(t, f.db, "%pg_advisory_xact_lock%")
	go func() { _, e := f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, "0")); manual <- e }()
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-automatic; err != nil {
		t.Fatal(err)
	}
	if err = <-manual; err != nil && !errors.Is(err, auth.ErrConflict) {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != 1 {
		t.Fatal("serialized scan and expected-head recompute duplicated advice", count, err)
	}
	current := currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	if _, err = f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, current.ID)); err != nil {
		t.Fatal(err)
	}
	scanWalletInputs(t, f.s)
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != 2 {
		t.Fatal("manual history was suppressed or re-appended by scanner", count, err)
	}
}

func TestWalletInputScanTimeoutAdvancesAndAuditFailureRollsBack(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	createParticipant(t, f.s, f.token, f.c, 22, "Second wallet")
	var first string
	if err := f.db.QueryRow(ctx, `SELECT min(id) FROM participants`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if err = lockWallet(ctx, blocker, first); err != nil {
		t.Fatal(err)
	}
	type result struct {
		after string
		err   error
	}
	done := make(chan result, 1)
	go func() { after, e := f.s.processWalletRecommendations(ctx, ""); done <- result{after, e} }()
	observePoolWait(t, f.db, "%pg_advisory_xact_lock%")
	r := <-done
	if r.err == nil || r.after != first {
		t.Fatal("busy participant stopped cursor progression", r)
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.processWalletRecommendations(ctx, r.after); err != nil {
		t.Fatal("timeout poisoned subsequent scan", err)
	}
	scanWalletInputs(t, f.s)
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != 2 {
		t.Fatal("later wallet or retry was lost", count, err)
	}
	exec(t, f.db, `CREATE FUNCTION wallet_input_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='wallet.recommendation' THEN RAISE EXCEPTION 'forced input audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER wallet_input_audit_fail BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION wallet_input_audit_fail()`)
	exec(t, f.db, `UPDATE participants SET revision=revision+1 WHERE id=$1`, first)
	if _, err = f.s.processWalletRecommendations(ctx, ""); err == nil {
		t.Fatal("audit failure acknowledged automatic advice")
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed audit left a recommendation", count, err)
	}
	exec(t, f.db, `DROP TRIGGER wallet_input_audit_fail ON management_events`)
	scanWalletInputs(t, f.s)
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_recommendations`).Scan(&count); err != nil || count != 3 {
		t.Fatal("failed input basis was not retried", count, err)
	}
}

func TestWalletInputScanRecordsExpiryWithoutInventingNewBasis(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	a := adoptManaged(t, f.s, f.token, f.c, 1, "Expiry source")
	savePool(t, f.s, f.token, PoolUpdate{PoolInput: poolInput(f.c, []ManagedAccount{a}, []Participant{f.p}, []string{"25"})})
	estimate, err := f.s.SaveEstimateConfiguration(ctx, f.token, estimateInput(a, f.c))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	estimate.Windows = []EstimateWindow{{Kind: "short", Period: &ObservationPeriod{EndAt: expires}}}
	body, _ := json.Marshal(estimate.EstimateSnapshot)
	exec(t, f.db, `INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES($1,clock_timestamp(),$2)`, a.ID, body)
	scanWalletInputs(t, f.s)
	fresh := currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	if fresh.ExpiresAt == nil || !fresh.ExpiresAt.Equal(expires) {
		t.Fatal("fixture did not retain the real upcoming expiry", fresh)
	}
	time.Sleep(time.Until(expires) + 5*time.Millisecond)
	scanWalletInputs(t, f.s)
	stale := currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	if stale.ID == fresh.ID || stale.BasisHash != fresh.BasisHash || stale.ExpiresAt != nil || stale.Contributions[0].EstimateState != "source_stale" {
		t.Fatal("expiry was ignored or fabricated a new input basis", fresh, stale)
	}
	scanWalletInputs(t, f.s)
	if current := currentWalletRecommendation(t, f.s, f.token, f.p.ID); current.ID != stale.ID {
		t.Fatal("expired unchanged input appended repeatedly")
	}
}

func TestWalletInputScanRespectsRuntimeExclusiveAndClosedBarrier(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	cfg := f.db.Config()
	cfg.ConnConfig.RuntimeParams["application_name"] = "sub-pool-wallet-input-history-test"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Close()
	f.db, f.s.pool = pool, pool
	f.s.auth = auth.New(pool)
	t.Cleanup(pool.Close)
	runtime, err := database.NewRuntime(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	f.s.Runtime = runtime
	entered, release := make(chan struct{}), make(chan struct{})
	exclusive := make(chan error, 1)
	go func() {
		exclusive <- runtime.Exclusive(ctx, func(*pgxpool.Conn) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case err = <-exclusive:
		t.Fatal("runtime exclusive barrier did not open", err)
	case <-time.After(3 * time.Second):
		t.Fatal("runtime exclusive barrier timed out")
	}
	done := make(chan error, 1)
	go func() { _, e := f.s.processWalletRecommendations(ctx, ""); done <- e }()
	select {
	case err = <-done:
		close(release)
		t.Fatal("scan crossed an active runtime exclusive barrier", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err = <-exclusive; err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil && !errors.Is(err, database.ErrRuntimeChanged) {
		t.Fatal("scan did not recover after runtime generation changed", err)
	}
	scanWalletInputs(t, f.s)
	currentWalletRecommendation(t, f.s, f.token, f.p.ID)
	runtime.Close()
	if _, err = f.s.processWalletRecommendations(ctx, ""); !errors.Is(err, database.ErrRuntimeUnavailable) {
		t.Fatal("closed runtime accepted a new input-history scan", err)
	}
}
