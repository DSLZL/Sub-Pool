package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func maintenanceFixture(t *testing.T) (*Service, *pgxpool.Pool, string, Configuration, *atomic.Int32) {
	t.Helper()
	s, db, token := setup(t)
	calls, quota := new(atomic.Int32), new(atomic.Int32)
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe maintenance fixture request")
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") {
			row := fullManagedAccount(1, "maintenance-owned-resource")
			row["extra"] = map[string]any{"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339Nano), "codex_5h_used_percent": quota.Add(1), "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339)}
			respond(w, row)
			return
		}
		t.Errorf("maintenance unexpectedly contacted %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return s, db, token, save(t, s, token, server.URL), calls
}

func maintenanceInputFor(a ManagedAccount, first, last ObservationRecord) MaintenanceInput {
	return MaintenanceInput{MaintenanceScope: MaintenanceScope{AccountID: a.ID, From: first.Snapshot.ReceivedAt.Format(time.RFC3339Nano), Until: last.Snapshot.ReceivedAt.Add(time.Microsecond).Format(time.RFC3339Nano)}, Kind: "rebuild", RulesVersion: maintenanceRules}
}

func processMaintenanceForTest(t *testing.T, s *Service) {
	t.Helper()
	conn, err := s.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	processed, err := s.processMaintenance(context.Background(), conn)
	if err != nil || !processed {
		t.Fatal("maintenance queue was not processed", processed, err)
	}
}

