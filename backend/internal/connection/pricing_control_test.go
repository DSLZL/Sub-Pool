package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

const controlledPricingGroup = `{"id":1,"name":"Owned group","platform":"openai","status":"active","account_count":2,"created_at":"2026-11-01T00:00:00Z","updated_at":"2026-11-01T01:00:00Z","rate_multiplier":1.25,"force_openai_fast":false,"free_openai_fast":false,"long_context_pricing_enabled":true,"model_pricing":[{"platform":"openai","models":["model-a*"],"billing_mode":"token","input_price":0.1,"output_price":2,"cache_write_price":null,"cache_write_1h_price":null,"cache_read_price":null,"image_input_price":null,"image_output_price":null,"per_request_price":null,"fast_multiplier":2,"flex_multiplier":0.5,"reasoning_effort_multipliers":{"high":2},"intervals":[{"min_tokens":0,"max_tokens":1000,"input_multiplier":1.5}],"time_pricing":{"timezone":"UTC","weekdays_only":true,"periods":[{"start_time":"08:00","end_time":"09:00","multiplier":1.5}]}}],"credentials":{"secret":"PRIVATE_NATIVE_BODY"}}`

type pricingFixture struct {
	s               *Service
	db              *pgxpool.Pool
	token           string
	c               Configuration
	mu              sync.Mutex
	groups          map[string]map[string]json.RawMessage
	mode, writeMode string
	reads, writes   int
	patches         []map[string]json.RawMessage
	onRead          func(int)
	onWrite         func()
}

func newPricingFixture(t *testing.T) *pricingFixture {
	t.Helper()
	s, db, token := setup(t)
	f := &pricingFixture{s: s, db: db, token: token, mode: "standard", groups: map[string]map[string]json.RawMessage{}}
	for _, id := range []string{"1", "2"} {
		var group map[string]json.RawMessage
		_ = json.Unmarshal([]byte(controlledPricingGroup), &group)
		group["id"] = json.RawMessage(id)
		f.groups[id] = group
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer controlled-pricing-jwt" || r.Header.Get("x-api-key") != "" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
			t.Error("pricing forwarded unexpected credentials or query")
		}
		f.mu.Lock()
		if r.URL.Path == "/api/v1/auth/me" && r.Method == http.MethodGet {
			mode := f.mode
			f.mu.Unlock()
			respond(w, map[string]any{"role": "admin", "run_mode": mode})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/groups/")
		group := f.groups[id]
		if group == nil {
			f.mu.Unlock()
			t.Error("unexpected native path", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		writeMode := f.writeMode
		var hook func()
		switch r.Method {
		case http.MethodGet:
			f.reads++
			reads := f.reads
			onRead := f.onRead
			if onRead != nil {
				hook = func() { onRead(reads) }
			}
		case http.MethodPut:
			f.writes++
			var patch map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&patch) != nil || len(patch) == 0 {
				t.Error("empty or malformed native pricing patch")
			}
			for key := range patch {
				if key != "model_pricing" && key != "force_openai_fast" && key != "free_openai_fast" && key != "long_context_pricing_enabled" {
					t.Error("pricing touched unrelated native field", key)
				}
			}
			f.patches = append(f.patches, patch)
			if writeMode != "denied" && writeMode != "ignore" {
				for key, value := range patch {
					group[key] = value
				}
				var timestamp time.Time
				_ = json.Unmarshal(group["updated_at"], &timestamp)
				group["updated_at"], _ = json.Marshal(timestamp.Add(time.Microsecond))
			}
			hook = f.onWrite
		default:
			f.mu.Unlock()
			t.Error("unexpected native method", r.Method)
			w.WriteHeader(405)
			return
		}
		body, _ := json.Marshal(group)
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		if r.Method == http.MethodPut {
			switch writeMode {
			case "denied":
				w.WriteHeader(403)
				return
			case "bad":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				return
			case "http500":
				w.WriteHeader(500)
				return
			case "timeout":
				<-r.Context().Done()
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(append([]byte(`{"code":0,"data":`), body...), '}'))
	}))
	t.Cleanup(server.Close)
	c, err := s.Get(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Save(context.Background(), token, SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "Bearer controlled-pricing-jwt", Confirmation: "更换地址"})
	if err != nil {
		t.Fatal(err)
	}
	f.c = saved.Configuration
	if err = s.ConfigurePricingControl(*f.c.SourceID + ":" + f.c.Revision); err != nil {
		t.Fatal(err)
	}
	return f
}

