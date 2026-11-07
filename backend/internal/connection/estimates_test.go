package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

func estimateFixture(t *testing.T) (*Service, *pgxpool.Pool, string, Configuration, *atomic.Int32) {
	t.Helper()
	s, db, token := setup(t)
	calls := new(atomic.Int32)
	quota := new(atomic.Int32)
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe estimate fixture request")
		}
		if r.URL.Path == "/api/v1/admin/usage" {
			respond(w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 100})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			row := fullManagedAccount(id, fmt.Sprintf("estimate-resource-%d", id))
			row["extra"] = map[string]any{"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339Nano), "codex_5h_used_percent": quota.Add(1), "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339), "codex_7d_used_percent": 42}
			respond(w, row)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return s, db, token, save(t, s, token, server.URL), calls
}

func estimateInput(a ManagedAccount, c Configuration) EstimateSaveInput {
	return EstimateSaveInput{AccountID: a.ID, Revision: "1", EstimateAssumptions: EstimateAssumptions{Plan: estimation.Plan, CostBasis: "standard_usd", KeepRatio: "0.8", Premise: "Stable declared pricing and workload; independent coverage is still required", Windows: []string{"short", "seven_day"}, SourceID: a.SourceID, ConnectionRevision: c.Revision, AccountRevision: a.Revision}}
}

func estimateKind(t *testing.T, r EstimateRecord, kind string) EstimateWindow {
	t.Helper()
	for _, w := range r.Windows {
		if w.Kind == kind {
			return w
		}
	}
	t.Fatalf("missing window %s", kind)
	return EstimateWindow{}
}

