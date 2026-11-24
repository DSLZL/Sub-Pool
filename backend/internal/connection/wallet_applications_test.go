package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func applicationInput(f *walletFixture, key byte) WalletApplicationInput {
	raw := make([]byte, 16)
	raw[15] = key
	return WalletApplicationInput{WalletExpected: walletExpected(f.p, f.c, "0"), RequestID: base64.RawURLEncoding.EncodeToString(raw), RecommendationID: "0"}
}

func TestWalletApplicationRejectionIsImmutableAndLocallyIdempotent(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	before := f.calls.Load()
	in := applicationInput(f, 1)
	first, err := f.s.RequestWalletApplication(ctx, f.token, in)
	if err != nil || first.ID == "" || first.State != "rejected" || first.DispatchState != "not_dispatched" || first.Target != nil || first.Details == nil || !slices.Contains(first.Details.Reasons, "RECOMMENDATION_NOT_GENERATED") || f.calls.Load() != before {
		t.Fatal("unqualified request dispatched or lost its rejection receipt", first, err)
	}
	stored, err := f.s.GetWalletApplication(ctx, f.token, first.ID)
	if err != nil || !reflect.DeepEqual(stored, first) {
		t.Fatal("persisted request differs from its receipt", stored, err)
	}
	bad := in
	bad.LatestID = "1"
	if _, err = f.s.RequestWalletApplication(ctx, f.token, bad); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("request key accepted changed input", err)
	}
	read, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
	if err != nil {
		t.Fatal(err)
	}
	before = f.calls.Load()
	replayed, err := f.s.RequestWalletApplication(ctx, f.token, in)
	if err != nil || !reflect.DeepEqual(first, replayed) || f.calls.Load() != before {
		t.Fatal("local readback replayed a native action or rewrote old input", replayed, err)
	}
	stale := applicationInput(f, 2)
	if _, err = f.s.RequestWalletApplication(ctx, f.token, stale); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale wallet head accepted", err)
	}
	stale.LatestID = read.ID
	if _, err = f.s.RequestWalletApplication(ctx, f.token, stale); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale recommendation head accepted", err)
	}
	save(t, f.s, f.token, "https://changed-application.example")
	replayed, err = f.s.RequestWalletApplication(ctx, f.token, in)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatal("changed configuration prevented immutable local readback", err)
	}
	if _, err = f.s.RequestWalletApplication(ctx, f.token, applicationInput(f, 3)); !errors.Is(err, auth.ErrConflict) || f.calls.Load() != before {
		t.Fatal("old source created a new request", err)
	}
	restarted, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := restarted.GetWalletApplication(ctx, f.token, first.ID)
	if err != nil || !reflect.DeepEqual(first, retained) {
		t.Fatal("service replacement lost immutable rejection", err)
	}
	var count, auditCount int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM management_events WHERE action='wallet.application.request' AND outcome='denied' AND operation_id=$1)`, "wallet-application:"+first.ID).Scan(&count, &auditCount); err != nil || count != 1 || auditCount != 1 {
		t.Fatal("local deduplication created extra records/audits", count, auditCount, err)
	}
}

func TestWalletApplicationHistoryScopesNumericAndExactTime(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	q := createParticipant(t, f.s, f.token, f.c, 22, "Hidden wallet member")
	before := f.calls.Load()
	input := applicationInput(f, 1)
	input.WalletExpected = walletExpected(q, f.c, "0")
	hidden, err := f.s.RequestWalletApplication(ctx, f.token, input)
	if err != nil {
		t.Fatal(err)
	}
	var first WalletApplication
	for key := byte(2); key <= 12; key++ {
		r, err := f.s.RequestWalletApplication(ctx, f.token, applicationInput(f, key))
		if err != nil {
			t.Fatal(err)
		}
		if key == 2 {
			first = r
		}
	}
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "application-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	query := WalletApplicationQuery{After: "0", Limit: 1}
	page, err := f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || len(page.Requests) != 1 || page.Requests[0].ID != "2" || page.NextAfter == nil || *page.NextAfter != "2" || page.Requests[0].Details != nil || page.Requests[0].Visibility != "participant_summary" {
		t.Fatal("scope was applied after pagination or leaked details", page, err)
	}
	body, _ := json.Marshal(page)
	for _, secret := range []string{q.ID, f.p.Binding.SourceID, first.Details.ActorID, first.Details.EvaluatedBasisHash, first.Details.Input.RequestID} {
		if strings.Contains(string(body), secret) {
			t.Fatal("ordinary summary leaked hidden/admin metadata")
		}
	}
	query.After = "9"
	page, err = f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || len(page.Requests) != 1 || page.Requests[0].ID != "10" {
		t.Fatal("history sorted decimal IDs lexically", page, err)
	}
	query = WalletApplicationQuery{ParticipantID: f.p.ID, StartAt: first.AttemptedAt.Format(time.RFC3339Nano), EndAt: first.AttemptedAt.Add(time.Nanosecond).Format(time.RFC3339Nano), After: "0", Limit: 100}
	page, err = f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || len(page.Requests) != 1 || page.Requests[0].ID != first.ID {
		t.Fatal("submicrosecond upper bound rounded a receipt out", page, err)
	}
	query.StartAt, query.EndAt = first.AttemptedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano), first.AttemptedAt.Format(time.RFC3339Nano)
	page, err = f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || len(page.Requests) != 0 {
		t.Fatal("exclusive end included its receipt", page, err)
	}
	query.StartAt, query.EndAt = first.AttemptedAt.Add(time.Nanosecond).Format(time.RFC3339Nano), first.AttemptedAt.Add(time.Second).Format(time.RFC3339Nano)
	page, err = f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || slices.ContainsFunc(page.Requests, func(r WalletApplication) bool { return r.ID == first.ID }) {
		t.Fatal("submicrosecond lower bound rounded a receipt in", page, err)
	}
	query.EndAt, query.Limit = "", 1
	page, err = f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || len(page.Requests) != 1 || page.Requests[0].ID != "3" {
		t.Fatal("open-ended operation filter did not precede the cursor page", err)
	}
	for _, id := range []string{hidden.ID, "999999"} {
		if _, err = f.s.GetWalletApplication(ctx, reader, id); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("hidden and absent detail differ", err)
		}
	}
	if _, err = f.s.RequestWalletApplication(ctx, reader, applicationInput(f, 30)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("balances read grant allowed an application request", err)
	}
	query = WalletApplicationQuery{ParticipantID: q.ID, After: "0", Limit: 100}
	if _, err = f.s.ListWalletApplications(ctx, reader, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("explicit hidden participant history leaked", err)
	}
	exec(t, f.db, `UPDATE auth_users SET participant_ids='{}' WHERE id=$1`, member.ID)
	query.ParticipantID = ""
	page, err = f.s.ListWalletApplications(ctx, reader, query)
	if err != nil || len(page.Requests) != 0 {
		t.Fatal("current scope contraction retained history", page, err)
	}
	if _, err = f.s.GetWalletApplication(ctx, reader, first.ID); !errors.Is(err, auth.ErrNotFound) || f.calls.Load() != before {
		t.Fatal("revoked detail retained access or history called upstream", err)
	}
}

func TestWalletApplicationConcurrentKeyExpiryAndAtomicAudit(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	before := f.calls.Load()
	in := applicationInput(f, 1)
	results := make(chan WalletApplication, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() { r, e := f.s.RequestWalletApplication(ctx, f.token, in); results <- r; errs <- e }()
	}
	a, b := <-results, <-results
	if e1, e2 := <-errs, <-errs; e1 != nil || e2 != nil || a.ID == "" || !reflect.DeepEqual(a, b) {
		t.Fatal("concurrent request key did not converge on one immutable receipt", e1, e2)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if err = lockWallet(ctx, blocker, f.p.ID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(2 * time.Second)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() {
		r, e := f.s.RequestWalletApplication(ctx, f.token, applicationInput(f, 2))
		if r.ID != "" {
			t.Error("expired request returned protected data")
		}
		done <- e
	}()
	observePoolWait(t, f.db, "%pg_advisory_xact_lock%")
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired queued application request persisted", err)
	}
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '30 minutes' WHERE digest=$1`, digest[:])
	exec(t, f.db, `CREATE FUNCTION application_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='wallet.application.request' THEN RAISE EXCEPTION 'forced application audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER application_audit_fail BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION application_audit_fail()`)
	if r, err := f.s.RequestWalletApplication(ctx, f.token, applicationInput(f, 3)); err == nil || r.ID != "" {
		t.Fatal("audit failure acknowledged a receipt", err)
	}
	var count, auditCount int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM management_events WHERE action='wallet.application.request')`).Scan(&count, &auditCount); err != nil || count != 1 || auditCount != 1 || f.calls.Load() != before {
		t.Fatal("request/audit rollback or local deduplication failed", count, auditCount, err)
	}
}

func TestWalletApplicationFinalAuditWaitRechecksExpiry(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(1380991565)`); err != nil {
		t.Fatal(err)
	}
	exec(t, f.db, `CREATE FUNCTION application_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='wallet.application.request' THEN PERFORM pg_advisory_xact_lock(1380991565); END IF; RETURN NEW; END $$; CREATE TRIGGER application_audit_wait BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION application_audit_wait()`)
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(2 * time.Second)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() {
		r, e := f.s.RequestWalletApplication(ctx, f.token, applicationInput(f, 1))
		if r.ID != "" {
			t.Error("expired final audit returned a protected receipt")
		}
		done <- e
	}()
	observePoolWait(t, f.db, "%INSERT INTO management_events%")
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("final audit wait retained expired authority", err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_application_requests`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired final audit committed the request", count, err)
	}
}

func TestWalletApplicationRejectionDoesNotCreateABindingObligation(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	first, err := f.s.RequestWalletApplication(ctx, f.token, applicationInput(f, 1))
	if err != nil {
		t.Fatal(err)
	}
	edit := participantEdit(f.p, f.c)
	edit.UpstreamID = "22"
	changed, err := f.s.SaveParticipant(ctx, f.token, edit)
	if err != nil || changed.Participant == nil {
		t.Fatal("a rejected non-dispatched request blocked correction", err)
	}
	stored, err := f.s.GetWalletApplication(ctx, f.token, first.ID)
	if err != nil || !reflect.DeepEqual(first, stored) || stored.Details.UpstreamUserID != "21" {
		t.Fatal("binding correction rewrote the original request", stored, err)
	}
	in := applicationInput(f, 2)
	in.WalletExpected = walletExpected(*changed.Participant, f.c, "0")
	next, err := f.s.RequestWalletApplication(ctx, f.token, in)
	if err != nil || next.Details.UpstreamUserID != "22" || next.Details.Input.BindingID == first.Details.Input.BindingID || next.State != "rejected" {
		t.Fatal("new binding inherited old intent references", next, err)
	}
}

func TestWalletApplicationRecordsStaleBasisAndRejectsCrossParticipantKey(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	read, err := f.s.RefreshWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.LatestRecommendation == nil {
		t.Fatal("missing actual recommendation", err)
	}
	in := applicationInput(f, 1)
	in.LatestID, in.RecommendationID = read.ID, view.LatestRecommendation.ID
	fresh, err := f.s.RequestWalletApplication(ctx, f.token, in)
	if err != nil || slices.Contains(fresh.Details.Reasons, "RECOMMENDATION_STALE") {
		t.Fatal("current recommendation marked stale", fresh, err)
	}
	edit := participantEdit(f.p, f.c)
	edit.Name = "Changed after recommendation"
	changed, err := f.s.SaveParticipant(ctx, f.token, edit)
	if err != nil || changed.Participant == nil {
		t.Fatal(err)
	}
	in.WalletExpected = walletExpected(*changed.Participant, f.c, read.ID)
	in.RequestID = applicationInput(f, 2).RequestID
	before := f.calls.Load()
	stale, err := f.s.RequestWalletApplication(ctx, f.token, in)
	if err != nil || stale.Details == nil || !slices.Contains(stale.Details.Reasons, "RECOMMENDATION_STALE") || stale.Details.EvaluatedBasisHash == fresh.Details.EvaluatedBasisHash || !reflect.DeepEqual(stale.Details.RecommendationBasisHash, fresh.Details.RecommendationBasisHash) || f.calls.Load() != before {
		t.Fatal("changed basis lost its original recommendation or contacted upstream", stale, err)
	}
	q := createParticipant(t, f.s, f.token, f.c, 22, "Other participant")
	in.WalletExpected = walletExpected(q, f.c, "0")
	in.RecommendationID = "0"
	if r, err := f.s.RequestWalletApplication(ctx, f.token, in); !errors.Is(err, auth.ErrConflict) || r.ID != "" {
		t.Fatal("actor request key was reused for another participant", r, err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_application_requests`).Scan(&count); err != nil || count != 2 {
		t.Fatal("conflicting key created another receipt", count, err)
	}
}