func pricingRequestID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func pricingInput(t *testing.T, f *pricingFixture, group string, changes ...PricingChange) PricingApplyInput {
	t.Helper()
	in := PricingControlInput{Revision: f.c.Revision, SourceID: *f.c.SourceID, GroupID: group}
	control, err := f.s.GetPricingControl(context.Background(), f.token, in)
	if err != nil || !control.CanApply || control.Fingerprint == nil {
		t.Fatal("controlled pricing unavailable", control, err)
	}
	return PricingApplyInput{PricingControlInput: in, RequestID: pricingRequestID(t), ExpectedFingerprint: *control.Fingerprint, AcknowledgeSharedImpact: true, Changes: changes}
}
func fastPricing() PricingChange {
	force, free := true, true
	return PricingChange{Kind: "fast", ForceFast: &force, FreeFast: &free}
}
func applyPricing(t *testing.T, f *pricingFixture, group string) PricingOperation {
	t.Helper()
	p, err := f.s.ApplyPricing(context.Background(), f.token, pricingInput(t, f, group, fastPricing()))
	if err != nil || p.State != "applied" {
		t.Fatal("pricing apply not confirmed", p, err)
	}
	return p
}

func TestPricingControlPreservesComplexConfigurationAndRejectsLossyNumbers(t *testing.T) {
	n, ok := inspectPricingNative(json.RawMessage(controlledPricingGroup), "1", "controlled-pricing-jwt")
	if !ok {
		t.Fatal("fixture not trusted")
	}
	if string(n.Values["long_context_pricing_enabled"]) != "true" {
		t.Fatal("canonicalization changed true to false")
	}
	for _, value := range []string{"0", "0.1", "0.00000001", "123.25"} {
		if !nativePricingNumber(value) {
			t.Fatal("exact native value rejected", value)
		}
	}
	for _, value := range []string{"0.100000000000000001", "-1", "NaN", "1e-3", "01", "1.0"} {
		if nativePricingNumber(value) {
			t.Fatal("lossy or noncanonical native value accepted", value)
		}
	}
	for _, raw := range []string{`{"models|billing_mode":"bad"}`, `{"credentials":"bad"}`, `{"models":[],"models":[]}`} {
		if _, ok := canonicalPricing(json.RawMessage(raw), ""); ok {
			t.Fatal("unqualified native field accepted", raw)
		}
	}
	index := 0
	value := "0.2"
	change := PricingChange{Kind: "model_price", RuleIndex: &index, PriceField: "input_price", Value: &value}
	target, patch, err := pricingTarget(n, []PricingChange{change})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(patch, &fields)
	if len(fields) != 1 || !bytes.Contains(fields["model_pricing"], []byte(`"weekdays_only":true`)) || !bytes.Contains(fields["model_pricing"], []byte(`"reasoning_effort_multipliers":{"high":2}`)) || pricingValuesEqual(n, target) {
		t.Fatal("scalar edit damaged complex rules", string(patch))
	}
	if validPricingChanges([]PricingChange{change, change}) || validPricingChanges([]PricingChange{{Kind: "fast"}}) {
		t.Fatal("duplicate/incomplete changes accepted")
	}
}

