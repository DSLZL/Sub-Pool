package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sub-pool/backend/internal/auth"
	"testing"
	"time"
)

func seedCapacityMembers(t *testing.T, db *pgxpool.Pool, source string, first, last int, at time.Time) []PoolShareInput {
	t.Helper()
	rows, err := db.Query(context.Background(), `WITH inputs AS MATERIALIZED (
		SELECT n,translate(encode(int8send(0)||int8send(n),'base64'),'+/=','-_') AS id FROM generate_series($2::bigint,$3::bigint) n
	), added AS (
		INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at)
		SELECT id,'Capacity member '||n,'','',true,$4,$4 FROM inputs RETURNING id
	)
	INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at)
	SELECT added.id,$1,inputs.n,'{}'::jsonb,$4 FROM added JOIN inputs USING(id) ORDER BY inputs.n
	RETURNING participant_id`, source, first, last, at)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []PoolShareInput
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, PoolShareInput{PoolMemberInput: PoolMemberInput{ID: id, Revision: "1"}, SharePercent: "0"})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) != last-first+1 {
		t.Fatal("seeded wrong participant count", len(out))
	}
	return out
}

func TestPoolLargeParticipantCapacityPagesAndAtomicUpdate(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	upstream, _ := poolUpstream(t)
	c := save(t, s, token, upstream.URL)
	account := adoptManaged(t, s, token, c, 1, "Capacity root account")
	input := poolInput(c, []ManagedAccount{account}, nil, nil)
	input.Participants = seedCapacityMembers(t, db, *c.SourceID, 1000, 1999, time.Now().UTC().Add(-time.Minute))
	for i := range input.Participants {
		input.Participants[i].SharePercent = "0.1"
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) <= 65_536 {
		t.Fatal("test did not cross the original JSON body limit", len(body), err)
	}
	const origin = "http://127.0.0.1:3000"
	boundary, err := auth.NewHandler(s.auth, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pools/create", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: token})
	w := httptest.NewRecorder()
	(&PoolsHandler{Service: s, Boundary: boundary}).ServeHTTP(w, r)
	var initial PoolContract
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &initial) != nil || len(initial.Participants) != 1000 || initial.UnallocatedPercent == nil || *initial.UnallocatedPercent != "0" {
		t.Fatal("large contract failed", w.Code, len(initial.Participants))
	}

	seen := map[string]bool{}
	for after := ""; ; {
		page, err := s.ListPoolMembers(ctx, token, initial.ID, after, 97)
		if err != nil || page.ID != initial.ID || page.UnallocatedPercent == nil || *page.UnallocatedPercent != "0" {
			t.Fatal("page did not retain the complete contract remainder", err)
		}
		for _, member := range page.Participants {
			if seen[member.ID] || member.SharePercent != "0.1" {
				t.Fatal("wrong member projection", member)
			}
			seen[member.ID] = true
		}
		if page.NextAfter == nil {
			break
		}
		after = *page.NextAfter
	}
	if len(seen) != 1000 {
		t.Fatal("incomplete member traversal", len(seen))
	}

	first, last := initial.Participants[0].ID, initial.Participants[999].ID
	reader, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "capacity-reader", Password: "ReaderPassword123", Enabled: true,
		Grants: auth.Grants{Features: []string{"pools", "balances"}, AccountIDs: []string{account.ID}, ParticipantIDs: []string{first, last}}})
	if err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := s.auth.Login(ctx, reader.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	for after, want := "", first; ; after, want = first, last {
		page, err := s.ListPoolMembers(ctx, memberToken, initial.ID, after, 1)
		if err != nil || len(page.Participants) != 1 || page.Participants[0].ID != want || page.UnallocatedPercent != nil || page.CurrentRevision != nil {
			t.Fatal("payload escaped the authorized scope", err)
		}
		if want == last {
			if page.NextAfter != nil {
				t.Fatal("scope leaked a third participant")
			}
			break
		}
		if page.NextAfter == nil || *page.NextAfter != first {
			t.Fatal("missing scoped continuation")
		}
	}

	update := PoolUpdate{ID: initial.PoolID, Revision: initial.Revision, PoolInput: input}
	for i := range update.Participants {
		if update.Participants[i].ID == last {
			update.Participants[i].Revision = "2"
		}
	}
	if _, err := s.SavePool(ctx, token, update); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("late stale member accepted", err)
	}
	unchanged, err := s.GetPool(ctx, token, initial.PoolID)
	if err != nil || unchanged.ID != initial.ID || len(unchanged.Participants) != 1000 {
		t.Fatal("failed update changed the durable contract", err)
	}
	exit, err := s.ExitParticipant(ctx, token, last, "1", initial.Participants[999].Name)
	if err != nil || len(exit.PoolContractIDs) != 1 {
		t.Fatal("large contract exit failed", err)
	}
	current, err := s.GetPool(ctx, token, initial.PoolID)
	if err != nil || len(current.Participants) != 999 || current.UnallocatedPercent == nil || *current.UnallocatedPercent != "0.1" {
		t.Fatal("large exit lost exact remainder", err)
	}
	previous, err := s.GetPoolContract(ctx, token, initial.ID)
	if err != nil || previous.IsCurrent || len(previous.Participants) != 1000 {
		t.Fatal("large exit rewrote historical membership", err)
	}
	receipt, err := s.GetParticipantExit(ctx, token, last, "1")
	if err != nil || len(receipt.PoolContractIDs) != 1 || receipt.PoolContractIDs[0] != current.ID {
		t.Fatal("immutable exit receipt missing", err)
	}
}

func TestParticipantExitTraversesMoreThanFiveHundredContracts(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	upstream, _ := poolUpstream(t)
	c := save(t, s, token, upstream.URL)
	member := createParticipant(t, s, token, c, 21, "Participant in many contracts")
	for i := 1000; i < 1501; i++ {
		account := adoptManaged(t, s, token, c, i, "Root account")
		savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{account}, []Participant{member}, []string{"25"})})
	}
	result, err := s.ExitParticipant(ctx, token, member.ID, member.Revision, member.Name)
	if err != nil || len(result.PoolContractIDs) != 501 {
		t.Fatal("large membership set was not exited atomically", len(result.PoolContractIDs), err)
	}
	saved, err := s.GetParticipantExit(ctx, token, member.ID, member.Revision)
	if err != nil || len(saved.PoolContractIDs) != 501 {
		t.Fatal("large exit receipt unavailable", len(saved.PoolContractIDs), err)
	}
	for _, id := range []string{saved.PoolContractIDs[0], saved.PoolContractIDs[500]} {
		contract, err := s.GetPoolContract(ctx, token, id)
		if err != nil || len(contract.Participants) != 0 || contract.UnallocatedPercent == nil || *contract.UnallocatedPercent != "100" {
			t.Fatal("exit did not release the full share", id, err)
		}
	}
}
