package connection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/database"
)

func TestCollectorUnitCannotCrossRuntimeReplacement(t *testing.T) {
	s, pool, _ := setup(t)
	ctx := context.Background()
	gate, err := database.NewRuntime(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	s.Runtime = gate
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	done := make(chan error, 1)
	err = gate.Exclusive(ctx, func(*pgxpool.Conn) error {
		go func() { _, e := s.collectorCycle(ctx, conn); done <- e }()
		select {
		case e := <-done:
			t.Fatalf("collector entered replacement: %v", e)
		case <-time.After(30 * time.Millisecond):
		}
		var untouched bool
		if e := pool.QueryRow(ctx, `SELECT heartbeat_at IS NULL FROM monitoring_worker`).Scan(&untouched); e != nil || !untouched {
			t.Fatalf("collector wrote during replacement: %v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, database.ErrRuntimeChanged) {
		t.Fatal("stale worker unit continued", err)
	}
	if _, err = s.collectorCycle(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var heartbeat bool
	if err = pool.QueryRow(ctx, `SELECT heartbeat_at IS NOT NULL FROM monitoring_worker`).Scan(&heartbeat); err != nil || !heartbeat {
		t.Fatal("fresh collector unit did not run", err)
	}
}
