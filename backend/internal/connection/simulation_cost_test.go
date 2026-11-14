package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

type simulationCostFixture struct {
	s        *Service
	db       *pgxpool.Pool
	token    string
	c        Configuration
	a        ManagedAccount
	points   []ObservationRecord
	times    []time.Time
	index    atomic.Int32
	calls    atomic.Int32
	badQuota atomic.Bool
	mu       sync.RWMutex
	change   func(map[string]any, *http.Request)
	raw      func([]byte) []byte
	block    chan struct{}
	arrive   chan struct{}
}

func simulatedCostFixture(t *testing.T) *simulationCostFixture {
	t.Helper()
	s, db, token := setup(t)
	anchor := time.Now().UTC().Add(-6 * time.Minute).Truncate(time.Second)
	f := &simulationCostFixture{s: s, db: db, token: token, times: []time.Time{anchor, anchor.Add(2 * time.Minute), anchor.Add(4 * time.Minute), anchor.Add(5 * time.Minute)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("cost provider crossed its read-only credential boundary")
			http.Error(w, "denied", 403)
			return
		}
		if r.URL.Path == simulationPrefix+"/coverage" {
			f.calls.Add(1)
			page := f.page(t, r)
			f.mu.RLock()
			change, raw, block, arrive := f.change, f.raw, f.block, f.arrive
			f.mu.RUnlock()
			if block != nil {
				select {
				case arrive <- struct{}{}:
				default:
				}
				<-block
			}
			if change != nil {
				change(page, r)
			}
			body, _ := json.Marshal(page)
			if raw != nil {
				body = raw(body)
			}
			respond(w, json.RawMessage(body))
			return
		}
		row := fullManagedAccount(1, "cost-simulation-resource")
		if r.URL.Path == "/api/v1/admin/accounts/1" {
			respond(w, row)
			return
		}
		if r.URL.Path != simulationPrefix+"/accounts/1/quota" {
			t.Error("cost fixture received unexpected route", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		index := int(f.index.Load())
		reset := anchor.Add(2 * time.Hour)
		row["extra"] = map[string]any{"codex_usage_updated_at": f.times[index].Format(time.RFC3339Nano), "codex_5h_used_percent": []int{10, 20, 40, 100}[index], "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339Nano), "codex_7d_used_percent": []int{70, 80, 90, 100}[index], "codex_7d_window_minutes": 10080, "codex_7d_reset_at": reset.Format(time.RFC3339Nano)}
		epoch := simulationTestEpoch
		if f.badQuota.Load() {
			epoch = "AwMDAwMDAwMDAwMDAwMDAw"
		}
		respond(w, map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": epoch, "read_only": true, "account": row})
	}))
	t.Cleanup(server.Close)
	f.c = save(t, s, token, server.URL)
	f.a = adoptManaged(t, s, token, f.c, 1, "Simulated complete costs")
	bindSimulation(t, s, f.c)
	for index := range 3 {
		f.index.Store(int32(index))
		f.points = append(f.points, collectObservation(t, s, db, token, f.c, f.a))
	}
	return f
}

func (f *simulationCostFixture) page(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	query := r.URL.Query()
	index := -1
	for i := 0; i < len(f.times)-1; i++ {
		if query.Get("start") == f.times[i].Format(time.RFC3339Nano) && query.Get("end") == f.times[i+1].Format(time.RFC3339Nano) {
			index = i
			break
		}
	}
	if query.Get("account_id") != "1" || query.Get("limit") != "100" || index < 0 {
		t.Error("wrong exact cost interval", r.URL.RawQuery)
		index = 0
	}
	standard, actual := []string{"3.00000000", "7.00000000", "9.00000000"}[index], []string{"1.50000000", "3.50000000", "4.50000000"}[index]
	price := map[string]any{"account_id": "1", "version": "2", "standard_rate": "1", "wallet_multiplier": "0.5", "billing_type": "standard_wallet", "effective_at": f.times[0].Add(-time.Minute).Format(time.RFC3339Nano)}
	entry := map[string]any{"seq": strconv.Itoa(index + 9), "kind": "charge", "request_id": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(index + 3)}, 16)), "account_id": "1", "user_id": "7", "charged_at": f.times[index].Add(time.Second).Format(time.RFC3339Nano), "standard_cost": standard, "actual_cost": actual, "price_version": "2"}
	return map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": simulationTestEpoch, "account_id": "1", "resource_id": "cost-simulation-resource", "start": query.Get("start"), "end": query.Get("end"), "interval": "[start,end)", "source_start": f.times[0].Add(-2 * time.Minute).Format(time.RFC3339Nano), "closed_through": f.times[3].Add(time.Second).Format(time.RFC3339Nano), "ledger_revision": "17", "pending_requests": "0", "complete": true, "unit": "USD", "standard_cost": standard, "actual_cost": actual, "prices": []any{price}, "entries": []any{entry}, "next_after": nil, "bound_exhausted": false}
}

