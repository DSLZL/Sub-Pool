package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate never changes an applied migration or resets existing data.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return apply(ctx, pool, migrations)
}

// MigrationManifest identifies the complete schema understood by this binary.
func MigrationManifest() (map[string]string, error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		return nil, errors.New("migration source unavailable")
	}
	result := make(map[string]string, len(names))
	for _, name := range names {
		body, err := migrations.ReadFile(name)
		if err != nil {
			return nil, err
		}
		result[name] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	return result, nil
}

func apply(ctx context.Context, pool *pgxpool.Pool, source fs.FS) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, runtimeLock); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7210461101)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, digest text NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(source, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		return errors.New("migration source unavailable")
	}
	sort.Strings(names)
	known := make(map[string]string, len(names))
	for _, name := range names {
		body, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		known[name] = digest
		var applied string
		err = tx.QueryRow(ctx, `SELECT digest FROM schema_migrations WHERE name=$1`, name).Scan(&applied)
		if err == nil {
			if applied != digest {
				return fmt.Errorf("applied migration changed: %s", name)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name,digest) VALUES ($1,$2)`, name, digest); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT name,digest FROM schema_migrations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, digest string
		if err = rows.Scan(&name, &digest); err != nil {
			return err
		}
		if known[name] != digest {
			return errors.New("database migration version is newer than this service")
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return tx.Commit(ctx)
}
