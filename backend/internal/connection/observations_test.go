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

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func observationFixture(t *testing.T) (*Service, *pgxpool.Pool, string, Configuration, *atomic.Int32) {
	t.Helper()
	s, db, token := setup(t)
	calls := new(atomic.Int32)
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("unsafe observation fixture request")
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			row := fullManagedAccount(id, fmt.Sprintf("observed-resource-%d", id))
			row["extra"] = map[string]any{"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339Nano), "codex_5h_used_percent": 0, "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339), "codex_7d_used_percent": 42, "private": "never expose"}
			respond(w, row)
			return
		}
		t.Errorf("unexpected observation upstream route %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return s, db, token, save(t, s, token, server.URL), calls
}

func observationQuery() ObservationQuery {
	return ObservationQuery{SourceMode: "all", Validity: "all", StartAt: "1970-01-01T00:00:00Z", EndAt: "9998-12-31T00:00:00Z", After: "0", Limit: 100}
}

func collectObservation(t *testing.T, s *Service, db *pgxpool.Pool, token string, c Configuration, a ManagedAccount) ObservationRecord {
	t.Helper()
	advanceCollectionFixture(t, db)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	query := observationQuery()
	query.AccountID = a.ID
	page, err := s.ListObservations(context.Background(), token, query)
	if err != nil || len(page.Records) == 0 {
		t.Fatalf("native collection did not produce a queryable observation: %+v %v", page, err)
	}
	return page.Records[len(page.Records)-1]
}

