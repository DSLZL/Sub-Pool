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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/fakesub2api"
)

func usageRow(id int64, stamp string) map[string]any {
	return fakesub2api.UsageRow(id, stamp)
}

func TestUsageProjectionPreservesExactFactsAndUnknowns(t *testing.T) {
	row := usageRow(9007199254740993, "2026-11-01T08:00:00.123456789+08:00")
	row["input_tokens"] = int64(9007199254740993)
	row["total_cost"] = json.Number("1e-18")
	row["actual_cost"] = json.Number("0.30000000000000004")
	row["request_id"] = ""
	body, _ := json.Marshal(row)
	f, ok := projectUsage(body, "1", "management-secret")
	if !ok || f.ID != "9007199254740993" || *f.Metrics["input_tokens"] != "9007199254740993" || *f.Metrics["total_cost"] != "0.000000000000000001" || *f.Metrics["output_tokens"] != "0" || f.Metrics["cache_creation_tokens"] != nil || f.Metrics["account_stats_cost"] != nil || f.RequestID != nil {
		t.Fatalf("precision/null/zero lost: %+v", f)
	}
	if f.Model != "requested-model" || *f.UpstreamModel != "upstream-model" || f.BillingCandidate != "wallet" || f.DeductionState != "unknown" || f.CreatedAt.Format(time.RFC3339Nano) != "2026-11-01T00:00:00.123456789Z" {
		t.Fatal("model/billing/native timestamp meaning changed")
	}
	safe, _ := json.Marshal(f)
	if bytes.Contains(safe, []byte("private-")) || bytes.Contains(safe, []byte("api_key\"")) || bytes.Contains(safe, []byte("ip_address")) {
		t.Fatal("associated secrets or unnecessary private fields escaped")
	}
	summary := summarizeUsage([]UsageFact{f, f})
	if *summary.Metrics["actual_cost"] != "0.60000000000000008" || *summary.Metrics["total_cost"] != "0.000000000000000002" || summary.Metrics["account_stats_cost"] != nil || *summary.Metrics["input_tokens"] != "18014398509481986" {
		t.Fatal("facts were rounded, mixed, reconstructed or filled with zero")
	}
	if summarizeUsage(nil).Metrics["actual_cost"] != nil {
		t.Fatal("empty evidence claimed zero spending")
	}
	for _, change := range []func(map[string]any){
		func(r map[string]any) { r["actual_cost"] = "0.2" },
		func(r map[string]any) { r["actual_cost"] = json.Number("1e99") },
		func(r map[string]any) { r["actual_cost"] = json.Number("0.0000000000000000000000000000000000001") },
		func(r map[string]any) { r["input_tokens"] = 1.5 },
		func(r map[string]any) { r["model"] = "management-secret" },
		func(r map[string]any) { r["account_id"] = 2 },
		func(r map[string]any) { delete(r, "created_at") },
	} {
		r := usageRow(1, "2026-11-01T00:00:00Z")
		change(r)
		body, _ = json.Marshal(r)
		if _, ok := projectUsage(body, "1", "management-secret"); ok {
			t.Fatalf("accepted invalid/untrusted fact: %s", body)
		}
	}
	if _, ok := projectUsage([]byte(`{"id":1,"id":2}`), "1", ""); ok {
		t.Fatal("duplicate fact fields accepted")
	}
}

