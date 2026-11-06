package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func nativeUserRow(id int) map[string]any {
	return map[string]any{"id": id, "email": fmt.Sprintf("member%d@example.test", id), "username": "same-name", "role": "user", "status": "active", "created_at": "2020-01-01T00:00:00Z", "deleted_at": nil,
		"balance": 999, "notes": "private-upstream-note", "api_keys": []map[string]any{{"key": "private-api-key"}}, "allowed_groups": []int{1}}
}

func createParticipant(t *testing.T, s *Service, token string, c Configuration, upstream int, name string) Participant {
	t.Helper()
	result, err := s.SaveParticipant(context.Background(), token, ParticipantUpdate{ParticipantInput: ParticipantInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, UpstreamID: strconv.Itoa(upstream), Name: name, Contact: "private-contact", Notes: "private-local-note", Enabled: true}})
	if err != nil || result.Participant == nil || result.Read == nil || result.Read.State != "available" {
		t.Fatalf("participant verification failed: %+v %v", result, err)
	}
	return *result.Participant
}

func participantEdit(p Participant, c Configuration) ParticipantUpdate {
	return ParticipantUpdate{ID: p.ID, Revision: p.Revision, ParticipantInput: ParticipantInput{SourceID: p.Binding.SourceID, ConnectionRevision: c.Revision, UpstreamID: p.Binding.UpstreamUserID, Name: p.Name, Contact: *p.Contact, Notes: *p.Notes, Enabled: p.Enabled}}
}

func TestParticipantProjectionRequiresExplicitSafeNativeIdentity(t *testing.T) {
	row := nativeUserRow(21)
	body, _ := json.Marshal(row)
	user, ok := projectNativeUser(body, "21", "management-secret")
	if !ok || user.UpstreamID != "21" || user.CreatedAt.Location() != time.UTC {
		t.Fatal("valid native identity was lost")
	}
	safe, _ := json.Marshal(user)
	if strings.Contains(string(safe), "private-") || strings.Contains(string(safe), "balance") || strings.Contains(string(safe), "allowed_groups") {
		t.Fatal("unrelated permissions or associations escaped")
	}
	for _, change := range []func(map[string]any){
		func(r map[string]any) { r["id"] = 22 },
		func(r map[string]any) { delete(r, "username") },
		func(r map[string]any) { r["email"] = "management-secret" },
		func(r map[string]any) { r["created_at"] = "invalid" },
		func(r map[string]any) { r["deleted_at"] = "2021-01-01T00:00:00Z" },
	} {
		row = nativeUserRow(21)
		change(row)
		body, _ = json.Marshal(row)
		if _, ok := projectNativeUser(body, "21", "management-secret"); ok {
			t.Fatal("invalid, reflected or mismatched identity accepted")
		}
	}
	if _, ok := projectNativeUser([]byte(`{"id":21,"id":22}`), "21", ""); ok {
		t.Fatal("duplicate identity fields accepted")
	}
}

