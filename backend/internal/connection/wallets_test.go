package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

type walletFixture struct {
	s       *Service
	db      *pgxpool.Pool
	token   string
	c       Configuration
	p       Participant
	calls   atomic.Int32
	status  atomic.Int32
	balance atomic.Value
	frozen  atomic.Value
	entered chan struct{}
	release chan struct{}
	block   atomic.Bool
}

func newWalletFixture(t *testing.T, bearer bool) *walletFixture {
	t.Helper()
	f := &walletFixture{entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.s, f.db, f.token = setup(t)
	f.balance.Store("-17.000000000000000001")
	f.frozen.Store("2.5")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || (bearer && (r.Header.Get("Authorization") != "Bearer wallet-local-test-only" || r.Header.Get("x-api-key") != "")) || (!bearer && (r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Authorization") != "")) {
			t.Error("wallet boundary forwarded a secret/cookie or wrote upstream")
		}
		switch {
		case r.URL.Path == "/api/v1/auth/me":
			respond(w, map[string]any{"role": "admin", "run_mode": "simple"})
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			if f.block.Load() {
				f.entered <- struct{}{}
				select {
				case <-f.release:
				case <-r.Context().Done():
					return
				}
			}
			if status := f.status.Load(); status != 0 {
				http.Error(w, "private-upstream-denial wallet-local-test-only", int(status))
				return
			}
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			row := nativeUserRow(id)
			row["balance"], row["frozen_balance"] = json.RawMessage(f.balance.Load().(string)), json.RawMessage(f.frozen.Load().(string))
			respond(w, row)
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			respond(w, fullManagedAccount(id, "wallet-root-"+strconv.Itoa(id)))
		default:
			t.Errorf("unexpected wallet native route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.c = save(t, f.s, f.token, server.URL)
	if bearer {
		result, err := f.s.Save(context.Background(), f.token, SaveInput{Revision: f.c.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "Bearer wallet-local-test-only"})
		if err != nil {
			t.Fatal(err)
		}
		f.c = result.Configuration
	}
	f.p = createParticipant(t, f.s, f.token, f.c, 21, "Wallet member")
	return f
}

func walletExpected(p Participant, c Configuration, head string) WalletExpected {
	return WalletExpected{ParticipantID: p.ID, ParticipantRevision: p.Revision, BindingID: p.Binding.ID, ConnectionRevision: c.Revision, SourceID: p.Binding.SourceID, LatestID: head}
}

func TestWalletFieldsExactZeroNegativeMissingAndMasked(t *testing.T) {
	for _, c := range []struct {
		raw, want  string
		frozen, ok bool
	}{
		{"0", "0", false, true}, {"-0", "0", false, true}, {"-12.000000000000000001", "-12.000000000000000001", false, true}, {"2.5e-8", "0.000000025", true, true},
		{"-1", "", true, false}, {`"1"`, "", false, false}, {"1e999", "", false, false}, {"01", "", false, false}, {"null", "", false, false}, {"NaN", "", false, false},
	} {
		v, ok := walletNumber(json.RawMessage(c.raw), c.frozen)
		if ok != c.ok || ok && (v == nil || *v != c.want) {
			t.Fatal("native field numeric boundary changed", c, v, ok)
		}
	}
	if v := walletField(json.RawMessage("0"), false, false); v.Value != nil || v.Reason != "FIELD_PERMISSION_UNVERIFIED" {
		t.Fatal("masked zero became money")
	}
	if v := walletField(nil, false, true); v.Value != nil || v.Reason != "FIELD_NOT_RETURNED" {
		t.Fatal("missing field became zero")
	}
	if walletAdmin([]byte(`{"role":"admin","role":"user"}`)) {
		t.Fatal("ambiguous native authority accepted")
	}
}

func TestWalletNativeReadReceiptHistoryAndServiceReplacement(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.LatestRead != nil || view.LatestRecommendation != nil || view.CurrentTarget != nil {
		t.Fatal("unread wallet got a default balance", view, err)
	}
	input := walletExpected(f.p, f.c, "0")
	first, err := f.s.RefreshWallet(ctx, f.token, input)
	if err != nil || first.Balance.Value == nil || *first.Balance.Value != "-17.000000000000000001" || *first.FrozenBalance.Value != "2.5" || first.ReadAt == nil {
		t.Fatal("native wallet fields were altered", first, err)
	}
	before := f.calls.Load()
	if _, err = f.s.RefreshWallet(ctx, f.token, input); !errors.Is(err, auth.ErrConflict) || f.calls.Load() != before {
		t.Fatal("stale receipt replay reached native wallet", err)
	}
	f.status.Store(403)
	input.LatestID = first.ID
	failed, err := f.s.RefreshWallet(ctx, f.token, input)
	if err != nil || failed.Read.State != "denied" || failed.Balance.Value != nil || failed.FrozenBalance.Value != nil {
		t.Fatal("failed native read was fabricated", failed, err)
	}
	view, err = f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.LatestRead.ID != failed.ID || view.LastConfirmedRead.ID != first.ID || view.LatestRecommendation == nil || view.LatestRecommendation.Complete || view.LatestRecommendation.Applicable || view.Difference != nil || view.LastApplication != nil {
		t.Fatal("failed attempt erased prior read or implied application", view, err)
	}
	f.status.Store(0)
	f.balance.Store("0")
	f.frozen.Store("null")
	input.LatestID = failed.ID
	partial, err := f.s.RefreshWallet(ctx, f.token, input)
	if err != nil || partial.Balance.Value == nil || *partial.Balance.Value != "0" || partial.FrozenBalance.Value != nil {
		t.Fatal("partial field lost exact zero", partial, err)
	}
	view, _ = f.s.GetWallet(ctx, f.token, f.p.ID)
	if view.LastConfirmedRead.ID != first.ID {
		t.Fatal("partial read claimed both fields confirmed")
	}
	f.frozen.Store("0")
	input.LatestID = partial.ID
	for i := 0; i < 9; i++ {
		r, e := f.s.RefreshWallet(ctx, f.token, input)
		if e != nil {
			t.Fatal(e)
		}
		input.LatestID = r.ID
	}
	page, err := f.s.ListWalletHistory(ctx, f.token, f.p.ID, "wallet", "8", 1)
	if err != nil || len(page.WalletReads) != 1 || page.WalletReads[0].ID != "9" || page.NextAfter == nil || *page.NextAfter != "9" {
		t.Fatal("wallet cursor sorted lexical IDs", page, err)
	}
	page, err = f.s.ListWalletHistory(ctx, f.token, f.p.ID, "wallet", *page.NextAfter, 100)
	if err != nil || page.WalletReads[0].ID != "10" || page.NextAfter != nil {
		t.Fatal("wallet history skipped cross-digit ID", page, err)
	}
	before = f.calls.Load()
	old, _ := json.Marshal(view.LastConfirmedRead)
	restarted, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restarted.ListWalletHistory(ctx, f.token, f.p.ID, "wallet", "0", 100)
	retained, _ := json.Marshal(restored.WalletReads[0])
	if err != nil || !bytes.Equal(old, retained) || f.calls.Load() != before {
		t.Fatal("replacement rewrote history or queried upstream", err)
	}
}

func TestWalletOpaqueKeyValuesRemainUnverified(t *testing.T) {
	f := newWalletFixture(t, false)
	r, err := f.s.RefreshWallet(context.Background(), f.token, walletExpected(f.p, f.c, "0"))
	if err != nil || r.Read.State != "available" || r.FieldAuthority.State != "unverified" || r.Balance.Value != nil || r.FrozenBalance.Value != nil {
		t.Fatal("unqualified field permission accepted", r, err)
	}
}

func TestWalletNativeNetworkExpiryAndAuditRollback(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(2 * time.Second)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	f.block.Store(true)
	done := make(chan error, 1)
	go func() {
		r, e := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
		if e != nil && r.ID != "" {
			t.Error("expired operation returned protected receipt")
		}
		done <- e
	}()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("actual wallet GET not observed")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	close(f.release)
	if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired wallet native read committed", err)
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_reads)+(SELECT count(*) FROM wallet_recommendations)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired wallet read persisted", count, err)
	}
	f.block.Store(false)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '30 minutes' WHERE digest=$1`, digest[:])
	exec(t, f.db, `CREATE FUNCTION wallet_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='wallet.read' THEN RAISE EXCEPTION 'forced wallet audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER wallet_audit_fail BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION wallet_audit_fail()`)
	if r, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0")); err == nil || r.ID != "" {
		t.Fatal("audit failure acknowledged wallet receipt", err)
	}
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_reads)+(SELECT count(*) FROM wallet_recommendations)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("receipt/recommendation/audit were not atomic", count, err)
	}
}

