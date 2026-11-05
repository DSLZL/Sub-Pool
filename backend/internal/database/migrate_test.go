package database

import (
	"context"
	"sync"
	"testing"
	"testing/fstest"

	"sub-pool/backend/internal/testdb"
)

func TestMigrationsConcurrentRepeatAndDrift(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Migrate(ctx, pool) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET digest='changed'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("accepted changed applied migration")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('auth_users') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatal("drift check damaged existing tables")
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	source := fstest.MapFS{
		"migrations/001.sql": {Data: []byte(`CREATE TABLE rollback_probe(id integer)`)},
		"migrations/002.sql": {Data: []byte(`THIS IS INVALID SQL`)},
	}
	if err := apply(ctx, pool, source); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	var clean bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('rollback_probe') IS NULL AND to_regclass('schema_migrations') IS NULL`).Scan(&clean); err != nil || !clean {
		t.Fatal("migration failure was not atomic")
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations VALUES ('migrations/future.sql','future')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("accepted unknown newer database version")
	}
}
