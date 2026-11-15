package database

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"sub-pool/backend/internal/testdb"
)

func TestWalletApplicationResultUpgradePreservesOriginalIntentAndAddsNoResolution(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	before := fstest.MapFS{}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "migrations/023_wallet_application_results.sql" {
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
		`INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES($1,'legacy.result','admin','preserved-hash',now())`,
		`INSERT INTO connection_sources VALUES($1,'https://example.invalid',now())`,
		`INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at) VALUES($1,'Legacy member','','',true,now(),now())`,
		`INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at) VALUES($1,$1,21,'{}',now())`,
		`INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) VALUES($1,$1,repeat('a',64),$1,1,now(),'{"state":"rejected","dispatch_state":"not_dispatched","target":null,"unit":"native_wallet"}')`,
	} {
		if _, err = pool.Exec(ctx, sql, actor); err != nil {
			t.Fatal(err)
		}
	}
	var original, retained string
	if err = pool.QueryRow(ctx, `SELECT to_jsonb(r)::text FROM wallet_application_requests r`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.QueryRow(ctx, `SELECT to_jsonb(r)::text FROM wallet_application_requests r`).Scan(&retained); err != nil || retained != original {
		t.Fatal("result migration rewrote original receipt", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM wallet_application_results`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration fabricated a remote resolution", count, err)
	}
	for _, body := range []string{`{}`, `{"unit":"native_wallet","state":"applied","dispatch_state":"confirmed","simulation":{"environment":"simulated"}}`, `{"unit":"simulated_wallet_usd","state":"applied","dispatch_state":"unknown","simulation":{"environment":"simulated"}}`} {
		if _, err = pool.Exec(ctx, `INSERT INTO wallet_application_results(application_id,recorded_at,resolved,snapshot) VALUES(1,now(),true,$1)`, body); err == nil {
			t.Fatal("malformed result accepted", body)
		}
	}
}