func TestWalletFullSourcesBeforeCurrentPrivacyAndInvalidation(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	a := adoptManaged(t, f.s, f.token, f.c, 1, "Visible root")
	b := adoptManaged(t, f.s, f.token, f.c, 2, "Hidden root")
	q := createParticipant(t, f.s, f.token, f.c, 22, "Hidden member")
	pool := savePool(t, f.s, f.token, PoolUpdate{PoolInput: poolInput(f.c, []ManagedAccount{a, b}, []Participant{f.p, q}, []string{"25", "50"})})
	record, err := f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
	if err != nil || record.Global.SourceCount != 2 || len(record.Contributions) != 2 || record.Target != nil || record.Applicable || record.Complete {
		t.Fatal("whole-source unavailable advice was incomplete or qualified", record, err)
	}
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "wallet-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	page, err := f.s.ListWallets(ctx, reader, "", 1)
	if err != nil || len(page.Participants) != 1 || page.Participants[0].ParticipantID != f.p.ID || page.NextAfter != nil {
		t.Fatal("wallet scope applied after paging", page, err)
	}
	view, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || view.LatestRecommendation != nil || view.NeedsRecalculation != nil || view.Visibility != "authorized_sources" || len(view.Contributions) != 1 || view.Contributions[0].AccountID != a.ID {
		t.Fatal("wallet global metadata reached scoped reader", view, err)
	}
	encoded, _ := json.Marshal(view)
	for _, secret := range []string{b.ID, q.ID, "source_count", "basis_hash", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "private-contact", "wallet-local-test-only"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("hidden wallet metadata leaked", secret)
		}
	}
	if _, err = f.s.GetWallet(ctx, reader, q.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("hidden participant existence leaked", err)
	}
	if _, err = f.s.ListWalletHistory(ctx, reader, f.p.ID, "recommendation", "0", 100); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("global history leaked to ordinary reader", err)
	}
	if _, err = f.s.RefreshWallet(ctx, reader, walletExpected(f.p, f.c, "0")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("read feature granted native collection", err)
	}
	if f.calls.Load() != before {
		t.Fatal("local/scoped wallet reads contacted native target")
	}
	admin, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || admin.NeedsRecalculation == nil || *admin.NeedsRecalculation {
		t.Fatal("unchanged source snapshot was invalidated", admin, err)
	}
	old, _ := json.Marshal(record)
	// Removing the member does not erase recorded contributions from an old contract.
	nextInput := poolInput(f.c, []ManagedAccount{a, b}, []Participant{q}, []string{"50"})
	savePool(t, f.s, f.token, PoolUpdate{ID: pool.PoolID, Revision: pool.Revision, PoolInput: nextInput})
	admin, err = f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || !*admin.NeedsRecalculation || admin.CurrentState != "recalculation_required" || len(admin.Contributions) != 2 || admin.Contributions[0].State != "contract_unavailable" {
		t.Fatal("contract change erased history or preserved advice", admin, err)
	}
	next, err := f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, record.ID))
	if err != nil || next.Global.SourceCount != 2 {
		t.Fatal("recompute dropped historical managed source", next, err)
	}
	history, err := f.s.ListWalletHistory(ctx, f.token, f.p.ID, "recommendation", "0", 100)
	retained, _ := json.Marshal(history.Recommendations[0])
	if err != nil || !bytes.Equal(old, retained) {
		t.Fatal("old advice rewritten", err)
	}
	exec(t, f.db, `UPDATE auth_users SET account_ids=ARRAY[$2]::text[] WHERE id=$1`, member.ID, b.ID)
	view, err = f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || len(view.Contributions) != 1 || view.Contributions[0].AccountID != b.ID {
		t.Fatal("current account contraction not enforced", view, err)
	}
}

