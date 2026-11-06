package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"sub-pool/backend/internal/auth"
)

func observePoolWait(t *testing.T, db *pgxpool.Pool, pattern string) {
	t.Helper()
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		var waiting bool
		if err := db.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, pattern).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("actual PostgreSQL wait was not observed", pattern)
}

func TestPoolCaptureAndContractChangeUseActualAccountLock(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Root")
	p := createParticipant(t, s, token, c, 21, "Member")
	first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"100"})})
	f.logs[1] = []map[string]any{usageRow(1, first.EffectiveAt.Format(time.RFC3339Nano))}
	f.blocked.Store(true)
	unblock := sync.OnceFunc(func() { close(f.release) })
	defer unblock()
	captured := make(chan UsageCapture, 1)
	captureErr := make(chan error, 1)
	go func() {
		r, e := s.CaptureUsage(ctx, token, UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: first.EffectiveAt.Add(-time.Minute).Format(time.RFC3339Nano), EndAt: first.EffectiveAt.Add(time.Hour).Format(time.RFC3339Nano)})
		captured <- r
		captureErr <- e
	}()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not reach actual upstream barrier")
	}
	changed := make(chan PoolContract, 1)
	changeErr := make(chan error, 1)
	go func() {
		r, e := s.SavePool(ctx, token, PoolUpdate{ID: first.PoolID, Revision: first.Revision, PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"50"})})
		changed <- r
		changeErr <- e
	}()
	observePoolWait(t, db, "%FROM managed_accounts WHERE id=$1 FOR UPDATE")
	unblock()
	capture := <-captured
	if err := <-captureErr; err != nil {
		t.Fatal(err)
	}
	next := <-changed
	if err := <-changeErr; err != nil || next.Revision != "2" {
		t.Fatal("waiting contract did not commit after capture", err)
	}
	evidence, err := s.GetPoolUsageEvidence(ctx, token, capture.ID, "0", 100)
	if err != nil || len(evidence.Records) != 1 || evidence.Records[0].ContractID == nil || *evidence.Records[0].ContractID != first.ID {
		t.Fatal("capture used contract created after its locked observation", evidence, err)
	}
}

func TestPoolContractCommitPreventsWaitingParticipantRebind(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Root")
	p := createParticipant(t, s, token, c, 21, "Member")
	barrier, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "LOCK TABLE pool_events IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	contractDone := make(chan error, 1)
	go func() {
		_, e := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"100"})})
		contractDone <- e
	}()
	observePoolWait(t, db, "INSERT INTO pool_events%")
	calls := f.calls.Load()
	rebind := participantEdit(p, c)
	rebind.UpstreamID = "22"
	rebindDone := make(chan error, 1)
	go func() { _, e := s.SaveParticipant(ctx, token, rebind); rebindDone <- e }()
	observePoolWait(t, db, "SELECT p.id,p.revision,p.name%")
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-contractDone; err != nil {
		t.Fatal(err)
	}
	if err = <-rebindDone; !errors.Is(err, auth.ErrConflict) || f.calls.Load() != calls {
		t.Fatal("waiting rebind missed committed pool history or dispatched new user read", err)
	}
}

func TestPoolActualLockExpiryReturnsNoProtectedResultOrPersistence(t *testing.T) {
	for _, operation := range []string{"save", "read", "commit"} {
		t.Run(operation, func(t *testing.T) {
			s, db, token := setup(t)
			ctx := context.Background()
			server, _ := poolUpstream(t)
			c := save(t, s, token, server.URL)
			a := adoptManaged(t, s, token, c, 1, "Root")
			first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, nil, nil)})
			barrier, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(ctx)
			if operation == "commit" {
				_, err = barrier.Exec(ctx, "LOCK TABLE pool_events IN ACCESS EXCLUSIVE MODE")
			} else {
				_, err = barrier.Exec(ctx, `SELECT id FROM pools WHERE id=$1 FOR UPDATE`, first.PoolID)
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				var result PoolContract
				var e error
				if operation != "read" {
					input := poolInput(c, []ManagedAccount{a}, nil, nil)
					input.Name = "Expired"
					result, e = s.SavePool(ctx, token, PoolUpdate{ID: first.PoolID, Revision: first.Revision, PoolInput: input})
				} else {
					result, e = s.GetPool(ctx, token, first.PoolID)
				}
				if result.PoolID != "" {
					done <- errors.New("expired wait returned protected pool data")
					return
				}
				done <- e
			}()
			pattern := "SELECT revision::text FROM pools WHERE id=$1 FOR UPDATE"
			if operation == "read" {
				pattern = "SELECT id FROM pools WHERE id=$1 FOR SHARE"
			}
			if operation == "commit" {
				pattern = "INSERT INTO pool_events%"
			}
			observePoolWait(t, db, pattern)
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("expired actual wait was accepted", err)
			}
			var name, revision string
			var count int
			if err = db.QueryRow(ctx, `SELECT name,revision::text FROM pools WHERE id=$1`, first.PoolID).Scan(&name, &revision); err != nil || name != first.Name || revision != first.Revision {
				t.Fatal("expired edit changed pool")
			}
			if err = db.QueryRow(ctx, `SELECT count(*) FROM pool_contracts WHERE pool_id=$1`, first.PoolID).Scan(&count); err != nil || count != 1 {
				t.Fatal("expired wait created contract history")
			}
		})
	}
}
