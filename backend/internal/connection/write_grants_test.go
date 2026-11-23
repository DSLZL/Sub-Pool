package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func testGrantInput(view WriteGrantView, capability string, enabled bool) WriteGrantUpdate {
	in := WriteGrantUpdate{
		SourceID: *view.SourceID, ConnectionRevision: view.ConnectionRevision, Capability: capability,
		Enabled: &enabled, ExpectedGrantID: "0", ConfirmationSHA256: view.ConfirmationSHA256, CurrentPassword: "TestPassword123",
	}
	if grant := findTestGrant(view, capability); grant != nil && grant.RevokedAt == nil {
		in.ExpectedGrantID = grant.ID
	}
	return in
}

func findTestGrant(view WriteGrantView, capability string) *WriteGrant {
	for _, status := range view.Grants {
		if status.Capability == capability {
			return status.Grant
		}
	}
	return nil
}

func setTestWriteGrant(t *testing.T, s *Service, token, capability string, enabled bool) *WriteGrant {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	view, err := s.GetWriteGrants(ctx, token)
	if err != nil || view.SourceID == nil {
		t.Fatal("grant lookup failed", err)
	}
	out, err := s.UpdateWriteGrant(ctx, token, testGrantInput(view, capability, enabled))
	if err != nil {
		t.Fatal("grant update failed", err)
	}
	return findTestGrant(out, capability)
}

