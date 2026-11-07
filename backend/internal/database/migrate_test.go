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

func TestParticipantUpgradePreservesFactsWithoutInventingHistoricalAttribution(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/009_participants.sql" {
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
	if _, err := pool.Exec(ctx, `INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES ($1,'legacy.admin','admin','preserved-hash',now())`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_sources VALUES ($1,'https://example.invalid',now())`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES ($1,$1,1,'Preserved',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`, actor); err != nil {
		t.Fatal(err)
	}
	var capture int64
	if err := pool.QueryRow(ctx, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) VALUES ($1,$1,now(),'{"state":"exhausted","observed_count":"1"}') RETURNING id`, actor).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) VALUES ($1,7,'{"id":"7","user_id":"21","created_at":"2020-01-01T00:00:00Z","metrics":{"total_cost":"0.1","actual_cost":"0.2"}}')`, capture); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := pool.QueryRow(ctx, `SELECT fact::text FROM usage_capture_facts`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at) VALUES ($1,'New binding','','',true,now(),now())`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at) VALUES ($1,$1,21,'{"upstream_id":"21"}',now())`, actor); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_attributions)=1 AND (SELECT basis='mapping_not_recorded' AND participant_id IS NULL AND binding_id IS NULL AND participant_name IS NULL FROM usage_attributions) AND (SELECT fact::text=$1 FROM usage_capture_facts) AND (SELECT snapshot->>'observed_count'='1' FROM usage_captures) AND (SELECT password_hash='preserved-hash' FROM auth_users)`, original).Scan(&retained); err != nil || !retained {
		t.Fatal("upgrade/repeat/current binding rewrote earlier facts or invented ownership", err)
	}
}

func TestPoolUpgradePreservesAssignedFactsWithoutInventingContracts(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/010_pool_contracts.sql" {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err = apply(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	const actor = "AAAAAAAAAAAAAAAAAAAAAA"
	for _, statement := range []string{
		`INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES ($1,'legacy.pool.admin','admin','preserved-hash',now())`,
		`INSERT INTO connection_sources VALUES ($1,'https://example.invalid',now())`,
		`INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES($1,$1,1,'Legacy root',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`,
		`INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at) VALUES($1,'Recorded member','preserved-contact','preserved-note',true,now(),now())`,
		`INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at) VALUES($1,$1,21,'{"upstream_id":"21"}',now())`,
	} {
		if _, err = pool.Exec(ctx, statement, actor); err != nil {
			t.Fatal(err)
		}
	}
	var capture int64
	if err = pool.QueryRow(ctx, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) VALUES($1,$1,now(),'{"state":"exhausted","observed_count":"1"}') RETURNING id`, actor).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) VALUES($1,7,'{"id":"7","metrics":{"total_cost":"0.1","actual_cost":"0.2"}}')`, capture); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO usage_attributions(capture_id,upstream_id,participant_id,binding_id,participant_name,basis) VALUES($1,7,$2,1,'Recorded member','local_binding_at_log_time')`, capture, actor); err != nil {
		t.Fatal(err)
	}
	var original string
	if err = pool.QueryRow(ctx, `SELECT fact::text FROM usage_capture_facts`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var intact bool
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM pools)=0 AND (SELECT count(*) FROM pool_contracts)=0 AND (SELECT pool_contract_id IS NULL AND pool_basis='mapping_not_recorded' AND participant_id=$1 AND binding_id=1 AND participant_name='Recorded member' AND basis='local_binding_at_log_time' FROM usage_attributions) AND (SELECT fact::text=$2 FROM usage_capture_facts) AND (SELECT password_hash='preserved-hash' FROM auth_users) AND (SELECT contact='preserved-contact' FROM participants)`, actor, original).Scan(&intact); err != nil || !intact {
		t.Fatal("upgrade/repeat rewrote identity, fees or invented historical pool membership", err)
	}
}

func TestObservationUpgradePreservesSnapshotsWithoutInventingDecisions(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/011_observation_management.sql" {
			continue
		}
		body, e := fs.ReadFile(migrations, name)
		if e != nil {
			t.Fatal(e)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err = apply(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	const actor = "AAAAAAAAAAAAAAAAAAAAAA"
	for _, statement := range []string{
		`INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES ($1,'legacy.observer','admin','preserved-hash',now())`,
		`INSERT INTO connection_sources VALUES ($1,'https://example.invalid',now())`,
		`INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES($1,$1,1,'Legacy observation',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`,
	} {
		if _, err = pool.Exec(ctx, statement, actor); err != nil {
			t.Fatal(err)
		}
	}
	var run, job, observation int64
	if err = pool.QueryRow(ctx, `INSERT INTO collection_runs(reason,selected,created_at) VALUES('scheduled',false,now()) RETURNING id`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO collection_jobs(run_id,account_id,source_id,connection_revision,account_revision,policy_revision,source_mode,state,reason,queued_at) VALUES($1,$2,$2,1,1,1,'passive','succeeded','VERIFIED_PASSIVE_SNAPSHOT',now()) RETURNING id`, run, actor).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO quota_observations(job_id,account_id,received_at,snapshot) VALUES($1,$2,'2026-11-07T12:00:00.000001Z','{"source_at":"2026-11-07T12:00:00.0000014Z","received_at":"2026-11-07T12:00:00.000001Z","windows":[{"kind":"short","used_percent":"99.123456789012345678","duration_seconds":null,"reset_at":null}]}') RETURNING id`, job, actor).Scan(&observation); err != nil {
		t.Fatal(err)
	}
	var original string
	if err = pool.QueryRow(ctx, `SELECT jsonb_build_array(id,job_id,account_id,received_at,snapshot)::text FROM quota_observations`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var intact bool
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM observation_management)=0 AND (SELECT count(*) FROM observation_changes)=0 AND (SELECT jsonb_build_array(id,job_id,account_id,received_at,snapshot)::text=$1 FROM quota_observations) AND (SELECT password_hash='preserved-hash' FROM auth_users)`, original).Scan(&intact); err != nil || !intact {
		t.Fatal("upgrade rewrote old precision, snapshots or invented management decisions", err)
	}
	const other = "BBBBBBBBBBBBBBBBBBBBBA"
	if _, err = pool.Exec(ctx, `INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES($1,$2,2,'Other account',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`, other, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO observation_management(account_id) VALUES($1)`, other); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO observation_changes(account_id,revision,action,observation_id,reason,actor_id,occurred_at) VALUES($1,2,'exclude',$2,'Must belong to selected account',$3,now())`, other, observation, actor); err == nil {
		t.Fatal("database accepted a decision targeting another account's immutable observation")
	}
}

