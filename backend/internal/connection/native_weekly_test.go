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
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

func TestNativeWeeklyUsesSharedPeriodAndClearsReset(t *testing.T) {
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	reset := at.Add(time.Hour)
	point := func(id, percent string, observed, end time.Time) weeklyPoint {
		t.Helper()
		raw := fmt.Appendf(nil, `{"extra":{"codex_usage_updated_at":%q,"codex_5h_window_minutes":0,"codex_7d_used_percent":%s,"codex_7d_window_minutes":10080,"codex_7d_reset_at":%q}}`,
			observed.Format(time.RFC3339Nano), percent, end.Format(time.RFC3339Nano))
		snap, ok := passiveQuota(raw)
		snap.ReceivedAt = observed
		p := weeklyObservation(id, snap)
		if !ok || p.period == nil {
			t.Fatal("weekly window unavailable with zero-duration short window")
		}
		return p
	}
	first := point("1", "7", at, reset)
	rise := point("2", "19", at.Add(5*time.Minute), reset.Add(230*time.Second))
	duplicate := point("3", "19.0", rise.At, reset.Add(2*time.Second))
	later := point("4", "19", at.Add(6*time.Minute), reset.Add(time.Second))
	period, points, state := weeklyCycle([]weeklyPoint{first, rise, duplicate, later})
	if state != "" || !period.EndAt.Equal(reset) || !period.StartAt.Equal(reset.Add(-nativeWeek)) ||
		len(points) != 2 || points[0].ID != "1" || points[1].ID != "2" {
		t.Fatalf("lost the cycle anchor or repeated quota as a checkpoint: %v %#v %q", period, points, state)
	}
	for _, sample := range []struct {
		obs   weeklyPoint
		state string
		count int
	}{
		{point("5", "20", rise.At, reset), "invalid_evidence", 0},
		{point("6", "2", later.At.Add(time.Minute), reset), "quota_decreased", 0},
		{point("7", "21", reset.Add(time.Minute), reset.Add(nativeWeek)), "", 1},
	} {
		period, points, state = weeklyCycle([]weeklyPoint{first, rise, sample.obs})
		if len(points) != sample.count || state != sample.state || sample.count == 1 && (!period.StartAt.Equal(reset) || points[0].ID != "7") {
			t.Fatalf("quota conflict or rollover handled incorrectly: %v %#v %q", period, points, state)
		}
	}
}

func TestNativeWeeklyActualCostChangeRescansWithoutInvalidatingStandardEvidence(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	first := NativeWeeklyPoint{ID: "1", At: start.Add(24 * time.Hour), UsedPercent: "20"}
	plan := nativeWeeklyPlan{Binding: "test", Period: &ObservationPeriod{StartAt: start, EndAt: start.Add(nativeWeek)},
		Points: []NativeWeeklyPoint{first, {ID: "2", At: first.At.Add(time.Hour), UsedPercent: "25"}}, WalletBasis: true, CostVersion: 1}
	old := plan
	old.CostVersion, old.Points = 0, []NativeWeeklyPoint{first}
	firstDay := start.Format(weeklyDate)
	prior := &nativeWeeklyJob{Work: nativeWeeklyWork{Plan: old, Local: map[string]nativeWeeklyLocalDay{
		firstDay: {Capture: "8", Done: true, Costs: []nativeWeeklyCost{{Records: 13}}},
	}}}
	if !plan.includes(old) {
		t.Fatal("equivalent standard cost evidence was lost during requalification")
	}
	rescan := newNativeLocalWeeklyWork(plan, prior)
	if len(rescan.Local) != 0 || !rescan.Day.Equal(start.Truncate(24*time.Hour)) {
		t.Fatal("old actual cost aggregates were reused", rescan.Day, rescan.Local)
	}
}