func TestWriteGrantManagementBindsActorSourceAndExpectedGrant(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	c := save(t, s, token, "http://127.0.0.1:17010")
	view, err := s.GetWriteGrants(ctx, token)
	if err != nil || view.SourceID == nil || *view.SourceID != *c.SourceID || len(view.Grants) != 3 || view.ConfirmationSHA256 != writeGrantConfirmationHash {
		t.Fatal("missing initial grant view", view, err)
	}
	for _, status := range view.Grants {
		if status.Grant != nil {
			t.Fatal("new source inherited a grant", status)
		}
	}
	_, err = s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "grant-reader", Password: "ReaderPassword123", Enabled: true,
		Grants: auth.Grants{Features: []string{}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, "grant-reader", "ReaderPassword123", netip.MustParseAddr("127.0.0.2"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.auth.Current(ctx, token, false)
	if err != nil {
		t.Fatal(err)
	}
	in := testGrantInput(view, writePricing, true)
	in.CurrentPassword = "wrong"
	if _, err := s.UpdateWriteGrant(ctx, token, in); !errors.Is(err, auth.ErrCurrentPassword) {
		t.Fatal("wrong password accepted", err)
	}
	in.CurrentPassword = "TestPassword123"
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Service: s, Boundary: boundary}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.auth.IssueAPIKey(ctx, token, "grant boundary", "management")
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/connection/write-grants/update"
	if result := machineCall(handler, path, string(body), key.Secret); result.Code != http.StatusForbidden {
		t.Fatal("machine credential reached the grant mutation", result.Code)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://elsewhere.test")
	request.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("foreign origin reached the grant mutation", response.Code)
	}
	if _, err := s.UpdateWriteGrant(ctx, reader, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("reader authorized to grant", err)
	}
	stale := in
	stale.ConnectionRevision = "1"
	if _, err = s.UpdateWriteGrant(ctx, token, stale); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale connection authorized", err)
	}
	stale = in
	stale.ConfirmationSHA256 = "old"
	if _, err = s.UpdateWriteGrant(ctx, token, stale); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale acknowledgement authorized", err)
	}
	exec(t, db, `CREATE FUNCTION fail_grant_audit() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN RAISE EXCEPTION 'audit unavailable'; END $$;
	CREATE TRIGGER fail_grant_audit BEFORE INSERT ON management_events FOR EACH ROW
	WHEN (NEW.action='connection.write_grant.enable') EXECUTE FUNCTION fail_grant_audit()`)
	if _, err = s.UpdateWriteGrant(ctx, token, in); err == nil {
		t.Fatal("audit failure did not roll back grant")
	}
	exec(t, db, `DROP TRIGGER fail_grant_audit ON management_events`)
	fresh, err := s.UpdateWriteGrant(ctx, token, in)
	first := findTestGrant(fresh, writePricing)
	if err != nil || first == nil || first.GrantedBy != admin.User.ID || first.GrantedAt.IsZero() ||
		first.ConfirmationSHA256 != view.ConfirmationSHA256 || first.RevokedAt != nil {
		t.Fatal("grant did not retain actor and acknowledgement", first, err)
	}
	if _, err = s.UpdateWriteGrant(ctx, token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale enable accepted", err)
	}
	revoke := testGrantInput(fresh, writePricing, false)
	closed, err := s.UpdateWriteGrant(ctx, token, revoke)
	history := findTestGrant(closed, writePricing)
	if err != nil || history == nil || history.ID != first.ID || history.RevokedAt == nil ||
		history.RevokedAt.Before(first.GrantedAt) || history.RevokedBy == nil || *history.RevokedBy != admin.User.ID {
		t.Fatal("revocation history unavailable", history, err)
	}
	second := setTestWriteGrant(t, s, token, writePricing, true)
	if second.ID == first.ID {
		t.Fatal("regranted the same record")
	}
	if _, err = s.UpdateWriteGrant(ctx, token, revoke); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale revoke affected another grant", err)
	}
	if err = s.auth.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateWriteGrant(ctx, token, in); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("revoked session authorized to grant", err)
	}
	var events, rows int
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM management_events WHERE actor_id=$1 AND source_id=$2 AND action LIKE 'connection.write_grant.%'),
		(SELECT count(*) FROM upstream_write_grants)`, admin.User.ID, c.SourceID).Scan(&events, &rows); err != nil || events != 3 || rows != 2 {
		t.Fatal("state and audit diverged", events, rows, err)
	}
}

func TestWriteGrantPricingRevocationDuringPreflightAndDispatch(t *testing.T) {
	f := newPricingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type pricingResult struct {
		record PricingOperation
		err    error
	}
	start := func(in PricingApplyInput) <-chan pricingResult {
		result := make(chan pricingResult, 1)
		go func() {
			record, err := f.s.ApplyPricing(ctx, f.token, in)
			result <- pricingResult{record, err}
		}()
		return result
	}
	first := pricingInput(t, f, "1", fastPricing())
	entered, release := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	secondRead := f.reads + 2
	f.onRead = func(read int) {
		if read == secondRead {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	f.mu.Unlock()
	done := start(first)
	select {
	case <-entered:
	case result := <-done:
		t.Fatal("pricing never reached the final preflight", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	old := setTestWriteGrant(t, f.s, f.token, writePricing, false)
	replacement := setTestWriteGrant(t, f.s, f.token, writePricing, true)
	if replacement.ID == old.ID {
		t.Fatal("new authorization reused the old grant")
	}
	close(release)
	blocked := <-done
	if blocked.err != nil || blocked.record.State != "conflict" || blocked.record.Reason != writeGrantNeeded {
		t.Fatal("previous intent escaped after regrant", blocked)
	}
	again, err := f.s.ApplyPricing(ctx, f.token, first)
	if err != nil || again.ID != blocked.record.ID || again.Reason != writeGrantNeeded {
		t.Fatal("previous intent was eligible for another attempt", again, err)
	}
	f.mu.Lock()
	f.onRead = nil
	if f.writes != 0 {
		t.Error("preflight reached native write after revocation", f.writes)
	}
	f.mu.Unlock()

	second := pricingInput(t, f, "1", fastPricing())
	sent, complete := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.onWrite = func() {
		close(sent)
		select {
		case <-complete:
		case <-ctx.Done():
		}
	}
	f.mu.Unlock()
	done = start(second)
	select {
	case <-sent:
	case result := <-done:
		t.Fatal("fresh grant did not dispatch", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	view, err := f.s.GetWriteGrants(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() {
		_, err := f.s.UpdateWriteGrant(ctx, f.token, testGrantInput(view, writePricing, false))
		revoked <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock'
			AND query LIKE 'UPDATE upstream_write_grants SET revoked_by=%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-revoked:
			t.Fatal("revoke completed before the already-sent write", err)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("revoke did not reach the grant row")
		}
	}
	close(complete)
	applied := <-done
	if applied.err != nil || applied.record.State != "applied" || applied.record.AfterFingerprint == nil {
		t.Fatal("already-sent operation lost independent read-back", applied)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	reverted, err := f.s.RevertPricing(ctx, f.token, PricingOperationInput{ID: applied.record.ID, Revision: applied.record.Revision, AcknowledgeSharedImpact: true})
	if err != nil || reverted.State != "needs_review" || reverted.Reason != writeGrantNeeded {
		t.Fatal("revert was not closed after revocation", reverted, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 {
		t.Fatal("a closed grant dispatched a write", f.writes)
	}
}

func TestWriteGrantClosesDispatchWorkerAfterRevocationAndEmergency(t *testing.T) {
	for _, emergency := range []bool{false, true} {
		name := "revocation"
		if emergency {
			name = "emergency"
		}
		t.Run(name, func(t *testing.T) {
			f := newDispatchFixture(t, true)
			ctx := context.Background()
			paused := pauseDispatch(t, f)
			exec(t, f.db, `UPDATE dispatch_pauses SET restore_at=clock_timestamp()-interval '1 second' WHERE id=$1`, paused.ID)
			reason := writeGrantNeeded
			if emergency {
				f.s.UpstreamWritesDisabled = true
				reason = writesDisabled
			} else {
				setTestWriteGrant(t, f.s, f.token, writeScheduling, false)
			}
			if processed, err := f.s.processDispatchDue(ctx); err != nil || !processed {
				t.Fatal("closed restore duty did not get a persisted result", processed, err)
			}
			var state, got string
			if err := f.db.QueryRow(ctx, `SELECT state,reason FROM dispatch_pauses WHERE id=$1`, paused.ID).Scan(&state, &got); err != nil || state != "needs_review" || got != reason {
				t.Fatal("restore was not retained for inspection", state, got, err)
			}
			if emergency {
				f.s.UpstreamWritesDisabled = false
			} else {
				setTestWriteGrant(t, f.s, f.token, writeScheduling, true)
			}
			if processed, err := f.s.processDispatchDue(ctx); err != nil || processed {
				t.Fatal("restore duty was automatically retried", processed, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.enabled || len(f.writes) != 1 {
				t.Fatal("unexpected restoration write", f.enabled, f.writes)
			}
		})
	}
}