func TestObservationScopedHistoryExactTimeAndFrozenContract(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	b := passiveManaged(t, s, token, c, 2)
	p := createParticipant(t, s, token, c, 21, "Frozen visible member")
	q := createParticipant(t, s, token, c, 22, "Hidden member")
	contract := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p, q}, []string{"33.333333", "50"})})
	first := collectObservation(t, s, db, token, c, a)
	for i := 0; i < 11; i++ {
		collectObservation(t, s, db, token, c, a)
	}
	hidden := collectObservation(t, s, db, token, c, b)
	before := calls.Load()
	reader, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "observer", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"observations"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, scoped, err := s.auth.Login(ctx, "observer", "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	query := observationQuery()
	query.Limit = 3
	seen := 0
	for {
		page, e := s.ListObservations(ctx, scoped, query)
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range page.Records {
			seen++
			if item.ID != strconv.Itoa(seen) || item.AccountID != a.ID {
				t.Fatal("scope or numeric cursor changed before pagination", item)
			}
		}
		if page.NextAfter == nil {
			break
		}
		query.After = *page.NextAfter
	}
	if seen != 12 {
		t.Fatal("scope-filtered history omitted facts", seen)
	}
	for _, id := range []string{hidden.ID, "999999"} {
		if result, e := s.GetObservation(ctx, scoped, id); !errors.Is(e, auth.ErrNotFound) || result.Observation.ID != "" {
			t.Fatal("hidden and absent observations must return the same empty result", e)
		}
	}
	query = observationQuery()
	query.AccountID = b.ID
	if _, err = s.ListObservations(ctx, scoped, query); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("explicit account filter escaped current scope", err)
	}
	query.AccountID, query.SourceID, query.SourceMode = a.ID, *c.SourceID, "passive"
	query.StartAt = first.Snapshot.ReceivedAt.Format(time.RFC3339Nano)
	query.EndAt = first.Snapshot.ReceivedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)
	page, err := s.ListObservations(ctx, scoped, query)
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != first.ID || page.TimeBasis != "received_at" || !page.QueryEndAt.Equal(first.Snapshot.ReceivedAt.Add(time.Nanosecond)) {
		t.Fatalf("half-open nanosecond bound lost an exact stored receipt: %+v %v", page, err)
	}
	query.StartAt, query.EndAt = query.EndAt, first.Snapshot.ReceivedAt.Add(2*time.Nanosecond).Format(time.RFC3339Nano)
	if page, err = s.ListObservations(ctx, scoped, query); err != nil || len(page.Records) != 0 {
		t.Fatal("sub-microsecond range rounded backward to a stored receipt", page, err)
	}
	changed := poolInput(c, []ManagedAccount{a}, []Participant{p, q}, []string{"10", "80"})
	savePool(t, s, token, PoolUpdate{ID: contract.PoolID, Revision: contract.Revision, PoolInput: changed})
	detail, err := s.GetObservation(ctx, scoped, first.ID)
	if err != nil || detail.Basis.ContractID == nil || *detail.Basis.ContractID != contract.ID || len(detail.Basis.Members) != 1 || detail.Basis.Members[0].ParticipantID != p.ID || detail.Basis.Members[0].Name != p.Name || detail.Basis.Members[0].SharePercent != "33.333333" || detail.Basis.UnallocatedPercent != nil || detail.Basis.UsageState != "unavailable" || detail.Basis.EstimateState != "unavailable" {
		t.Fatalf("historical participant projection changed or invented financial evidence: %+v %v", detail, err)
	}
	encoded, _ := json.Marshal(detail)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), q.ID) || strings.Contains(string(encoded), "adm_sc_") {
		t.Fatal("unrelated native or hidden member fields escaped", string(encoded))
	}
	adminDetail, err := s.GetObservation(ctx, token, first.ID)
	if err != nil || len(adminDetail.Basis.Members) != 2 || adminDetail.Basis.UnallocatedPercent == nil || *adminDetail.Basis.UnallocatedPercent != "16.666667" {
		t.Fatal("administrator lost exact original contractual remainder", adminDetail, err)
	}
	if calls.Load() != before {
		t.Fatal("history queries or local contract change contacted upstream")
	}
	exec(t, db, `SELECT setval(pg_get_serial_sequence('quota_observations','id'),9007199254740992,true)`)
	large := collectObservation(t, s, db, token, c, a)
	if large.ID != "9007199254740993" {
		t.Fatal("large observation identity lost decimal precision", large.ID)
	}
	before = calls.Load()
	reader, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: reader.ID, Revision: reader.Revision, Username: reader.Username, Enabled: true, Grants: auth.Grants{Features: []string{"observations"}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if result, e := s.GetObservation(ctx, scoped, first.ID); !errors.Is(e, auth.ErrNotFound) || result.Observation.ID != "" {
		t.Fatal("new read ignored contracted account grants", e)
	}
	_, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: reader.ID, Revision: reader.Revision, Username: reader.Username, Enabled: true, Grants: auth.Grants{Features: []string{}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListObservations(ctx, scoped, observationQuery()); !errors.Is(err, auth.ErrForbidden) || calls.Load() != before {
		t.Fatal("feature contraction or local-only query boundary failed", err)
	}
}

func TestObservationDecisionsPreserveFactsAndRequireNativePeriod(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	first := collectObservation(t, s, db, token, c, a)
	last := collectObservation(t, s, db, token, c, a)
	before := calls.Load()
	var original string
	if err := db.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(id,snapshot,received_at) ORDER BY id)::text FROM quota_observations`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	review := ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: first.ID, Excluded: true, Reason: "异常源读数，保留原始事实"}
	item, err := s.ReviewObservation(ctx, token, review)
	if err != nil || !item.Excluded || item.ManagementRevision != "2" || item.ReviewReason == nil || *item.ReviewReason != review.Reason {
		t.Fatal("exclusion was not retained", item, err)
	}
	start := ObservationStartInput{AccountID: a.ID, Revision: "2", StartObservationID: first.ID, EndObservationID: last.ID, WindowKind: "short", Reason: "Use two actual native observations"}
	if _, err = s.ChangeObservationStart(ctx, token, start, false); !errors.Is(err, ErrObservationRange) {
		t.Fatal("excluded endpoint accepted as a manual basis", err)
	}
	if _, err = s.ReviewObservation(ctx, token, review); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale management revision accepted", err)
	}
	review.Revision = "2"
	if _, err = s.ReviewObservation(ctx, token, review); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("duplicate exclusion silently appended", err)
	}
	review.Excluded, review.Reason = false, "核实后恢复"
	if item, err = s.ReviewObservation(ctx, token, review); err != nil || item.Excluded || item.ManagementRevision != "3" {
		t.Fatal("restoration failed", item, err)
	}
	start.Revision = "3"
	state, err := s.ChangeObservationStart(ctx, token, start, false)
	if err != nil || state.Revision != "4" || state.ManualStart == nil || !state.ManualStart.Usable || state.ManualStart.Period == nil || state.ManualStart.Period.DurationSeconds != "10800" || state.ManualStart.Period.EndAt.Sub(state.ManualStart.Period.StartAt) != 3*time.Hour || state.RecalculationState != "not_implemented" {
		managed, _ := s.GetManaged(ctx, token, a.ID)
		currentFirst, _ := s.GetObservation(ctx, token, first.ID)
		currentLast, _ := s.GetObservation(ctx, token, last.ID)
		policy, _ := s.GetMonitoringConfig(ctx, token)
		period := observationPeriod(currentFirst.Observation, currentLast.Observation, "short")
		reason := observationStartReason(currentFirst.Observation, currentLast.Observation, period, storedAccount{ManagedAccount: managed.Account}, storedConfig{Configuration: c}, policy, time.Now().UTC())
		body, _ := json.Marshal([]any{currentFirst.Observation, currentLast.Observation, period, reason})
		t.Log(string(body))
		t.Fatal("actual native period was replaced with an assumed cycle", state, err)
	}
	restarted, err := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	state, err = restarted.GetObservationManagement(ctx, token, a.ID)
	if err != nil || state.ManualStart == nil || !state.ManualStart.Usable || state.ManualStart.Event.Revision != "4" {
		t.Fatal("service reconstruction lost persisted range decisions", state, err)
	}
	start.Revision, start.WindowKind = "4", "seven_day"
	if _, err = s.ChangeObservationStart(ctx, token, start, false); !errors.Is(err, ErrObservationRange) {
		t.Fatal("missing native duration/reset invented a seven-day period", err)
	}
	review.Revision, review.ObservationID, review.Excluded = "4", last.ID, true
	if _, err = s.ReviewObservation(ctx, token, review); err != nil {
		t.Fatal(err)
	}
	state, err = s.GetObservationManagement(ctx, token, a.ID)
	if err != nil || state.ManualStart == nil || state.ManualStart.Usable || state.ManualStart.Reason != "ENDPOINT_EXCLUDED" {
		t.Fatal("excluded range was still usable or erased", state, err)
	}
	review.Revision, review.Excluded = "5", false
	if _, err = s.ReviewObservation(ctx, token, review); err != nil {
		t.Fatal(err)
	}
	state, err = s.ChangeObservationStart(ctx, token, ObservationStartInput{AccountID: a.ID, Revision: "6", Reason: "撤销本地起点"}, true)
	if err != nil || state.ManualStart != nil || state.Revision != "7" {
		t.Fatal("revocation did not append a retained decision", state, err)
	}
	query := observationQuery()
	query.Validity = "excluded"
	if page, e := s.ListObservations(ctx, token, query); e != nil || len(page.Records) != 0 {
		t.Fatal("restoration did not affect current validity filter", page, e)
	}
	events, err := s.ListObservationEvents(ctx, token, a.ID, "0", 100)
	if err != nil || len(events.Events) != 6 || events.Events[0].Action != "exclude" || events.Events[1].Action != "restore" || events.Events[2].Action != "start" || events.Events[5].Action != "revoke" || events.Events[0].Reason != "异常源读数，保留原始事实" {
		t.Fatal("decision history was overwritten", events, err)
	}
	for i := 0; i < 12; i++ {
		review.Revision, review.Excluded = strconv.Itoa(i+7), i%2 == 0
		if _, err = s.ReviewObservation(ctx, token, review); err != nil {
			t.Fatal(err)
		}
	}
	after, seen := "0", 0
	for {
		page, e := s.ListObservationEvents(ctx, token, a.ID, after, 3)
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range page.Events {
			seen++
			if event.ID != strconv.Itoa(seen) {
				t.Fatal("numeric decision cursor lost order", event.ID)
			}
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if seen != 18 {
		t.Fatal("decision pagination lost retained events", seen)
	}
	save(t, s, token, c.BaseURL)
	start.Revision, start.WindowKind = "19", "short"
	if _, err = s.ChangeObservationStart(ctx, token, start, false); !errors.Is(err, ErrObservationRange) {
		t.Fatal("old connection evidence accepted after configuration revision changed", err)
	}
	var retained string
	if err = db.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(id,snapshot,received_at) ORDER BY id)::text FROM quota_observations`).Scan(&retained); err != nil || retained != original || calls.Load() != before {
		t.Fatal("local decisions rewrote facts or contacted upstream", err)
	}
}

