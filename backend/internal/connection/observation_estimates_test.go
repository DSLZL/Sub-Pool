package connection

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

func TestObservationEstimateLinkKeepsFrozenHistoryAndBothCurrentPermissions(t *testing.T) {
	s, db, token, config, calls := estimateFixture(t)
	ctx := context.Background()
	account := passiveManaged(t, s, token, config, 1)
	first := collectObservation(t, s, db, token, config, account)
	head := collectObservation(t, s, db, token, config, account)
	window := EstimateWindow{Kind: "short", Basis: "official", ObservationIDs: []string{first.ID},
		CurrentObservationID: &first.ID, Result: estimation.Result{State: "insufficient_samples"}}
	archived, err := seedArchivedEstimate(ctx, db, account, config, []EstimateWindow{window})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "frozen-estimate-reader", Password: "ReaderPassword123", Enabled: true,
		Grants: auth.Grants{Features: []string{"observations", "estimates"}, AccountIDs: []string{account.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	linked, err := s.GetObservationEstimateLink(ctx, reader, first.ID)
	if err != nil || linked.Estimate == nil || linked.Estimate.ID != archived.ID {
		t.Fatal(linked, err)
	}
	if current, err := s.GetObservationEstimateLink(ctx, reader, head.ID); err != nil || current.Estimate != nil {
		t.Fatal("head alone must not prove frozen use", current, err)
	}
	if _, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true,
		Grants: auth.Grants{Features: []string{"observations"}, AccountIDs: []string{account.ID}, ParticipantIDs: []string{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetObservationEstimateLink(ctx, reader, first.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("a grant on both features is required", err)
	}
	if calls.Load() != before {
		t.Fatal("archived lookup touched upstream")
	}
}