func TestPricingControlAppliesDeduplicatesRevertsAndKeepsImmutableHistory(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	enabled := false
	index := 0
	value := "0.2"
	in := pricingInput(t, f, "1", fastPricing(), PricingChange{Kind: "long_context", Enabled: &enabled}, PricingChange{Kind: "model_price", RuleIndex: &index, PriceField: "input_price", Value: &value})
	p, err := f.s.ApplyPricing(ctx, f.token, in)
	if err != nil || p.State != "applied" || p.AfterFingerprint == nil || *p.AfterFingerprint == p.BeforeFingerprint {
		t.Fatal("native write/readback not confirmed", p, err)
	}
	duplicate, err := f.s.ApplyPricing(ctx, f.token, in)
	if err != nil || !reflect.DeepEqual(duplicate, p) {
		t.Fatal("request replay changed receipt", duplicate, err)
	}
	in.Changes = []PricingChange{fastPricing()}
	if _, err = f.s.ApplyPricing(ctx, f.token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("same key different input accepted", err)
	}
	restarted, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.ConfigurePricingControl(*f.c.SourceID + ":" + f.c.Revision); err != nil {
		t.Fatal(err)
	}
	f.s = restarted
	control, err := f.s.GetPricingControl(ctx, f.token, in.PricingControlInput)
	if err != nil || control.CanApply || control.OwnedChangeID == nil || *control.OwnedChangeID != p.ID {
		t.Fatal("open owned operation lost", control, err)
	}
	reverted, err := f.s.RevertPricing(ctx, f.token, PricingOperationInput{ID: p.ID, Revision: p.Revision, AcknowledgeSharedImpact: true})
	if err != nil || reverted.State != "reverted" {
		t.Fatal("owned change not reverted", reverted, err)
	}
	if again, err := f.s.RevertPricing(ctx, f.token, PricingOperationInput{ID: p.ID, Revision: reverted.Revision, AcknowledgeSharedImpact: true}); err != nil || again.State != "reverted" {
		t.Fatal("revert receipt not idempotent", err)
	}
	f.mu.Lock()
	body, _ := json.Marshal(f.groups["1"])
	writes := f.writes
	patches := f.patches
	f.mu.Unlock()
	before, _ := inspectPricingNative(json.RawMessage(controlledPricingGroup), "1", "")
	after, ok := inspectPricingNative(body, "1", "")
	if !ok || !pricingValuesEqual(before, after) || writes != 2 || len(patches[0]) != 4 || len(patches[1]) != 4 {
		t.Fatal("revert changed unrelated values or replayed a write", writes)
	}
	var events, audits int
	var snapshot []byte
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM pricing_events),(SELECT count(*) FROM management_events WHERE action LIKE 'pricing.%'),(SELECT snapshot FROM pricing_events ORDER BY pricing_events.id LIMIT 1)`).Scan(&events, &audits, &snapshot); err != nil || events != 4 || audits != 4 || !bytes.Contains(snapshot, []byte(`"state": "applying"`)) {
		t.Fatal("history/audit not immutable and atomic", events, audits, err)
	}
	if bytes.Contains(snapshot, []byte("PRIVATE_NATIVE_BODY")) || bytes.Contains(snapshot, []byte("controlled-pricing-jwt")) {
		t.Fatal("native secret leaked into event")
	}
}

func TestPricingControlStopsExternalChangesAndNeverRetriesUnknownWrites(t *testing.T) {
	for _, scenario := range []string{"external_version", "external_value", "bad", "http500", "ignore", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPricingFixture(t)
			ctx := context.Background()
			if strings.HasPrefix(scenario, "external") {
				p := applyPricing(t, f, "1")
				f.mu.Lock()
				f.groups["1"]["updated_at"] = json.RawMessage(`"2026-11-02T00:00:00Z"`)
				if scenario == "external_value" {
					f.groups["1"]["free_openai_fast"] = json.RawMessage(`false`)
				}
				f.mu.Unlock()
				result, err := f.s.RevertPricing(ctx, f.token, PricingOperationInput{ID: p.ID, Revision: p.Revision, AcknowledgeSharedImpact: true})
				if err != nil || result.State != "needs_review" || result.Reason != "PRECONDITION_CHANGED" {
					t.Fatal("external version overwritten", result, err)
				}
				p = result
				if _, err = f.s.RetryPricing(ctx, f.token, PricingRetryInput{ID: p.ID, Revision: p.Revision, RequestID: pricingRequestID(t), AcknowledgeSharedImpact: true}); !errors.Is(err, auth.ErrConflict) {
					t.Fatal("unknown revert replayed", err)
				}
				closed, err := f.s.ReviewPricing(ctx, f.token, PricingReviewInput{ID: p.ID, Revision: p.Revision, AcknowledgeResultUnknown: true})
				if err != nil || closed.State != "reviewed" || closed.Reason != "MANUALLY_REVIEWED_RESULT_UNKNOWN" {
					t.Fatal("review invented native success", closed, err)
				}
			} else {
				in := pricingInput(t, f, "1", fastPricing())
				f.mu.Lock()
				f.writeMode = scenario
				f.mu.Unlock()
				f.s.upstream.http.Timeout = 100 * time.Millisecond
				p, err := f.s.ApplyPricing(ctx, f.token, in)
				if err != nil || p.State != "needs_review" || p.Reason != "WRITE_RESULT_UNKNOWN" {
					t.Fatal("unknown write reported definite outcome", p, err)
				}
				if _, err = f.s.RetryPricing(ctx, f.token, PricingRetryInput{ID: p.ID, Revision: p.Revision, RequestID: pricingRequestID(t), AcknowledgeSharedImpact: true}); !errors.Is(err, auth.ErrConflict) {
					t.Fatal("unknown apply replayed", err)
				}
				if err = f.s.recoverPricingIntents(ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 {
				t.Fatal("unsafe extra native write", f.writes)
			}
		})
	}
}

func TestPricingControlKeepsGroupsIndependentAndRetriesOnlyKnownRejections(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	in := pricingInput(t, f, "1", fastPricing())
	f.mu.Lock()
	f.writeMode = "denied"
	f.mu.Unlock()
	failed, err := f.s.ApplyPricing(ctx, f.token, in)
	if err != nil || failed.State != "rejected" {
		t.Fatal(failed, err)
	}
	f.mu.Lock()
	f.writeMode = ""
	f.mu.Unlock()
	good := applyPricing(t, f, "2")
	retry := PricingRetryInput{ID: failed.ID, Revision: failed.Revision, RequestID: pricingRequestID(t), AcknowledgeSharedImpact: true}
	retried, err := f.s.RetryPricing(ctx, f.token, retry)
	if err != nil || retried.State != "applied" || retried.ID == failed.ID || retried.GroupID != "1" {
		t.Fatal("known rejection retry failed", retried, err)
	}
	again, err := f.s.RetryPricing(ctx, f.token, retry)
	if err != nil || again.ID != retried.ID {
		t.Fatal("retry confirmation replayed write", again, err)
	}
	history, err := f.s.ListPricingHistory(ctx, f.token, PricingHistoryInput{SourceID: *f.c.SourceID, After: "0", Limit: 10})
	if err != nil || len(history.Operations) != 3 || history.Operations[0].State != "rejected" || history.Operations[1].ID != good.ID {
		t.Fatal("partial results were collapsed", history, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 3 {
		t.Fatal("successful group replayed", f.writes)
	}
}

func TestPricingControlAuditFailureKeepsIntentAndRecoveryNeverReplays(t *testing.T) {
	for _, stage := range []string{"intent", "result"} {
		t.Run(stage, func(t *testing.T) {
			f := newPricingFixture(t)
			ctx := context.Background()
			in := pricingInput(t, f, "1", fastPricing())
			action := "pricing.applying"
			if stage == "result" {
				action = "pricing.applied"
			}
			exec(t, f.db, `CREATE FUNCTION reject_pricing_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+action+`' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$`)
			exec(t, f.db, `CREATE TRIGGER reject_pricing_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_pricing_audit()`)
			if _, err := f.s.ApplyPricing(ctx, f.token, in); err == nil {
				t.Fatal("audit failure reported success")
			}
			exec(t, f.db, `DROP TRIGGER reject_pricing_audit ON management_events`)
			replacement, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
			if err != nil {
				t.Fatal(err)
			}
			if err = replacement.recoverPricingIntents(ctx); err != nil {
				t.Fatal(err)
			}
			history, err := replacement.ListPricingHistory(ctx, f.token, PricingHistoryInput{SourceID: *f.c.SourceID, After: "0", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if stage == "intent" {
				if f.writes != 0 || len(history.Operations) != 0 {
					t.Fatal("unpersisted intent reached upstream")
				}
			} else if f.writes != 1 || len(history.Operations) != 1 || history.Operations[0].State != "needs_review" || history.Operations[0].Reason != "PROCESS_INTERRUPTED_RESULT_UNKNOWN" {
				t.Fatal("recovery replayed or lost uncertain result", history, f.writes)
			}
		})
	}
}

