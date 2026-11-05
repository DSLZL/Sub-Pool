// Package testdb owns isolated PostgreSQL schemas for the backend's integration tests.
package testdb

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SUB_POOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PostgreSQL integration requires SUB_POOL_TEST_DATABASE_URL")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "::1" || !strings.HasPrefix(config.ConnConfig.Database, "subpool_test_") {
		t.Fatal("tests require an explicit loopback subpool_test_ database")
	}
	config.MaxConns = 8
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("test database connection unavailable")
	}
	suffix := make([]byte, 12)
	if _, err = rand.Read(suffix); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("auth_test_%x", suffix)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal("could not create isolated test schema")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatal("test schema connection unavailable")
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("isolated test schema cleanup failed")
		}
		admin.Close()
	})
	return pool
}