func TestEstimateSelectedEndpointsMustAllBeFreshAndDuplicateNumbersCompareExactly(t *testing.T) {
	s, db, token, c, _ := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	first := collectObservation(t, s, db, token, c, a)
	middle := collectObservation(t, s, db, token, c, a)
	collectObservation(t, s, db, token, c, a)
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.IntervalSeconds, policy.StaleAfterSeconds, policy.NearResetIntervalSeconds = 60, 60, 60
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	writeFixture := func(id string, snapshot QuotaSnapshot) {
		t.Helper()
		body, e := json.Marshal(snapshot)
		if e == nil {
			_, e = db.Exec(ctx, `UPDATE quota_observations SET snapshot=$2 WHERE id=$1`, id, body)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	stale := first.Snapshot
	sourceAt := stale.ReceivedAt.Add(-61 * time.Second)
	stale.SourceAt = &sourceAt
	writeFixture(first.ID, stale)
	saved, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
	if err != nil || estimateKind(t, saved, "short").State != "source_stale" || len(estimateKind(t, saved, "short").ObservationIDs) != 3 {
		t.Fatal("fresh latest quota hid a stale selected endpoint", saved, err)
	}
	sourceAt = time.Now().UTC().Add(-58 * time.Second)
	writeFixture(first.ID, stale)
	nearExpiry, err := s.RecomputeEstimate(ctx, token, a.ID, "2", saved.ID)
	if err != nil || estimateKind(t, nearExpiry, "short").State != "evidence_incomplete" {
		t.Fatal("fresh selected evidence was not retained", nearExpiry, err)
	}
	time.Sleep(time.Until(sourceAt.Add(60*time.Second)) + time.Millisecond)
	current, err := s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.CurrentState != "source_stale" || estimateKind(t, *current.Latest, "short").State != "evidence_incomplete" {
		t.Fatal("current projection ignored an expired endpoint or rewrote history", current, err)
	}
	writeFixture(first.ID, first.Snapshot)
	duplicate := middle.Snapshot
	duplicate.SourceAt = first.Snapshot.SourceAt
	value := *quotaUsed(first.Snapshot, "short") + "e0"
	for i := range duplicate.Windows {
		if duplicate.Windows[i].Kind == "short" {
			duplicate.Windows[i].UsedPercent = &value
		}
	}
	writeFixture(middle.ID, duplicate)
	recomputed, err := s.RecomputeEstimate(ctx, token, a.ID, "2", nearExpiry.ID)
	if err != nil || estimateKind(t, recomputed, "short").State != "insufficient_samples" || len(estimateKind(t, recomputed, "short").ObservationIDs) != 2 {
		t.Fatal("equivalent native numeric formatting invented a changed condition", recomputed, err)
	}
	old, err := s.GetEstimateRecord(ctx, token, saved.ID)
	if err != nil || estimateKind(t, old, "short").State != "source_stale" {
		t.Fatal("recompute rewrote the earlier explanation", old, err)
	}
}

func TestNativePeriodRejectsSubmicrosecondFutureSourceWithoutTolerance(t *testing.T) {
	source := time.Date(2026, 11, 7, 1, 0, 0, 123000, time.UTC)
	reset := source.Add(time.Hour)
	duration, percent := "10800", "2e1"
	snapshot := QuotaSnapshot{SourceAt: &source, ReceivedAt: source.Add(-200 * time.Nanosecond), Windows: []QuotaWindow{{Kind: "short", DurationSeconds: &duration, UsedPercent: &percent, ResetAt: &reset}}}
	if nativeObservationPeriod(snapshot, "short") != nil {
		t.Fatal("future source was relabeled as a verified official period")
	}
	snapshot.ReceivedAt = source
	if period := nativeObservationPeriod(snapshot, "short"); period == nil || period.DurationSeconds != duration {
		t.Fatal("verified native timestamp lost its actual duration", period)
	}
}

func TestEstimateConfigurationNativeHistoryAndActualProducerUpdates(t *testing.T) {
	s, db, token, c, calls := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	first := collectObservation(t, s, db, token, c, a)
	current, err := s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Configuration.Revision != "1" || current.Configuration.Assumptions != nil || current.Latest != nil || current.CurrentState != "not_configured" {
		t.Fatal("unconfigured estimate invented defaults", current, err)
	}
	saved, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
	if err != nil || saved.Configuration.Revision != "2" || saved.Configuration.Assumptions.KeepRatio != "0.8" || saved.GeneratedAt.Location() != time.UTC || saved.State == "scenario" {
		t.Fatal("native config did not preserve explicit assumption", saved, err)
	}
	if w := estimateKind(t, saved, "seven_day"); w.Period != nil || w.State != "period_unknown" {
		t.Fatal("window label invented a period", w)
	}
	middle := collectObservation(t, s, db, token, c, a)
	last := collectObservation(t, s, db, token, c, a)
	current, err = s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Latest == nil || current.Latest.Trigger != "observation" || current.NeedsRecalculation || current.CurrentRemaining != nil {
		t.Fatal("collector did not append current explanation", current, err)
	}
	w := estimateKind(t, *current.Latest, "short")
	if w.State != "evidence_incomplete" || w.Period == nil || w.Period.DurationSeconds != "10800" || !reflect.DeepEqual(w.ObservationIDs, []string{first.ID, middle.ID, last.ID}) || w.Capacity != nil || w.Remaining != nil {
		t.Fatal("bounded records acquired completeness or lost real period", w)
	}
	before := calls.Load()
	state, err := s.ChangeObservationStart(ctx, token, ObservationStartInput{AccountID: a.ID, Revision: "1", StartObservationID: first.ID, EndObservationID: middle.ID, WindowKind: "short", Reason: "Explicit earlier local sample range"}, false)
	if err != nil || state.RecalculationState != "recorded" {
		t.Fatal("range change did not record recomputation", state, err)
	}
	current, err = s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Latest == nil {
		t.Fatal(err)
	}
	w = estimateKind(t, *current.Latest, "short")
	if w.Basis != "manual" || w.ManualEndAt == nil || !w.ManualEndAt.Equal(*middle.Snapshot.SourceAt) || w.CurrentObservationID == nil || *w.CurrentObservationID != last.ID || len(w.ObservationIDs) != 2 || w.State != "insufficient_samples" {
		t.Fatal("manual range erased current official consumption or invented samples", w)
	}
	if _, err = s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "2", ObservationID: first.ID, Excluded: true, Reason: "Retain invalidated manual basis"}); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || estimateKind(t, *current.Latest, "short").State != "manual_range_unusable" {
		t.Fatal("excluded range still estimated", current, err)
	}
	if calls.Load() != before {
		t.Fatal("local estimate decisions contacted upstream")
	}
	capture, err := s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: AccountReadInput{ID: a.ID, Revision: a.Revision, SourceID: a.SourceID, ConnectionRevision: c.Revision}, StartAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), EndAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil || capture.State != "exhausted" || capture.Complete || capture.SettlementEligible {
		t.Fatal("empty exhausted pages became complete costs", capture, err)
	}
	current, err = s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Latest.Trigger != "usage" || current.Latest.CaptureHead != capture.ID || current.Latest.Complete || current.Latest.SettlementEligible || current.Latest.Remaining != nil {
		t.Fatal("capture did not retain bounded estimate explanation", current, err)
	}
	old, err := s.GetEstimateRecord(ctx, token, saved.ID)
	if err != nil || !reflect.DeepEqual(old, saved) {
		t.Fatal("old estimate rewritten by current changes", old, err)
	}
	restarted, err := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := restarted.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || next.Latest.ID != current.Latest.ID || !next.Latest.GeneratedAt.Equal(current.Latest.GeneratedAt) {
		t.Fatal("restart lost immutable calculation", next, err)
	}
	config := estimateInput(a, c)
	config.Revision = "2"
	config.KeepRatio = "0.6"
	updated, err := s.SaveEstimateConfiguration(ctx, token, config)
	if err != nil || updated.Configuration.Revision != "3" || old.Configuration.Assumptions.KeepRatio != "0.8" {
		t.Fatal("assumption update rewrote history", updated, err)
	}
}

