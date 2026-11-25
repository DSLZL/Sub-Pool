package connection

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/fakesub2api"
)

func TestUsageDayResumesAndPublishesFiftyThousandFacts(t *testing.T) {
	testUsageDayResumesAndPublishes(t, 50000, time.Second)
}

func TestUsageDayResumesBeyondPreviousEstimatorLimit(t *testing.T) {
	testUsageDayResumesAndPublishes(t, 100001, 500*time.Millisecond)
}

func testUsageDayResumesAndPublishes(t *testing.T, count int, spacing time.Duration) {
	t.Helper()
	s, pool, token := setup(t)
	ctx := context.Background()
	leader, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Release()
	now := time.Now().UTC()
	day := now.Truncate(24*time.Hour).AddDate(0, 0, -1)
	fixture := fakesub2api.New(fakesub2api.Options{At: day.Add(time.Hour), UsageRows: count, UsageSpacing: spacing})
	var fault, requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/usage" {
			requests.Add(1)
			if r.Method != http.MethodGet {
				t.Error("usage collector attempted a native write")
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			switch fault.Load() {
			case 502:
				http.Error(w, "disconnected", http.StatusBadGateway)
				return
			case 429:
				w.Header().Set("Retry-After", "3")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	c := save(t, s, token, upstream.URL)
	a := adoptManaged(t, s, token, c, 1, "Recorded day")
	input := UsageCaptureInput{
		AccountReadInput: readInput(a, c), StartAt: day.Format(time.RFC3339), EndAt: day.AddDate(0, 0, 1).Format(time.RFC3339),
	}
	initial, err := s.QueueUsageDays(ctx, token, input)
	if err != nil || len(initial.Days) != 1 || initial.Days[0].State != "queued" {
		t.Fatalf("queue: %+v %v", initial, err)
	}
	status := func() UsageDay {
		t.Helper()
		result, err := s.UsageDayStatus(ctx, token, input)
		if err != nil || len(result.Days) != 1 {
			t.Fatalf("status: %+v %v", result, err)
		}
		return result.Days[0]
	}
	process := func() {
		t.Helper()
		if did, err := s.processUsageDay(ctx, leader); !did || err != nil {
			t.Fatalf("worker did not advance: %v %v, %+v", did, err, status())
		}
	}
	process()
	first := status()
	if first.Records != "800" || first.State != "collecting" {
		t.Fatalf("unexpected first checkpoint: %+v", first)
	}
	retained, err := s.ListUsageCaptures(ctx, token, a.ID, "0", 100)
	if err != nil || len(retained.Captures) != 0 {
		t.Fatalf("working capture leaked into legacy history: %+v %v", retained, err)
	}
	fault.Store(502)
	process()
	if row := status(); row.State != "waiting" || row.Records != first.Records {
		t.Fatalf("network error lost the checkpoint: %+v", row)
	}
	makeDue := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE usage_day_jobs SET retry_at=now() WHERE account_id=$1`, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	makeDue()
	fault.Store(429)
	process()
	limited := status()
	if limited.Reason != "RATE_LIMITED" || limited.Records != first.Records || limited.NextAttempt == nil || !limited.NextAttempt.After(time.Now()) {
		t.Fatalf("Retry-After was not retained: %+v", limited)
	}
	if did, err := s.processUsageDay(ctx, leader); did || err != nil {
		t.Fatalf("waiting job dispatched early: %v %v", did, err)
	}
	fault.Store(0)
	time.Sleep(time.Until(*limited.NextAttempt) + 50*time.Millisecond)
	for i := 0; i < 150 && status().State != "complete"; i++ {
		process()
	}
	final, expected := status(), strconv.Itoa(count)
	if final.State != "complete" || final.Records != expected || final.Total == nil || *final.Total != expected || final.CompletedAt == nil {
		t.Fatalf("day incomplete: %+v", final)
	}
	pages := (count + usagePageSize - 1) / usagePageSize
	t.Logf("%d facts, %d native usage requests across pagination, retry and verification", count, requests.Load())
	if int(requests.Load()) > pages+pages/4+20 {
		t.Fatalf("a healthy scan repeated the full day: %d requests for %d facts", requests.Load(), count)
	}
	retained, err = s.ListUsageCaptures(ctx, token, a.ID, "0", 100)
	if err != nil || len(retained.Captures) != 1 || retained.Captures[0].Complete || retained.Captures[0].SettlementEligible {
		t.Fatalf("published capture or financial gate: %+v %v", retained, err)
	}
	var facts, attributions int
	err = pool.QueryRow(ctx, `SELECT count(*),count(a.upstream_id)
		FROM usage_capture_facts f LEFT JOIN usage_attributions a USING(capture_id,upstream_id)
		WHERE f.capture_id=$1`, retained.Captures[0].ID).Scan(&facts, &attributions)
	if err != nil || facts != count || attributions != facts {
		t.Fatalf("durable usage and attribution counts: %d %d %v", facts, attributions, err)
	}
	query := RecordedUsageQuery{AccountIDs: []string{a.ID}, AttributedUsageQuery: AttributedUsageQuery{
		After: "0", Limit: 100, Grain: "day", StartAt: input.StartAt, EndAt: input.EndAt,
	}}
	view, err := s.GetRecordedUsage(ctx, token, query)
	if err != nil || view.View == nil || view.View.Summary.RequestCount != expected || len(view.View.Records) != 100 || view.View.NextAfter == nil ||
		view.View.Summary.Metrics["total_cost"] == nil || *view.View.Summary.Metrics["total_cost"] != usageDecimal(big.NewRat(int64(count), 10)) ||
		view.View.Summary.Metrics["actual_cost"] == nil || *view.View.Summary.Metrics["actual_cost"] != usageDecimal(big.NewRat(int64(count), 5)) || view.View.SettlementEligible {
		t.Fatalf("server summary or first page disagrees with the fixture: %+v %v", view.View, err)
	}
	query.After, query.Basis = *view.View.NextAfter, view.Basis
	next, err := s.GetRecordedUsage(ctx, token, query)
	if err != nil || next.View == nil || len(next.View.Records) != 100 || next.View.Records[0].Fact.ID != "101" || next.Basis != view.Basis {
		t.Fatalf("read-back cursor failed: %+v %v", next, err)
	}
}

func TestUsageDaysRecheckChangedFactsAndScheduleOpenDay(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	leader, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Release()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	closed, empty := today.AddDate(0, 0, -1), today.AddDate(0, 0, -2)
	firstAt := closed.Add(time.Hour)
	closedInitial := fakesub2api.New(fakesub2api.Options{At: firstAt, UsageRows: 202})
	closedChanged := fakesub2api.New(fakesub2api.Options{At: firstAt.Add(time.Nanosecond), UsageRows: 202})
	openAt := maxTime(today, time.Now().UTC().Add(-time.Minute))
	var changeDuringScan, appended, late atomic.Bool
	var appendAt atomic.Pointer[time.Time]
	var closedFirstPage, openCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/usage" {
			switch r.URL.Query().Get("start_date") {
			case today.Format(weeklyDate):
				openCalls.Add(1)
				q := r.URL.Query()
				size, _ := strconv.Atoi(q.Get("page_size"))
				page, _ := strconv.Atoi(q.Get("page"))
				total := 102
				if appended.Load() {
					total = 105
				}
				if late.Load() {
					total = 106
				}
				items := []map[string]any{}
				for offset := (page - 1) * size; offset < min(page*size, total); offset++ {
					id, at := offset+1, openAt
					if q.Get("sort_order") == "desc" {
						id = total - offset
					}
					if id > 102 && id <= 105 {
						at = *appendAt.Load()
					}
					items = append(items, fakesub2api.UsageRow(int64(id), at.Format(time.RFC3339Nano)))
				}
				respond(w, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
				return
			case closed.Format(weeklyDate):
				q := r.URL.Query()
				if changeDuringScan.Load() && (closedFirstPage.Load() > 1 ||
					q.Get("sort_order") == "asc" && q.Get("page") == "1" && closedFirstPage.Add(1) > 1) {
					closedChanged.ServeHTTP(w, r)
					return
				}
			}
		}
		closedInitial.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	c := save(t, s, token, upstream.URL)
	a := adoptManaged(t, s, token, c, 1, "Recorded days")
	input := func(start, end time.Time) UsageCaptureInput {
		return UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: start.Format(time.RFC3339), EndAt: end.Format(time.RFC3339)}
	}
	closedInput, zeroInput, openInput := input(closed, today), input(empty, closed), input(today, today.AddDate(0, 0, 1))
	process := func(want bool) {
		t.Helper()
		if did, err := s.processUsageDay(ctx, leader); did != want || err != nil {
			t.Fatalf("worker progress: %v, want %v: %v", did, want, err)
		}
	}
	status := func(input UsageCaptureInput) UsageDay {
		t.Helper()
		result, err := s.UsageDayStatus(ctx, token, input)
		if err != nil || len(result.Days) != 1 {
			t.Fatalf("day status on %s: %+v %v", input.StartAt, result, err)
		}
		return result.Days[0]
	}
	due := func(day time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE usage_day_jobs SET retry_at=now() WHERE account_id=$1 AND day=$2`, a.ID, day); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil || policy.UsageEnabled || policy.UsageIntervalSeconds != policy.IntervalSeconds {
		t.Fatalf("new usage policy: %+v %v", policy, err)
	}
	legacy, err := s.CaptureUsage(ctx, token, closedInput)
	if err != nil || legacy.ObservedCount != "202" || legacy.CollectionDay != "" {
		t.Fatalf("capture legacy history: %+v %v", legacy, err)
	}
	if _, err = s.QueueUsageDays(ctx, token, zeroInput); err != nil {
		t.Fatal(err)
	}
	process(true)
	zero := status(zeroInput)
	if zero.State != "complete" || zero.Total == nil || *zero.Total != "0" || zero.Records != "0" {
		t.Fatalf("verified zero day: %+v", zero)
	}
	if _, err = s.QueueUsageDays(ctx, token, closedInput); err != nil {
		t.Fatal(err)
	}
	changeDuringScan.Store(true)
	process(true)
	if day := status(closedInput); day.Reason != "PAGINATION_CHANGED" || day.Records != "0" {
		t.Fatalf("same-size edit survived verification: %+v", day)
	}
	due(closed)
	process(true)
	if day := status(closedInput); day.State != "complete" || day.Records != "202" {
		t.Fatalf("rescan failed: %+v", day)
	}
	if retained := status(zeroInput); !retained.CompletedAt.Equal(*zero.CompletedAt) {
		t.Fatalf("rescan changed another day: %+v", retained)
	}
	raw, err := s.GetUsageCapture(ctx, token, UsageQuery{ID: legacy.ID, After: "0", Limit: 1, Grain: "day"})
	if err != nil || len(raw.Records) != 1 || !raw.Records[0].CreatedAt.Equal(firstAt) {
		t.Fatalf("legacy fact was replaced: %+v %v", raw, err)
	}
	process(false)
	policy.Enabled, policy.UsageEnabled, policy.UsageIntervalSeconds = true, true, 120
	policy, err = s.UpdateMonitoringConfig(ctx, token, policy)
	if err != nil || !policy.UsageEnabled {
		t.Fatalf("enable usage scheduling: %+v %v", policy, err)
	}
	process(true)
	open := status(openInput)
	if open.State != "partial" || open.Records != "102" || open.NextAttempt == nil || open.NextAttempt.Before(time.Now().Add(110*time.Second)) {
		t.Fatalf("open-day scheduled result: %+v", open)
	}
	if result, err := s.QueueUsageDays(ctx, token, openInput); err != nil || result.Days[0].Records != "102" || !result.Days[0].NextAttempt.Equal(*open.NextAttempt) {
		t.Fatalf("manual queue did not reuse the to-be-scheduled job: %+v %v", result, err)
	}
	stamp := time.Now().UTC()
	appendAt.Store(&stamp)
	appended.Store(true)
	process(false)
	due(today)
	calls := openCalls.Load()
	process(true)
	if day := status(openInput); day.State != "partial" || day.Records != "105" {
		t.Fatalf("open day did not append: %+v", day)
	}
	if calls = openCalls.Load() - calls; calls > 5 {
		t.Fatalf("open day did not reuse its checkpoint: %d requests", calls)
	}
	var priorCapture string
	if err = pool.QueryRow(ctx, `SELECT work_capture_id::text FROM usage_day_jobs WHERE account_id=$1 AND day=$2`, a.ID, today).Scan(&priorCapture); err != nil {
		t.Fatal(err)
	}
	late.Store(true)
	due(today)
	process(true)
	if day := status(openInput); day.Reason != "PAGINATION_CHANGED" || day.Records != "0" {
		t.Fatalf("late row reopened verified coverage: %+v", day)
	}
	due(today)
	process(true)
	if day := status(openInput); day.State != "partial" || day.Records != "106" {
		t.Fatalf("late row was not recovered: %+v", day)
	}
	archived, err := s.GetUsageCapture(ctx, token, UsageQuery{ID: priorCapture, After: "0", Limit: 1, Grain: "day"})
	if err != nil || len(archived.Records) != 1 || archived.Capture.ObservedCount != "105" {
		t.Fatalf("late corrections replaced the previous capture: %+v %v", archived, err)
	}
	policy.UsageEnabled = false
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	due(today)
	process(false)
	if day := status(openInput); day.State != "blocked" || day.Reason != "COLLECTION_DISABLED" || day.Records != "106" {
		t.Fatalf("disabled automatic work changed its checkpoint: %+v", day)
	}
	revoked := input(empty.AddDate(0, 0, -1), empty)
	if _, err = s.QueueUsageDays(ctx, token, revoked); err != nil {
		t.Fatal(err)
	}
	if err = s.auth.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	process(false)
	var state, reason string
	err = pool.QueryRow(ctx, `SELECT state,reason FROM usage_day_jobs WHERE account_id=$1 AND day=$2::date`, a.ID, revoked.StartAt[:10]).Scan(&state, &reason)
	if err != nil || state != "blocked" || reason != "AUTHORITY_CHANGED" {
		t.Fatalf("revoked session retained manual execution authority: %s %s %v", state, reason, err)
	}
	_, fresh, err := s.auth.Login(ctx, "admin", "TestPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueUsageDays(ctx, fresh, revoked); err != nil {
		t.Fatal(err)
	}
	replacement := httptest.NewServer(closedInitial)
	defer replacement.Close()
	save(t, s, fresh, replacement.URL)
	process(false)
	if _, err = s.UsageDayStatus(ctx, fresh, revoked); !errors.Is(err, auth.ErrConflict) {
		t.Fatalf("old source job was eligible after rebinding: %v", err)
	}
	old, err := s.GetUsageCapture(ctx, fresh, UsageQuery{ID: legacy.ID, After: "0", Limit: 1, Grain: "day"})
	if err != nil || old.Capture.SourceCurrent || old.Summary.RequestCount != "202" {
		t.Fatalf("switching sources changed retained raw history: %+v %v", old.Capture, err)
	}
}