func maintenanceFactSnapshot(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var body string
	err := db.QueryRow(context.Background(), `SELECT jsonb_build_object(
	 'observations',(SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY q.id),'[]'::jsonb) FROM quota_observations q),
	 'reviews',(SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY v.id),'[]'::jsonb) FROM observation_changes v),
	 'estimates',(SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY e.id),'[]'::jsonb) FROM estimate_results e),
	 'captures',(SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]'::jsonb) FROM usage_captures c),
	 'notifications',(SELECT COALESCE(jsonb_agg(to_jsonb(n) ORDER BY n.id),'[]'::jsonb) FROM notifications n),
	 'wallets',(SELECT COALESCE(jsonb_agg(to_jsonb(w) ORDER BY w.id),'[]'::jsonb) FROM wallet_application_requests w))::text`).Scan(&body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestMaintenanceOfflinePreflightExplicitQueueAtomicResultAndReplacement(t *testing.T) {
	s, db, token, c, calls := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	first := collectObservation(t, s, db, token, c, a)
	last := collectObservation(t, s, db, token, c, a)
	if _, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c)); err != nil {
		t.Fatal(err)
	}
	beforeCalls, beforeFacts := calls.Load(), maintenanceFactSnapshot(t, db)
	in := maintenanceInputFor(a, first, last)
	p, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil || p.State != "ready" || p.Validity != "valid" || p.ResultID != nil || p.Preview.Observations != 2 || len(p.Preview.Periods) != 1 || p.Difference.Added != 1 {
		t.Fatal("real preflight did not freeze owned native facts", p, err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, false); !errors.Is(err, auth.ErrInput) {
		t.Fatal("missing explicit acknowledgement was accepted", err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, strings.Repeat("0", 64), true); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("unbound confirmation was accepted", err)
	}
	queued, err := s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true)
	if err != nil || queued.State != "queued" || queued.ResultID != nil || queued.RequestedBy == nil || queued.StartedAt != nil {
		t.Fatal("queue acknowledgement pretended to be an application", queued, err)
	}
	if duplicate, err := s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil || duplicate.State != "queued" || duplicate.ID != p.ID {
		t.Fatal("duplicate confirmation did not reconcile the same queued plan", duplicate, err)
	}
	processMaintenanceForTest(t, s)
	applied, err := s.GetMaintenancePlan(ctx, token, p.ID)
	if err != nil || applied.State != "applied" || applied.ResultID == nil || *applied.ResultID != p.ID || applied.StartedAt == nil || applied.FinishedAt == nil {
		t.Fatal("durable background result unavailable", applied, err)
	}
	restarted, err := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := restarted.GetMaintenancePlan(ctx, token, p.ID)
	if err != nil || retained.ResultID == nil || retained.CreatedAt != applied.CreatedAt || retained.Basis.Fingerprint != p.Basis.Fingerprint {
		t.Fatal("service replacement changed retained result", retained, err)
	}
	next, err := restarted.PreflightMaintenance(ctx, token, in)
	if err != nil || next.Basis.PreviousAppliedPlanID == nil || *next.Basis.PreviousAppliedPlanID != p.ID || next.Difference != (MaintenanceDifference{}) {
		t.Fatal("new preflight did not compare the previous applied projection", next, err)
	}
	if _, err = restarted.ApplyMaintenancePlan(ctx, token, next.ID, next.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	processMaintenanceForTest(t, restarted)
	stalePeer, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, peer.ID, peer.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	processMaintenanceForTest(t, s)
	stalePeer, err = s.GetMaintenancePlan(ctx, token, stalePeer.ID)
	if err != nil || stalePeer.Validity != "basis_changed" {
		t.Fatal("changed derived base did not invalidate a prior preview", stalePeer, err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, stalePeer.ID, stalePeer.Basis.Fingerprint, true); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("old derived base was promoted", err)
	}
	if calls.Load() != beforeCalls || maintenanceFactSnapshot(t, db) != beforeFacts {
		t.Fatal("maintenance changed original facts or contacted upstream")
	}
	var applications, queues int
	if err = db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='maintenance.apply' AND outcome='succeeded'),count(*) FILTER(WHERE action='maintenance.queue') FROM management_events`).Scan(&applications, &queues); err != nil || applications != 3 || queues != 3 {
		t.Fatal("application or duplicate queue audit was not exactly once", applications, queues, err)
	}
}

func TestMaintenanceBasisRejectsChangedFactsReviewsConfigurationAndWorkerRace(t *testing.T) {
	for _, change := range []string{"observation", "review", "connection", "policy", "estimate", "account"} {
		t.Run(change, func(t *testing.T) {
			s, db, token, c, _ := maintenanceFixture(t)
			ctx := context.Background()
			a := passiveManaged(t, s, token, c, 1)
			fact := collectObservation(t, s, db, token, c, a)
			p, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact))
			if err != nil || p.State != "ready" {
				t.Fatal(p, err)
			}
			queued := change == "observation" || change == "review"
			if queued {
				if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "observation":
				collectObservation(t, s, db, token, c, a)
			case "review":
				_, err = s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, ObservationID: fact.ID, Revision: "1", Reason: "Historical exclusion", Excluded: true})
			case "connection":
				_, err = s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: c.BaseURL, CredentialAction: "clear"})
			case "policy":
				exec(t, db, `UPDATE monitoring_config SET revision=revision+1`)
			case "estimate":
				_, err = s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
			case "account":
				exec(t, db, `UPDATE managed_accounts SET revision=revision+1 WHERE id=$1`, a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if queued {
				processMaintenanceForTest(t, s)
			} else if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); !errors.Is(err, auth.ErrConflict) {
				t.Fatal("changed basis was accepted", err)
			}
			p, err = s.GetMaintenancePlan(ctx, token, p.ID)
			if err != nil || p.State != "expired" || p.Reason != "BASIS_CHANGED" || p.ResultID != nil {
				t.Fatal("changed basis did not retain an explicit terminal result", p, err)
			}
		})
	}
}

func TestMaintenanceAdminOnlyBoundedScopeAndIndependentRulesGate(t *testing.T) {
	s, db, token, c, calls := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	in := maintenanceInputFor(a, fact, fact)
	user, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "maintenance.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"security", "observations", "estimates"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	for _, action := range []func() error{
		func() error { _, e := s.PreflightMaintenance(ctx, reader, in); return e },
		func() error { _, e := s.GetMaintenancePlan(ctx, reader, p.ID); return e },
		func() error { _, e := s.ListMaintenancePlans(ctx, reader, a.ID, "0", 20); return e },
		func() error { _, e := s.ApplyMaintenancePlan(ctx, reader, p.ID, p.Basis.Fingerprint, true); return e },
	} {
		if err = action(); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("ordinary/global read grant exposed maintenance", err)
		}
	}
	for _, change := range []func(*MaintenanceInput){
		func(i *MaintenanceInput) { i.Kind = "upgrade" },
		func(i *MaintenanceInput) { i.RulesVersion = "unreviewed-v2" },
		func(i *MaintenanceInput) { i.Until = fact.Snapshot.ReceivedAt.Format(time.RFC3339Nano) },
	} {
		candidate := in
		change(&candidate)
		if candidate.From == candidate.Until {
			candidate.From = fact.Snapshot.ReceivedAt.Add(-time.Microsecond).Format(time.RFC3339Nano)
		}
		blocked, e := s.PreflightMaintenance(ctx, token, candidate)
		if e != nil || blocked.State != "blocked" || !maintenanceBlocked(blocked.maintenancePreview) || blocked.ResultID != nil {
			t.Fatal("unqualified rule/empty half-open scope looked applicable", blocked, e)
		}
		if candidate.RulesVersion != maintenanceRules && len(blocked.Preview.Periods) != 0 {
			t.Fatal("unreviewed rules returned a fabricated projection", blocked)
		}
		if _, e = s.ApplyMaintenancePlan(ctx, token, blocked.ID, blocked.Basis.Fingerprint, true); !errors.Is(e, auth.ErrConflict) {
			t.Fatal("blocked preflight could be applied", e)
		}
	}
	if calls.Load() != before {
		t.Fatal("maintenance contacted upstream")
	}
	raw, _ := json.Marshal(p)
	for _, secret := range []string{"queued_digest", "queued_actor_revision", "adm_sc_test_only_secret", "snapshot"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("maintenance returned private/raw storage field %s", secret)
		}
	}
}

func TestMaintenanceQueuedAuthorityAndInterruptedRecoveryDoNotReplay(t *testing.T) {
	for _, condition := range []string{"expired", "demoted", "interrupted"} {
		t.Run(condition, func(t *testing.T) {
			s, db, token, c, _ := maintenanceFixture(t)
			ctx := context.Background()
			a := passiveManaged(t, s, token, c, 1)
			fact := collectObservation(t, s, db, token, c, a)
			p, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
				t.Fatal(err)
			}
			switch condition {
			case "expired":
				exec(t, db, `UPDATE auth_sessions SET created_at=clock_timestamp()-interval '1 hour',idle_expires_at=clock_timestamp()-interval '1 second'`)
			case "demoted":
				exec(t, db, `UPDATE auth_users SET role='user',revision=revision+1 WHERE username='admin'`)
			case "interrupted":
				exec(t, db, `UPDATE maintenance_plans SET state='running',reason='RUNNING',started_at=clock_timestamp() WHERE id=$1`, p.ID)
			}
			conn, err := db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			if condition == "interrupted" {
				err = recoverMaintenance(ctx, conn)
			} else {
				_, err = s.processMaintenance(ctx, conn)
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err = scanMaintenance(conn.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1`, p.ID))
			if err != nil || p.State != "failed" || p.ResultID != nil || p.FinishedAt == nil {
				t.Fatal("unqualified/crashed queued principal was replayed", p, err)
			}
			processed, err := s.processMaintenance(ctx, conn)
			if err != nil || processed {
				t.Fatal("failed job was automatically replayed", processed, err)
			}
		})
	}
}

