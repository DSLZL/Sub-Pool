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
	"strconv"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func observationRecommendationFixture(t *testing.T) (*walletFixture, ManagedAccount, []ObservationRecord, EstimateRecord) {
	t.Helper()
	f := &walletFixture{}
	f.s, f.db, f.token = setup(t)
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		if r.Method != "GET" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe observation reference fixture request")
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
			respond(w, nativeUserRow(id))
			return
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
		row := fullManagedAccount(id, "observation-wallet-"+strconv.Itoa(id))
		row["extra"] = map[string]any{"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339Nano), "codex_5h_used_percent": n, "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339)}
		respond(w, row)
	}))
	t.Cleanup(server.Close)
	f.c = save(t, f.s, f.token, server.URL)
	f.p = createParticipant(t, f.s, f.token, f.c, 21, "Referenced wallet")
	a := passiveManaged(t, f.s, f.token, f.c, 1)
	facts := []ObservationRecord{collectObservation(t, f.s, f.db, f.token, f.c, a), collectObservation(t, f.s, f.db, f.token, f.c, a), collectObservation(t, f.s, f.db, f.token, f.c, a)}
	saved, err := f.s.SaveEstimateConfiguration(context.Background(), f.token, estimateInput(a, f.c))
	if err != nil {
		t.Fatal(err)
	}
	savePool(t, f.s, f.token, PoolUpdate{PoolInput: poolInput(f.c, []ManagedAccount{a}, []Participant{f.p}, []string{"75"})})
	return f, a, facts, saved
}

func TestObservationRecommendationLinkFollowsActualFrozenChainAndNumericHistory(t *testing.T) {
	f, a, facts, estimate := observationRecommendationFixture(t)
	ctx := context.Background()
	if link, err := f.s.GetObservationRecommendationLink(ctx, f.token, facts[0].ID); err != nil || link.Recommendation != nil || link.ObservationID != facts[0].ID || link.AccountID != a.ID {
		t.Fatal("estimate alone invented a wallet reference", link, err)
	}
	head := "0"
	var actual WalletRecommendation
	for _, expected := range []string{"99", "100", "9007199254740993"} {
		exec(t, f.db, "ALTER TABLE wallet_recommendations ALTER COLUMN id RESTART WITH "+expected)
		var err error
		actual, err = f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, head))
		if err != nil || actual.ID != expected || len(actual.Contributions) != 1 || actual.Contributions[0].EstimateID == nil || *actual.Contributions[0].EstimateID != estimate.ID || actual.Target != nil || actual.Applicable {
			t.Fatal("fixture did not retain the actual unavailable chain", actual, err)
		}
		head = actual.ID
		for _, fact := range facts {
			link, e := f.s.GetObservationRecommendationLink(ctx, f.token, fact.ID)
			if e != nil || link.Recommendation == nil || link.Recommendation.ID != expected || link.Recommendation.ParticipantID != f.p.ID || link.Recommendation.EstimateID != estimate.ID || !link.Recommendation.GeneratedAt.Equal(actual.GeneratedAt) {
				t.Fatal("exact sampled/current chain or numeric precision lost", link, e)
			}
		}
	}
	if _, err := f.s.ChangeObservationStart(ctx, f.token, ObservationStartInput{AccountID: a.ID, Revision: "1", StartObservationID: facts[0].ID, EndObservationID: facts[1].ID, WindowKind: "short", Reason: "Separate samples from current consumption"}, false); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.GetEstimateCurrent(ctx, f.token, a.ID)
	if err != nil || current.Latest == nil {
		t.Fatal(err)
	}
	estimate = *current.Latest
	w := estimateKind(t, estimate, "short")
	if w.CurrentObservationID == nil || *w.CurrentObservationID != facts[2].ID || len(w.ObservationIDs) != 2 {
		t.Fatal("fixture did not separate the actual current-only reference", w)
	}
	actual, err = f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, head))
	if err != nil {
		t.Fatal(err)
	}
	head = actual.ID
	currentLink, err := f.s.GetObservationRecommendationLink(ctx, f.token, facts[2].ID)
	if err != nil || currentLink.Recommendation == nil || currentLink.Recommendation.ID != head || currentLink.Recommendation.EstimateID != estimate.ID {
		t.Fatal("actual current-consumption-only chain was omitted", currentLink, err)
	}
	before := f.calls.Load()
	if _, err := f.s.ReviewObservation(ctx, f.token, ObservationReviewInput{AccountID: a.ID, Revision: "2", ObservationID: facts[0].ID, Excluded: true, Reason: "Retain the actual older chain"}); err != nil {
		t.Fatal(err)
	}
	newer, err := f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, head))
	if err != nil || newer.ID == head {
		t.Fatal(err)
	}
	replacement, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var body string
		if e := f.db.QueryRow(ctx, "SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM quota_observations q),(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM estimate_results e),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM wallet_recommendations r),(SELECT count(*) FROM management_events))::text").Scan(&body); e != nil {
			t.Fatal(e)
		}
		return body
	}
	original := snapshot()
	link, err := replacement.GetObservationRecommendationLink(ctx, f.token, facts[0].ID)
	if err != nil || link.Recommendation == nil || link.Recommendation.ID != actual.ID || link.Recommendation.EstimateID != estimate.ID || snapshot() != original || f.calls.Load() != before {
		t.Fatal("later unrelated advice/replacement rewrote history or read caused effects", link, err)
	}
}

