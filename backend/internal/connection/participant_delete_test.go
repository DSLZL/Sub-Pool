package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestParticipantDeletionReceiptRequiresCommittedExactVersionAndCurrentAdmin(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Error("delete contacted native writer")
		}
		respond(w, nativeUserRow(21))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	p := createParticipant(t, s, token, c, 21, "Unused member")
	if _, err := s.GetParticipantDeletion(ctx, token, p.ID, p.Revision); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("an active or merely absent member was treated as a deletion", err)
	}
	if err := s.DeleteParticipant(ctx, token, p.ID, p.Revision, "wrong"); !errors.Is(err, auth.ErrInput) {
		t.Fatal(err)
	}
	before := calls.Load()
	if err := s.DeleteParticipant(ctx, token, p.ID, p.Revision, p.Name); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.GetParticipantDeletion(ctx, token, p.ID, p.Revision)
	if err != nil || receipt.ParticipantID != p.ID || receipt.PreviousRevision != p.Revision || receipt.Revision != "2" ||
		receipt.BindingID != p.Binding.ID || receipt.Name != p.Name || receipt.EffectiveAt.Before(p.SavedAt) {
		t.Fatalf("wrong exact deletion receipt: %+v %v", receipt, err)
	}
	for _, revision := range []string{"2", "9223372036854775807"} {
		if _, err := s.GetParticipantDeletion(ctx, token, p.ID, revision); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("wrong original version produced a receipt", err)
		}
	}
	if _, err := s.GetParticipantDeletion(ctx, "", p.ID, p.Revision); err == nil {
		t.Fatal("anonymous receipt read succeeded")
	}
	if _, err := s.GetParticipant(ctx, token, p.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("deleted member stayed active", err)
	}
	bindings, err := s.ListParticipantBindings(ctx, token, p.ID, "0", 100)
	if err != nil || len(bindings.Bindings) != 1 || bindings.Bindings[0].EndedAt == nil ||
		!bindings.Bindings[0].EndedAt.Equal(receipt.EffectiveAt) {
		t.Fatal("binding history was lost", err)
	}
	again, err := s.GetParticipantDeletion(ctx, token, p.ID, p.Revision)
	if err != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatal("read-only recovery changed its receipt", err)
	}
	if err := s.DeleteParticipant(ctx, token, p.ID, p.Revision, p.Name); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("deleted action was replayed", err)
	}
	var events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM participant_events WHERE participant_id=$1 AND action='participant.delete'", p.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("duplicate delete event", err)
	}
	raw, _ := json.Marshal(receipt)
	if calls.Load() != before || len(raw) == 0 {
		t.Fatal("recovery contacted upstream or lost its record")
	}
}
