// Package fakesub2api serves deterministic Sub2API HTTP fixtures.
package fakesub2api

import (
	"encoding/json"
	"maps"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ManagementKey and BearerToken authenticate only the test fixture.
	ManagementKey = "adm_sc_test_only_secret"
	BearerToken   = "fixture-admin-token"
	adminPath     = "/api/v1/admin"
)

// Options seed the fixture at a known UTC time. A zero UsageRows uses two rows.
type Options struct {
	At        time.Time
	Version   string
	Mode      string
	UsageRows int
	Faults    []Fault
}

// Fault applies to one method and path. Zero Page or Request matches every
// page or matching request. Request counts only requests that match Page.
type Fault struct {
	Method     string
	Path       string
	Page       int
	Request    int
	Delay      time.Duration
	Status     int
	RetryAfter int
	TotalDelta int
}

type faultCounter struct {
	Fault
	seen int
}

type usageData struct {
	at  time.Time
	row map[string]any
}

type fixture struct {
	at           time.Time
	version      string
	mode         string
	usage        []usageData
	browserSmoke bool

	mu        sync.Mutex
	faults    []faultCounter
	account   map[string]any
	otherUser map[string]any
	group     map[string]any
	balance   *big.Rat
	history   []map[string]any
	applied   map[string]appliedBalance
}

type result struct {
	data   any
	status int
	reason string
}

type route func(*http.Request, int) result

// New creates an independent fixture for httptest or the loopback command.
func New(opts Options) http.Handler {
	mux, _ := newFixture(opts)
	return mux
}

// NewBrowserSmoke adds historical usage and a passive Codex quota snapshot.
// The returned function records one later usage row at the caller's request.
func NewBrowserSmoke(now time.Time) (http.Handler, func() time.Time) {
	now = now.UTC()
	yesterday := now.Truncate(24*time.Hour).AddDate(0, 0, -1).Add(time.Hour)
	mux, f := newFixture(Options{At: yesterday})
	f.browserSmoke = true
	f.account["extra"] = map[string]any{
		"codex_usage_updated_at":  now.Format(time.RFC3339Nano),
		"codex_7d_used_percent":   25,
		"codex_7d_window_minutes": 7 * 24 * 60,
		"codex_7d_reset_at":       now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}
	f.otherUser = map[string]any{"id": 22, "email": "second@example.invalid", "username": "fixture-second",
		"role": "user", "status": "active", "created_at": yesterday, "balance": json.Number("0")}
	return mux, func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		at := time.Now().UTC()
		row := UsageRow(int64(len(f.usage))+1, at.Format(time.RFC3339Nano))
		f.usage = append(f.usage, usageData{at: at, row: row})
		return at
	}
}

func newFixture(opts Options) (http.Handler, *fixture) {
	at := opts.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	}
	version, mode := opts.Version, opts.Mode
	if version == "" {
		version = "0.2.23"
	}
	if mode == "" {
		mode = "standard"
	}
	count := opts.UsageRows
	if count == 0 {
		count = 2
	}
	f := &fixture{
		at: at, version: version, mode: mode, account: managedAccount(at), group: pricingGroup(at),
		balance: big.NewRat(100, 1), applied: make(map[string]appliedBalance),
	}
	for _, fault := range opts.Faults {
		f.faults = append(f.faults, faultCounter{Fault: fault})
	}
	for i := 0; i < count; i++ {
		stamp := at.Add(time.Duration(i) * time.Second)
		row := UsageRow(int64(i)+1, stamp.Format(time.RFC3339Nano))
		row["billing_type"] = i % 2
		f.usage = append(f.usage, usageData{at: stamp, row: row})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", f.serve(func(*http.Request, int) result {
		return result{data: map[string]string{"status": "ok"}}
	}))
	mux.HandleFunc("GET /api/v1/auth/me", f.serve(func(*http.Request, int) result {
		return result{data: map[string]any{"id": 1, "role": "admin", "run_mode": f.mode, "balance": json.Number("100")}}
	}))
	mux.HandleFunc("GET "+adminPath+"/system/version", f.serve(func(*http.Request, int) result {
		return result{data: map[string]string{"version": f.version}}
	}))
	mux.HandleFunc("GET "+adminPath+"/groups/0/model-allowlist-candidates", f.serve(f.modelCandidates))
	mux.HandleFunc("GET "+adminPath+"/accounts", f.serve(f.listAccounts))
	mux.HandleFunc("GET "+adminPath+"/accounts/{id}", f.serve(f.getAccount))
	mux.HandleFunc("POST "+adminPath+"/accounts/{id}/schedulable", f.serve(f.setSchedulable))
	mux.HandleFunc("GET "+adminPath+"/usage", f.serve(f.listUsage))
	mux.HandleFunc("GET "+adminPath+"/users", f.serve(f.listUsers))
	mux.HandleFunc("GET "+adminPath+"/users/{id}", f.serve(f.getUser))
	mux.HandleFunc("GET "+adminPath+"/users/{id}/balance-history", f.serve(f.listBalanceHistory))
	mux.HandleFunc("POST "+adminPath+"/users/{id}/balance", f.serve(f.adjustBalance))
	mux.HandleFunc("GET "+adminPath+"/groups", f.serve(f.listGroups))
	mux.HandleFunc("GET "+adminPath+"/groups/{id}", f.serve(f.getGroup))
	mux.HandleFunc("PUT "+adminPath+"/groups/{id}", f.serve(f.updateGroup))
	mux.HandleFunc("GET "+adminPath+"/channels/model-pricing", f.serve(defaultPricing))
	mux.HandleFunc("GET "+adminPath+"/settings", f.serve(func(*http.Request, int) result {
		return result{data: map[string]bool{"affiliate_enabled": false, "affiliate_admin_recharge_enabled": false}}
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		write(w, result{status: http.StatusNotFound}, false)
	})
	return mux, f
}

