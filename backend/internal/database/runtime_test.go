package database

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/testdb"
)

func runtimeFixture(t *testing.T) (*pgxpool.Pool, *Runtime) {
	t.Helper()
	pool := testdb.Open(t)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	gate, err := NewRuntime(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.Close)
	return pool, gate
}

func TestRuntimeLostLifetimeSessionDeniesPoolReconnect(t *testing.T) {
	base := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := Migrate(ctx, base); err != nil {
		t.Fatal(err)
	}
	config := base.Config()
	base.Close()
	var owner atomic.Pointer[Runtime]
	config.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		gate := owner.Load()
		return gate == nil || gate.CheckConnection(ctx, conn)
	}
	config.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		if gate := owner.Load(); gate != nil && !gate.Available() {
			return ErrRuntimeUnavailable
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	gate, err := NewRuntime(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	owner.Store(gate)
	t.Cleanup(gate.Close)
	if err = pool.Ping(ctx); err != nil {
		t.Fatal("live lifetime owner was not accepted", err)
	}
	observer, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(context.Background())
	var terminated bool
	err = observer.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2 AND application_name=$3 AND datname=current_database()`, gate.pid, gate.startedAt, gate.name).Scan(&terminated)
	if err != nil || !terminated {
		t.Fatal("owned lifetime session was not terminated", err)
	}
	for {
		var alive bool
		if err = observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2)`, gate.pid, gate.startedAt).Scan(&alive); err != nil {
			t.Fatal(err)
		}
		if !alive {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned lifetime session did not exit")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err = pool.Ping(ctx); err == nil || gate.Available() {
		t.Fatal("pool reconnected without its lifetime exclusion", err)
	}
	if _, err = gate.Enter(ctx); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatal("lost owner still admitted work", err)
	}
}

func TestRuntimeDrainsWorkAndRejectsRequestsWaitingAcrossReplacement(t *testing.T) {
	_, gate := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := gate.Enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exclusive, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- gate.Exclusive(ctx, func(*pgxpool.Conn) error {
			close(exclusive)
			<-finish
			return nil
		})
	}()
	select {
	case <-exclusive:
		t.Fatal("exclusive work crossed an active domain unit")
	default:
	}
	release()
	select {
	case <-exclusive:
	case <-ctx.Done():
		t.Fatal("exclusive work did not drain the domain unit")
	}
	started, pending := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		unlock, err := gate.Enter(ctx)
		if err == nil {
			unlock()
		}
		pending <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // Let the real reader wait on the held write lock.
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-pending; !errors.Is(err, ErrRuntimeChanged) {
		t.Fatalf("old request crossed the replacement: %v", err)
	}
	release, err = gate.Enter(ctx)
	if err != nil {
		t.Fatal("fresh request remained unavailable")
	}
	release()
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := gate.Enter(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled request entered")
	}
	gate.Close()
	gate.Close()
	if _, err := gate.Enter(ctx); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatal("closed runtime admitted work")
	}
}

func TestRuntimeExcludesOtherInstancesClientsAndConcurrentMigrations(t *testing.T) {
	pool, gate := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["application_name"] = "sub-pool-other_0123456789abcdef"
	other, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRuntime(ctx, other)
	if err != nil {
		other.Close()
		t.Fatal(err)
	}
	called := false
	if err = gate.Exclusive(ctx, func(*pgxpool.Conn) error { called = true; return nil }); !errors.Is(err, ErrRuntimeBusy) || called {
		t.Fatal("another runtime was bypassed")
	}
	second.Close()
	if err = gate.Exclusive(ctx, func(*pgxpool.Conn) error { called = true; return nil }); !errors.Is(err, ErrRuntimeBusy) || called {
		t.Fatal("foreign remaining database clients were bypassed")
	}
	other.Close()
	err = gate.Exclusive(ctx, func(conn *pgxpool.Conn) error {
		migrationCtx, stop := context.WithCancel(ctx)
		result := make(chan error, 1)
		go func() { result <- Migrate(migrationCtx, pool) }()
		observed := false
		for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
			if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%pg_advisory_xact_lock_shared%')`).Scan(&observed); err != nil {
				stop()
				<-result
				return err
			}
			if observed {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		stop()
		if err := <-result; err == nil || !observed {
			t.Error("migration was not observed waiting on the actual restore barrier")
		}
		blocked, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			return err
		}
		defer blocked.Close()
		instance, err := NewRuntime(ctx, blocked)
		if instance != nil {
			instance.Close()
		}
		if !errors.Is(err, ErrRuntimeBusy) {
			t.Error("new runtime entered an active restore")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal("migration remained blocked after the restore barrier released")
	}
}