func (f *simulationCostFixture) input() EstimateSaveInput {
	input := estimateInput(f.a, f.c)
	input.Windows = []string{"short"}
	return input
}

func TestSimulationCostProducesExactScenarioKnownZeroAndBoundCurrentOnly(t *testing.T) {
	f := simulatedCostFixture(t)
	ctx := context.Background()
	r, err := f.s.SaveEstimateConfiguration(ctx, f.token, f.input())
	if err != nil || r.State != "scenario" || !r.Complete || r.SettlementEligible || r.ModelCalibrated || r.Remaining == nil || r.Remaining.Numerator != "72" || r.Remaining.Denominator != "5" {
		t.Fatalf("complete simulated ledger did not yield the independent scenario: %+v %v", r, err)
	}
	w := r.Windows[0]
	if w.Capacity.Numerator != "24" || w.Capacity.Denominator != "1" || w.CostProof == nil || w.CostProof.Intervals[0].ActualCost.Numerator != "3" || w.CostProof.Intervals[0].ActualCost.Denominator != "2" || w.Period.DurationSeconds != "10800" {
		t.Fatal("standard/actual costs or native period conflated", w)
	}
	encoded, _ := json.Marshal(r)
	for _, hidden := range []string{"cost-simulation-resource", "user_id", "request_id", "adm_sc_"} {
		if bytes.Contains(encoded, []byte(hidden)) {
			t.Fatal("estimate reader received private ledger data", hidden)
		}
	}
	before := f.calls.Load()
	user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "simulated.estimate.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{f.a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.s.GetEstimateCurrent(ctx, reader, f.a.ID)
	if err != nil || read.CurrentState != "scenario" || !reflect.DeepEqual(read.CurrentRemaining, r.Remaining) {
		t.Fatal("estimate-only scope could not read its own qualified result", read, err)
	}
	if _, err = f.s.GetEstimateCurrent(ctx, reader, "AwMDAwMDAwMDAwMDAwMDAw"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("hidden account existence leaked", err)
	}
	if _, err = f.s.auth.UpdateUser(ctx, f.token, auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true, Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{}, ParticipantIDs: []string{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetEstimateRecord(ctx, reader, r.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("grant contraction retained simulated money", err)
	}
	replacement, err := New(f.db, f.s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	current, err := replacement.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil || current.CurrentState != "condition_changed" || current.CurrentRemaining != nil || !reflect.DeepEqual(*current.Latest, r) {
		t.Fatal("unbound replacement exposed current simulated money or rewrote history", current, err)
	}
	bindSimulation(t, replacement, f.c)
	current, err = replacement.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil || current.CurrentState != "scenario" || !reflect.DeepEqual(current.CurrentRemaining, r.Remaining) || f.calls.Load() != before {
		t.Fatal("current read retried the source or lost qualified binding", current, err)
	}
	f.index.Store(3)
	collectObservation(t, f.s, f.db, f.token, f.c, f.a)
	current, err = f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil || current.Latest.Trigger != "observation" || current.CurrentState != "scenario" || current.CurrentRemaining == nil || current.CurrentRemaining.Numerator != "0" {
		t.Fatal("known exhaustion became unknown or producer failed", current, err)
	}
	old, err := f.s.GetEstimateRecord(ctx, f.token, r.ID)
	if err != nil || !reflect.DeepEqual(old, r) {
		t.Fatal("later exhaustion rewrote the frozen estimate", err)
	}
}

func TestSimulationCostRejectsIncompleteAmbiguousChangedAndMixedEvidence(t *testing.T) {
	f := simulatedCostFixture(t)
	ctx := context.Background()
	tx, _ := f.db.Begin(ctx)
	c, err := f.s.config(ctx, tx, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := readManaged(ctx, tx, f.a.ID, c, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	cases := []struct {
		name, state string
		change      func(map[string]any, *http.Request)
	}{
		{"unclosed", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["closed_through"] = f.times[0].Format(time.RFC3339Nano) }},
		{"pending", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["pending_requests"] = "1" }},
		{"partial", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["complete"] = false }},
		{"bound", "evidence_limit", func(p map[string]any, _ *http.Request) { p["bound_exhausted"] = true }},
		{"bound-null-costs", "evidence_limit", func(p map[string]any, _ *http.Request) {
			p["bound_exhausted"], p["complete"], p["standard_cost"], p["actual_cost"] = true, false, nil, nil
		}},
		{"future", "evidence_incomplete", func(p map[string]any, _ *http.Request) {
			p["closed_through"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
		}},
		{"late-source", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["source_start"] = f.times[1].Format(time.RFC3339Nano) }},
		{"epoch", "condition_changed", func(p map[string]any, _ *http.Request) { p["epoch"] = "AwMDAwMDAwMDAwMDAwMDAw" }},
		{"resource", "condition_changed", func(p map[string]any, _ *http.Request) { p["resource_id"] = "different-resource" }},
		{"account", "condition_changed", func(p map[string]any, _ *http.Request) { p["account_id"] = "2" }},
		{"unit", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["unit"] = "native_wallet" }},
		{"total", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["standard_cost"] = "999" }},
		{"actual-total", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["actual_cost"] = "999" }},
		{"cursor", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["next_after"] = "9" }},
		{"refund", "condition_changed", func(p map[string]any, _ *http.Request) { p["entries"].([]any)[0].(map[string]any)["kind"] = "refund" }},
		{"wrong-price", "condition_changed", func(p map[string]any, _ *http.Request) {
			p["entries"].([]any)[0].(map[string]any)["price_version"] = "3"
		}},
		{"deduction", "condition_changed", func(p map[string]any, _ *http.Request) { p["entries"].([]any)[0].(map[string]any)["actual_cost"] = "3" }},
		{"entry-end", "condition_changed", func(p map[string]any, _ *http.Request) {
			p["entries"].([]any)[0].(map[string]any)["charged_at"] = p["end"]
		}},
		{"price-change", "condition_changed", func(p map[string]any, r *http.Request) {
			if r.URL.Query().Get("start") == f.times[1].Format(time.RFC3339Nano) {
				p["prices"].([]any)[0].(map[string]any)["version"] = "3"
			}
		}},
		{"price-effective", "condition_changed", func(p map[string]any, _ *http.Request) {
			p["prices"].([]any)[0].(map[string]any)["effective_at"] = f.times[0].Add(time.Second).Format(time.RFC3339Nano)
		}},
		{"mixed-prices", "condition_changed", func(p map[string]any, _ *http.Request) {
			p["prices"] = append(p["prices"].([]any), p["prices"].([]any)[0])
		}},
		{"fraction-tail", "evidence_incomplete", func(p map[string]any, _ *http.Request) { p["closed_through"] = "2026-11-15T00:00:00.1234567891Z" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			f.change = tc.change
			f.mu.Unlock()
			before := f.calls.Load()
			proof, state := f.s.simulationCosts(ctx, c, a, f.points, f.points[2].Snapshot)
			if proof != nil || state != tc.state || f.calls.Load()-before > 2 {
				t.Fatal("unqualified cost accepted or retried", proof, state, tc.state)
			}
		})
	}
	f.mu.Lock()
	f.change = nil
	f.mu.Unlock()
	for _, raw := range []func([]byte) []byte{
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"complete":true`), []byte(`"complete":true,"complete":true`), 1)
		},
		func(b []byte) []byte { return bytes.Replace(b, []byte(`"protocol":`), []byte(`"Protocol":`), 1) },
		func(b []byte) []byte { return bytes.Replace(b, []byte(`"seq":`), []byte(`"Seq":`), 1) },
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"wallet_multiplier":"0.5"`), []byte(`"wallet_multiplier":"0.5","wallet_multiplier":"0.5"`), 1)
		},
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"bound_exhausted":false`), []byte(`"bound_exhausted":null`), 1)
		},
	} {
		f.mu.Lock()
		f.raw = raw
		f.mu.Unlock()
		if proof, _ := f.s.simulationCosts(ctx, c, a, f.points, f.points[2].Snapshot); proof != nil {
			t.Fatal("ambiguous protocol response accepted")
		}
	}
	if err := f.s.ConfigureSimulation(""); err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	if proof, _ := f.s.simulationCosts(ctx, c, a, f.points, f.points[2].Snapshot); proof != nil || f.calls.Load() != before {
		t.Fatal("unbound native source contacted simulated costs")
	}
}

func TestSimulationCostReadExpiryAndAuditFailureRollBackConfiguration(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(fmt.Sprint("expiry=", expiry), func(t *testing.T) {
			f := simulatedCostFixture(t)
			ctx := context.Background()
			if expiry {
				block, arrive := make(chan struct{}), make(chan struct{}, 2)
				defer close(block)
				f.mu.Lock()
				f.block, f.arrive = block, arrive
				f.mu.Unlock()
				var expires time.Time
				if err := f.db.QueryRow(ctx, "UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '800 milliseconds' RETURNING idle_expires_at").Scan(&expires); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { _, err := f.s.SaveEstimateConfiguration(ctx, f.token, f.input()); done <- err }()
				select {
				case <-arrive:
				case <-time.After(2 * time.Second):
					t.Fatal("cost network wait not observed")
				}
				time.Sleep(time.Until(expires.Add(20 * time.Millisecond)))
				f.mu.Lock()
				f.block = nil
				f.mu.Unlock()
				block <- struct{}{}
				if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatal("expired actor committed a scenario", err)
				}
			} else {
				exec(t, f.db, `CREATE FUNCTION reject_simulated_estimate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejects estimate audit'; END $$; CREATE TRIGGER reject_simulated_estimate BEFORE INSERT ON management_events FOR EACH ROW WHEN (NEW.action LIKE 'estimate.%') EXECUTE FUNCTION reject_simulated_estimate()`)
				if _, err := f.s.SaveEstimateConfiguration(ctx, f.token, f.input()); err == nil {
					t.Fatal("audit failure returned a successful scenario")
				}
			}
			var count int
			if err := f.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM estimate_results)+(SELECT count(*) FROM estimate_config)").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed protected derivation left partial facts", count, err)
			}
		})
	}
}