func (f *fixture) serve(h route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health := r.URL.Path == "/health"
		if !health && !authorized(r) {
			write(w, result{status: http.StatusUnauthorized}, false)
			return
		}
		fault := f.requestFault(r)
		if fault.Delay > 0 {
			timer := time.NewTimer(fault.Delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		if r.Context().Err() != nil {
			return
		}
		if fault.Status != 0 {
			if fault.Status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", strconv.Itoa(fault.RetryAfter))
			}
			write(w, result{status: fault.Status}, false)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		write(w, h(r, fault.TotalDelta), health)
	}
}

func authorized(r *http.Request) bool {
	jwt, key := r.Header.Get("Authorization"), r.Header.Get("x-api-key")
	if r.URL.Path == "/api/v1/auth/me" {
		return jwt == "Bearer "+BearerToken && key == ""
	}
	return jwt == "Bearer "+BearerToken && key == "" || key == ManagementKey && jwt == ""
}

func (f *fixture) requestFault(r *http.Request) Fault {
	page, _ := pagination(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	var selected Fault
	for i := range f.faults {
		rule := &f.faults[i]
		if rule.Method != r.Method || rule.Path != r.URL.Path || rule.Page != 0 && rule.Page != page {
			continue
		}
		rule.seen++
		if selected.Method == "" && (rule.Request == 0 || rule.Request == rule.seen) {
			selected = rule.Fault
		}
	}
	return selected
}

func write(w http.ResponseWriter, out result, raw bool) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	if out.status != 0 {
		w.WriteHeader(out.status)
		body := map[string]any{"code": out.status, "message": http.StatusText(out.status)}
		if out.reason != "" {
			body["reason"] = out.reason
		}
		_ = encoder.Encode(body)
	} else if raw {
		_ = encoder.Encode(out.data)
	} else {
		_ = encoder.Encode(map[string]any{"code": 0, "message": "success", "data": out.data})
	}
}

func pagination(r *http.Request) (page, size int) {
	q := r.URL.Query()
	page, _ = strconv.Atoi(q.Get("page"))
	size, _ = strconv.Atoi(q.Get("page_size"))
	if size <= 0 {
		size = 20
	}
	return max(page, 1), size
}

func listing(r *http.Request, all []map[string]any, totalDelta int) result {
	page, size := pagination(r)
	items := make([]map[string]any, 0)
	if page-1 <= len(all)/size {
		start := (page - 1) * size
		items = append(items, all[start:start+min(size, len(all)-start)]...)
	}
	return result{data: map[string]any{
		"items": items, "total": max(len(items), len(all)+totalDelta), "page": page, "page_size": size,
	}}
}

func matches(want, actual string) bool { return want == "" || want == actual }

func (f *fixture) modelCandidates(r *http.Request, _ int) result {
	if r.URL.Query().Get("platform") != "openai" {
		return result{status: http.StatusBadRequest}
	}
	if f.mode == "simple" {
		return result{status: http.StatusForbidden, reason: "SIMPLE_MODE_OPERATION_UNSUPPORTED"}
	}
	return result{data: map[string]any{"platform": "openai", "models": []string{model}}}
}

func (f *fixture) listAccounts(r *http.Request, delta int) result {
	q := r.URL.Query()
	if !matches(q.Get("platform"), "openai") || !matches(q.Get("type"), "oauth") ||
		!matches(q.Get("group"), "1") || !matches(q.Get("status"), "active") ||
		!strings.Contains("account 1", strings.ToLower(q.Get("search"))) {
		return listing(r, nil, delta)
	}
	f.mu.Lock()
	account := maps.Clone(f.account)
	f.mu.Unlock()
	return listing(r, []map[string]any{account}, delta)
}

func (f *fixture) getAccount(r *http.Request, _ int) result {
	if r.PathValue("id") != "1" {
		return result{status: http.StatusNotFound}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return result{data: maps.Clone(f.account)}
}

func (f *fixture) listUsage(r *http.Request, delta int) result {
	q := r.URL.Query()
	if !matches(q.Get("account_id"), "1") || !matches(q.Get("user_id"), "21") ||
		!matches(q.Get("api_key_id"), "31") || !matches(q.Get("model"), model) {
		return listing(r, nil, delta)
	}
	zone := time.UTC
	if name := q.Get("timezone"); name != "" {
		var err error
		zone, err = time.LoadLocation(name)
		if err != nil {
			return result{status: http.StatusBadRequest}
		}
	}
	var start, end time.Time
	for _, bound := range []struct {
		name string
		time *time.Time
	}{{"start_date", &start}, {"end_date", &end}} {
		if value := q.Get(bound.name); value != "" {
			date, err := time.ParseInLocation(time.DateOnly, value, zone)
			if err != nil {
				return result{status: http.StatusBadRequest}
			}
			*bound.time = date
		}
	}
	if !end.IsZero() {
		end = end.AddDate(0, 0, 1)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]map[string]any, 0, len(f.usage))
	for _, usage := range f.usage {
		if !usage.at.Before(start) && (end.IsZero() || usage.at.Before(end)) {
			rows = append(rows, usage.row)
		}
	}
	if q.Get("sort_order") == "desc" {
		slices.Reverse(rows)
	}
	return listing(r, rows, delta)
}

func (f *fixture) listUsers(r *http.Request, delta int) result {
	q := r.URL.Query()
	if !matches(q.Get("role"), "user") || !matches(q.Get("status"), "active") {
		return listing(r, nil, delta)
	}
	f.mu.Lock()
	user := f.user()
	f.mu.Unlock()
	search := strings.ToLower(q.Get("search"))
	var rows []map[string]any
	for _, item := range []map[string]any{user, f.otherUser} {
		if item != nil && strings.Contains(item["username"].(string)+" "+item["email"].(string), search) {
			rows = append(rows, item)
		}
	}
	return listing(r, rows, delta)
}

func (f *fixture) getUser(r *http.Request, _ int) result {
	if r.PathValue("id") == "22" && f.otherUser != nil {
		return result{data: f.otherUser}
	}
	if r.PathValue("id") != "21" {
		return result{status: http.StatusNotFound}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return result{data: f.user()}
}

func (f *fixture) user() map[string]any {
	user := map[string]any{"id": 21, "email": "fixture@example.invalid", "username": "fixture-user",
		"role": "user", "status": "active", "created_at": f.at, "balance": decimal(f.balance)}
	if f.browserSmoke {
		// The adjustment requires an explicit known frozen amount.
		user["frozen_balance"] = 0
	}
	return user
}

func (f *fixture) listGroups(r *http.Request, delta int) result {
	q := r.URL.Query()
	if !matches(q.Get("platform"), "openai") || !matches(q.Get("status"), "active") ||
		!strings.Contains("fixture group", strings.ToLower(q.Get("search"))) {
		return listing(r, nil, delta)
	}
	f.mu.Lock()
	group := maps.Clone(f.group)
	f.mu.Unlock()
	return listing(r, []map[string]any{group}, delta)
}

func (f *fixture) getGroup(r *http.Request, _ int) result {
	if r.PathValue("id") != "1" {
		return result{status: http.StatusNotFound}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return result{data: maps.Clone(f.group)}
}

func defaultPricing(r *http.Request, _ int) result {
	if r.URL.Query().Get("model") != model {
		return result{data: map[string]any{"found": false}}
	}
	return result{data: map[string]any{
		"found": true, "input_price": json.Number("0.1"), "output_price": json.Number("2"),
		"cache_write_price": nil, "cache_write_1h_price": nil, "cache_read_price": nil,
		"image_input_price": nil, "image_output_price": nil,
	}}
}