func TestUsageCapturePaginationBoundariesFiltersAndRetainedHistory(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	var empty atomic.Bool
	rows := []map[string]any{}
	for id := 1; id <= 103; id++ {
		stamp := "2026-11-01T00:00:01Z"
		if id == 1 {
			stamp = "2026-11-01T00:00:00.999999999Z"
		} else if id == 103 {
			stamp = "2026-11-02T00:00:01Z"
		}
		row := usageRow(int64(id), stamp)
		if id%2 == 0 {
			row["user_id"], row["api_key_id"], row["model"], row["billing_type"] = 22, 32, "other-model", 1
		}
		rows = append(rows, row)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/accounts/1" {
			respond(w, fullManagedAccount(1, "resource"))
			return
		}
		calls.Add(1)
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/usage" || q.Get("account_id") != "1" || q.Get("timezone") != "UTC" || q.Get("sort_by") != "id" || q.Get("sort_order") != "asc" || q.Get("exact_total") != "true" || q.Get("start_date") != "2026-11-01" || q.Get("end_date") != "2026-11-02" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe/incorrect usage request")
			http.Error(w, "denied", 403)
			return
		}
		page, _ := strconv.Atoi(q.Get("page"))
		items := rows
		if empty.Load() {
			items = []map[string]any{}
		}
		from := min((page-1)*usagePageSize, len(items))
		to := min(from+usagePageSize, len(items))
		respond(w, map[string]any{"items": items[from:to], "page": page, "page_size": 100, "total": len(items)})
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Usage account")
	input := UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: "2026-11-01T00:00:01Z", EndAt: "2026-11-02T00:00:01Z"}
	capture, err := s.CaptureUsage(ctx, token, input)
	if err != nil || capture.State != "exhausted" || capture.PagesRead != 2 || capture.ScannedCount != "103" || capture.ObservedCount != "101" || capture.Complete || capture.SettlementEligible || calls.Load() != 2 || !slices.Contains(capture.Limitations, "RETENTION_UNVERIFIED") {
		t.Fatalf("bad capture: %+v %v", capture, err)
	}
	query := UsageQuery{ID: capture.ID, After: "0", Limit: 8, Grain: "hour"}
	view, err := s.GetUsageCapture(ctx, token, query)
	if err != nil || view.Summary.RequestCount != "101" || view.Records[7].ID != "9" || view.NextAfter == nil || *view.NextAfter != "9" || *view.Summary.Metrics["total_cost"] != "10.1" || len(view.Buckets) != 1 || !view.Buckets[0].StartAt.Equal(capture.StartAt) {
		t.Fatalf("bad exact summary/first numeric page: %+v %v", view, err)
	}
	query.After = *view.NextAfter
	next, err := s.GetUsageCapture(ctx, token, query)
	if err != nil || next.Records[0].ID != "10" || next.Summary.RequestCount != "101" {
		t.Fatal("numeric cursor skipped/misordered facts or changed summary")
	}
	query.After, query.Model, query.UserID, query.APIKeyID = "0", "other-model", "22", "32"
	filtered, err := s.GetUsageCapture(ctx, token, query)
	if err != nil || filtered.Summary.RequestCount != "51" || *filtered.Summary.Metrics["actual_cost"] != "10.2" || filtered.Records[0].BillingCandidate != "subscription" || filtered.Records[0].DeductionState != "unknown" {
		t.Fatal("filters were applied after aggregation/pagination or billing was inferred")
	}
	for i := 0; i < 10; i++ {
		if _, err = s.CaptureUsage(ctx, token, input); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListUsageCaptures(ctx, token, a.ID, "0", 9)
	if err != nil || len(page.Captures) != 9 || page.Captures[8].ID != "9" || page.NextAfter == nil {
		t.Fatal("capture history sorted text IDs")
	}
	page, err = s.ListUsageCaptures(ctx, token, a.ID, *page.NextAfter, 9)
	if err != nil || len(page.Captures) != 2 || page.Captures[0].ID != "10" || page.NextAfter != nil {
		t.Fatal("capture history pagination lost records")
	}
	empty.Store(true)
	emptyCapture, err := s.CaptureUsage(ctx, token, input)
	if err != nil || emptyCapture.ObservedCount != "0" || emptyCapture.Complete {
		t.Fatal("empty source claimed complete consumption")
	}
	restarted, _ := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	before := calls.Load()
	retained, err := restarted.GetUsageCapture(ctx, token, UsageQuery{ID: capture.ID, After: "0", Limit: 100, Grain: "day"})
	if err != nil || retained.Summary.RequestCount != "101" || calls.Load() != before {
		t.Fatal("upstream cleanup/restart replaced captured facts or replayed a request")
	}
	changed := save(t, s, token, "https://other.example")
	if _, err = s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: readInput(a, changed), StartAt: input.StartAt, EndAt: input.EndAt}); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
		t.Fatal("old account retargeted to a different source")
	}
	history, err := s.GetUsageCapture(ctx, token, UsageQuery{ID: capture.ID, After: "0", Limit: 1, Grain: "day"})
	if err != nil || history.Capture.SourceCurrent || history.Capture.ConnectionCurrent || history.Summary.RequestCount != "101" {
		t.Fatal("historical facts lost source binding")
	}
}