func TestNativeWeeklyConfidenceUsesThreeStableEstimatesAndMinimumUsage(t *testing.T) {
	points := []NativeWeeklyCheckpoint{
		{Observation: NativeWeeklyPoint{UsedPercent: "5"}, Capacity: &estimation.Amount{Numerator: "90", Denominator: "1"}},
		{Observation: NativeWeeklyPoint{UsedPercent: "10"}, Capacity: &estimation.Amount{Numerator: "95", Denominator: "1"}},
		{Observation: NativeWeeklyPoint{UsedPercent: "20"}, Capacity: &estimation.Amount{Numerator: "100", Denominator: "1"}},
	}
	if state := weeklyConfidence(points); state != "trusted" {
		t.Fatal("exactly 10% difference at 20% usage should meet the threshold", state)
	}
	if state := weeklyConfidence(points[1:]); state != "collecting" {
		t.Fatal("two estimates enabled allocation", state)
	}
	points[2].Observation.UsedPercent = "19.9999"
	if state := weeklyConfidence(points); state != "collecting" {
		t.Fatal("sub-threshold usage enabled allocation", state)
	}
	points[2].Observation.UsedPercent = "21"
	points[0].Capacity.Numerator = "89"
	if state := weeklyConfidence(points); state != "unstable" {
		t.Fatal("unstable totals enabled allocation", state)
	}
}