func TestParticipantCandidatesLifecycleUniquenessAndRetainedBindings(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("unsafe candidate request")
		}
		if r.URL.Path == "/api/v1/admin/users" {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if r.URL.Query().Get("include_subscriptions") != "false" || r.URL.Query().Get("page_size") != "100" {
				t.Error("candidate query fetched unrelated associations")
			}
			items := []map[string]any{}
			for id := (page-1)*100 + 1; id <= min(page*100, 201); id++ {
				items = append(items, nativeUserRow(id))
			}
			respond(w, map[string]any{"items": items, "total": 201, "page": page, "page_size": 100})
			return
		}
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
		if err != nil || id == 999 {
			http.Error(w, "private denial adm_sc_test_only_secret", 403)
			return
		}
		respond(w, nativeUserRow(id))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	page, err := s.DiscoverUsers(ctx, token, DiscoverInput{SourceID: *c.SourceID, Revision: c.Revision, Page: 3})
	if err != nil || len(page.Users) != 1 || page.Users[0].UpstreamID != "201" || *page.Total != "201" || page.NextPage != nil {
		t.Fatalf("candidate directory truncated at a leaderboard: %+v %v", page, err)
	}
	input := ParticipantUpdate{ParticipantInput: ParticipantInput{SourceID: *c.SourceID, ConnectionRevision: c.Revision, UpstreamID: "21", Name: "Explicit member", Enabled: true}}
	results := make(chan ParticipantMutation, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { r, e := s.SaveParticipant(ctx, token, input); results <- r; errs <- e }()
	}
	passed, conflict := 0, 0
	var p Participant
	for i := 0; i < 2; i++ {
		r := <-results
		if r.Participant != nil {
			p = *r.Participant
		}
		err := <-errs
		if err == nil {
			passed++
		} else if errors.Is(err, auth.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || conflict != 1 || p.ID == "" {
		t.Fatal("concurrent native identity created competing owners")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_users`).Scan(&count); err != nil || count != 1 {
		t.Fatal("participant creation granted a login")
	}
	q := createParticipant(t, s, token, c, 22, p.Name)
	if q.ID == p.ID || q.Binding.UpstreamUserID != "22" {
		t.Fatal("same-name candidate was automatically bound")
	}
	old := participantEdit(p, c)
	edit := old
	edit.Name, edit.Enabled = "Renamed", false
	updated, err := s.SaveParticipant(ctx, token, edit)
	if err != nil || updated.Participant == nil || updated.Read != nil || updated.Participant.Enabled || updated.Participant.Binding.ID != p.Binding.ID {
		t.Fatal("local edit lost native binding or contacted upstream", err)
	}
	p = *updated.Participant
	if _, err := s.SaveParticipant(ctx, token, old); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale participant edit succeeded")
	}
	for native := 30; native < 40; native++ {
		edit = participantEdit(p, c)
		edit.UpstreamID = strconv.Itoa(native)
		result, err := s.SaveParticipant(ctx, token, edit)
		if err != nil || result.Participant == nil {
			t.Fatal("unused binding correction failed", err)
		}
		p = *result.Participant
	}
	history, err := s.ListParticipantBindings(ctx, token, p.ID, "0", 9)
	if err != nil || len(history.Bindings) != 9 || history.NextAfter == nil || history.Bindings[0].UpstreamUserID != "21" || history.Bindings[0].EndedAt == nil {
		t.Fatal("old binding was rewritten or numeric history was lost", err)
	}
	rest, err := s.ListParticipantBindings(ctx, token, p.ID, *history.NextAfter, 9)
	if err != nil || len(rest.Bindings) != 2 || rest.Bindings[0].EndedAt == nil || rest.Bindings[1].EndedAt != nil || rest.NextAfter != nil {
		t.Fatal("cross-digit binding history skipped rows", err)
	}
	edit = participantEdit(p, c)
	edit.UpstreamID, edit.Name = "22", "Must rollback"
	if _, err = s.SaveParticipant(ctx, token, edit); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("competing native wallet binding succeeded", err)
	}
	retained, _ := s.GetParticipant(ctx, token, p.ID)
	if retained.Revision != p.Revision || retained.Binding.ID != p.Binding.ID || retained.Name != p.Name {
		t.Fatal("failed rebind partially changed local history")
	}
	denied := input
	denied.UpstreamID = "999"
	result, err := s.SaveParticipant(ctx, token, denied)
	if err != nil || result.Participant != nil || result.Read == nil || result.Read.Reason != "FORBIDDEN" {
		t.Fatal("denied native identity became a participant", err)
	}
	if err := s.DeleteParticipant(ctx, token, q.ID, q.Revision, "wrong"); !errors.Is(err, auth.ErrInput) {
		t.Fatal("deletion accepted an incorrect confirmation")
	}
	if err := s.DeleteParticipant(ctx, token, q.ID, q.Revision, q.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetParticipant(ctx, token, q.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("deleted participant stayed active")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM participants WHERE id=$1 AND deleted_at IS NOT NULL`, q.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("deletion destroyed identity history")
	}
	deletedHistory, err := s.ListParticipantBindings(ctx, token, q.ID, "0", 100)
	if err != nil || len(deletedHistory.Bindings) != 1 || deletedHistory.Bindings[0].EndedAt == nil {
		t.Fatal("tombstoned participant lost binding history")
	}
	before := calls.Load()
	changed := save(t, s, token, "https://new-source.example")
	edit = participantEdit(p, changed)
	edit.Name = "Local old-source edit"
	result, err = s.SaveParticipant(ctx, token, edit)
	if err != nil || result.Participant == nil || result.Participant.SourceCurrent || calls.Load() != before {
		t.Fatal("local edit retargeted an old binding", err)
	}
	if _, err = s.SaveParticipant(ctx, token, input); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
		t.Fatal("stale creation reached another source")
	}
}

