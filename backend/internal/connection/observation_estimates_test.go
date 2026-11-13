package connection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestObservationEstimateLinkUsesFrozenWindowReferencesAndNumericHistory(t *testing.T) {
	s, db, token, c, calls := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	first := collectObservation(t, s, db, token, c, a)
	middle := collectObservation(t, s, db, token, c, a)
	last := collectObservation(t, s, db, token, c, a)
	if link, err := s.GetObservationEstimateLink(ctx, token, first.ID); err != nil || link.Estimate != nil || link.ObservationID != first.ID || link.AccountID != a.ID {
		t.Fatal("unconfigured observation acquired a reference", link, err)
	}
	saved, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
	if err != nil || !slices.Contains(estimateKind(t, saved, "short").ObservationIDs, first.ID) {
		t.Fatal("fixture did not create an actual sampled explanation", saved, err)
	}
	before := calls.Load()
	link, err := s.GetObservationEstimateLink(ctx, token, first.ID)
	if err != nil || link.Estimate == nil || link.Estimate.ID != saved.ID || !link.Estimate.GeneratedAt.Equal(saved.GeneratedAt) {
		t.Fatal("sample reference or generation time lost", link, err)
	}
	if _, err = s.ChangeObservationStart(ctx, token, ObservationStartInput{AccountID: a.ID, Revision: "1", StartObservationID: first.ID, EndObservationID: middle.ID, WindowKind: "short", Reason: "Use exact earlier native endpoints"}, false); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Latest == nil {
		t.Fatal(err)
	}
	w := estimateKind(t, *current.Latest, "short")
	if w.CurrentObservationID == nil || *w.CurrentObservationID != last.ID || slices.Contains(w.ObservationIDs, last.ID) {
		t.Fatal("fixture did not separate current consumption from selected samples", w)
	}
	link, err = s.GetObservationEstimateLink(ctx, token, last.ID)
	if err != nil || link.Estimate == nil || link.Estimate.ID != current.Latest.ID {
		t.Fatal("actual current-consumption reference was omitted", link, err)
	}
	manualID := current.Latest.ID
	if _, err = s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "2", ObservationID: first.ID, Excluded: true, Reason: "Retain original fact but exclude its use"}); err != nil {
		t.Fatal(err)
	}
	link, err = s.GetObservationEstimateLink(ctx, token, first.ID)
	if err != nil || link.Estimate == nil || link.Estimate.ID != manualID {
		t.Fatal("unrelated later explanation replaced a real historical reference", link, err)
	}
	current, err = s.GetEstimateCurrent(ctx, token, a.ID)
	if err != nil || current.Latest == nil {
		t.Fatal(err)
	}
	exec(t, db, `ALTER TABLE estimate_results ALTER COLUMN id RESTART WITH 99`)
	for _, expected := range []string{"99", "100", "9007199254740993"} {
		if expected == "9007199254740993" {
			exec(t, db, `ALTER TABLE estimate_results ALTER COLUMN id RESTART WITH 9007199254740993`)
		}
		next, e := s.RecomputeEstimate(ctx, token, a.ID, current.Configuration.Revision, current.Latest.ID)
		if e != nil || next.ID != expected {
			t.Fatal("actual explanation was not appended", next, e)
		}
		current.Latest = &next
		link, err = s.GetObservationEstimateLink(ctx, token, last.ID)
		if err != nil || link.Estimate == nil || link.Estimate.ID != expected {
			t.Fatal("reference ordering was lexical or lost ID precision", link, err)
		}
	}
	var original string
	if err = db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM quota_observations q),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM estimate_results r))::text`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateManaged(ctx, token, ManagedUpdate{ID: a.ID, Revision: a.Revision, Name: "Changed current display name", Monitoring: a.Monitoring, SourceMode: a.SourceMode}); err != nil {
		t.Fatal(err)
	}
	replacement, err := New(db, auth.New(db), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	link, err = replacement.GetObservationEstimateLink(ctx, token, first.ID)
	if err != nil || link.Estimate == nil || link.Estimate.ID != manualID {
		t.Fatal("current metadata or service replacement rewrote historical reference", link, err)
	}
	var after string
	if err = db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM quota_observations q),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM estimate_results r))::text`).Scan(&after); err != nil || after != original || calls.Load() != before {
		t.Fatal("local reference reads mutated facts or called upstream", err, calls.Load(), before)
	}
}

