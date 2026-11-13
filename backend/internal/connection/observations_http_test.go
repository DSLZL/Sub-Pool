package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestObservationStrictHTTPBoundaryAndCurrentAuthority(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	item := collectObservation(t, s, db, token, c, a)
	boundary, err := auth.NewHandler(s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &ObservationsHandler{Service: s, Boundary: boundary}
	request := func(method, path, body, session, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:3000/api/v1/observations/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: session})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("observation response changed cookies or allowed caching", w.Header())
		}
		return w
	}
	queryJSON, _ := json.Marshal(observationQuery())
	reviewJSON, _ := json.Marshal(ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: item.ID, Excluded: true, Reason: "Local review"})
	for _, test := range []struct {
		name, method, path, body, session, origin string
		status                                    int
	}{
		{"list", "POST", "list", string(queryJSON), token, "http://127.0.0.1:3000", 200},
		{"state", "POST", "state", `{"account_id":"` + a.ID + `"}`, token, "http://127.0.0.1:3000", 200},
		{"estimate-link", "POST", "estimate-link", `{"id":"` + item.ID + `"}`, token, "http://127.0.0.1:3000", 200},
		{"estimate-link-method", "GET", "estimate-link", `{"id":"` + item.ID + `"}`, token, "http://127.0.0.1:3000", 405},
		{"estimate-link-extra", "POST", "estimate-link", `{"id":"` + item.ID + `","account_id":"` + a.ID + `"}`, token, "http://127.0.0.1:3000", 400},
		{"estimate-link-duplicate", "POST", "estimate-link", `{"id":"1","id":"2"}`, token, "http://127.0.0.1:3000", 400},
		{"estimate-link-null", "POST", "estimate-link", `{"id":null}`, token, "http://127.0.0.1:3000", 400},
		{"usage-evidence", "POST", "usage-evidence", `{"id":"` + item.ID + `"}`, token, "http://127.0.0.1:3000", 200},
		{"usage-evidence-method", "GET", "usage-evidence", `{}`, token, "http://127.0.0.1:3000", 405},
		{"usage-evidence-extra", "POST", "usage-evidence", `{"id":"` + item.ID + `","account_id":"` + a.ID + `"}`, token, "http://127.0.0.1:3000", 400},
		{"usage-evidence-duplicate", "POST", "usage-evidence", `{"id":"1","id":"2"}`, token, "http://127.0.0.1:3000", 400},
		{"usage-evidence-null", "POST", "usage-evidence", `{"id":null}`, token, "http://127.0.0.1:3000", 400},
		{"missing-cookie", "POST", "list", string(queryJSON), "", "http://127.0.0.1:3000", 401},
		{"method", "GET", "list", string(queryJSON), token, "http://127.0.0.1:3000", 405},
		{"cross-origin", "POST", "review", string(reviewJSON), token, "https://attacker.example", 403},
		{"query-override", "POST", "list?account_id=all", string(queryJSON), token, "http://127.0.0.1:3000", 400},
		{"unknown-route", "POST", "destroy", `{}`, token, "http://127.0.0.1:3000", 404},
		{"duplicate-id", "POST", "get", `{"id":"1","id":"2"}`, token, "http://127.0.0.1:3000", 400},
		{"null-id", "POST", "get", `{"id":null}`, token, "http://127.0.0.1:3000", 400},
		{"number-id", "POST", "get", `{"id":1}`, token, "http://127.0.0.1:3000", 400},
		{"unknown-field", "POST", "get", `{"id":"1","all":true}`, token, "http://127.0.0.1:3000", 400},
		{"missing-bool", "POST", "review", `{"account_id":"` + a.ID + `","revision":"1","observation_id":"1","reason":"test"}`, token, "http://127.0.0.1:3000", 400},
		{"recommendation-link", "POST", "recommendation-link", `{"id":"` + item.ID + `"}`, token, "http://127.0.0.1:3000", 200},
		{"recommendation-link-anonymous", "POST", "recommendation-link", `{"id":"` + item.ID + `"}`, "", "http://127.0.0.1:3000", 401},
		{"recommendation-link-method", "GET", "recommendation-link", `{}`, token, "http://127.0.0.1:3000", 405},
		{"recommendation-link-extra", "POST", "recommendation-link", `{"id":"` + item.ID + `","target":"0"}`, token, "http://127.0.0.1:3000", 400},
		{"recommendation-link-duplicate", "POST", "recommendation-link", `{"id":"1","id":"2"}`, token, "http://127.0.0.1:3000", 400},
		{"recommendation-link-invalid", "POST", "recommendation-link", `{"id":"01"}`, token, "http://127.0.0.1:3000", 400},
		{"empty-start-cannot-revoke", "POST", "start", `{"account_id":"` + a.ID + `","revision":"1","start_observation_id":"","end_observation_id":"","window_kind":"","reason":"test"}`, token, "http://127.0.0.1:3000", 400},
		{"revoke-cannot-set", "POST", "revoke", `{"account_id":"` + a.ID + `","revision":"1","start_observation_id":"1","reason":"test"}`, token, "http://127.0.0.1:3000", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := request(test.method, test.path, test.body, test.session, test.origin)
			if w.Code != test.status {
				t.Fatalf("boundary status %d, want %d: %s", w.Code, test.status, w.Body.String())
			}
		})
	}
	for _, mutate := range []func(*ObservationQuery){
		func(q *ObservationQuery) { q.StartAt = "2026-11-01T00:00:00+00:00" },
		func(q *ObservationQuery) { q.StartAt = "2026-11-01T00:00:00,1Z" },
		func(q *ObservationQuery) { q.StartAt = "2026-11-01T00:00:00.1234567890Z" },
		func(q *ObservationQuery) { q.StartAt = "1969-12-31T00:00:00Z" },
		func(q *ObservationQuery) { q.EndAt = "9999-01-01T00:00:00Z" },
		func(q *ObservationQuery) { q.EndAt = q.StartAt },
		func(q *ObservationQuery) { q.SourceMode = "guess" },
		func(q *ObservationQuery) { q.Validity = "deleted" },
		func(q *ObservationQuery) { q.Limit = 0 },
		func(q *ObservationQuery) { q.After = "01" },
	} {
		query := observationQuery()
		mutate(&query)
		body, _ := json.Marshal(query)
		if w := request("POST", "list", string(body), token, "http://127.0.0.1:3000"); w.Code != 400 {
			t.Fatal("invalid query accepted", string(body), w.Code, w.Body.String())
		}
	}
	_, err = s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "observer", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"observations"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, scoped, err := s.auth.Login(ctx, "observer", "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if w := request("POST", "estimate-link", `{"id":"`+item.ID+`"}`, scoped, "http://127.0.0.1:3000"); w.Code != 403 {
		t.Fatal("observation grant exposed estimate reference", w.Code, w.Body.String())
	}
	if w := request("POST", "review", string(reviewJSON), scoped, "http://127.0.0.1:3000"); w.Code != 403 {
		t.Fatal("ordinary observation reader acquired mutation authority", w.Code, w.Body.String())
	}
	if w := request("POST", "get", `{"id":"`+item.ID+`"}`, scoped, "http://127.0.0.1:3000"); w.Code != 200 {
		t.Fatal("observation-only reader required unrelated feature grants", w.Code, w.Body.String())
	}
	if err = s.auth.Logout(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", "get", `{"id":"`+item.ID+`"}`, scoped, "http://127.0.0.1:3000"); w.Code != 401 || calls.Load() != before {
		t.Fatal("revoked read or local-only boundary failed", w.Code, w.Body.String())
	}
}

