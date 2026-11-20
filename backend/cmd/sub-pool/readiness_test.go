package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/connection"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/notifications"
	"sub-pool/backend/internal/testdb"
)

func TestReadinessRequiresMatchingMigrationsAndRunningWorkers(t *testing.T) {
	ctx := context.Background()
	fixture := testdb.Open(t)
	config := fixture.Config()
	config.ConnConfig.RuntimeParams["application_name"] = "sub-pool-" + strings.Repeat("A", 22)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	runtime, err := database.NewRuntime(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	manifest, err := database.MigrationManifest()
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.New(pool)
	collector, err := connection.New(pool, identity, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := notifications.New(pool, identity, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	collector.Runtime, mail.Runtime = runtime, runtime
	probe := readiness(runtime, pool, manifest, &collector.Heartbeat, &mail.Heartbeat)
	status := func() int {
		res := httptest.NewRecorder()
		probe.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
		return res.Code
	}
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("before workers: got %d", got)
	}
	workers, cancel := context.WithCancel(ctx)
	var done sync.WaitGroup
	done.Add(2)
	go func() { defer done.Done(); collector.RunCollector(workers) }()
	go func() { defer done.Done(); mail.Run(workers) }()
	defer func() { cancel(); done.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for !collector.Heartbeat.Recent(time.Minute) || !mail.Heartbeat.Recent(time.Minute) {
		if time.Now().After(deadline) {
			t.Fatal("background workers did not complete a cycle")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("healthy worker cycles: got %d", got)
	}
	const migration = "migrations/001_auth.sql"
	if _, err = pool.Exec(ctx, `UPDATE schema_migrations SET digest='mismatch' WHERE name=$1`, migration); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("schema mismatch: got %d", got)
	}
	if _, err = pool.Exec(ctx, `UPDATE schema_migrations SET digest=$2 WHERE name=$1`, migration, manifest[migration]); err != nil {
		t.Fatal(err)
	}
	cancel()
	done.Wait()
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("stopped workers: got %d", got)
	}
	live := httptest.NewRecorder()
	connection.ProcessHealth(live, httptest.NewRequest(http.MethodGet, "/health", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness: got %d", live.Code)
	}
}