func TestSimulationCostObjectRejectsMissingExtraNullAndDuplicateFields(t *testing.T) {
	var value struct {
		Value string `json:"value"`
	}
	for _, body := range []string{`{}`, `{"value":null}`, `{"value":"1","extra":true}`, `{"Value":"1"}`, `{"value":"1","value":"1"}`, `{"value":"1"}{}`} {
		if simulationObject(json.RawMessage(body), "value", &value) {
			t.Fatal("invalid protocol object accepted", body)
		}
	}
	if !simulationObject(json.RawMessage(`{"value":"1"}`), "value", &value) {
		t.Fatal("valid strict object rejected")
	}
}

func TestSimulationCostPaginationFreezesHeadersAndValidatesEveryExactEntry(t *testing.T) {
	f := simulatedCostFixture(t)
	ctx := context.Background()
	tx, _ := f.db.Begin(ctx)
	c, _ := f.s.config(ctx, tx, false)
	a, err := readManaged(ctx, tx, f.a.ID, c, false)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fault atomic.Int32
	f.change = func(page map[string]any, r *http.Request) {
		index := byte(1)
		if r.URL.Query().Get("start") == f.times[1].Format(time.RFC3339Nano) {
			index = 2
		}
		from, through := 1, 100
		page["next_after"] = "100"
		if r.URL.Query().Get("after") == "100" {
			from, through, page["next_after"] = 101, 101, nil
		}
		entries := []any{}
		for seq := from; seq <= through; seq++ {
			identity := make([]byte, 16)
			identity[0], identity[15] = index, byte(seq)
			entries = append(entries, map[string]any{"seq": strconv.Itoa(seq), "kind": "charge", "request_id": base64.RawURLEncoding.EncodeToString(identity), "account_id": "1", "user_id": "7", "charged_at": f.times[int(index)-1].Add(time.Second).Format(time.RFC3339Nano), "standard_cost": "0.01", "actual_cost": "0.005", "price_version": "2"})
		}
		page["entries"], page["standard_cost"], page["actual_cost"] = entries, "1.01", "0.505"
		if from == 101 {
			switch fault.Load() {
			case 1:
				page["ledger_revision"] = "18"
			case 2:
				entries[0].(map[string]any)["seq"] = "100"
			case 3:
				page["next_after"] = "101"
			}
		}
	}
	proof, state := f.s.simulationCosts(ctx, c, a, f.points, f.points[2].Snapshot)
	if proof == nil || state != "" || len(proof.Intervals) != 2 || proof.Intervals[0].EntryCount != 101 || proof.Intervals[0].StandardCost.Numerator != "101" || proof.Intervals[0].StandardCost.Denominator != "100" || proof.Intervals[0].ActualCost.Denominator != "200" {
		t.Fatal("numeric page/entry/exact totals were truncated", proof, state)
	}
	for mode := int32(1); mode <= 3; mode++ {
		fault.Store(mode)
		before := f.calls.Load()
		if proof, _ := f.s.simulationCosts(ctx, c, a, f.points, f.points[2].Snapshot); proof != nil || f.calls.Load()-before != 2 {
			t.Fatal("unstable later page became success or retried", mode, proof)
		}
	}
}

