package connection

import (
	"context"
	"errors"
	"net/netip"
	"sub-pool/backend/internal/auth"
	"testing"
)

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