func TestObservationCurrentStartMarkerFiltersBeforePaging(t *testing.T) {
	s, db, token, c, _ := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	first := collectObservation(t, s, db, token, c, a)
	middle := collectObservation(t, s, db, token, c, a)
	last := collectObservation(t, s, db, token, c, a)
	start := ObservationStartInput{AccountID: a.ID, Revision: "1", StartObservationID: middle.ID, EndObservationID: last.ID, WindowKind: "short", Reason: "Use the actual later interval"}
	state, err := s.ChangeObservationStart(ctx, token, start, false)
	if err != nil || state.ManualStart == nil || !state.ManualStart.Start.ManualStart || state.ManualStart.End.ManualStart {
		t.Fatal("current range marker did not bind its first endpoint", state, err)
	}
	query := observationQuery()
	query.Validity, query.Limit = "manual_start", 1
	page, err := s.ListObservations(ctx, token, query)
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != middle.ID || page.NextAfter != nil {
		t.Fatal("start filter was applied after numeric paging", page, err)
	}
	start.Revision, start.StartObservationID, start.EndObservationID = "2", first.ID, middle.ID
	if _, err = s.ChangeObservationStart(ctx, token, start, false); err != nil {
		t.Fatal(err)
	}
	if old, e := s.GetObservation(ctx, token, middle.ID); e != nil || old.Observation.ManualStart {
		t.Fatal("replacement retained an obsolete start marker", old, e)
	}
	if _, err = s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "3", ObservationID: first.ID, Excluded: true, Reason: "Keep an unusable range visible for revocation"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListObservations(ctx, token, query)
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != first.ID || !page.Records[0].Excluded || !page.Records[0].ManualStart {
		t.Fatal("exclusion erased the retained local range", page, err)
	}
	if _, err = s.ChangeObservationStart(ctx, token, ObservationStartInput{AccountID: a.ID, Revision: "4", Reason: "Revoke the retained range"}, true); err != nil {
		t.Fatal(err)
	}
	if page, err = s.ListObservations(ctx, token, query); err != nil || len(page.Records) != 0 || page.NextAfter != nil {
		t.Fatal("revoked range still appeared as a current start", page, err)
	}
}

