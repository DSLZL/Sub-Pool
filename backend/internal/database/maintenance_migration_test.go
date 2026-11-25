package database

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"sub-pool/backend/internal/testdb"
)

func TestMaintenanceUpgradePreservesOwnedHistoryWithoutInventingResults(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/017_maintenance.sql" {
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
		`INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES($1,'owned.maintenance','admin','preserved-hash',now())`,
		`INSERT INTO connection_sources VALUES($1,'https://owned.invalid',now())`,
		`INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES($1,$1,1,'Owned account',false,'passive','unverified','{}','{}',now(),now(),1,now(),now())`,
		`INSERT INTO collection_runs(reason,selected,created_at) SELECT 'scheduled',false,now() FROM auth_users WHERE id=$1`,
		`INSERT INTO collection_jobs(run_id,account_id,source_id,connection_revision,account_revision,policy_revision,source_mode,state,reason,queued_at) SELECT id,$1,$1,1,1,1,'passive','succeeded','OWNED_NATIVE_FACT',now() FROM collection_runs`,
		`INSERT INTO quota_observations(job_id,account_id,received_at,snapshot) SELECT id,$1,now(),'{}'::jsonb FROM collection_jobs`,
		`INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) VALUES($1,$1,now(),jsonb_build_object('state','exhausted','complete',false,'settlement_eligible',false))`,
		`INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) SELECT id,7,jsonb_build_object('metrics',jsonb_build_object('total_cost','0.123456789012345678','actual_cost','0')) FROM usage_captures WHERE account_id=$1`,
	} {
		if _, err = pool.Exec(ctx, sql, actor); err != nil {
			t.Fatal(err)
		}
	}
	var original string
	query := `SELECT jsonb_build_array((SELECT to_jsonb(q) FROM quota_observations q),(SELECT to_jsonb(c)-'collection_day'-'staging' FROM usage_captures c),(SELECT to_jsonb(f) FROM usage_capture_facts f))::text`
	if err = pool.QueryRow(ctx, query).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var current string
	var plans int
	if err = pool.QueryRow(ctx, query).Scan(&current); err != nil || current != original {
		t.Fatal("maintenance migration rewrote original observations or exact costs", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM maintenance_plans`).Scan(&plans); err != nil || plans != 0 {
		t.Fatal("migration invented historical plans or applied results", plans, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO maintenance_plans(account_id,actor_id,created_at,expires_at,snapshot,state,reason) VALUES($1,$1,now(),now()+interval '10 minutes','{}','applied','APPLIED')`, actor); err == nil {
		t.Fatal("storage accepted an applied result without an authorized queue or execution")
	}
}