func TestObservationEstimateLinkRejectsHeadOnlyCrossAccountAndMissingAuthority(t *testing.T) {
	s, db, token, c, calls := estimateFixture(t)
	ctx := context.Background()
	a, b := passiveManaged(t, s, token, c, 1), passiveManaged(t, s, token, c, 2)
	first := collectObservation(t, s, db, token, c, a)
	other := collectObservation(t, s, db, token, c, b)
	saved, err := s.SaveEstimateConfiguration(ctx, token, estimateInput(a, c))
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately inconsistent retained fixtures must not create a link from a head or another account.
	body, _ := json.Marshal(saved.EstimateSnapshot)
	exec(t, db, `INSERT INTO estimate_config(account_id) VALUES ($1)`, b.ID)
	exec(t, db, `INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES ($1,now(),$2::jsonb || jsonb_build_object('observation_head',$3::text,'windows','[]'::jsonb))`, b.ID, body, other.ID)
	exec(t, db, `INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES ($1,now(),$2::jsonb || jsonb_build_object('windows',jsonb_build_array(jsonb_build_object('observation_ids',jsonb_build_object($3::text,true),'current_observation_id',$3::text::bigint))))`, b.ID, body, other.ID)
	exec(t, db, `INSERT INTO estimate_results(account_id,generated_at,snapshot) VALUES ($1,now(),$2::jsonb || jsonb_build_object('windows',jsonb_build_array(jsonb_build_object('observation_ids',jsonb_build_array($3::text)))))`, a.ID, body, other.ID)
	before := calls.Load()
	if link, e := s.GetObservationEstimateLink(ctx, token, other.ID); e != nil || link.Estimate != nil {
		t.Fatal("head-only or cross-account fixture invented a link", link, e)
	}
	if link, e := s.GetObservationEstimateLink(ctx, token, first.ID); e != nil || link.Estimate == nil || link.Estimate.ID != saved.ID {
		t.Fatal("unrelated later fixture displaced the actual reference", link, e)
	}
	for index, test := range []struct {
		features []string
		accounts []string
		want     error
	}{
		{[]string{"observations"}, []string{b.ID}, auth.ErrForbidden},
		{[]string{"estimates"}, []string{b.ID}, auth.ErrForbidden},
		{[]string{"observations", "estimates"}, []string{a.ID}, auth.ErrNotFound},
		{[]string{"observations", "estimates"}, []string{b.ID}, nil},
	} {
		name := fmt.Sprintf("link-reader-%d", index)
		if _, e := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: name, Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: test.features, AccountIDs: test.accounts, ParticipantIDs: []string{}}}); e != nil {
			t.Fatal(e)
		}
		_, scoped, e := s.auth.Login(ctx, name, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
		if e != nil {
			t.Fatal(e)
		}
		link, e := s.GetObservationEstimateLink(ctx, scoped, other.ID)
		if !errors.Is(e, test.want) || test.want != nil && link.ObservationID != "" || link.Estimate != nil {
			t.Fatal("reference escaped current feature/account scope", link, e, test.want)
		}
	}
	for _, id := range []string{"", "0", "01", "9223372036854775808"} {
		if _, e := s.GetObservationEstimateLink(ctx, token, id); !errors.Is(e, auth.ErrInput) {
			t.Fatal("invalid reference accepted", id, e)
		}
	}
	if _, e := s.GetObservationEstimateLink(ctx, token, "9223372036854775807"); !errors.Is(e, auth.ErrNotFound) {
		t.Fatal("absent reference did not match hidden 404", e)
	}
	if calls.Load() != before {
		t.Fatal("reference reads contacted upstream")
	}
}

func TestObservationEstimateLinkRejectsExpiryAfterActualQueryWait(t *testing.T) {
	for _, table := range []string{"quota_observations", "estimate_results"} {
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
			if _, err = barrier.Exec(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				link, e := s.GetObservationEstimateLink(ctx, token, fact.ID)
				if link.ObservationID != "" || link.AccountID != "" || link.Estimate != nil {
					e = errors.New("expired reference read exposed protected data")
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
