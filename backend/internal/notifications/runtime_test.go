package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/database"
)

func TestMailRecoveryCannotCrossRuntimeReplacement(t *testing.T) {
	s, token := setup(t)
	sink := newSink(t)
	c := sinkConfig(t, s, token, sink)
	n := request(t, s, token, c.Revision, 1)
	exec(t, s.pool, `UPDATE notifications SET state='dispatching',reason='DISPATCHING',started_at=clock_timestamp() WHERE id=$1`, n.ID)
	ctx := context.Background()
	gate, err := database.NewRuntime(ctx, s.pool)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	s.Runtime = gate
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	done := make(chan error, 1)
	err = gate.Exclusive(ctx, func(*pgxpool.Conn) error {
		go func() { done <- s.lead(ctx, conn) }()
		select {
		case e := <-done:
			t.Fatalf("mail entered replacement: %v", e)
		case <-time.After(30 * time.Millisecond):
		}
		var state string
		if e := s.pool.QueryRow(ctx, `SELECT state FROM notifications WHERE id=$1`, n.ID).Scan(&state); e != nil || state != "dispatching" || sink.count.Load() != 0 {
			t.Fatal("mail recovered during replacement", state, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, database.ErrRuntimeChanged) {
		t.Fatal("stale mail worker continued", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	go func() { done <- s.lead(runCtx, conn) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var state string
		if err = s.pool.QueryRow(ctx, `SELECT state FROM notifications WHERE id=$1`, n.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "unknown" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fresh worker did not recover interrupted mail")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sink.count.Load() != 0 {
		t.Fatal("interrupted mail was replayed")
	}
}