func TestEstimateUpgradePreservesOldFactsWithoutInventingScenarios(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/012_estimates.sql" {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err = apply(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	const actor = "AAAAAAAAAAAAAAAAAAAAAA"
	for _, sql := range []string{
		`INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES($1,'legacy.estimate','admin','preserved-hash',now())`,
		`INSERT INTO connection_sources VALUES($1,'https://example.invalid',now())`,
		`INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES($1,$1,1,'Legacy estimated account',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`,
		`INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) VALUES($1,$1,now(),'{"state":"exhausted","complete":false,"settlement_eligible":false}')`,
		`INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) SELECT id,7,'{"metrics":{"total_cost":"0.123456789012345678","actual_cost":"0"}}'::jsonb FROM usage_captures WHERE account_id=$1`,
		`INSERT INTO observation_management(account_id,revision) VALUES($1,1)`,
	} {
		if _, err = pool.Exec(ctx, sql, actor); err != nil {
			t.Fatal(err)
		}
	}
	var original string
	if err = pool.QueryRow(ctx, `SELECT jsonb_build_array(c.snapshot,f.fact)::text FROM usage_captures c JOIN usage_capture_facts f ON f.capture_id=c.id`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var intact bool
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimate_config)=0 AND (SELECT count(*) FROM estimate_results)=0 AND (SELECT jsonb_build_array(c.snapshot,f.fact)::text=$1 FROM usage_captures c JOIN usage_capture_facts f ON f.capture_id=c.id) AND (SELECT revision=1 FROM observation_management) AND (SELECT password_hash='preserved-hash' FROM auth_users)`, original).Scan(&intact); err != nil || !intact {
		t.Fatal("upgrade rewrote old costs, limits or created estimates", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO estimate_config(account_id,configuration) VALUES($1,'{}')`, actor); err == nil {
		t.Fatal("partial configuration accepted without saved time")
	}
}
