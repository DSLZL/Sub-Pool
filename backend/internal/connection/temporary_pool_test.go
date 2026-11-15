package connection

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTemporaryRoundFreezeSerializesPoolWriters(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	r, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	var id, revision string
	if err = f.db.QueryRow(ctx, "SELECT id,revision::text FROM pools ORDER BY id LIMIT 1").Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	in := PoolUpdate{ID: id, Revision: revision, PoolInput: PoolInput{SourceID: r.Scope.SourceID, ConnectionRevision: r.Scope.ConnectionRevision, Name: "owned frozen pool", Enabled: true, Accounts: []PoolMemberInput{{ID: r.Scope.Accounts[0].AccountID, Revision: r.Scope.Accounts[0].AccountRevision}}, Participants: []PoolShareInput{}}}
	for _, p := range r.Scope.Accounts[0].Members {
		in.Participants = append(in.Participants, PoolShareInput{PoolMemberInput: PoolMemberInput{ID: p.ParticipantID, Revision: "1"}, SharePercent: "50"})
	}
	for _, action := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"save", func(ctx context.Context) error { _, err := f.s.SavePool(ctx, f.token, in); return err }},
		{"disable", func(ctx context.Context) error { _, err := f.s.DisablePool(ctx, f.token, id, revision); return err }},
	} {
		t.Run(action.name, func(t *testing.T) {
			tx, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, temporaryLock); err != nil {
				t.Fatal(err)
			}
			waiting, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
			err = action.run(waiting)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("pool mutation bypassed the round-freeze lock", err)
			}
			if err = tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = action.run(ctx); !errors.Is(err, errPoolChangeBlocked) {
				t.Fatal("active frozen obligations did not block pool mutation", err)
			}
		})
	}
	if f.writes.Load() != 0 {
		t.Fatal("lifecycle guard dispatched money")
	}
}