func TestSimulationCostStandardScenarioNeverSubstitutesIndependentActualCharges(t *testing.T) {
	for _, zeroStandard := range []bool{false, true} {
		t.Run(fmt.Sprint("zero-standard=", zeroStandard), func(t *testing.T) {
			f := simulatedCostFixture(t)
			f.change = func(page map[string]any, _ *http.Request) {
				page["prices"].([]any)[0].(map[string]any)["wallet_multiplier"] = "0"
				page["actual_cost"] = "0"
				entry := page["entries"].([]any)[0].(map[string]any)
				entry["actual_cost"] = "0"
				if zeroStandard {
					page["standard_cost"], entry["standard_cost"] = "0", "0"
				}
			}
			r, err := f.s.SaveEstimateConfiguration(context.Background(), f.token, f.input())
			if err != nil || !r.Complete || r.SettlementEligible || r.Windows[0].CostProof.Intervals[0].ActualCost.Numerator != "0" {
				t.Fatal("independent zero actual charge was lost", r, err)
			}
			if zeroStandard {
				if r.State != "non_identifying_delta" || r.Remaining != nil {
					t.Fatal("zero cost invented capacity", r)
				}
			} else if r.State != "scenario" || r.Remaining.Numerator != "72" || r.Remaining.Denominator != "5" {
				t.Fatal("actual charge replaced standard costs", r)
			}
		})
	}
}