func TestObservationConcurrentRevisionAndAuditRollback(t *testing.T) {
	s, db, token, c, _ := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	item := collectObservation(t, s, db, token, c, a)
	input := ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: item.ID, Excluded: true, Reason: "Concurrent review"}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := s.ReviewObservation(ctx, token, input)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, auth.ErrConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatal("concurrent expected revision was not serialized", succeeded, conflicted)
	}
	exec(t, db, `CREATE FUNCTION reject_observation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject fixture audit'; END $$; CREATE TRIGGER reject_observation_audit BEFORE INSERT ON management_events FOR EACH ROW WHEN (NEW.action LIKE 'observation.%') EXECUTE FUNCTION reject_observation_audit()`)
	input.Revision, input.Excluded = "2", false
	if result, err := s.ReviewObservation(ctx, token, input); err == nil || result.ID != "" {
		t.Fatal("failed audit returned a protected success result", err)
	}
	detail, err := s.GetObservation(ctx, token, item.ID)
	events, eventErr := s.ListObservationEvents(ctx, token, a.ID, "0", 100)
	if err != nil || eventErr != nil || !detail.Observation.Excluded || detail.Observation.ManagementRevision != "2" || len(events.Events) != 1 {
		t.Fatal("failed audit left partial management state", detail, events, err, eventErr)
	}
}

func TestObservationActualLockExpiryReturnsNoDataOrMutation(t *testing.T) {
	for _, operation := range []string{"review", "state", "commit"} {
		t.Run(operation, func(t *testing.T) {
			s, db, token, c, _ := observationFixture(t)
			ctx := context.Background()
			a := passiveManaged(t, s, token, c, 1)
			item := collectObservation(t, s, db, token, c, a)
			barrier, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(ctx)
			if operation == "commit" {
				_, err = barrier.Exec(ctx, "LOCK TABLE management_events IN ACCESS EXCLUSIVE MODE")
			} else {
				_, err = barrier.Exec(ctx, `SELECT id FROM managed_accounts WHERE id=$1 FOR UPDATE`, a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				if operation == "state" {
					result, e := s.GetObservationManagement(ctx, token, a.ID)
					if result.AccountID != "" {
						done <- errors.New("expired wait returned protected management state")
					} else {
						done <- e
					}
					return
				}
				result, e := s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: item.ID, Excluded: true, Reason: "Must expire while waiting"})
				if result.ID != "" {
					done <- errors.New("expired wait returned protected observation")
				} else {
					done <- e
				}
			}()
			pattern := "%FROM managed_accounts%FOR UPDATE"
			if operation == "state" {
				pattern = "%FROM managed_accounts%FOR SHARE"
			} else if operation == "commit" {
				pattern = "INSERT INTO management_events%"
			}
			observePoolWait(t, db, pattern)
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("actual database wait outlived session authority", err)
			}
			var count int
			if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM observation_changes)+(SELECT count(*) FROM observation_management)`).Scan(&count); err != nil || count != 0 {
				t.Fatal("expired operation persisted a decision or revision", count, err)
			}
		})
	}
}