func TestUsageCapturePartialPageFailuresAndScanLimit(t *testing.T) {
	for _, failure := range []string{"changed-total", "duplicate", "wrong-account", "denied", "limit"} {
		t.Run(failure, func(t *testing.T) {
			s, _, token := setup(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/admin/usage" {
					respond(w, fullManagedAccount(1, "resource"))
					return
				}
				calls.Add(1)
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if page == 2 && failure == "denied" {
					http.Error(w, "private-upstream-error", 403)
					return
				}
				total := 201
				if failure == "limit" {
					total = 1001
				} else if failure == "changed-total" && page == 2 {
					total = 202
				}
				items := []map[string]any{}
				for i := 1; i <= 100; i++ {
					id := int64((page-1)*100 + i)
					if page == 2 && failure == "duplicate" && i == 1 {
						id = 100
					}
					row := usageRow(id, "2026-11-01T00:00:00Z")
					if page == 2 && failure == "wrong-account" && i == 70 {
						row["account_id"] = 2
					}
					items = append(items, row)
				}
				respond(w, map[string]any{"items": items, "page": page, "page_size": 100, "total": total})
			}))
			defer server.Close()
			c := save(t, s, token, server.URL)
			a := adoptManaged(t, s, token, c, 1, "Account")
			capture, err := s.CaptureUsage(context.Background(), token, UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: "2026-11-01T00:00:00Z", EndAt: "2026-11-02T00:00:00Z"})
			count, pages, requests := "100", 1, int32(2)
			if failure == "limit" {
				count, pages, requests = "1000", 10, 10
			}
			if err != nil || capture.State != "partial" || capture.ObservedCount != count || capture.PagesRead != pages || calls.Load() != requests || capture.Complete || capture.SettlementEligible {
				t.Fatalf("unsafe partial result: %+v %v", capture, err)
			}
			view, err := s.GetUsageCapture(context.Background(), token, UsageQuery{ID: capture.ID, After: "0", Limit: 100, Grain: "day"})
			if err != nil || view.Summary.RequestCount != count {
				t.Fatal("failed page leaked partial-page rows or dropped prior pages")
			}
			body, _ := json.Marshal(view)
			if strings.Contains(string(body), "private-") {
				t.Fatal("upstream error/private fields leaked")
			}
		})
	}
}

func TestUsageCaptureAuthorityAuditAndObservedNetworkExpiry(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	var blocked atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/usage" {
			respond(w, fullManagedAccount(1, "resource"))
			return
		}
		calls.Add(1)
		if blocked.Load() {
			entered <- struct{}{}
			<-release
		}
		respond(w, map[string]any{"items": []any{usageRow(1, "2026-11-01T00:00:00Z")}, "total": 1, "page": 1, "page_size": 100})
	}))
	defer server.Close()
	defer unblock()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Account")
	input := UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: "2026-11-01T00:00:00Z", EndAt: "2026-11-02T00:00:00Z"}
	member, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "usage-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"usage"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{"AAAAAAAAAAAAAAAAAAAAAA"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CaptureUsage(ctx, reader, input); !errors.Is(err, auth.ErrForbidden) || calls.Load() != 0 {
		t.Fatal("ordinary reader initiated upstream collection")
	}
	if _, err = s.ListUsageCaptures(ctx, reader, a.ID, "0", 1); !errors.Is(err, auth.ErrNotFound) || calls.Load() != 0 {
		t.Fatal("unattributed account totals exposed with fabricated participant grants")
	}
	if _, err = s.GetUsageCapture(ctx, reader, UsageQuery{ID: "1", After: "0", Limit: 1, Grain: "day"}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("ordinary query disclosed existence")
	}
	exec(t, pool, `CREATE FUNCTION reject_usage_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`)
	exec(t, pool, `CREATE TRIGGER reject_usage_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_usage_audit()`)
	if _, err = s.CaptureUsage(ctx, token, input); err == nil {
		t.Fatal("audit failure reported persisted facts")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_captures)+(SELECT count(*) FROM usage_capture_facts)+(SELECT count(*) FROM usage_events)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure did not roll back all capture state")
	}
	exec(t, pool, `DROP TRIGGER reject_usage_audit ON management_events`)
	blocked.Store(true)
	deadline := time.Now().Add(time.Second)
	digest := sha256.Sum256([]byte(token))
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() { _, err := s.CaptureUsage(ctx, token, input); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("ended before actual network barrier: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("capture never reached upstream")
	}
	if delay := time.Until(deadline) + 25*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	unblock()
	if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired observed read returned/persisted facts: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM usage_captures`).Scan(&count); err != nil || count != 0 || calls.Load() != 2 {
		t.Fatal("expired request persisted capture or retried upstream")
	}
}

func TestUsageCaptureRechecksExpiryAfterObservedConfigurationWait(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/usage" {
			calls.Add(1)
		}
		respond(w, fullManagedAccount(1, "resource"))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Account")
	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(ctx)
	if _, err = locker.Exec(ctx, `SELECT revision FROM connection_config FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(1200 * time.Millisecond)
	digest := sha256.Sum256([]byte(token))
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], expires)
	done := make(chan error, 1)
	go func() {
		_, err := s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: "2026-11-01T00:00:00Z", EndAt: "2026-11-02T00:00:00Z"})
		done <- err
	}()
	waiting := false
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT c.revision,c.source_id,%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("configuration wait was not actually observed")
	}
	if delay := time.Until(expires) + 25*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) || calls.Load() != 0 {
		t.Fatalf("expired waiter read upstream: %v", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM usage_captures`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired waiter persisted capture")
	}
}
