package connection

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestPoolJournalAndCaptureEvidenceFailureRollBackEntireState(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "One")
	b := adoptManaged(t, s, token, c, 2, "Two")
	p := createParticipant(t, s, token, c, 21, "Member")
	first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"100"})})
	exec(t, db, `CREATE FUNCTION reject_pool_journal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='pool' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$`)
	exec(t, db, `CREATE TRIGGER reject_pool_journal BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_pool_journal()`)
	for _, operation := range []string{"create", "update", "disable"} {
		var result PoolContract
		var err error
		switch operation {
		case "create":
			result, err = s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{b}, []Participant{p}, []string{"50"})})
		case "update":
			result, err = s.SavePool(ctx, token, PoolUpdate{ID: first.PoolID, Revision: first.Revision, PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{p}, []string{"50"})})
		default:
			result, err = s.DisablePool(ctx, token, first.PoolID, first.Revision)
		}
		if err == nil || result.PoolID != "" {
			t.Fatal("journal failure reported successful pool", operation)
		}
	}
	var intact bool
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM pools)=1 AND (SELECT count(*) FROM pool_contracts)=1 AND (SELECT count(*) FROM pool_events)=1 AND (SELECT count(*) FROM pool_contract_accounts WHERE current)=1 AND (SELECT revision=1 AND enabled FROM pools) AND (SELECT ended_at IS NULL FROM pool_contracts)`).Scan(&intact); err != nil || !intact {
		t.Fatal("journal failure partially changed reservations/history", err)
	}
	exec(t, db, "DROP TRIGGER reject_pool_journal ON management_events")
	_ = savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{b}, nil, nil)})
	f.logs[1] = []map[string]any{usageRow(1, first.EffectiveAt.Format(time.RFC3339Nano))}
	exec(t, db, `CREATE FUNCTION reject_pool_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`)
	exec(t, db, `CREATE TRIGGER reject_pool_evidence BEFORE UPDATE OF pool_contract_id,pool_basis ON usage_attributions FOR EACH ROW EXECUTE FUNCTION reject_pool_evidence()`)
	input := UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: first.EffectiveAt.Add(-time.Minute).Format(time.RFC3339Nano), EndAt: first.EffectiveAt.Add(time.Hour).Format(time.RFC3339Nano)}
	result, err := s.CaptureUsage(ctx, token, input)
	if err == nil || result.ID != "" {
		t.Fatal("failed contract evidence returned capture success")
	}
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_captures)=0 AND (SELECT count(*) FROM usage_capture_facts)=0 AND (SELECT count(*) FROM usage_attributions)=0 AND (SELECT count(*) FROM usage_events)=0`).Scan(&intact); err != nil || !intact {
		t.Fatal("evidence failure committed partial facts", err)
	}
	exec(t, db, "DROP TRIGGER reject_pool_evidence ON usage_attributions")
	captured, err := s.CaptureUsage(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := s.GetPoolUsageEvidence(ctx, token, captured.ID, "0", 100)
	if err != nil || len(evidence.Records) != 1 || evidence.Records[0].ContractID == nil || *evidence.Records[0].ContractID != first.ID {
		t.Fatal("valid capture did not persist original contract", err)
	}
}

func TestPoolsHTTPStrictNestedMembersAndCurrentBoundary(t *testing.T) {
	s, _, token := setup(t)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := &PoolsHandler{Service: s, Boundary: boundary}
	origin := "http://127.0.0.1:3000"
	valid := `{"after":"","search":"","limit":100}`
	nested := `{"source_id":"AAAAAAAAAAAAAAAAAAAAAA","connection_revision":"1","name":"Pool","enabled":true,"accounts":[{"id":"AAAAAAAAAAAAAAAAAAAAAA","revision":"1"}],"participants":[]}`
	for _, tc := range []struct {
		method, action, body, origin, cookie string
		status                               int
	}{
		{"GET", "list", "", "", token, 405},
		{"POST", "list", valid, "", token, 403},
		{"POST", "list", valid, origin, "", 401},
		{"POST", "list", strings.Replace(valid, `"limit":100`, `"limit":null`, 1), origin, token, 400},
		{"POST", "list", strings.Replace(valid, `"limit":100`, `"limit":100,"limit":1`, 1), origin, token, 400},
		{"POST", "list", strings.Replace(valid, `"after"`, `"After"`, 1), origin, token, 400},
		{"POST", "list?hidden=%GG", valid, origin, token, 400},
		{"POST", "list", valid, origin, token + "; sub_pool_dev_session=" + token, 401},
		{"POST", "list", valid, origin, token, 200},
		{"POST", "update", strings.Replace(nested, `{"source_id"`, `{"id":"","revision":"","source_id"`, 1), origin, token, 400},
		{"POST", "create", strings.Replace(nested, `"revision":"1"}`, `"revision":"1","revision":"2"}`, 1), origin, token, 400},
		{"POST", "create", strings.Replace(nested, `"revision":"1"}`, `"revision":"1","unknown":true}`, 1), origin, token, 400},
		{"POST", "create", strings.Replace(nested, `"participants":[]`, `"participants":[{"id":"AAAAAAAAAAAAAAAAAAAAAA","revision":"1","share_percent":"25","share_percent":"50"}]`, 1), origin, token, 400},
		{"POST", "create", strings.Replace(nested, `"participants":[]`, `"participants":[{"id":"AAAAAAAAAAAAAAAAAAAAAA","revision":"1","share_percent":null}]`, 1), origin, token, 400},
		{"POST", "history", `{"id":"AAAAAAAAAAAAAAAAAAAAAA","after":"bad","limit":100}`, origin, token, 400},
		{"POST", "usage-evidence", `{"capture_id":"1","after":"0","limit":0}`, origin, token, 400},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/pools/"+tc.action, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.cookie != "" {
			r.Header.Set("Cookie", "sub_pool_dev_session="+tc.cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s %s got %d want %d: %s", tc.method, tc.action, w.Code, tc.status, w.Body.String())
		}
	}
}