func TestEstimateScopedReadersCurrentGrantsAndLocalOnlyHistory(t *testing.T) {
	s, _, token, c, calls := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	b := passiveManaged(t, s, token, c, 2)
	one, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(b, c))
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "estimate.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if r, err := s.GetEstimateRecord(ctx, reader, one.ID); err != nil || r.AccountID != a.ID {
		t.Fatal("estimate-only reader required unrelated feature", r, err)
	}
	for _, id := range []string{two.ID, "9223372036854775807"} {
		if _, err = s.GetEstimateRecord(ctx, reader, id); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("hidden record revealed", err)
		}
	}
	if _, err = s.GetEstimateCurrent(ctx, reader, b.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("hidden account current revealed", err)
	}
	if _, err = s.SaveEstimateConfiguration(ctx, reader, estimateInput(a, c)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary reader wrote config", err)
	}
	history, err := s.ListEstimateHistory(ctx, reader, EstimateHistoryInput{AccountID: a.ID, After: "0", Limit: 100, WindowKind: "all"})
	if err != nil || len(history.Records) != 1 {
		t.Fatal(history, err)
	}
	if _, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true, Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{}, ParticipantIDs: []string{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEstimateRecord(ctx, reader, one.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("scope contraction leaked old result", err)
	}
	if calls.Load() != before {
		t.Fatal("history or denied operation contacted upstream")
	}
}