func TestMaintenanceFailedAuditRollsBackResultAndPreservesFacts(t *testing.T) {
	s, db, token, c, calls := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	p, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	before, requests := maintenanceFactSnapshot(t, db), calls.Load()
	exec(t, db, `ALTER TABLE management_events ADD CONSTRAINT reject_maintenance_success CHECK (action<>'maintenance.apply' OR outcome<>'succeeded')`)
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	processed, err := s.processMaintenance(ctx, conn)
	if !processed || err == nil {
		t.Fatal("failed result audit was ignored", processed, err)
	}
	p, err = s.GetMaintenancePlan(ctx, token, p.ID)
	if err != nil || p.State != "running" || p.ResultID != nil {
		t.Fatal("a failed transaction partially applied a projection", p, err)
	}
	if err = recoverMaintenance(ctx, conn); err != nil {
		t.Fatal(err)
	}
	p, err = s.GetMaintenancePlan(ctx, token, p.ID)
	if err != nil || p.State != "failed" || p.Reason != "WORKER_INTERRUPTED" || p.ResultID != nil || before != maintenanceFactSnapshot(t, db) || requests != calls.Load() {
		t.Fatal("failed result did not retain an honest atomic outcome", p, err)
	}
}

func TestMaintenanceNumericPlanPagination(t *testing.T) {
	s, db, token, c, _ := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	for i := 0; i < 12; i++ {
		if _, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact)); err != nil {
			t.Fatal(err)
		}
	}
	after, total := "0", 0
	for {
		page, err := s.ListMaintenancePlans(ctx, token, a.ID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Plans {
			total++
			if p.ID != fmt.Sprint(total) {
				t.Fatal("numeric bigint plan ordering became lexical", page)
			}
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if total != 12 {
		t.Fatal("pagination lost real plans", total)
	}
}

func TestMaintenanceCorrectsOnlyItsOwnRetainedDerivedIndex(t *testing.T) {
	s, db, token, c, calls := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	in := maintenanceInputFor(a, fact, fact)
	p, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	processMaintenanceForTest(t, s)
	wrongStart := p.Preview.Periods[0].StartAt.Add(-time.Hour).Format(time.RFC3339Nano)
	// An owned fixture simulates a bad prior cache; original native facts stay untouched.
	exec(t, db, `UPDATE maintenance_plans SET snapshot=jsonb_set(snapshot,'{preview,periods,0,start_at}',to_jsonb($2::text)) WHERE id=$1`, p.ID, wrongStart)
	var retained string
	if err = db.QueryRow(ctx, `SELECT snapshot::text FROM maintenance_plans WHERE id=$1`, p.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	before, requests := maintenanceFactSnapshot(t, db), calls.Load()
	in.Kind = "cycles"
	correction, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil || correction.State != "ready" || correction.Difference.Added != 1 || correction.Difference.Removed != 1 || correction.Basis.PreviousAppliedPlanID == nil || *correction.Basis.PreviousAppliedPlanID != p.ID {
		t.Fatal("independent native facts did not expose wrong local bounds", correction, err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, correction.ID, correction.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	processMaintenanceForTest(t, s)
	corrected, err := s.GetMaintenancePlan(ctx, token, correction.ID)
	if err != nil || corrected.State != "applied" || !corrected.Preview.Periods[0].StartAt.Equal(p.Preview.Periods[0].StartAt) {
		t.Fatal("local derived correction did not use actual reset/duration", corrected, err)
	}
	var old string
	if err = db.QueryRow(ctx, `SELECT snapshot::text FROM maintenance_plans WHERE id=$1`, p.ID).Scan(&old); err != nil || old != retained || before != maintenanceFactSnapshot(t, db) || requests != calls.Load() {
		t.Fatal("correction rewrote a prior result/fact or caused external effects", err)
	}
}

func TestMaintenanceExactReceiptScopeExpiryAndExcludedFactsRemainExplicit(t *testing.T) {
	s, db, token, c, _ := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	in := maintenanceInputFor(a, fact, fact)
	in.From = fact.Snapshot.ReceivedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
	in.Until = fact.Snapshot.ReceivedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)
	p, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil || p.Preview.Observations != 1 || p.State != "ready" {
		t.Fatal("nanosecond receipt bounds rounded away an included fact", p, err)
	}
	exec(t, db, `UPDATE maintenance_plans SET created_at=created_at-interval '11 minutes',expires_at=expires_at-interval '11 minutes' WHERE id=$1`, p.ID)
	read, err := s.GetMaintenancePlan(ctx, token, p.ID)
	if err != nil || read.State != "ready" || read.Validity != "expired" || read.ResultID != nil {
		t.Fatal("query hid preview expiry or fabricated a result", read, err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("expired preview was queued", err)
	}
	read, err = s.GetMaintenancePlan(ctx, token, p.ID)
	if err != nil || read.State != "expired" || read.Reason != "PLAN_EXPIRED" {
		t.Fatal("expired confirmation lacked a durable outcome", read, err)
	}
	queuedExpiry, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, queuedExpiry.ID, queuedExpiry.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `UPDATE maintenance_plans SET created_at=created_at-interval '11 minutes',expires_at=expires_at-interval '11 minutes' WHERE id=$1`, queuedExpiry.ID)
	processMaintenanceForTest(t, s)
	queuedExpiry, err = s.GetMaintenancePlan(ctx, token, queuedExpiry.ID)
	if err != nil || queuedExpiry.State != "expired" || queuedExpiry.Reason != "PLAN_EXPIRED" || queuedExpiry.ResultID != nil {
		t.Fatal("queued work ignored the retained expiry", queuedExpiry, err)
	}
	in.From = fact.Snapshot.ReceivedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)
	in.Until = fact.Snapshot.ReceivedAt.Add(2 * time.Microsecond).Format(time.RFC3339Nano)
	missing, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil || missing.Preview.Observations != 0 || missing.State != "blocked" || len(missing.Preview.Periods) != 0 {
		t.Fatal("receipt outside exact scope was included or filled with zeros", missing, err)
	}
	if _, err = s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, ObservationID: fact.ID, Revision: "1", Reason: "Exclude the original sample without deleting it", Excluded: true}); err != nil {
		t.Fatal(err)
	}
	excluded, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact))
	if err != nil || excluded.State != "blocked" || excluded.Preview.Observations != 1 || excluded.Preview.Excluded != 1 || len(excluded.Preview.Periods) != 0 {
		t.Fatal("excluded facts became fabricated periods or disappeared", excluded, err)
	}
}