func TestObservationRecommendationLinkRejectsInventedAndMistypedChains(t *testing.T) {
	f, a, facts, estimate := observationRecommendationFixture(t)
	ctx := context.Background()
	actual, err := f.s.RecomputeWallet(ctx, f.token, walletExpected(f.p, f.c, "0"))
	if err != nil {
		t.Fatal(err)
	}
	b := passiveManaged(t, f.s, f.token, f.c, 2)
	exec(t, f.db, "INSERT INTO estimate_config(account_id) VALUES ($1)", b.ID)
	var foreign string
	body, _ := json.Marshal(estimate.EstimateSnapshot)
	if err = f.db.QueryRow(ctx, "INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES ($1,now(),$2) RETURNING id::text", b.ID, body).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(actual.WalletRecommendationSnapshot)
	for name, contributions := range map[string]any{
		"wrong-account":          []any{map[string]any{"account_id": b.ID, "estimate_id": estimate.ID}},
		"cross-account-estimate": []any{map[string]any{"account_id": a.ID, "estimate_id": foreign}},
		"numeric-estimate":       []any{map[string]any{"account_id": a.ID, "estimate_id": json.Number(estimate.ID)}},
		"missing-estimate":       []any{map[string]any{"account_id": a.ID, "estimate_id": "9223372036854775807"}},
		"object-estimate":        []any{map[string]any{"account_id": a.ID, "estimate_id": map[string]any{estimate.ID: true}}},
		"object-contributions":   map[string]any{"account_id": a.ID, "estimate_id": estimate.ID},
		"head-only":              []any{map[string]any{"account_id": a.ID, "observation_head": facts[0].ID}},
	} {
		t.Run(name, func(t *testing.T) {
			value, _ := json.Marshal(contributions)
			exec(t, f.db, "INSERT INTO wallet_recommendations(participant_id,binding_id,generated_at,snapshot) VALUES ($1,$2,now(),$3::jsonb || jsonb_build_object('contributions',$4::jsonb))", f.p.ID, f.p.Binding.ID, payload, value)
			link, e := f.s.GetObservationRecommendationLink(ctx, f.token, facts[0].ID)
			if e != nil || link.Recommendation == nil || link.Recommendation.ID != actual.ID {
				t.Fatal("invalid retained contribution invented a later chain", link, e)
			}
		})
	}
	for name, window := range map[string]any{
		"head-only":       map[string]any{"observation_head": facts[0].ID},
		"numeric-current": map[string]any{"current_observation_id": json.Number(facts[0].ID)},
		"numeric-samples": map[string]any{"observation_ids": []any{json.Number(facts[0].ID)}},
		"object-samples":  map[string]any{"observation_ids": map[string]any{facts[0].ID: true}},
	} {
		t.Run(name, func(t *testing.T) {
			value, _ := json.Marshal([]any{window})
			var id string
			if e := f.db.QueryRow(ctx, "INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES ($1,now(),$2::jsonb || jsonb_build_object('windows',$3::jsonb)) RETURNING id::text", a.ID, body, value).Scan(&id); e != nil {
				t.Fatal(e)
			}
			exec(t, f.db, "INSERT INTO wallet_recommendations(participant_id,binding_id,generated_at,snapshot) VALUES ($1,$2,now(),$3::jsonb || jsonb_build_object('contributions',jsonb_build_array(jsonb_build_object('account_id',$4::text,'estimate_id',$5::text))))", f.p.ID, f.p.Binding.ID, payload, a.ID, id)
			link, e := f.s.GetObservationRecommendationLink(ctx, f.token, facts[0].ID)
			if e != nil || link.Recommendation == nil || link.Recommendation.ID != actual.ID {
				t.Fatal("head or malformed observation reference invented a later chain", link, e)
			}
		})
	}
}

func TestObservationRecommendationLinkPreservesAdministratorHistoryBoundary(t *testing.T) {
	s, db, token, c, calls := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	if _, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "all-read-scoped", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"observations", "estimates", "balances"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}}); err != nil {
		t.Fatal(err)
	}
	_, ordinary, err := s.auth.Login(ctx, "all-read-scoped", "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	for _, id := range []string{fact.ID, "9223372036854775807"} {
		link, e := s.GetObservationRecommendationLink(ctx, ordinary, id)
		if !errors.Is(e, auth.ErrForbidden) || link.ObservationID != "" || link.Recommendation != nil {
			t.Fatal("ordinary reader probed administrator history", link, e)
		}
	}
	for _, id := range []string{"", "0", "01", "9223372036854775808"} {
		if _, e := s.GetObservationRecommendationLink(ctx, token, id); !errors.Is(e, auth.ErrInput) {
			t.Fatal("invalid observation ID accepted", id, e)
		}
	}
	if _, e := s.GetObservationRecommendationLink(ctx, token, "9223372036854775807"); !errors.Is(e, auth.ErrNotFound) {
		t.Fatal("absent administrator reference was not 404", e)
	}
	if calls.Load() != before {
		t.Fatal("reference read contacted upstream")
	}
}

func TestObservationRecommendationLinkRejectsExpiryAfterActualQueryWait(t *testing.T) {
	for _, table := range []string{"quota_observations", "estimate_results", "wallet_recommendations"} {
		t.Run(table, func(t *testing.T) {
			s, db, token, c, _ := estimateFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a := passiveManaged(t, s, token, c, 1)
			fact := collectObservation(t, s, db, token, c, a)
			if _, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c)); err != nil {
				t.Fatal(err)
			}
			barrier, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			if _, err = barrier.Exec(ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				link, e := s.GetObservationRecommendationLink(ctx, token, fact.ID)
				if link.ObservationID != "" || link.AccountID != "" || link.Recommendation != nil {
					e = errors.New("expired query exposed protected reference")
				}
				done <- e
			}()
			awaitMaintenanceLock(t, db, table)
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("actual query wait retained expired authority", err)
			}
		})
	}
}