func TestObservationPeriodRejectsMissingOrMismatchedNativeEvidence(t *testing.T) {
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	later, reset := at.Add(time.Minute), at.Add(time.Hour)
	duration, percent := "10800", "0"
	first := ObservationRecord{ID: "1", AccountID: "account", Snapshot: QuotaSnapshot{SourceID: "source", SourceMode: "passive", SourceAt: &at, ReceivedAt: at.Add(time.Second), Windows: []QuotaWindow{{Kind: "short", DurationSeconds: &duration, UsedPercent: &percent, ResetAt: &reset}}}}
	last := ObservationRecord{ID: "2", AccountID: "account", Snapshot: QuotaSnapshot{SourceID: "source", SourceMode: "passive", SourceAt: &later, ReceivedAt: later.Add(time.Second), Windows: []QuotaWindow{{Kind: "short", DurationSeconds: &duration, UsedPercent: &percent, ResetAt: &reset}}}}
	if period := observationPeriod(first, last, "short"); period == nil || period.DurationSeconds != "10800" {
		t.Fatal("complete native period rejected")
	}
	preciseSource := at.Add(499 * time.Nanosecond)
	if got := ceilMicrosecond(preciseSource); !got.Equal(at.Add(time.Microsecond)) || got.Before(preciseSource) || !ceilMicrosecond(at).Equal(at) {
		t.Fatal("database receipt quantization moved a received native source into the future", got)
	}
	for name, mutate := range map[string]func(*ObservationRecord){
		"same-id":                   func(item *ObservationRecord) { item.ID = first.ID },
		"account":                   func(item *ObservationRecord) { item.AccountID = "other" },
		"source":                    func(item *ObservationRecord) { item.Snapshot.SourceID = "other" },
		"mode":                      func(item *ObservationRecord) { item.Snapshot.SourceMode = "active" },
		"missing-source-time":       func(item *ObservationRecord) { item.Snapshot.SourceAt = nil },
		"non-advancing-source-time": func(item *ObservationRecord) { item.Snapshot.SourceAt = &at },
		"future-source-time":        func(item *ObservationRecord) { item.Snapshot.ReceivedAt = at },
		"missing-reset":             func(item *ObservationRecord) { item.Snapshot.Windows[0].ResetAt = nil },
		"different-reset": func(item *ObservationRecord) {
			value := reset.Add(time.Second)
			item.Snapshot.Windows[0].ResetAt = &value
		},
		"missing-duration":   func(item *ObservationRecord) { item.Snapshot.Windows[0].DurationSeconds = nil },
		"different-duration": func(item *ObservationRecord) { value := "18000"; item.Snapshot.Windows[0].DurationSeconds = &value },
		"missing-percent":    func(item *ObservationRecord) { item.Snapshot.Windows[0].UsedPercent = nil },
		"outside-period": func(item *ObservationRecord) {
			value := reset.Add(time.Second)
			item.Snapshot.SourceAt = &value
			item.Snapshot.ReceivedAt = value.Add(time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(last)
			var changed ObservationRecord
			if err := json.Unmarshal(body, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if observationPeriod(first, changed, "short") != nil {
				t.Fatal("incomplete or incompatible native evidence invented a period")
			}
		})
	}
}