func TestEstimateExpectedVersionsAndAuditFailuresAreAtomic(t *testing.T) {
	s, db, token, c, _ := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	input := estimateInput(a, c)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() { defer group.Done(); _, err := s.SaveEstimateConfiguration(ctx, token, input); errs <- err }()
	}
	group.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, auth.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent config lost revision", success, conflict)
	}
	current, err := s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RecomputeEstimate(ctx, token, a.ID, current.Configuration.Revision, current.Latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecomputeEstimate(ctx, token, a.ID, current.Configuration.Revision, current.Latest.ID); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("uncertain recompute could replay stale intent", err)
	}
	exec(t, db, `CREATE FUNCTION reject_estimate_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejects estimate audit'; END $$; CREATE TRIGGER reject_estimate_audit BEFORE INSERT ON management_events FOR EACH ROW WHEN (NEW.action LIKE 'estimate.%') EXECUTE FUNCTION reject_estimate_audit()`)
	input.Revision = "2"
	input.KeepRatio = "0.5"
	if result, err := s.SaveEstimateConfiguration(ctx, token, input); err == nil || result.ID != "" {
		t.Fatal("failed estimate audit returned success", err)
	}
	current, err = s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Configuration.Revision != "2" || current.Configuration.Assumptions.KeepRatio != "0.8" || current.Latest.ID != next.ID {
		t.Fatal("failed audit left config/result partial state", current, err)
	}
	var facts string
	if err = db.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(snapshot ORDER BY id),'[]')::text FROM quota_observations`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	first := collectObservationWithoutEstimateFailure(t, s, db, token, c, a)
	if first != "0" || facts != "[]" {
		t.Fatal("failed derivative audit kept a partial observation", first, facts)
	}
}

func collectObservationWithoutEstimateFailure(t *testing.T, s *Service, db *pgxpool.Pool, token string, c Configuration, a ManagedAccount) string {
	t.Helper()
	advanceCollectionFixture(t, db)
	queueAccount(t, s, token, c, a.ID)
	conn, err := db.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = s.processCollection(context.Background(), conn); err == nil {
		t.Fatal("failed estimate audit committed collected fact")
	}
	var count string
	if err = db.QueryRow(context.Background(), `SELECT count(*)::text FROM quota_observations WHERE account_id=$1`, a.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestEstimateNumericHistoryRealPeriodAndFrozenProvenance(t *testing.T) {
	s, db, token, c, _ := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	collectObservation(t, s, db, token, c, a)
	first, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
	if err != nil {
		t.Fatal(err)
	}
	current := first
	for range 12 {
		current, err = s.RecomputeEstimate(ctx, token, a.ID, current.Configuration.Revision, current.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	after := "0"
	seen := []string{}
	for {
		page, err := s.ListEstimateHistory(ctx, token, EstimateHistoryInput{AccountID: a.ID, After: after, Limit: 3, WindowKind: "short", PeriodEndAt: estimateKind(t, first, "short").Period.EndAt.Format(time.RFC3339Nano)})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Records {
			seen = append(seen, r.ID)
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if len(seen) != 13 || seen[8] != "9" || seen[9] != "10" {
		t.Fatal("history cursor was lexical or filtered after page", seen)
	}
	exec(t, db, `ALTER TABLE estimate_results ALTER COLUMN id RESTART WITH 9007199254740993`)
	huge, err := s.RecomputeEstimate(ctx, token, a.ID, current.Configuration.Revision, current.ID)
	if err != nil || huge.ID != "9007199254740993" {
		t.Fatal("history ID lost precision", huge, err)
	}
	original, _ := json.Marshal(first)
	if _, err = s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Renamed only current account", Monitoring: a.Monitoring, SourceMode: a.SourceMode}); err != nil {
		t.Fatal(err)
	}
	read, err := s.GetEstimateRecord(ctx, token, first.ID)
	body, _ := json.Marshal(read)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("current metadata rewrote old scenario", err)
	}
	now, err := s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || now.CurrentState != "condition_changed" || now.CurrentRemaining != nil {
		t.Fatal("account version change kept old advice current", now, err)
	}
}

func TestEstimateActualLockExpiryCannotReturnOrCommitProtectedData(t *testing.T) {
	for _, operation := range []string{"configure", "current", "recompute", "audit_commit"} {
		t.Run(operation, func(t *testing.T) {
			s, db, token, c, _ := estimateFixture(t)
			ctx := context.Background()
			a := passiveManaged(t, s, token, c, 1)
			saved, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
			if err != nil {
				t.Fatal(err)
			}
			barrier, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(ctx)
			if operation == "audit_commit" {
				_, err = barrier.Exec(ctx, `LOCK TABLE management_events IN ACCESS EXCLUSIVE MODE`)
			} else {
				_, err = barrier.Exec(ctx, `SELECT id FROM managed_accounts WHERE id=$1 FOR UPDATE`, a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				if operation == "current" {
					r, e := s.GetEstimateCurrent(ctx, token, a.ID)
					if r.Configuration.AccountID != "" {
						done <- errors.New("expired current leaked protected state")
					} else {
						done <- e
					}
					return
				}
				var r EstimateRecord
				var e error
				if operation == "recompute" {
					r, e = s.RecomputeEstimate(ctx, token, a.ID, "2", saved.ID)
				} else {
					input := estimateInput(a, c)
					input.Revision = "2"
					r, e = s.SaveEstimateConfiguration(ctx, token, input)
				}
				if r.ID != "" {
					done <- errors.New("expired write returned result")
				} else {
					done <- e
				}
			}()
			pattern := "%FROM managed_accounts%FOR UPDATE"
			if operation == "current" || operation == "recompute" {
				pattern = "%FROM managed_accounts%FOR SHARE"
			}
			if operation == "audit_commit" {
				pattern = "INSERT INTO management_events%"
			}
			observePoolWait(t, db, pattern)
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("observed wait retained expired authority", err)
			}
			var intact bool
			if err = db.QueryRow(ctx, `SELECT (SELECT revision=2 FROM estimate_config WHERE account_id=$1) AND (SELECT count(*)=1 FROM estimate_results WHERE account_id=$1)`, a.ID).Scan(&intact); err != nil || !intact {
				t.Fatal("expired write persisted partial estimate", err)
			}
		})
	}
}

func TestEstimateRejectsUnqualifiedAssumptionsWithoutMutation(t *testing.T) {
	s, db, token, c, _ := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	for _, change := range []func(*EstimateSaveInput){func(i *EstimateSaveInput) { i.Plan = "posterior" }, func(i *EstimateSaveInput) { i.CostBasis = "actual_cost" }, func(i *EstimateSaveInput) { i.CostBasis = "wallet_deducted_usd" }, func(i *EstimateSaveInput) { i.KeepRatio = "0" }, func(i *EstimateSaveInput) { i.KeepRatio = "1.01" }, func(i *EstimateSaveInput) { i.Premise = " " }, func(i *EstimateSaveInput) { i.Windows = nil }, func(i *EstimateSaveInput) { i.Windows = []string{"short", "short"} }, func(i *EstimateSaveInput) { i.SourceMode = "passive" }} {
		in := estimateInput(a, c)
		change(&in)
		if _, err := s.SaveEstimateConfiguration(ctx, token, in); !errors.Is(err, auth.ErrInput) {
			t.Fatal("unqualified assumption accepted", in, err)
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM estimate_config`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid input left configuration", count, err)
	}
}