func TestParticipantCapturedAttributionPreservesHistoryAndCurrentTwoDimensionalScope(t *testing.T) {
	s, pool, admin := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	var payload atomic.Value
	payload.Store(map[int][]map[string]any{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" {
			t.Error("participant work mutated the upstream")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			respond(w, fullManagedAccount(id, fmt.Sprintf("resource-%d", id)))
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
		case r.URL.Path == "/api/v1/admin/usage":
			account, _ := strconv.Atoi(r.URL.Query().Get("account_id"))
			items := payload.Load().(map[int][]map[string]any)[account]
			if items == nil {
				items = []map[string]any{}
			}
			respond(w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100})
		default:
			t.Error("unexpected native route")
			http.Error(w, "denied", 403)
		}
	}))
	defer server.Close()
	c := save(t, s, admin, server.URL)
	a := adoptManaged(t, s, admin, c, 1, "Visible account")
	b := adoptManaged(t, s, admin, c, 2, "Other account")
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	end := start.Add(time.Hour)
	capture := func(account ManagedAccount) UsageCapture {
		t.Helper()
		result, err := s.CaptureUsage(ctx, admin, UsageCaptureInput{AccountReadInput: readInput(account, c), StartAt: start.Format(time.RFC3339Nano), EndAt: end.Format(time.RFC3339Nano)})
		if err != nil || result.State != "exhausted" {
			t.Fatalf("capture failed %+v %v", result, err)
		}
		return result
	}
	payload.Store(map[int][]map[string]any{1: {usageRow(55, time.Now().UTC().Format(time.RFC3339Nano))}})
	old := capture(a)
	p := createParticipant(t, s, admin, c, 21, "Member P")
	q := createParticipant(t, s, admin, c, 22, "Member Q")
	rowsA := []map[string]any{}
	for _, id := range []int64{1, 2, 3, 4, 5, 9, 10, 9007199254740993} {
		row := usageRow(id, p.Binding.StartedAt.Format(time.RFC3339Nano))
		switch id {
		case 1:
			row["created_at"] = p.Binding.StartedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		case 3:
			row["user_id"], row["created_at"] = 22, q.Binding.StartedAt.Format(time.RFC3339Nano)
		case 4:
			row["user_id"] = nil
		case 5:
			row["user_id"] = 999
		}
		rowsA = append(rowsA, row)
	}
	rowsB := []map[string]any{}
	for _, user := range []int{21, 22} {
		row := usageRow(int64(user-10), q.Binding.StartedAt.Format(time.RFC3339Nano))
		row["user_id"], row["account_id"] = user, 2
		rowsB = append(rowsB, row)
	}
	payload.Store(map[int][]map[string]any{1: rowsA, 2: rowsB})
	ca, cb := capture(a), capture(b)
	query := AttributedUsageQuery{CaptureIDs: []string{ca.ID, cb.ID}, After: "0", Limit: 100, Grain: "hour"}
	view, err := s.GetAttributedUsage(ctx, admin, query)
	if err != nil || view.Summary.RequestCount != "10" || view.Assigned.RequestCount != "7" || view.Unassigned == nil || view.Unassigned.RequestCount != "3" || *view.Summary.Metrics["total_cost"] != "1" || *view.Summary.Metrics["actual_cost"] != "2" || view.Summary.Metrics["account_stats_cost"] != nil || view.Complete || view.SettlementEligible {
		t.Fatalf("observed reconciliation/cost basis changed: %+v %v", view, err)
	}
	if view.Records[0].Attribution.ParticipantID != nil || view.Records[1].Attribution.ParticipantID == nil || *view.Records[1].Attribution.ParticipantID != p.ID || view.Records[3].Attribution.Basis != "missing_user_id" {
		t.Fatal("nanosecond boundary or missing native identity invented attribution")
	}
	if !view.OldestCaptureAt.Equal(ca.CapturedAt) || !view.NewestCaptureAt.Equal(cb.CapturedAt) {
		t.Fatal("cross-account observations hid the oldest capture")
	}
	oldView, err := s.GetAttributedUsage(ctx, admin, AttributedUsageQuery{CaptureIDs: []string{old.ID}, After: "0", Limit: 100, Grain: "day"})
	if err != nil || oldView.Assigned.RequestCount != "0" || oldView.Unassigned.RequestCount != "1" {
		t.Fatal("new current binding retroactively reassigned an old capture", err)
	}
	if _, err := s.GetAttributedUsage(ctx, admin, AttributedUsageQuery{CaptureIDs: []string{old.ID, ca.ID}, After: "0", Limit: 100, Grain: "day"}); !errors.Is(err, auth.ErrInput) {
		t.Fatal("overlapping captures were added as independent spending")
	}
	member, err := s.auth.CreateUser(ctx, admin, auth.UserCreate{Username: "participant-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"pools", "usage"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	visible, err := s.ListParticipants(ctx, reader, "", "", 1)
	if err != nil || len(visible.Participants) != 1 || visible.Participants[0].ID != p.ID || visible.NextAfter != nil || visible.Participants[0].Contact != nil || visible.Participants[0].Notes != nil || visible.Participants[0].Binding.Identity != nil {
		t.Fatal("participant catalog scope/redaction was applied after pagination", err)
	}
	for _, search := range []string{q.Name, "%", "_"} {
		page, err := s.ListParticipants(ctx, reader, "", search, 100)
		if err != nil || len(page.Participants) != 0 {
			t.Fatal("literal search broadened participant scope")
		}
	}
	if _, err := s.GetParticipant(ctx, reader, q.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("out-of-scope participant existence leaked")
	}
	if _, err := s.DiscoverUsers(ctx, reader, DiscoverInput{SourceID: *c.SourceID, Revision: c.Revision, Page: 1}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary identity queried administrative candidates")
	}
	if _, err := s.SaveParticipant(ctx, reader, participantEdit(p, c)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("read grants authorized management")
	}
	catalog, err := s.ListAttributedCaptures(ctx, reader, a.ID, "0", 100)
	if err != nil || len(catalog.Captures) != 1 || catalog.Captures[0].ID != ca.ID {
		t.Fatal("capture catalog exposed pre-binding/unassigned captures", err)
	}
	encoded, _ := json.Marshal(catalog)
	if strings.Contains(string(encoded), "observed_count") || strings.Contains(string(encoded), "pages_read") {
		t.Fatal("account-wide capture counts leaked")
	}
	if _, err := s.GetAttributedUsage(ctx, reader, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("cross-account summary leaked an ungranted account")
	}
	query.CaptureIDs, query.Limit = []string{ca.ID}, 2
	scoped, err := s.GetAttributedUsage(ctx, reader, query)
	if err != nil || scoped.Summary.RequestCount != "4" || scoped.Assigned.RequestCount != "4" || scoped.Unassigned != nil || scoped.UnassignedVisible || len(scoped.Groups) != 1 || scoped.NextAfter == nil || *scoped.NextAfter != ca.ID+".9" || *scoped.Summary.Metrics["actual_cost"] != "0.8" {
		t.Fatalf("two-dimensional scope leaked totals or reordered native IDs: %+v %v", scoped, err)
	}
	t.Run("window filters native nanoseconds before scoped aggregates and pages", func(t *testing.T) {
		window := query
		window.StartAt = p.Binding.StartedAt.Format(time.RFC3339Nano)
		window.EndAt = p.Binding.StartedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)
		filtered, err := s.GetAttributedUsage(ctx, reader, window)
		if err != nil || filtered.QueryStartAt == nil || filtered.QueryEndAt == nil || !filtered.StartAt.Equal(start) || filtered.Summary.RequestCount != "4" || filtered.Unassigned != nil || len(filtered.Groups) != 1 || len(filtered.Buckets) != 1 || !filtered.Buckets[0].StartAt.Equal(p.Binding.StartedAt) || !filtered.Buckets[0].EndAt.Equal(p.Binding.StartedAt.Add(time.Nanosecond)) || filtered.NextAfter == nil || *filtered.NextAfter != ca.ID+".9" {
			t.Fatalf("window lost exact bounds, pagination or scope: %+v %v", filtered, err)
		}
		window.After = *filtered.NextAfter
		last, err := s.GetAttributedUsage(ctx, reader, window)
		if err != nil || len(last.Records) != 2 || last.Records[0].Fact.ID != "10" || last.Summary.RequestCount != "4" || *last.Summary.Metrics["actual_cost"] != "0.8" {
			t.Fatal("window cursor changed filtered totals", err)
		}
		window.After = "0"
		administrator, err := s.GetAttributedUsage(ctx, admin, window)
		if err != nil || administrator.Summary.RequestCount != "6" || administrator.Unassigned.RequestCount != "2" {
			t.Fatal("window discarded authorized unassigned evidence", err)
		}
		window.StartAt, window.EndAt = window.EndAt, p.Binding.StartedAt.Add(2*time.Nanosecond).Format(time.RFC3339Nano)
		empty, err := s.GetAttributedUsage(ctx, reader, window)
		if err != nil || empty.Summary.RequestCount != "0" || empty.Summary.Metrics["actual_cost"] != nil || len(empty.Records) != 0 || len(empty.Buckets) != 0 || len(empty.Groups) != 0 || empty.NextAfter != nil {
			t.Fatal("sub-microsecond empty window rounded into recorded consumption", err)
		}
		window.StartAt, window.EndAt = end.Add(time.Hour).Format(time.RFC3339Nano), end.Add(2*time.Hour).Format(time.RFC3339Nano)
		outside, err := s.GetAttributedUsage(ctx, reader, window)
		if err != nil || outside.Summary.RequestCount != "0" || len(outside.Buckets) != 0 || outside.Complete || outside.SettlementEligible {
			t.Fatal("outside-capture query invented coverage", err)
		}
		window.CaptureIDs = []string{cb.ID}
		if _, err = s.GetAttributedUsage(ctx, reader, window); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("window bypassed account scope", err)
		}
		legacy, _ := json.Marshal(scoped)
		if strings.Contains(string(legacy), "query_start_at") {
			t.Fatal("legacy view wire shape changed")
		}
	})
	query.After = *scoped.NextAfter
	next, err := s.GetAttributedUsage(ctx, reader, query)
	if err != nil || next.Records[0].Fact.ID != "10" || next.Records[1].Fact.ID != "9007199254740993" || next.NextAfter != nil || next.Summary.RequestCount != "4" {
		t.Fatal("numeric record cursor changed totals or lost a large ID", err)
	}
	query.ParticipantID = q.ID
	if _, err := s.GetAttributedUsage(ctx, reader, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("supplied participant ID bypassed grants")
	}
	if calls.Load() != before {
		t.Fatal("protected local queries or denied management dispatched upstream")
	}
	if _, err := s.GetUsageCapture(ctx, reader, UsageQuery{ID: ca.ID, After: "0", Limit: 100, Grain: "day"}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("new mapping opened account-wide raw capture metadata")
	}
	edit := participantEdit(p, c)
	edit.UpstreamID = "23"
	if _, err := s.SaveParticipant(ctx, admin, edit); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("historical ownership was rebound")
	}
	if err := s.DeleteParticipant(ctx, admin, p.ID, p.Revision, p.Name); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("historical participant was deleted")
	}
	edit = participantEdit(p, c)
	edit.Name, edit.Enabled = "Renamed inactive P", false
	updated, err := s.SaveParticipant(ctx, admin, edit)
	if err != nil || updated.Participant == nil || updated.Participant.Enabled {
		t.Fatal("historical member could not exit current allocation", err)
	}
	query.After, query.ParticipantID = "0", ""
	retained, err := s.GetAttributedUsage(ctx, reader, query)
	if err != nil || *retained.Records[0].Attribution.ParticipantName != "Member P" || retained.Summary.RequestCount != "4" {
		t.Fatal("rename/disable rewrote captured history")
	}
	member, err = s.auth.UpdateUser(ctx, admin, auth.UserUpdate{ID: member.ID, Revision: member.Revision, Username: member.Username, Enabled: true, Grants: auth.Grants{Features: []string{"pools", "usage"}, AccountIDs: []string{a.ID, b.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	query.CaptureIDs = []string{ca.ID, cb.ID}
	cross, err := s.GetAttributedUsage(ctx, reader, query)
	if err != nil || cross.Summary.RequestCount != "5" || *cross.Summary.Metrics["actual_cost"] != "1" || cross.Unassigned != nil {
		t.Fatal("cross-account visible ownership summary failed", err)
	}
	member, err = s.auth.UpdateUser(ctx, admin, auth.UserUpdate{ID: member.ID, Revision: member.Revision, Username: member.Username, Enabled: true, Grants: auth.Grants{Features: []string{"pools", "usage"}, AccountIDs: []string{}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAttributedUsage(ctx, reader, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("scope contraction left prior summary access")
	}
	if _, err := s.ListAttributedCaptures(ctx, reader, a.ID, "0", 100); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("scope contraction leaked capture selection")
	}
	payload.Store(map[int][]map[string]any{})
	before = calls.Load()
	restarted, _ := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if _, err := restarted.GetAttributedUsage(ctx, admin, query); err != nil || calls.Load() != before {
		t.Fatal("restart/cleanup rewrote attribution or replayed collection", err)
	}
}

func TestParticipantWaitsRecheckActualExpiryBeforePersistence(t *testing.T) {
	for _, wait := range []string{"network", "participant-lock"} {
		t.Run(wait, func(t *testing.T) {
			s, pool, token := setup(t)
			ctx := context.Background()
			entered, release := make(chan struct{}, 1), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
				if id == 22 {
					entered <- struct{}{}
					<-release
				}
				respond(w, nativeUserRow(id))
			}))
			defer server.Close()
			defer unblock()
			c := save(t, s, token, server.URL)
			p := createParticipant(t, s, token, c, 21, "Original")
			lock, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(ctx)
			input := participantEdit(p, c)
			lifetime := time.Second
			if wait == "participant-lock" {
				if _, err := lock.Exec(ctx, `SELECT id FROM participants WHERE id=$1 FOR UPDATE`, p.ID); err != nil {
					t.Fatal(err)
				}
				lifetime = 5 * time.Second
				input.Name = "Expired edit"
			} else {
				input.ID, input.Revision, input.UpstreamID = "", "", "22"
			}
			deadline := time.Now().Add(lifetime)
			digest := sha256.Sum256([]byte(token))
			exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				result, err := s.SaveParticipant(ctx, token, input)
				if result.Participant != nil {
					done <- errors.New("expired wait returned a participant")
					return
				}
				done <- err
			}()
			if wait == "network" {
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("ended before actual native request: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("native request not observed")
				}
			} else {
				observed := false
				for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
					select {
					case err := <-done:
						t.Fatalf("ended before actual participant lock: %v", err)
					default:
					}
					if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT p.id,p.revision,p.name%')`).Scan(&observed); err != nil {
						t.Fatal(err)
					}
					if observed {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !observed {
					t.Fatal("actual participant lock was not observed")
				}
			}
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			unblock()
			_ = lock.Rollback(ctx)
			if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("expired actual wait committed protected results", err)
			}
			var name, revision string
			var count int
			if err := pool.QueryRow(ctx, `SELECT name,revision::text FROM participants WHERE id=$1`, p.ID).Scan(&name, &revision); err != nil || name != p.Name || revision != p.Revision {
				t.Fatal("expired edit partially persisted")
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM participants`).Scan(&count); err != nil || count != 1 {
				t.Fatal("expired creation partially persisted")
			}
		})
	}
}

func TestParticipantAuditFailureRollsBackLifecycleAndCapturedAttribution(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var stamp atomic.Value
	stamp.Store(time.Now().UTC().Format(time.RFC3339Nano))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/"):
			respond(w, fullManagedAccount(1, "resource"))
		default:
			respond(w, map[string]any{"items": []map[string]any{usageRow(1, stamp.Load().(string))}, "total": 1, "page": 1, "page_size": 100})
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	p := createParticipant(t, s, token, c, 21, "Original")
	exec(t, pool, `CREATE FUNCTION reject_participant_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`)
	exec(t, pool, `CREATE TRIGGER reject_participant_event BEFORE INSERT ON participant_events FOR EACH ROW EXECUTE FUNCTION reject_participant_event()`)
	edit := participantEdit(p, c)
	edit.Name = "Must rollback"
	if _, err := s.SaveParticipant(ctx, token, edit); err == nil {
		t.Fatal("audit failure reported successful edit")
	}
	edit.ID, edit.Revision, edit.UpstreamID = "", "", "22"
	if _, err := s.SaveParticipant(ctx, token, edit); err == nil {
		t.Fatal("audit failure reported successful creation")
	}
	if err := s.DeleteParticipant(ctx, token, p.ID, p.Revision, p.Name); err == nil {
		t.Fatal("audit failure reported successful deletion")
	}
	after, err := s.GetParticipant(ctx, token, p.ID)
	var count int
	if err != nil || after.Revision != p.Revision || after.Name != p.Name || after.Binding.ID != p.Binding.ID {
		t.Fatal("audit failure changed local participant history", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM participant_bindings WHERE ended_at IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatal("audit failure changed native binding ownership")
	}
	a := adoptManaged(t, s, token, c, 1, "Account")
	stamp.Store(p.Binding.StartedAt.Format(time.RFC3339Nano))
	exec(t, pool, `CREATE TRIGGER reject_usage_attribution BEFORE INSERT ON usage_attributions FOR EACH ROW EXECUTE FUNCTION reject_participant_event()`)
	if _, err := s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: p.CreatedAt.Add(-time.Minute).Format(time.RFC3339Nano), EndAt: p.CreatedAt.Add(time.Hour).Format(time.RFC3339Nano)}); err == nil {
		t.Fatal("attribution failure reported successful capture")
	}
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_captures)+(SELECT count(*) FROM usage_capture_facts)+(SELECT count(*) FROM usage_attributions)+(SELECT count(*) FROM usage_events)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("attribution failure partially committed facts/events")
	}
}

func TestParticipantConcurrentCaptureCommitsHistoryBeforeRebindGuard(t *testing.T) {
	s, pool, token := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var stamp atomic.Value
	stamp.Store(time.Now().UTC().Format(time.RFC3339Nano))
	var forbiddenVerification atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			if id == 23 {
				forbiddenVerification.Add(1)
			}
			respond(w, nativeUserRow(id))
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/"):
			respond(w, fullManagedAccount(1, "resource"))
		default:
			respond(w, map[string]any{"items": []map[string]any{usageRow(1, stamp.Load().(string))}, "total": 1, "page": 1, "page_size": 100})
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	p := createParticipant(t, s, token, c, 21, "History owner")
	oldStamp := p.Binding.StartedAt
	rebind := participantEdit(p, c)
	rebind.UpstreamID = "22"
	changed, err := s.SaveParticipant(ctx, token, rebind)
	if err != nil || changed.Participant == nil {
		t.Fatal("unused initial rebind failed", err)
	}
	p = *changed.Participant
	a := adoptManaged(t, s, token, c, 1, "Account")
	stamp.Store(oldStamp.Format(time.RFC3339Nano))
	barrier, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	if _, err = barrier.Exec(ctx, `LOCK TABLE usage_attributions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	captured, edited := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: p.CreatedAt.Add(-time.Minute).Format(time.RFC3339Nano), EndAt: p.CreatedAt.Add(time.Hour).Format(time.RFC3339Nano)})
		captured <- err
	}()
	witness := func(pattern string) {
		t.Helper()
		for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
			var observed bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, pattern).Scan(&observed); err != nil {
				t.Fatal(err)
			}
			if observed {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("real database wait not observed: %s", pattern)
	}
	witness("INSERT INTO usage_attributions%")
	edit := participantEdit(p, c)
	edit.UpstreamID = "23"
	go func() { _, err := s.SaveParticipant(ctx, token, edit); edited <- err }()
	witness("SELECT id::text FROM participant_bindings%")
	if err := barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-captured; err != nil {
		t.Fatal("capture and participant edit deadlocked or lost facts", err)
	}
	if err := <-edited; !errors.Is(err, auth.ErrConflict) || forbiddenVerification.Load() != 0 {
		t.Fatal("rebind checked history before a real capture lock wait", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_attributions WHERE participant_id=$1`, p.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent rebind discarded captured ownership")
	}
}