func TestNativeWeeklyCollectorDurablePaginationAndManagedRefresh(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	midnight := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	start, middle := midnight.Add(-15*time.Minute), midnight.Add(15*time.Minute)
	cycleStart := start.Add(-5 * time.Minute)
	reset := cycleStart.Add(nativeWeek)
	observedAt, use := cycleStart, 0
	var deny, repeat, tail, replaceIdentity atomic.Bool
	var usageCalls atomic.Int32
	type row struct {
		ID       int         `json:"id"`
		Account  int         `json:"account_id"`
		At       time.Time   `json:"created_at"`
		Standard json.Number `json:"total_cost"`
		Actual   json.Number `json:"actual_cost"`
		Model    string      `json:"model"`
		User     int         `json:"user_id"`
		Billing  int         `json:"billing_type"`
	}
	rows := map[string][]row{}
	makeRow := func(id int, at time.Time, standard string) row {
		return row{id, 8, at, json.Number(standard), "777", "gpt-5", 21, 0}
	}
	appendRow := func(id int, at time.Time, standard string) {
		key := at.Format(weeklyDate)
		rows[key] = append(rows[key], makeRow(id, at, standard))
	}
	appendRow(1, cycleStart.Add(-time.Nanosecond), "999")
	appendRow(2, cycleStart, "4")
	for id := 3; id <= 1202; id++ {
		appendRow(id, start.Add(-time.Second), "0.005")
	}
	appendRow(1203, start, "5")
	appendRow(1204, midnight, "5")
	appendRow(1205, middle, "5")
	appendRow(1206, now.Add(time.Minute), "999")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected upstream method or credential")
			http.Error(w, "bad", 400)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/users/21":
			respond(w, nativeUserRow(21))
		case "/api/v1/admin/users/22":
			respond(w, nativeUserRow(22))
		case "/api/v1/admin/accounts/8":
			resource := "native-weekly-bound"
			if replaceIdentity.Load() {
				resource = "replacement-native-identity"
			}
			a := fullManagedAccount(8, resource)
			a["extra"] = map[string]any{"codex_usage_updated_at": observedAt, "codex_5h_window_minutes": 0,
				"codex_7d_window_minutes": 10080, "codex_7d_used_percent": use, "codex_7d_reset_at": reset}
			respond(w, a)
		case "/api/v1/admin/usage":
			usageCalls.Add(1)
			if deny.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			q := r.URL.Query()
			date := q.Get("start_date")
			if q.Get("account_id") != "8" || q.Get("end_date") != date || q.Get("timezone") != "UTC" ||
				q.Get("sort_by") != "id" || q.Get("exact_total") != "true" {
				t.Error("usage read is not scoped to the verified account and UTC day")
			}
			items := append([]row{}, rows[date]...)
			if tail.Load() && date == start.Format(weeklyDate) {
				items = append(items, makeRow(99000, cycleStart.Add(-time.Nanosecond), "999"))
			}
			page, _ := strconv.Atoi(q.Get("page"))
			size, _ := strconv.Atoi(q.Get("page_size"))
			switch q.Get("sort_order") {
			case "desc":
				slices.Reverse(items)
			case "asc":
				if repeat.Load() && page == 3 && len(items) > 201 {
					items[200] = items[199]
				}
			}
			from := min((page-1)*size, len(items))
			to := min(from+size, len(items))
			respond(w, map[string]any{"items": items[from:to], "total": len(items), "page": page, "page_size": size})
		default:
			t.Errorf("unexpected upstream %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 8)
	firstMember := createParticipant(t, s, token, c, 21, "Visible Member")
	secondMember := createParticipant(t, s, token, c, 22, "Other Member")
	pool := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{firstMember, secondMember}, []string{"30", "50"})})
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	policy.StaleAfterSeconds = 3 * 24 * 60 * 60
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	read := func() EstimateCurrent {
		t.Helper()
		current, err := s.GetEstimateCurrent(ctx, token, a.ID)
		if err != nil || current.NativeWeekly == nil {
			t.Fatal("missing native weekly projection", err)
		}
		if current.CurrentRemaining != nil || current.Configuration.Assumptions != nil || current.Latest != nil {
			t.Fatal("native observation supplied a simulated amount or created a manual assumption")
		}
		return current
	}
	step := func() {
		t.Helper()
		collectWeeklyTestRound(t, s, ctx, a.ID)
	}
	retry := func() {
		exec(t, db, `UPDATE native_weekly_estimates SET retry_at=now()-interval '1 minute' WHERE result IS NULL`)
		exec(t, db, `UPDATE usage_day_jobs SET retry_at=now(),updated_at=now()-interval '1 hour' WHERE state='waiting'`)
	}
	zero := collectObservation(t, s, db, token, c, a)
	if state := read().NativeWeekly.State; state != "awaiting_increase" {
		t.Fatal("zero quota observation produced an estimate", state)
	}
	use, observedAt = 10, start
	collectObservation(t, s, db, token, c, a)
	use, observedAt = 20, middle
	reset = reset.Add(230 * time.Second)
	collectObservation(t, s, db, token, c, a)
	observedAt = now.Add(-time.Minute)
	collectObservation(t, s, db, token, c, a)
	if usageCalls.Load() != 0 {
		t.Fatal("reading the estimates page dispatched native requests")
	}
	deny.Store(true)
	step()
	if state := read().NativeWeekly.State; state != "usage_denied" {
		t.Fatal("denial omitted from current state", state)
	}
	deny.Store(false)
	retry()
	repeat.Store(true)
	step()
	if weekly := read().NativeWeekly; weekly.State != "pagination_changed" || weekly.Progress.Scanned != 0 {
		t.Fatalf("duplicate row did not restart the day: state=%s, progress=%+v", weekly.State, weekly.Progress)
	}
	repeat.Store(false)
	retry()
	step()
	beforeRestart := read().NativeWeekly.Progress
	if beforeRestart == nil || beforeRestart.Scanned != usageDayPagesPerBatch*usagePageSize || beforeRestart.Rechecking {
		t.Fatal("initial progress not persisted", beforeRestart)
	}
	// A growing tail must leave the fixed prefix eligible to finish after replacement.
	tail.Store(true)
	s, err = New(db, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	reads := usageCalls.Load()
	step()
	if reads = usageCalls.Load() - reads; reads > 8 {
		t.Fatal("replacement reread the already stored prefix", reads)
	}
	for i := 0; i < 25 && read().NativeWeekly.Latest == nil; i++ {
		step()
	}
	current := read().NativeWeekly
	sameAmount := func(value *estimation.Amount, numerator, denominator string) bool {
		return value != nil && *value == (estimation.Amount{Numerator: numerator, Denominator: denominator})
	}
	if current.State != "ready" || current.Latest == nil || current.Latest.Records != 1203 ||
		!sameAmount(current.Latest.StandardCost, "20", "1") || !sameAmount(current.Latest.Capacity, "100", "1") ||
		!sameAmount(current.Remaining, "80", "1") || !current.Period.StartAt.Equal(cycleStart) ||
		current.Latest.Cumulative.Confidence != "collecting" || current.Allocation != nil {
		t.Fatalf("expected reset-based cumulative usage and a provisional estimate: %+v", current)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	support, err := s.FinancialNotificationSupport(ctx, tx)
	_ = tx.Rollback(ctx)
	if err != nil || support.Simulation || support.NativeSourceID != "" {
		t.Fatalf("an unverified native source qualified financial notifications: %+v %v", support, err)
	}
	frozen := current.Latest
	calls := usageCalls.Load()
	refreshed, err := s.RefreshManaged(ctx, token, readInput(a, c))
	if err != nil || refreshed.Account.Revision == a.Revision {
		t.Fatal("account refresh did not advance CAS", err)
	}
	a = refreshed.Account
	step()
	if got := read().NativeWeekly; got.Latest == nil || got.Latest.ID != frozen.ID || usageCalls.Load() != calls {
		t.Fatal("pure managed status refresh invalidated verified native evidence")
	}
	use, observedAt = 25, now.Add(-30*time.Second)
	collectObservation(t, s, db, token, c, a)
	current = read().NativeWeekly
	if !sameAmount(current.Remaining, "75", "1") || current.Allocation != nil {
		t.Fatal("remaining did not follow the newest observed usage", current.Remaining)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	monitor, connection, account, err := s.estimateEnvironment(ctx, tx, a.ID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	unfinished, err := s.prepareNativeWeekly(ctx, tx, account, connection, monitor)
	if err != nil || unfinished == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("missing pending work on the old revision", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	c = save(t, s, token, server.URL)
	collectObservation(t, s, db, token, c, a)
	unfinished.Work.State = "usage_denied"
	if err = s.saveNativeWeeklyWork(ctx, a.ID, unfinished); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow(ctx, `SELECT work->>'state' FROM native_weekly_estimates WHERE id=$1`, unfinished.ID).Scan(&state); err != nil || state != "collecting" {
		t.Fatal("in-flight work from the old revision was updated", state, err)
	}
	for i := 0; i < 15 && (read().NativeWeekly.Latest == nil || read().NativeWeekly.Latest.ID == frozen.ID); i++ {
		step()
	}
	current = read().NativeWeekly
	if current.Latest == nil || current.Latest.ID == frozen.ID || current.Latest.AccountRevision != a.Revision ||
		!sameAmount(current.Latest.StandardCost, "25", "1") || !sameAmount(current.Latest.Capacity, "100", "1") ||
		!sameAmount(current.Remaining, "75", "1") || current.Latest.Cumulative.Confidence != "trusted" {
		t.Fatal("next observation did not update the cumulative estimate", current)
	}
	allocation := current.Allocation
	if allocation == nil || allocation.ContractID != pool.ID || len(allocation.Members) != 2 || allocation.Reserve == nil ||
		!sameAmount(allocation.Reserve.Remaining, "15", "1") {
		t.Fatal("trusted estimate did not project the current share contract", allocation)
	}
	for _, share := range allocation.Members {
		if share.ID == firstMember.ID && (!sameAmount(share.Capacity, "30", "1") || !sameAmount(share.Remaining, "45", "2")) {
			t.Fatal("pool allocation lost exact percentage", share)
		}
	}
	member, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "weekly.reader", Password: "ReaderPassword123", Enabled: true,
		Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetEstimateCurrent(ctx, reader, a.ID); err != nil || got.NativeWeekly.Allocation != nil {
		t.Fatal("estimate-only reader received pool allocation", err)
	}
	if _, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: member.ID, Revision: member.Revision, Username: member.Username, Enabled: true,
		Grants: auth.Grants{Features: []string{"estimates", "pools"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{firstMember.ID}}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetEstimateCurrent(ctx, reader, a.ID); err != nil || got.NativeWeekly.Allocation == nil ||
		len(got.NativeWeekly.Allocation.Members) != 1 || got.NativeWeekly.Allocation.Reserve != nil || got.NativeWeekly.Allocation.Members[0].ID != firstMember.ID {
		t.Fatal("scoped pool preview disclosed another member or the reserve", err)
	}
	history, err := s.ListNativeWeeklyHistory(ctx, reader, a.ID, "0")
	if err != nil || len(history.Records) != 2 || !sameAmount(history.Records[1].StandardCost, "20", "1") || history.Records[1].AccountRevision == a.Revision {
		t.Fatal("scoped history lost immutable estimate evidence", history, err)
	}
	body, _ := json.Marshal(history)
	if strings.Contains(string(body), "actual_cost") || strings.Contains(string(body), "credentials") {
		t.Fatal("raw native usage crossed the estimates boundary")
	}
	if _, err = s.ListNativeWeeklyHistory(ctx, reader, "AAAAAAAAAAAAAAAAAAAAAA", "0"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("out-of-scope history lookup disclosed account existence", err)
	}
	tx, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	monitor, connection, account, err = s.estimateEnvironment(ctx, tx, a.ID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	expired, err := s.nativeWeeklyCurrent(ctx, tx, account, connection, monitor, reset)
	_ = tx.Rollback(ctx)
	if err != nil || expired.State != "source_stale" || expired.Latest != nil {
		t.Fatal("expired native cycle still exposed the previous amount", expired, err)
	}
	reviewed, err := s.ReviewObservation(ctx, token, ObservationReviewInput{
		AccountID: a.ID, ObservationID: zero.ID, Revision: "1", Excluded: true, Reason: "Owned observation qualification test",
	})
	if err != nil || !reviewed.Excluded || read().NativeWeekly.Latest != nil {
		t.Fatal("excluded sample still qualified the prior reference", reviewed, err)
	}
	use, observedAt = 1, now.Add(-time.Second)
	collectObservation(t, s, db, token, c, a)
	if current = read().NativeWeekly; current.State != "quota_decreased" || current.Latest != nil || current.Remaining != nil {
		t.Fatal("quota drop left the previous cycle allocation available", current)
	}
	replaceIdentity.Store(true)
	changed, err := s.RefreshManaged(ctx, token, readInput(a, c))
	if err != nil || changed.Account.IdentityState != "conflict" || read().NativeWeekly.State != "condition_changed" {
		t.Fatal("resource replacement kept the old account's evidence active", changed.Account.IdentityState, err)
	}
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer replacement.Close()
	nextSource := save(t, s, token, replacement.URL)
	if *nextSource.SourceID == *c.SourceID || read().NativeWeekly.State != "condition_changed" {
		t.Fatal("old-source observations became current after changing the connection")
	}
}

func collectWeeklyTestRound(t *testing.T, s *Service, ctx context.Context, after string) string {
	t.Helper()
	cursor, err := s.processNativeWeekly(ctx, after)
	if err != nil {
		t.Fatal(err)
	}
	collectUsageTestRound(t, s, ctx)
	if _, err = s.processNativeWeekly(ctx, after); err != nil {
		t.Fatal(err)
	}
	return cursor
}

func collectUsageTestRound(t *testing.T, s *Service, ctx context.Context) {
	t.Helper()
	leader, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Release()
	if _, err = s.processUsageDay(ctx, leader); err != nil {
		t.Fatal(err)
	}
}

func TestNativeWeeklyAndWalletShareScopedCostRepairs(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	first := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	start, second := first.Add(12*time.Hour), first.AddDate(0, 0, 1)
	points := []time.Time{start.Add(time.Hour), second.Add(time.Hour)}
	var point, readsFirst, readsSecond atomic.Int32
	var fixedStandard, fixedActual atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/accounts/8":
			i := point.Load()
			a := fullManagedAccount(8, "shared-usage-repair")
			a["extra"] = map[string]any{
				"codex_usage_updated_at": points[i], "codex_5h_window_minutes": 0,
				"codex_7d_window_minutes": 10080, "codex_7d_used_percent": 25 * (i + 1), "codex_7d_reset_at": start.Add(nativeWeek),
			}
			respond(w, a)
		case "/api/v1/admin/usage":
			q := r.URL.Query()
			page, _ := strconv.Atoi(q.Get("page"))
			size, _ := strconv.Atoi(q.Get("page_size"))
			row := func(id, user int, at time.Time, standard, actual any) map[string]any {
				return map[string]any{
					"id": id, "account_id": 8, "user_id": user, "model": "gpt-5",
					"created_at": at, "total_cost": standard, "actual_cost": actual, "billing_type": 0,
				}
			}
			var items []map[string]any
			switch q.Get("start_date") {
			case first.Format(weeklyDate):
				readsFirst.Add(1)
				var standard any = "unavailable"
				if fixedStandard.Load() {
					standard = json.Number("1.00")
				}
				items = []map[string]any{
					row(1, 21, start.Add(-time.Nanosecond), 100, 100),
					row(2, 21, start, 0, json.Number("0.00")),
					row(3, 22, start.Add(time.Minute), standard, -1),
				}
			case second.Format(weeklyDate):
				readsSecond.Add(1)
				var actual any
				if fixedActual.Load() {
					actual = json.Number("1.00")
				}
				items = []map[string]any{row(4, 21, second, 4, actual), row(5, 21, points[1], 100, 100)}
			default:
				t.Errorf("collector requested an unneeded date: %s", q.Get("start_date"))
				http.NotFound(w, r)
				return
			}
			total := len(items)
			if q.Get("sort_order") == "desc" {
				slices.Reverse(items)
			}
			from := min((page-1)*size, total)
			respond(w, map[string]any{"items": items[from:min(page*size, total)], "total": total, "page": page, "page_size": size})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := passiveManaged(t, s, token, c, 8)
	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled, policy.StaleAfterSeconds = true, 3*24*60*60
	policy, err = s.UpdateMonitoringConfig(ctx, token, policy)
	if err != nil || !policy.Enabled || policy.UsageEnabled {
		t.Fatalf("expected weekly-only policy: %+v %v", policy, err)
	}
	collectObservation(t, s, db, token, c, a)
	point.Store(1)
	collectObservation(t, s, db, token, c, a)
	for range 4 {
		collectWeeklyTestRound(t, s, ctx, "")
	}

	weekly := func() *NativeWeeklyCurrent {
		t.Helper()
		v, err := s.GetEstimateCurrent(ctx, token, a.ID)
		if err != nil || v.NativeWeekly == nil {
			t.Fatalf("estimate: %+v %v", v, err)
		}
		return v.NativeWeekly
	}
	wallet := func(from, until time.Time) weeklyWalletUsageResult {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		_, config, account, err := s.estimateEnvironment(ctx, tx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		window := usageDayRange{Start: from, End: until}
		got, err := readWeeklyWalletUsage(ctx, tx, config, weeklyWalletUsageRequest{
			Key: weeklyWalletUsageKey{Range: window}, Account: account,
		}, "21")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if v := weekly(); v.State != "cost_unavailable" || v.Latest != nil {
		t.Fatalf("unavailable standard cost qualified: %+v", v)
	}
	if v := wallet(start, second); !v.Ready || len(v.Uses) != 1 || v.Uses[0].ID != "2" || v.Uses[0].Cost != "0" {
		t.Fatalf("unrelated anomaly blocked verified zero spend: %+v", v)
	}
	if v := wallet(second, points[1]); v.Ready {
		t.Fatalf("missing actual cost qualified: %+v", v)
	}
	var facts int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM usage_capture_facts`).Scan(&facts); err != nil || facts != 3 {
		t.Fatalf("facts outside the two authorized half-open intervals were stored: %d %v", facts, err)
	}
	due := func(day time.Time) {
		t.Helper()
		exec(t, db, `UPDATE usage_day_jobs SET retry_at=now() WHERE day=$1::date`, day)
		collectWeeklyTestRound(t, s, ctx, "")
	}
	beforeSecond := readsSecond.Load()
	fixedStandard.Store(true)
	due(first)
	due(first)
	if v := weekly(); v.State != "ready" || v.Latest == nil || v.Latest.StandardCost.Numerator != "5" || v.Latest.Records != 3 ||
		readsSecond.Load() != beforeSecond {
		t.Fatalf("standard cost repair disturbed the other day: %+v", v)
	}
	oldEstimate := weekly().Latest.ID
	beforeFirst := readsFirst.Load()
	fixedActual.Store(true)
	due(second)
	due(second)
	if v := wallet(second, points[1]); !v.Ready || len(v.Uses) != 1 || v.Uses[0].Cost != "1" || readsFirst.Load() != beforeFirst {
		t.Fatalf("actual cost repair did not unblock only the required day: %+v", v)
	}
	history, err := s.ListNativeWeeklyHistory(ctx, token, a.ID, "0")
	if err != nil || len(history.Records) != 2 || history.Records[0].ID == oldEstimate || history.Records[1].ID != oldEstimate {
		t.Fatalf("requalification overwrote the previous estimate: %+v %v", history, err)
	}
	policy.Enabled = false
	if _, err = s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	beforeFirst, beforeSecond = readsFirst.Load(), readsSecond.Load()
	due(first)
	if readsFirst.Load() != beforeFirst || readsSecond.Load() != beforeSecond {
		t.Fatal("the shared collector read usage after monitoring was disabled")
	}
}