func TestSimulationCostRequiredOverlappingWindowsUseBottleneckWithoutSumming(t *testing.T) {
	f := simulatedCostFixture(t)
	input := f.input()
	input.Windows = []string{"short", "seven_day"}
	r, err := f.s.SaveEstimateConfiguration(context.Background(), f.token, input)
	if err != nil || r.State != "scenario" || !r.Complete || r.Remaining.Numerator != "12" || r.Remaining.Denominator != "5" || len(r.Windows) != 2 {
		t.Fatal("required overlapping windows were summed or partially accepted", r, err)
	}
	if estimateKind(t, r, "seven_day").Period.DurationSeconds != "604800" || estimateKind(t, r, "short").Remaining.Numerator != "72" {
		t.Fatal("window identity or independent remaining lost", r)
	}
}

func TestSimulationCostLatestFailedQuotaInvalidatesCurrentWithoutRewritingHistory(t *testing.T) {
	f := simulatedCostFixture(t)
	ctx := context.Background()
	r, err := f.s.SaveEstimateConfiguration(ctx, f.token, f.input())
	if err != nil || r.State != "scenario" {
		t.Fatal(r, err)
	}
	f.badQuota.Store(true)
	advanceCollectionFixture(t, f.db)
	queueAccount(t, f.s, f.token, f.c, f.a.ID)
	processQueued(t, f.s)
	current, err := f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil || current.CurrentState != "evidence_incomplete" || current.CurrentRemaining != nil || !reflect.DeepEqual(*current.Latest, r) {
		t.Fatal("degraded source hidden by an old successful amount", current, err)
	}
	if run := queueAccount(t, f.s, f.token, f.c, f.a.ID); run.Counts.Skipped != "1" {
		t.Fatal("failed read cooldown was not retained", run)
	}
	current, err = f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil || current.CurrentState != "evidence_incomplete" || current.CurrentRemaining != nil {
		t.Fatal("skipped cooldown hid the actual source failure", current, err)
	}
	f.badQuota.Store(false)
	f.index.Store(3)
	collectObservation(t, f.s, f.db, f.token, f.c, f.a)
	current, err = f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil || current.CurrentState != "scenario" || current.CurrentRemaining.Numerator != "0" {
		t.Fatal("fresh qualified source did not restore current state", current, err)
	}
}
