package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestRetiredTemporaryRoundsPreserveReadOnlyHistory(t *testing.T) {
	ctx := context.Background()
	s, db, token, config, calls := estimateFixture(t)
	account := passiveManaged(t, s, token, config, 1)
	var actor, revision string
	if err := db.QueryRow(ctx, `SELECT id,revision::text FROM auth_users WHERE role='admin' AND enabled LIMIT 1`).Scan(&actor, &revision); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	id := func(n byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 16)) }
	period := ObservationPeriod{Kind: "short", StartAt: at, EndAt: at.Add(time.Hour), DurationSeconds: "3600", Basis: "native_reset_and_duration"}
	scope := TemporaryScope{Simulation: archivedSimulation, SourceID: account.SourceID, ConnectionRevision: config.Revision,
		Accounts: []TemporaryAccount{{AccountID: account.ID, AccountRevision: account.Revision, Periods: []ObservationPeriod{period},
			SettlementPeriod: period, Members: []TemporaryMember{{ParticipantID: id(90), BindingID: "1", UserID: "21"}}}}}
	frozen, err := json.Marshal(temporarySaved{Mode: "carry", Scope: scope, Caps: []TemporaryCap{}, AcknowledgedParticipants: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		start := at.Add(time.Duration(i) * time.Minute)
		round := id(byte(i + 40))
		exec(t, db, `INSERT INTO temporary_rounds(id,actor_id,actor_revision,request_id,request_digest,started_at,deadline,snapshot)
		 VALUES($1,$2,$3,$1,$4,$5,$6,$7)`, round, actor, revision, strings.Repeat("a", 64), start, start.Add(2*time.Hour), frozen)
	}
	for _, amount := range []string{"-0.00000002", "-0.00000001"} {
		receipt := TemporaryRightsReceipt{TemporaryRightsChange: TemporaryRightsChange{
			RoundID: id(40), AccountID: account.ID, ParticipantID: id(90), BindingID: "1", Period: period, After: amount,
		}, RecordedAt: at}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, db, `INSERT INTO temporary_events(round_id, account_id, kind, request_key, recorded_at, snapshot)
		 VALUES($1,$2,'rights',$3,$4,$5)`, id(40), account.ID, amount, at, raw)
	}
	originalCalls := calls.Load()
	after := ""
	for _, round := range []string{id(42), id(41), id(40)} {
		page, err := s.ListTemporaryHistory(ctx, token, after, 1)
		if err != nil || len(page.Rounds) != 1 || page.Rounds[0].ID != round {
			t.Fatal(page, err)
		}
		if page.NextAfter != nil {
			after = *page.NextAfter
		} else if round != id(40) {
			t.Fatal("cursor ended early", page)
		}
	}
	before, err := s.GetTemporaryRound(ctx, token, id(40))
	if err != nil || before.Round.Exit != nil {
		t.Fatal(before, err)
	}
	rights, err := s.GetTemporaryRights(ctx, token, id(40), account.ID)
	if err != nil || rights.Period == nil || rights.Adjustments[id(90)] != "-0.00000001" {
		t.Fatal(rights, err)
	}
	reader, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "temporary-history-reader", Password: "ReaderPassword123", Enabled: true,
		Grants: auth.Grants{Features: []string{"temporary"}, AccountIDs: []string{account.ID}, ParticipantIDs: []string{id(90)}}})
	if err != nil {
		t.Fatal(err)
	}
	_, ordinary, err := s.auth.Login(ctx, reader.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetTemporaryRound(ctx, ordinary, id(40)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("global archived history leaked", err)
	}
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	handler := TemporaryHandler{Service: s, Boundary: boundary}
	for _, action := range []string{"status", "preflight", "start", "stop", "rights-change", "reminders"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://127.0.0.1:3000/api/v1/temporary/"+action, strings.NewReader("{}"))
		r.Header.Set("Origin", "http://127.0.0.1:3000")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: token})
		handler.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = s.collectorCycle(ctx, conn); err != nil {
		t.Fatal(err)
	}
	latest, err := s.GetTemporaryRound(ctx, token, id(40))
	if err != nil || !reflect.DeepEqual(before, latest) || calls.Load() != originalCalls {
		t.Fatal("legacy data changed or reached upstream", latest, err)
	}
}