func TestPricingControlSerializesNativeWritesAcrossServiceInstances(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	in := pricingInput(t, f, "1", fastPricing())
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.mu.Lock()
	f.onWrite = func() { close(entered); <-release }
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := f.s.ApplyPricing(ctx, f.token, in); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("first write did not enter native", err)
	case <-time.After(5 * time.Second):
		t.Fatal("native write did not start")
	}
	other, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	_ = other.ConfigurePricingControl(*f.c.SourceID + ":" + f.c.Revision)
	if _, err = other.ApplyPricing(ctx, f.token, in); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("concurrent controller entered native", err)
	}
	if err = other.recoverPricingIntents(ctx); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 {
		t.Fatal("duplicate native writes", f.writes)
	}
}

func TestPricingControlRechecksSessionBeforeNativeWriteAndNeverResumesExpiredActor(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	in := pricingInput(t, f, "1", fastPricing())
	f.mu.Lock()
	f.onRead = func(reads int) {
		if reads == 3 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	f.mu.Unlock()
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '150 milliseconds'`)
	if _, err := f.s.ApplyPricing(ctx, f.token, in); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired session crossed native boundary", err)
	}
	if err := f.s.recoverPricingIntents(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 0 {
		t.Fatal("expired actor wrote native pricing")
	}
}

func TestPricingControlHistoryPagesNumericallyAcrossDecimalBoundary(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		p := applyPricing(t, f, "1")
		if _, err := f.s.RevertPricing(ctx, f.token, PricingOperationInput{ID: p.ID, Revision: p.Revision, AcknowledgeSharedImpact: true}); err != nil {
			t.Fatal(err)
		}
	}
	after := "0"
	seen := 0
	for {
		page, err := f.s.ListPricingHistory(ctx, f.token, PricingHistoryInput{SourceID: *f.c.SourceID, GroupID: "1", After: after, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Operations {
			seen++
			if p.ID != strconv.Itoa(seen) {
				t.Fatal("history used lexical ID order", p.ID, seen)
			}
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if seen != 12 {
		t.Fatal("history lost operations", seen)
	}
}

func TestPricingControlRequiresCurrentBindingModeIdentityAndExactPrecondition(t *testing.T) {
	for _, scenario := range []string{"binding", "simple", "missing_field", "identity", "external_version", "source_revision", "ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPricingFixture(t)
			in := pricingInput(t, f, "1", fastPricing())
			switch scenario {
			case "binding":
				if err := f.s.ConfigurePricingControl(""); err != nil {
					t.Fatal(err)
				}
			case "source_revision":
				exec(t, f.db, `UPDATE connection_config SET revision=revision+1`)
			case "ordinary":
				exec(t, f.db, `UPDATE auth_users SET role='user' WHERE username='admin'`)
			default:
				f.mu.Lock()
				switch scenario {
				case "simple":
					f.mode = "simple"
				case "missing_field":
					delete(f.groups["1"], "model_pricing")
				case "identity":
					f.groups["1"]["created_at"] = json.RawMessage(`"2026-10-01T00:00:00Z"`)
				case "external_version":
					f.groups["1"]["updated_at"] = json.RawMessage(`"2026-11-02T00:00:00Z"`)
				}
				f.mu.Unlock()
			}
			_, err := f.s.ApplyPricing(context.Background(), f.token, in)
			want := auth.ErrConflict
			if scenario == "ordinary" {
				want = auth.ErrForbidden
			}
			if !errors.Is(err, want) {
				t.Fatal("unqualified write accepted", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 0 {
				t.Fatal("failed precondition reached native write")
			}
		})
	}
}

func TestPricingControlExpiryAfterNativeWriteLeavesDurableUnknownIntent(t *testing.T) {
	f := newPricingFixture(t)
	ctx := context.Background()
	in := pricingInput(t, f, "1", fastPricing())
	f.mu.Lock()
	f.onWrite = func() { time.Sleep(250 * time.Millisecond) }
	f.mu.Unlock()
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '150 milliseconds'`)
	if _, err := f.s.ApplyPricing(ctx, f.token, in); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired actor committed successful receipt", err)
	}
	if err := f.s.recoverPricingIntents(ctx); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.db.QueryRow(ctx, `SELECT state,reason FROM pricing_changes`).Scan(&state, &reason); err != nil || state != "needs_review" || reason != "PROCESS_INTERRUPTED_RESULT_UNKNOWN" {
		t.Fatal("uncertain intent lost", state, reason, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 || string(f.groups["1"]["free_openai_fast"]) != "true" {
		t.Fatal("native result replayed or not simulated")
	}
}
