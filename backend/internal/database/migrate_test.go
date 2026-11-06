package database

import (
	"context"
	"io/fs"
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

func TestSecurityUpgradePreservesExistingHistoryAndBackfillsOnce(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/006_security.sql" {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := apply(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	const actor = "AAAAAAAAAAAAAAAAAAAAAA"
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_users(id,username,role,password_hash,created_at,read_features) VALUES ($1,'legacy.admin','admin','preserved-hash',now(),ARRAY['overview']);
	`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_events(occurred_at,action,outcome,user_id,target_user_id,source_ip) VALUES (now(),'bootstrap','success',$1,NULL,'local'),(now(),'user.update','success',$1,$1,'127.0.0.1')`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_sources VALUES ($1,'https://example.invalid',now())`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO system_events(occurred_at,actor_id,source_id,revision,action) VALUES (now(),$1,$1,2,'connection.save')`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES ($1,$1,1,'Preserved',false,'active','unverified','{}','{}',now(),now(),1,now(),now())`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_events(occurred_at,actor_id,account_id,revision,action) VALUES (now(),$1,$1,2,'account.update')`, actor); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM management_events)=4 AND (SELECT count(*) FROM auth_events)=2 AND (SELECT count(*) FROM managed_accounts)=1 AND (SELECT password_hash='preserved-hash' AND read_features=ARRAY['overview'] FROM auth_users WHERE id=$1) AND NOT (SELECT network_diagnostics_enabled FROM security_config) AND (SELECT count(*) FROM source_blocks)=0 AND NOT EXISTS(SELECT 1 FROM auth_events WHERE source_kind<>'legacy')`, actor).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("upgrade changed history, inferred source trust or duplicated backfill: %v", err)
	}
}