func TestWalletAdvisoryWaitExpiryAndSourceVersionsRejectBeforeDispatch(t *testing.T) {
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
	before := f.calls.Load()
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(2 * time.Second)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() { _, e := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0")); done <- e }()
	observePoolWait(t, f.db, "%pg_advisory_xact_lock%")
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) || f.calls.Load() != before {
		t.Fatal("expired waiting wallet request dispatched", err)
	}
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '30 minutes' WHERE digest=$1`, digest[:])
	stale := walletExpected(f.p, f.c, "0")
	stale.BindingID = "9007199254740993"
	if _, err = f.s.RefreshWallet(ctx, f.token, stale); !errors.Is(err, auth.ErrConflict) || f.calls.Load() != before {
		t.Fatal("stale binding contacted target", err)
	}
}

func TestWalletBindingCorrectionPreservesOldReceiptWithoutExposingItAsCurrent(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	first, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
	if err != nil {
		t.Fatal(err)
	}
	edit := participantEdit(f.p, f.c)
	edit.UpstreamID = "22"
	changed, err := f.s.SaveParticipant(ctx, f.token, edit)
	if err != nil || changed.Participant == nil {
		t.Fatal("read receipt invented a financial obligation", err)
	}
	current, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || current.UpstreamUserID != "22" || current.LatestRead != nil || current.LastConfirmedRead != nil || current.LatestRecommendation != nil {
		t.Fatal("old native wallet leaked after corrected binding", current, err)
	}
	page, err := f.s.ListWalletHistory(ctx, f.token, f.p.ID, "wallet", "0", 100)
	if err != nil || len(page.WalletReads) != 1 || page.WalletReads[0].BindingID != first.BindingID || page.WalletReads[0].UpstreamUserID != "21" {
		t.Fatal("old native receipt rebound retroactively", page, err)
	}
	if _, err = f.s.RefreshWallet(ctx, f.token, walletExpected(*changed.Participant, f.c, "0")); err != nil {
		t.Fatal("new binding inherited the old receipt cursor", err)
	}
	before := f.calls.Load()
	save(t, f.s, f.token, "https://changed-wallet.example")
	current, err = f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || current.SourceCurrent || current.ReadReferencesCurrent || current.CurrentState != "recalculation_required" || f.calls.Load() != before {
		t.Fatal("changed source reused stale wallet authority", current, err)
	}
	if _, err = f.s.RefreshWallet(ctx, f.token, walletExpected(*changed.Participant, f.c, current.LatestRead.ID)); !errors.Is(err, auth.ErrConflict) || f.calls.Load() != before {
		t.Fatal("stale source dispatched wallet read", err)
	}
}

func TestWalletConcurrentExpectedHeadAndReadLockExpiry(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	before := f.calls.Load()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0")); results <- err }()
	}
	succeeded, conflict := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, auth.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflict != 1 || f.calls.Load()-before != 2 {
		t.Fatal("concurrent expected head dispatched duplicate reads", succeeded, conflict, f.calls.Load()-before)
	}
	barrier, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, `SELECT id FROM participants WHERE id=$1 FOR UPDATE`, f.p.ID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(2 * time.Second)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() {
		view, e := f.s.GetWallet(ctx, f.token, f.p.ID)
		if view.ParticipantID != "" {
			done <- errors.New("expired wallet read returned protected fields")
		} else {
			done <- e
		}
	}()
	observePoolWait(t, f.db, "%FROM participants p%FOR SHARE OF p%")
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("waiting current wallet read retained expired authority", err)
	}
}

func TestWalletBasisExpiryAndEvidenceLimitNeverCreateMoney(t *testing.T) {
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	source, binding := "source", "1"
	sourceAt, oldest := now.Add(-10*time.Second), now.Add(-20*time.Second)
	e := &EstimateSnapshot{
		Configuration:  EstimateConfiguration{Revision: "1", Assumptions: &EstimateAssumptions{SourceID: source, ConnectionRevision: "1", AccountRevision: "1", SourceMode: "passive"}},
		PolicyRevision: "1", ManagementRevision: "1", ObservationHead: "1", CaptureHead: "0", State: "evidence_incomplete",
		Windows: []EstimateWindow{{SourceAt: &sourceAt, OldestSampleAt: &oldest, Period: &ObservationPeriod{EndAt: now.Add(30 * time.Second)}}},
	}
	b := walletBasis{ParticipantRevision: "1", BindingID: binding, SourceID: source, Enabled: true, CurrentSource: &source, ConnectionRevision: "1", PolicyRevision: "1", StaleAfterSeconds: 60,
		Sources: []walletSource{{AccountID: "account", Revision: "1", SourceID: source, SourceMode: "passive", IdentityState: "verified", ReadState: "available", ObservedConnection: "1", EstimateRevision: "1", ManagementRevision: "1", ObservationHead: "1", CaptureHead: "0", Estimate: e,
			Contract: &walletContract{ID: "1", SourceID: source, ConnectionRevision: "1", BindingID: &binding, Enabled: true}}}}
	fresh := walletRecommendation(b, "member", now)
	if fresh.ExpiresAt == nil || !fresh.ExpiresAt.Equal(now.Add(30*time.Second)) || fresh.Contributions[0].EstimateState != "evidence_incomplete" || fresh.Target != nil || fresh.Complete || fresh.Applicable {
		t.Fatal("unqualified inputs created money or lost earliest expiry", fresh)
	}
	stale := walletRecommendation(b, "member", now.Add(61*time.Second))
	if stale.BasisHash != fresh.BasisHash || stale.ExpiresAt != nil || stale.Contributions[0].EstimateState != "source_stale" || stale.Target != nil {
		t.Fatal("time expiry rewrote provenance or retained current evidence", stale)
	}
	b.Sources[0].CaptureHead = "2"
	changed := walletRecommendation(b, "member", now)
	if changed.BasisHash == fresh.BasisHash || changed.Contributions[0].EstimateState != "recalculation_required" {
		t.Fatal("new capture did not invalidate the saved estimate", changed)
	}
	b.Sources = make([]walletSource, 501)
	limited := walletRecommendation(b, "member", now)
	if limited.Global.State != "evidence_limit" || limited.Global.SourceCount != 501 || len(limited.Contributions) != 0 || limited.Target != nil || limited.Applicable {
		t.Fatal("bounded evaluation silently used partial sources", limited)
	}
}
