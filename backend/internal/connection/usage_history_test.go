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
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestUsageHistoryReusesCapturedFactsWithCalendarMonthsAndCurrentScopes(t *testing.T) {
	s, pool, admin := setup(t)
	ctx := context.Background()
	var payload atomic.Value
	payload.Store([]map[string]any{})
	var calls atomic.Int32
	var denied atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Error("history mutated the native source")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			respond(w, fullManagedAccount(id, "history-resource-"+strconv.Itoa(id)))
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
		case r.URL.Path == "/api/v1/admin/usage":
			if denied.Load() {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			items := payload.Load().([]map[string]any)
			respond(w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100})
		default:
			t.Error("unexpected native request")
			http.Error(w, "denied", http.StatusForbidden)
		}
	}))
	defer upstream.Close()
	c := save(t, s, admin, upstream.URL)
	a := adoptManaged(t, s, admin, c, 1, "A")
	b := adoptManaged(t, s, admin, c, 2, "B")
	p := createParticipant(t, s, admin, c, 21, "P")
	q := createParticipant(t, s, admin, c, 22, "Q")
	capture := func(account ManagedAccount, start, end string, rows []map[string]any) UsageCapture {
		t.Helper()
		if rows == nil {
			rows = []map[string]any{}
		}
		payload.Store(rows)
		result, err := s.CaptureUsage(ctx, admin, UsageCaptureInput{AccountReadInput: readInput(account, c), StartAt: start, EndAt: end})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	start, split, end := "2028-02-28T23:59:59.999999999Z", "2028-03-01T00:00:00.000000002Z", "2028-04-02T00:00:00Z"
	first, second, third := usageRow(9, "2028-02-29T23:59:59.999999999Z"), usageRow(10, "2028-03-01T00:00:00Z"), usageRow(9007199254740993, "2028-03-01T00:00:00.000000001Z")
	second["user_id"], third["user_id"] = 22, nil
	first["api_key_id"], third["api_key_id"] = 9, nil
	ca := capture(a, start, split, []map[string]any{first, second, third})
	fourth, zero := usageRow(11, split), usageRow(12, "2028-03-31T23:59:59.999999999Z")
	fourth["actual_cost"] = nil
	zero["total_cost"], zero["actual_cost"], zero["account_stats_cost"] = 0, 0, 0
	fourth["api_key_id"], zero["api_key_id"] = 9, 10
	cb := capture(a, split, "2028-04-01T00:00:00Z", []map[string]any{fourth, zero})
	last := usageRow(1, "2028-04-01T00:00:00Z")
	last["account_id"] = 2
	last["api_key_id"] = 9
	cc := capture(b, "2028-04-01T00:00:00Z", end, []map[string]any{last})
	query := AttributedUsageQuery{CaptureIDs: []string{cc.ID, cb.ID, ca.ID}, StartAt: start, EndAt: end, Grain: "month", After: "0", Limit: 2}
	beforeCalls := calls.Load()
	view, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || view.History == nil || view.History.Basis != "selected_captures" || view.History.Timezone != "UTC" || view.History.AuthorityRevision == "" || len(view.History.Selections) != 3 {
		t.Fatalf("history metadata unavailable: %+v %v", view, err)
	}
	if view.Summary.RequestCount != "6" || *view.Summary.Metrics["total_cost"] != "0.5" || view.Summary.Metrics["actual_cost"] != nil || view.Summary.Metrics["account_stats_cost"] != nil || view.Complete || view.SettlementEligible {
		t.Fatalf("history invented completeness or mixed independent costs: %+v", view)
	}
	if len(view.Buckets) != 3 || view.Buckets[0].RequestCount != "1" || view.Buckets[1].RequestCount != "4" || view.Buckets[2].RequestCount != "1" || view.Buckets[0].StartAt.Format(time.RFC3339Nano) != start || view.Buckets[0].EndAt.Format(time.RFC3339Nano) != "2028-03-01T00:00:00Z" || view.Buckets[1].EndAt.Format(time.RFC3339Nano) != "2028-04-01T00:00:00Z" {
		t.Fatalf("calendar month/leap day or clipped bounds changed: %+v", view.Buckets)
	}
	if view.NextAfter == nil || *view.NextAfter != ca.ID+".10" {
		t.Fatal("numeric history cursor did not cross 9/10")
	}
	keySummary := func(value AttributedUsageView, account, participant, key string) AttributedUsageKeyGroup {
		t.Helper()
		for _, group := range value.KeyGroups {
			member, id := "", ""
			if group.ParticipantID != nil {
				member = *group.ParticipantID
			}
			if group.APIKeyID != nil {
				id = *group.APIKeyID
			}
			if group.AccountID == account && member == participant && id == key {
				return group
			}
		}
		t.Fatal("missing scoped Key summary", account, participant, key)
		return AttributedUsageKeyGroup{}
	}
	key9, key10, unknownKey := keySummary(view, a.ID, p.ID, "9"), keySummary(view, a.ID, p.ID, "10"), keySummary(view, a.ID, "", "")
	if len(view.KeyGroups) != 5 || key9.RequestCount != "2" || key9.Metrics["total_cost"] == nil || *key9.Metrics["total_cost"] != "0.2" || key9.Metrics["actual_cost"] != nil || key10.RequestCount != "1" || key10.Metrics["total_cost"] == nil || *key10.Metrics["total_cost"] != "0" || unknownKey.RequestCount != "1" {
		t.Fatal("Key groups mixed accounts, members, unknown IDs, zero or independent costs", view.KeyGroups)
	}
	legacyInput := query
	legacyInput.CaptureIDs, legacyInput.EndAt, legacyInput.Grain = []string{ca.ID}, split, "day"
	legacy, err := s.GetAttributedUsage(ctx, admin, legacyInput)
	legacyBody, _ := json.Marshal(legacy)
	if err != nil || len(legacy.KeyGroups) != 0 || bytes.Contains(legacyBody, []byte(`"key_groups"`)) {
		t.Fatal("legacy view/window acquired history Key fields", err)
	}
	system, err := s.GetSettings(ctx, admin)
	if err != nil || view.History.SettingsRevision != system.Revision {
		t.Fatal("history did not bind the saved statistical setting", err)
	}
	system, err = s.UpdateSettings(ctx, admin, system.Revision, "Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	zoned, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || zoned.History.Timezone != "Asia/Taipei" || zoned.History.SettingsRevision != system.Revision || zoned.Summary.RequestCount != "6" || len(zoned.Buckets) != 2 || zoned.Buckets[0].RequestCount != "4" || zoned.Buckets[1].RequestCount != "2" || zoned.Buckets[0].StartAt.Format(time.RFC3339) != "2028-02-29T16:00:00Z" || zoned.Buckets[0].EndAt.Format(time.RFC3339) != "2028-03-31T16:00:00Z" || !zoned.Records[0].Fact.CreatedAt.Equal(view.Records[0].Fact.CreatedAt) {
		t.Fatalf("saved timezone did not reaggregate unchanged native facts: %+v %v", zoned, err)
	}
	if _, err = s.UpdateSettings(ctx, admin, system.Revision, "UTC"); err != nil {
		t.Fatal(err)
	}
	query.After = *view.NextAfter
	page, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || len(page.Records) != 2 || page.Records[0].Fact.ID != "9007199254740993" || page.Records[1].CaptureID != cb.ID || page.Summary.RequestCount != "6" || !reflect.DeepEqual(page.KeyGroups, view.KeyGroups) {
		t.Fatal("history cursor lost exact IDs or changed totals", err)
	}
	query.After, query.Limit = "0", 100
	query.StartAt, query.EndAt = split, "2028-03-01T00:00:00.000000003Z"
	precise, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || precise.Summary.RequestCount != "1" || precise.Records[0].Fact.ID != "11" || len(precise.Buckets) != 1 || precise.Buckets[0].EndAt.Format(time.RFC3339Nano) != query.EndAt || len(precise.KeyGroups) != 1 || precise.KeyGroups[0].RequestCount != "1" {
		t.Fatal("history rounded native nanoseconds before aggregation", err)
	}
	query.StartAt, query.EndAt = "2028-03-31T23:59:59.999999999Z", "2028-04-01T00:00:00Z"
	trueZero, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || trueZero.Summary.RequestCount != "1" || *trueZero.Summary.Metrics["account_stats_cost"] != "0" {
		t.Fatal("explicit recorded zero was lost", err)
	}
	query.StartAt, query.EndAt = "2028-04-01T00:00:00.000000001Z", "2028-04-01T00:00:00.000000002Z"
	empty, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || empty.Summary.RequestCount != "0" || empty.Summary.Metrics["total_cost"] != nil || len(empty.Buckets) != 0 || len(empty.KeyGroups) != 0 {
		t.Fatal("empty observed history became zero money", err)
	}
	query.StartAt, query.EndAt = start, end
	query.Model = "not-recorded"
	filtered, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || filtered.Summary.RequestCount != "0" || len(filtered.Groups) != 0 || len(filtered.Records) != 0 || len(filtered.KeyGroups) != 0 {
		t.Fatal("model filter followed aggregate", err)
	}
	query.Model = ""
	query.APIKeyID = "9"
	byKey, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || byKey.Summary.RequestCount != "3" || len(byKey.KeyGroups) != 2 {
		t.Fatal("Key filter followed aggregation or merged different accounts", byKey.KeyGroups, err)
	}
	query.APIKeyID = ""
	member, err := s.auth.CreateUser(ctx, admin, auth.UserCreate{Username: "history-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"usage"}, AccountIDs: []string{a.ID, b.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := s.GetUsageHistory(ctx, reader, query)
	if err != nil || scoped.Summary.RequestCount != "4" || *scoped.Summary.Metrics["total_cost"] != "0.3" || scoped.Unassigned != nil || scoped.UnassignedVisible || scoped.History.AuthorityRevision != member.Revision {
		t.Fatalf("two-dimensional history scope failed: %+v %v", scoped, err)
	}
	if len(scoped.KeyGroups) != 3 || keySummary(scoped, a.ID, p.ID, "9").RequestCount != "2" {
		t.Fatal("current participant scope did not precede Key aggregation", scoped.KeyGroups)
	}
	body, _ := json.Marshal(scoped)
	for _, hidden := range []string{"observed_count", "scanned_count", "upstream_day_total", q.ID, "pages_read"} {
		if strings.Contains(string(body), hidden) {
			t.Fatal("history leaked private remainder or another participant")
		}
	}
	query.ParticipantID = q.ID
	if _, err = s.GetUsageHistory(ctx, reader, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("supplied participant bypassed scope", err)
	}
	query.ParticipantID = ""
	member, err = s.auth.UpdateUser(ctx, admin, auth.UserUpdate{ID: member.ID, Revision: member.Revision, Username: member.Username, Enabled: true, Grants: auth.Grants{Features: []string{"usage"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetUsageHistory(ctx, reader, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("account contraction left cross-account history visible", err)
	}
	query.CaptureIDs = []string{ca.ID, cb.ID}
	contracted, err := s.GetUsageHistory(ctx, reader, query)
	if err != nil || contracted.Summary.RequestCount != "3" || contracted.History.AuthorityRevision != member.Revision || contracted.History.AuthorityRevision == scoped.History.AuthorityRevision {
		t.Fatal("current authority revision or retained authorized facts lost", err)
	}
	restarted, _ := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	retained, err := restarted.GetUsageHistory(ctx, reader, query)
	if err != nil || retained.Summary.RequestCount != contracted.Summary.RequestCount || !retained.OldestCaptureAt.Equal(contracted.OldestCaptureAt) || !reflect.DeepEqual(retained.KeyGroups, contracted.KeyGroups) {
		t.Fatal("service reconstruction changed retained history", err)
	}
	if calls.Load() != beforeCalls {
		t.Fatal("history read contacted the native service")
	}
	overlap := capture(a, start, split, []map[string]any{first})
	query.CaptureIDs = []string{ca.ID, overlap.ID}
	if _, err = s.GetUsageHistory(ctx, admin, query); !errors.Is(err, auth.ErrInput) {
		t.Fatal("overlapping captures were added as independent history", err)
	}
	query.CaptureIDs = []string{ca.ID, ca.ID}
	if _, err = s.GetUsageHistory(ctx, admin, query); !errors.Is(err, auth.ErrInput) {
		t.Fatal("duplicate captures were added twice", err)
	}
	query.CaptureIDs = []string{ca.ID, cb.ID}
	if _, err = s.GetAttributedUsage(ctx, admin, query); !errors.Is(err, auth.ErrInput) {
		t.Fatal("legacy view/window silently acquired multi-interval semantics", err)
	}
	query.CaptureIDs, query.Grain = []string{ca.ID}, "hour"
	if _, err = s.GetUsageHistory(ctx, admin, query); !errors.Is(err, auth.ErrInput) {
		t.Fatal("history accepted an undeclared grain", err)
	}
	query.Grain, query.StartAt, query.EndAt = "day", "2028-01-01T00:00:00Z", "2029-01-02T00:00:00Z"
	if _, err = s.GetUsageHistory(ctx, admin, query); !errors.Is(err, auth.ErrInput) {
		t.Fatal("history accepted an unbounded range", err)
	}
	denied.Store(true)
	failed := capture(a, "2028-05-01T00:00:00Z", "2028-05-02T00:00:00Z", nil)
	query.CaptureIDs, query.StartAt, query.EndAt = []string{failed.ID}, "2028-05-01T00:00:00Z", "2028-05-02T00:00:00Z"
	unavailable, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || unavailable.Summary.RequestCount != "0" || unavailable.Summary.Metrics["actual_cost"] != nil || !strings.Contains(strings.Join(unavailable.Limitations, ","), "CAPTURE_FAILED") {
		t.Fatal("failed source capture became a successful empty period", err)
	}
	far := capture(a, "2029-06-01T00:00:00Z", "2029-06-02T00:00:00Z", nil)
	query.CaptureIDs = []string{ca.ID, far.ID}
	if _, err = s.GetUsageHistory(ctx, admin, query); !errors.Is(err, auth.ErrInput) {
		t.Fatal("bounded query accepted an unbounded selected envelope", err)
	}
	query.CaptureIDs = []string{failed.ID}
	calendarLock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer calendarLock.Rollback(ctx)
	if _, err = calendarLock.Exec(ctx, `LOCK TABLE usage_captures IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	calendarContext, cancelCalendar := context.WithTimeout(ctx, 8*time.Second)
	defer cancelCalendar()
	calendarRead := make(chan AttributedUsageView, 1)
	calendarError := make(chan error, 1)
	go func() {
		v, e := s.GetUsageHistory(calendarContext, admin, query)
		calendarRead <- v
		calendarError <- e
	}()
	waitCalendarLock := func(prefix string) {
		t.Helper()
		for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
			var waiting bool
			if e := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, prefix+"%").Scan(&waiting); e != nil {
				t.Fatal(e)
			}
			if waiting {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("actual statistical setting/capture lock was not observed")
	}
	waitCalendarLock("SELECT c.id::text,c.account_id,c.snapshot FROM usage_captures")
	system, err = s.GetSettings(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	calendarWrite := make(chan error, 1)
	go func() {
		_, e := s.UpdateSettings(calendarContext, admin, system.Revision, "Asia/Tokyo")
		calendarWrite <- e
	}()
	waitCalendarLock("SELECT revision,timezone,saved_at FROM system_settings WHERE singleton FOR UPDATE")
	_ = calendarLock.Rollback(ctx)
	consistent := <-calendarRead
	if err = <-calendarError; err != nil || consistent.History == nil || consistent.History.Timezone != "UTC" || consistent.History.SettingsRevision != system.Revision {
		t.Fatal("concurrent setting writer changed a history read in flight", err)
	}
	if err = <-calendarWrite; err != nil {
		t.Fatal(err)
	}
	currentCalendar, err := s.GetUsageHistory(ctx, admin, query)
	if err != nil || currentCalendar.History.Timezone != "Asia/Tokyo" || currentCalendar.History.SettingsRevision == system.Revision {
		t.Fatal("next history read did not see the committed setting", err)
	}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, `LOCK TABLE usage_captures IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	digest := sha256.Sum256([]byte(admin))
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() {
		result, readErr := s.GetUsageHistory(ctx, admin, query)
		if result.History != nil || len(result.Records) != 0 || len(result.KeyGroups) != 0 {
			done <- errors.New("expired history returned protected data")
			return
		}
		done <- readErr
	}()
	observed := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT c.id::text,c.account_id,c.snapshot FROM usage_captures%')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observed {
		t.Fatal("actual history metadata lock was not observed")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	_ = lock.Rollback(ctx)
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired actual history wait returned a protected result", err)
	}
}

func TestUsageHistoryMidnightRollbackRetainsScopedFactsAndDisjointPeriods(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, native := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Calendar source")
	p := createParticipant(t, s, token, c, 21, "Calendar member")
	q := createParticipant(t, s, token, c, 22, "Other calendar member")
	exec(t, db, `UPDATE participant_bindings SET started_at='1988-10-28T00:00:00Z'`)
	stamps := []string{"1988-10-30T01:59:59.999999999Z", "1988-10-30T02:00:00Z", "1988-10-30T02:00:59.999999999Z", "1988-10-30T02:01:00Z", "1988-10-30T03:59:59.999999999Z", "1988-10-30T04:00:00Z"}
	for i, at := range stamps {
		row := usageRow(int64(i+1), at)
		row["total_cost"] = json.Number("0.000000000000000001")
		if i == 2 {
			row["user_id"], row["total_cost"] = 22, 0
		}
		if i == 5 {
			row["actual_cost"] = nil
		}
		native.logs[1] = append(native.logs[1], row)
	}
	const start, end = "1988-10-29T02:00:00Z", "1988-10-31T04:00:00Z"
	capture, err := s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: start, EndAt: end})
	if err != nil || capture.ObservedCount != "6" {
		t.Fatal("calendar fixture was not captured through the native boundary", capture, err)
	}
	system, err := s.GetSettings(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateSettings(ctx, token, system.Revision, "America/Goose_Bay"); err != nil {
		t.Fatal(err)
	}
	query := AttributedUsageQuery{CaptureIDs: []string{capture.ID}, StartAt: start, EndAt: end, Grain: "day", After: "0", Limit: 100}
	calls := native.calls.Load()
	view, err := s.GetUsageHistory(ctx, token, query)
	if err != nil || len(view.Buckets) != 4 || view.Summary.RequestCount != "6" || *view.Summary.Metrics["total_cost"] != "0.000000000000000005" || view.Summary.Metrics["actual_cost"] != nil || view.Complete || view.SettlementEligible {
		t.Fatal("calendar rollback lost records or invented complete money", view, err)
	}
	for i, bounds := range [][3]string{{start, stamps[1], "1"}, {stamps[1], stamps[3], "2"}, {stamps[3], stamps[5], "2"}, {stamps[5], end, "1"}} {
		bucket := view.Buckets[i]
		if bucket.StartAt.Format(time.RFC3339Nano) != bounds[0] || bucket.EndAt.Format(time.RFC3339Nano) != bounds[1] || bucket.RequestCount != bounds[2] {
			t.Fatal("calendar UTC components overlapped or selected the wrong date", bucket, bounds)
		}
	}
	member, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "calendar-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"usage"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := s.GetUsageHistory(ctx, reader, query)
	if err != nil || scoped.Summary.RequestCount != "5" || scoped.Buckets[1].RequestCount != "1" || scoped.Unassigned != nil {
		t.Fatal("calendar components bypassed participant scope", scoped, err)
	}
	body, _ := json.Marshal(scoped)
	if bytes.Contains(body, []byte(q.ID)) {
		t.Fatal("another calendar member escaped scope")
	}
	query.StartAt, query.EndAt = stamps[2], stamps[3]
	clipped, err := s.GetUsageHistory(ctx, token, query)
	if err != nil || clipped.Summary.RequestCount != "1" || len(clipped.Buckets) != 1 || clipped.Buckets[0].StartAt.Format(time.RFC3339Nano) != stamps[2] || clipped.Buckets[0].EndAt.Format(time.RFC3339Nano) != stamps[3] || *clipped.Summary.Metrics["total_cost"] != "0" {
		t.Fatal("one-nanosecond boundary crossed a date rollback", clipped, err)
	}
	query.StartAt, query.EndAt = start, end
	replacement, err := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := replacement.GetUsageHistory(ctx, token, query)
	if err != nil || !reflect.DeepEqual(retained, view) || native.calls.Load() != calls {
		t.Fatal("calendar history changed native facts or called upstream", err)
	}
}
