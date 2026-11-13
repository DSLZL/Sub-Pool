package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestPoolChangesRejectUnresolvedRequestsForOldAndNewMembers(t *testing.T) {
	for _, target := range []string{"old", "new"} {
		t.Run(target, func(t *testing.T) {
			s, db, token := setup(t)
			ctx := context.Background()
			server, native := poolUpstream(t)
			c := save(t, s, token, server.URL)
			a, b := adoptManaged(t, s, token, c, 1, "One"), adoptManaged(t, s, token, c, 2, "Two")
			p, q := createParticipant(t, s, token, c, 21, "Old member"), createParticipant(t, s, token, c, 22, "New member")
			old := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"50"})})
			member := p
			if target == "new" {
				member = q
			}
			exec(t, db, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot)
				SELECT id,'AAAAAAAAAAAAAAAAAAAAAA',$3,$1,$2,now(),'{}'::jsonb FROM auth_users WHERE role='admin'`, member.ID, member.Binding.ID, strings.Repeat("a", 64))
			counts := func() [4]int {
				var v [4]int
				if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM pool_contracts),(SELECT count(*) FROM pool_events),
					(SELECT count(*) FROM management_events),(SELECT count(*) FROM participant_bindings)`).Scan(&v[0], &v[1], &v[2], &v[3]); err != nil {
					t.Fatal(err)
				}
				return v
			}
			before, calls := counts(), native.calls.Load()
			for _, snapshot := range []string{`{}`, `{"state":"unknown","dispatch_state":"dispatched"}`, `{"state":"rejected","dispatch_state":"dispatched"}`, `{"state":"rejected"}`} {
				exec(t, db, `UPDATE wallet_application_requests SET snapshot=$1`, snapshot)
				operations := []func() error{
					func() error {
						_, err := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{b}, []Participant{member}, []string{"25"})})
						return err
					},
					func() error {
						_, err := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{p, q}, []string{"25", "50"}), ID: old.PoolID, Revision: old.Revision})
						return err
					},
				}
				if target == "old" {
					operations = append(operations, func() error {
						_, err := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{}, []string{}), ID: old.PoolID, Revision: old.Revision})
						return err
					}, func() error { _, err := s.DisablePool(ctx, token, old.PoolID, old.Revision); return err })
				}
				for _, operation := range operations {
					var failure *auth.Failure
					if err := operation(); !errors.As(err, &failure) || failure.Code != "POOL_CHANGE_BLOCKED" {
						t.Fatal("pool change accepted an unresolved member request", err)
					}
					current, err := s.GetPool(ctx, token, old.PoolID)
					if err != nil || !reflect.DeepEqual(current, old) || counts() != before || native.calls.Load() != calls {
						t.Fatal("denied pool change mutated contracts, bindings, audit or native state", err)
					}
				}
			}
			exec(t, db, `UPDATE wallet_application_requests SET snapshot='{"state":"rejected","dispatch_state":"not_dispatched"}'`)
			next := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a, b}, []Participant{p, q}, []string{"25", "50"}), ID: old.PoolID, Revision: old.Revision})
			if next.Revision != "2" || *next.UnallocatedPercent != "25" || native.calls.Load() != calls {
				t.Fatal("known undispatched rejection prevented a local contract change")
			}
		})
	}
}

func TestPoolObligationDenialRechecksExpiryAfterPoolLockWait(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, _ := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "One")
	p := createParticipant(t, s, token, c, 21, "Member")
	old := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"50"})})
	exec(t, db, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot)
		SELECT id,'AAAAAAAAAAAAAAAAAAAAAA',$3,$1,$2,now(),'{}'::jsonb FROM auth_users WHERE role='admin'`, p.ID, p.Binding.ID, strings.Repeat("a", 64))
	barrier, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, `SELECT id FROM pools WHERE id=$1 FOR UPDATE`, old.PoolID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	digest := sha256.Sum256([]byte(token))
	exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() { _, err := s.DisablePool(ctx, token, old.PoolID, old.Revision); done <- err }()
	observePoolWait(t, db, "SELECT revision::text FROM pools WHERE id=%FOR UPDATE")
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired caller learned a pending-operation result", err)
	}
	var revision string
	var enabled bool
	if err = db.QueryRow(ctx, `SELECT revision::text,enabled FROM pools WHERE id=$1`, old.PoolID).Scan(&revision, &enabled); err != nil || revision != old.Revision || !enabled {
		t.Fatal("expired pool change altered current state", err)
	}
}
