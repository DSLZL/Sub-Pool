package backups

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"sub-pool/backend/internal/connection"
)

// Native row fixtures deliberately keep opaque business JSON intact instead of recalculating it.
func seedEveryBusinessTable(t *testing.T, f fixture) {
	t.Helper()
	c := savedConnection(t, f)
	prefix := `WITH f AS (SELECT $1::text actor,$2::text source,$3::text account,$4::text participant,$5::jsonb fact) `
	fact := `{"upstream_id":9007199254740993,"actual_cost":12345678901234567890.1234567890123456789,"source_at":"2026-10-01T00:00:00.123456789Z","zero":0,"unknown":null}`
	statements := []string{
		`INSERT INTO upstream_write_grants(source_id,capability,granted_by,granted_at,confirmation_sha256,revoked_by,revoked_at)
		  SELECT source,'pricing',actor,'2026-10-01T00:00:00Z',repeat('9',64),actor,'2026-10-02T00:00:00Z' FROM f`,
		`INSERT INTO upstream_write_grants(source_id,capability,granted_by,confirmation_sha256) SELECT source,'pricing',actor,repeat('b',64) FROM f`,
		`INSERT INTO announcement_reads(user_id,announcement_id,content_sha256) SELECT actor,'owned-release-v1',repeat('a',64) FROM f`,
		`INSERT INTO auth_login_limits(bucket,window_start,failures) SELECT 'owned-native-recovery',clock_timestamp(),3 FROM f`,
		`INSERT INTO source_blocks(ip,revision,blocked,saved_at,actor_id) SELECT '198.51.100.44',1,true,clock_timestamp(),actor FROM f`,
		`INSERT INTO auth_api_keys(id,user_id,name,mode,digest,created_at) SELECT account,actor,'owned archive key','management',decode(repeat('cd',32),'hex'),clock_timestamp() FROM f`,
		`INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,resource_fingerprint,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) SELECT account,source,9007199254740993,'owned native account',true,'passive','verified',decode(repeat('ef',32),'hex'),fact,'{}',clock_timestamp(),clock_timestamp(),2,clock_timestamp(),clock_timestamp() FROM f`,
		`INSERT INTO account_events(occurred_at,actor_id,account_id,revision,action) SELECT clock_timestamp(),actor,account,1,'account.adopt' FROM f`,
		`INSERT INTO dispatch_pauses(account_id,actor_id,request_id,request_digest,source_id,connection_revision,account_revision,state,reason,previous_schedulable,native_updated_at,restore_at,created_at,updated_at) SELECT account,actor,account,repeat('d',64),source,2,1,'reviewed','MANUALLY_REVIEWED',false,clock_timestamp(),clock_timestamp(),clock_timestamp(),clock_timestamp() FROM f`,
		`INSERT INTO dispatch_events(pause_id,account_id,actor,action,occurred_at,snapshot) SELECT (SELECT min(id) FROM dispatch_pauses),account,'worker','interrupted',clock_timestamp(),fact FROM f`,
		`INSERT INTO pricing_changes(actor_id,request_id,request_digest,source_id,connection_revision,group_id,group_name,changes,native_before,native_target,state,reason,created_at,updated_at) SELECT actor,account,repeat('e',64),source,2,9007199254740993,'owned group','[]',fact,fact,'reviewed','MANUALLY_REVIEWED_RESULT_UNKNOWN',clock_timestamp(),clock_timestamp() FROM f`,
		`INSERT INTO pricing_events(change_id,occurred_at,snapshot) SELECT (SELECT min(id) FROM pricing_changes),clock_timestamp(),fact FROM f`,
		`INSERT INTO collection_runs(reason,selected,created_at) SELECT 'scheduled',false,clock_timestamp() FROM f`,
		`INSERT INTO collection_jobs(run_id,account_id,source_id,connection_revision,account_revision,policy_revision,source_mode,state,reason,queued_at,started_at,finished_at,read_check) SELECT (SELECT min(id) FROM collection_runs),account,source,2,1,1,'passive','succeeded','OBSERVED',clock_timestamp(),clock_timestamp(),clock_timestamp(),fact FROM f`,
		`INSERT INTO quota_observations(job_id,account_id,received_at,snapshot) SELECT (SELECT min(id) FROM collection_jobs),account,clock_timestamp(),fact FROM f`,
		`INSERT INTO native_weekly_estimates(account_id,basis_key,retry_at,work,result) SELECT account,repeat('b',64),clock_timestamp(),fact,fact FROM f`,
		`INSERT INTO native_weekly_estimates(account_id,basis_key,retry_at,work) SELECT account,repeat('c',64),clock_timestamp(),fact FROM f`,
		`INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) SELECT account,actor,clock_timestamp(),fact FROM f`,
		`INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) SELECT (SELECT min(id) FROM usage_captures),9007199254740993,fact FROM f`,
		`INSERT INTO usage_events(occurred_at,actor_id,capture_id,action) SELECT clock_timestamp(),actor,(SELECT min(id) FROM usage_captures),'usage.capture' FROM f`,
		`INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at) SELECT participant,'owned participant','owned@fixture.test','native recovery',true,clock_timestamp(),clock_timestamp() FROM f`,
		`INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at) SELECT participant,source,9007199254740993,fact,clock_timestamp() FROM f`,
		`INSERT INTO participant_events(occurred_at,actor_id,participant_id,revision,action) SELECT clock_timestamp(),actor,participant,1,'participant.create' FROM f`,
		`INSERT INTO pools(id,name,enabled,created_at,saved_at) SELECT account,'owned pool',true,clock_timestamp(),clock_timestamp() FROM f`,
		`INSERT INTO pool_contracts(pool_id,revision,name,enabled,source_id,connection_revision,effective_at) SELECT account,1,'owned pool',true,source,2,clock_timestamp() FROM f`,
		`INSERT INTO pool_contract_accounts(contract_id,account_id,revision,name,source_id,upstream_id,resource_fingerprint,current) SELECT (SELECT min(id) FROM pool_contracts),account,1,'owned account',source,9007199254740993,decode(repeat('ef',32),'hex'),true FROM f`,
		`INSERT INTO pool_contract_participants(contract_id,participant_id,revision,binding_id,name,share_units) SELECT (SELECT min(id) FROM pool_contracts),participant,1,(SELECT min(id) FROM participant_bindings),'owned participant',100000000 FROM f`,
		`INSERT INTO weekly_wallet_allocations(pool_id,account_id,participant_id,binding_id,period_end,phase,recorded_at,snapshot) SELECT account,account,participant,(SELECT min(id) FROM participant_bindings),clock_timestamp()+interval '7 days','advance',clock_timestamp(),fact FROM f`,
		`INSERT INTO weekly_wallet_groups(participant_id,binding_id,pool_ids,phase,period_end,recorded_at,basis,snapshot) SELECT participant,(SELECT min(id) FROM participant_bindings),ARRAY[account],'settlement',clock_timestamp()+interval '7 days',clock_timestamp(),repeat('e',64),fact FROM f`,
		`INSERT INTO pool_events(occurred_at,actor_id,pool_id,contract_id,action) SELECT clock_timestamp(),actor,account,(SELECT min(id) FROM pool_contracts),'pool.create' FROM f`,
		`INSERT INTO usage_attributions(capture_id,upstream_id,participant_id,binding_id,participant_name,basis,pool_contract_id,pool_basis) SELECT (SELECT min(id) FROM usage_captures),9007199254740993,participant,(SELECT min(id) FROM participant_bindings),'owned participant','local_binding_at_log_time',(SELECT min(id) FROM pool_contracts),'recorded_contract' FROM f`,
		`INSERT INTO usage_captures(id,account_id,actor_id,captured_at,snapshot,collection_day,staging) OVERRIDING SYSTEM VALUE SELECT 1,account,NULL,clock_timestamp(),fact,DATE '2026-10-01',false FROM f UNION ALL SELECT 2,account,actor,clock_timestamp(),fact,DATE '2026-10-02',true FROM f`,
		`UPDATE monitoring_config SET usage_enabled=true,usage_interval_seconds=420,usage_enabled_at=clock_timestamp()`,
		`INSERT INTO usage_day_jobs(source_id,account_id,day,connection_revision,fingerprint,scheduled,state,reason,published_capture_id,completed_at,checkpoint) SELECT source,account,'2026-10-01',2,decode(repeat('ef',32),'hex'),true,'complete','VERIFIED_READ',1,clock_timestamp(),fact FROM f`,
		`INSERT INTO usage_day_jobs(source_id,account_id,day,connection_revision,fingerprint,scheduled,actor_id,actor_revision,session_digest,state,work_capture_id,checkpoint) SELECT source,account,'2026-10-02',2,decode(repeat('ef',32),'hex'),false,actor,1,decode(repeat('ad',32),'hex'),'collecting',2,fact FROM f`,
		`INSERT INTO observation_management(account_id,revision) SELECT account,2 FROM f`,
		`INSERT INTO observation_changes(account_id,revision,action,observation_id,reason,actor_id,occurred_at) SELECT account,2,'exclude',(SELECT min(id) FROM quota_observations),'owned native fixture',actor,clock_timestamp() FROM f`,
		`INSERT INTO estimate_config(account_id,configuration,saved_at) SELECT account,fact,clock_timestamp() FROM f`,
		`INSERT INTO estimate_results(account_id,generated_at,snapshot) SELECT account,clock_timestamp(),fact FROM f`,
		`INSERT INTO wallet_reads(participant_id,binding_id,source_id,connection_revision,attempted_at,snapshot) SELECT participant,(SELECT min(id) FROM participant_bindings),source,2,clock_timestamp(),fact FROM f`,
		`INSERT INTO native_wallet_financial_samples(participant_id,binding_id,source_id,connection_revision,participant_revision,policy_revision,sampled_at,checked_at,snapshot)
		 SELECT participant,(SELECT min(id) FROM participant_bindings),source,2,1,1,clock_timestamp(),clock_timestamp(),fact FROM f`,
		`INSERT INTO wallet_recommendations(participant_id,binding_id,generated_at,snapshot) SELECT participant,(SELECT min(id) FROM participant_bindings),clock_timestamp(),fact FROM f`,
		`UPDATE wallet_automation SET revision=3,enabled=true,actor_id=f.actor,actor_revision=1,source_id=f.source,connection_revision=2,simulation_epoch='AAAAAAAAAAAAAAAAAAAAAA',updated_at=clock_timestamp() FROM f`,
		`INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) SELECT actor,account,repeat('a',64),participant,(SELECT min(id) FROM participant_bindings),clock_timestamp(),fact||'{"state":"rejected","dispatch_state":"not_dispatched"}'::jsonb FROM f`,
		`INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) SELECT actor,participant,repeat('b',64),participant,(SELECT min(id) FROM participant_bindings),clock_timestamp(),fact||'{"state":"pending","dispatch_state":"pending","unit":"simulated_wallet_usd","simulation":{"protocol":"sub-pool-simulator-v1","environment":"simulated","epoch":"AAAAAAAAAAAAAAAAAAAAAA"}}'::jsonb FROM f`,
		`INSERT INTO wallet_application_results(application_id,recorded_at,resolved,snapshot) SELECT (SELECT max(id) FROM wallet_application_requests),clock_timestamp(),true,fact||'{"state":"applied","dispatch_state":"confirmed","unit":"simulated_wallet_usd","simulation":{"protocol":"sub-pool-simulator-v1","environment":"simulated","epoch":"AAAAAAAAAAAAAAAAAAAAAA"}}'::jsonb FROM f`,
		`INSERT INTO notifications(event_key,kind,subject_type,subject_id,source_id,connection_revision,subject_revision,config_revision,channel,from_address,recipient_address,subject,body,event_reason,created_at,started_at,finished_at,state,reason) SELECT 'owned-unknown','collection_failed','account',account,source,2,1,1,'smtp','sender@fixture.test','admin@fixture.test','owned unknown','not replayed','OWNED',clock_timestamp(),clock_timestamp(),clock_timestamp(),'unknown','WORKER_INTERRUPTED' FROM f`,
		`UPDATE notification_config SET financial_enabled=true,native_source_id=(SELECT source FROM f)`,
		`INSERT INTO notifications(event_key,kind,subject_type,subject_id,participant_id,source_id,connection_revision,subject_revision,config_revision,channel,from_address,recipient_address,subject,body,event_reason,created_at,started_at,finished_at,state,reason,financial_evidence) SELECT 'financial:balance_low:1:0','balance_low','participant',participant,participant,source,2,1,1,'smtp','sender@fixture.test','admin@fixture.test','owned financial unknown','not replayed','SIMULATED_BALANCE_BELOW_TARGET',clock_timestamp(),clock_timestamp(),clock_timestamp(),'unknown','WORKER_INTERRUPTED',jsonb_build_object('kind','balance_low','subject_id',participant,'source_id',source,'connection_revision','2','subject_revision','1','record_id','1','previous_id','0','account_ids',jsonb_build_array(account),'as_of','2026-10-01T00:00:00Z') FROM f`,
		`INSERT INTO maintenance_plans(account_id,actor_id,created_at,expires_at,snapshot,state,reason) SELECT account,actor,clock_timestamp(),clock_timestamp()+interval '10 minutes',fact,'ready','READY' FROM f`,
		`INSERT INTO temporary_rounds(id,actor_id,actor_revision,request_id,request_digest,started_at,deadline,snapshot) SELECT account,actor,1,participant,repeat('f',64),'2026-10-01T09:00:00Z','2026-10-01T10:00:00Z',fact FROM f`,
		`INSERT INTO temporary_exits(round_id,exited_at,reason,actor_id,recorded_at) SELECT account,'2026-10-01T09:30:00Z','terminated',actor,'2026-10-01T09:30:00Z' FROM f`,
		`INSERT INTO temporary_events(round_id,account_id,kind,request_key,recorded_at,snapshot) SELECT account,account,'settlement','owned-temporary-settlement',clock_timestamp(),fact FROM f`,
	}
	exec(t, f.pool, `ALTER SEQUENCE usage_captures_id_seq RESTART WITH 9007199254740993`)
	account, participant := id(t), id(t)
	for _, statement := range statements {
		exec(t, f.pool, prefix+statement, f.admin.ID, *c.SourceID, account, participant, fact)
	}
}

func currentSnapshot(t *testing.T, f fixture) snapshot {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	shape, err := readSchema(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := capture(ctx, tx, shape, true)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEveryBusinessTableAndExactFactsRoundTrip(t *testing.T) {
	f := setup(t)
	seedEveryBusinessTable(t, f)
	file := export(t, f, true)
	source, err := f.s.unprotect(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range source.Tables {
		if len(table.Rows) == 0 {
			t.Fatal("table has no native roundtrip witness", table.Name)
		}
	}
	p := preflight(t, f, file)
	out, err := f.s.Apply(context.Background(), f.token, ApplyInput{p.ID, p.TargetBasis, true})
	if err != nil || out.State != "applied" {
		t.Fatal("native all-table restore failed", out.State, out.Reason, err)
	}
	actual := currentSnapshot(t, f)
	changed := map[string]bool{"auth_api_keys": true, "connection_config": true, "management_events": true, "monitoring_config": true, "notification_config": true, "maintenance_plans": true, "wallet_automation": true, "usage_day_jobs": true, "upstream_write_grants": true}
	for i, want := range source.Tables {
		if changed[want.Name] {
			continue
		}
		before, _ := json.Marshal(want)
		after, _ := json.Marshal(actual.Tables[i])
		if !reflect.DeepEqual(before, after) {
			t.Fatal("native rows changed", want.Name)
		}
	}
	var raw string
	var enabled bool
	var nativeSourceID *string
	if err = f.pool.QueryRow(context.Background(), `SELECT financial_enabled,native_source_id FROM notification_config`).Scan(&enabled, &nativeSourceID); err != nil || enabled || nativeSourceID != nil {
		t.Fatal("restore retained financial notification activation", enabled, err)
	}
	var usageRestored bool
	err = f.pool.QueryRow(context.Background(), `SELECT NOT usage_enabled AND usage_interval_seconds=420
		AND EXISTS(SELECT 1 FROM usage_day_jobs WHERE state='complete' AND published_capture_id=1 AND completed_at IS NOT NULL AND checkpoint->>'source_at'='2026-10-01T00:00:00.123456789Z')
		AND EXISTS(SELECT 1 FROM usage_day_jobs WHERE state='blocked' AND reason='RESTORE_INVALIDATED' AND work_capture_id=2 AND checkpoint->>'source_at'='2026-10-01T00:00:00.123456789Z')
		FROM monitoring_config WHERE singleton`).Scan(&usageRestored)
	if err != nil || !usageRestored {
		t.Fatal("restore changed usage history or retained automatic or session authority", err)
	}
	var automationRevision string
	if err = f.pool.QueryRow(context.Background(), `SELECT enabled,revision::text FROM wallet_automation`).Scan(&enabled, &automationRevision); err != nil || enabled || automationRevision != "4" {
		t.Fatal("restore retained automatic execution authority", enabled, automationRevision, err)
	}
	if err = f.pool.QueryRow(context.Background(), `SELECT fact::text FROM usage_capture_facts WHERE capture_id=9007199254740993 AND upstream_id=9007199254740993`).Scan(&raw); err != nil {
		t.Fatal("large identity/fact was lost", err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || string(fields["actual_cost"]) != "12345678901234567890.1234567890123456789" || string(fields["source_at"]) != `"2026-10-01T00:00:00.123456789Z"` {
		t.Fatal("money or nanosecond precision was lost")
	}
	var next string
	if err = f.pool.QueryRow(context.Background(), `SELECT nextval('usage_captures_id_seq')::text`).Scan(&next); err != nil || next != "9007199254740994" {
		t.Fatal("identity continuation was lost", next, err)
	}
	ctx := context.Background()
	var grantHistoryPreserved bool
	if err = f.pool.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(source_id=(SELECT source_id FROM connection_config)) AND bool_and(granted_by=$1 AND revoked_by=$1)
		AND bool_and(CASE id WHEN 1 THEN revoked_at='2026-10-02T00:00:00Z' AND confirmation_sha256=repeat('9',64)
		WHEN 2 THEN revoked_at>=granted_at AND confirmation_sha256=repeat('b',64) ELSE false END) FROM upstream_write_grants`, f.admin.ID).Scan(&grantHistoryPreserved); err != nil || !grantHistoryPreserved {
		t.Fatal("restore changed grant history or retained active authority", err)
	}
	token := login(t, f, f.admin.Username)
	view, err := f.connection.GetWriteGrants(ctx, token)
	if err != nil || view.SourceID == nil {
		t.Fatal("restored source grants unavailable", err)
	}
	enable := true
	regrant, err := f.connection.UpdateWriteGrant(ctx, token, connection.WriteGrantUpdate{
		SourceID: *view.SourceID, ConnectionRevision: view.ConnectionRevision, Capability: "pricing", ExpectedGrantID: "0",
		Enabled: &enable, ConfirmationSHA256: view.ConfirmationSHA256, CurrentPassword: fixturePassword,
	})
	if err != nil || regrant.Grants[0].Grant == nil || regrant.Grants[0].Grant.ID != "3" || regrant.Grants[0].Grant.RevokedAt != nil {
		t.Fatal("failed to create a fresh grant after restore", err)
	}
}

func TestWeeklyWalletRestorePreservesDebtAndDisablesExecution(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	older := export(t, f, true)
	seedEveryBusinessTable(t, f)
	exec(t, f.pool, `UPDATE weekly_wallet_allocations SET phase='settlement',snapshot='{"debt":"5"}'`)
	exec(t, f.pool, `UPDATE pools SET weekly_wallet='{"revision":"2","enabled":false,"auto_allocate":true}'`)
	blocked, err := f.s.Preflight(ctx, f.token, id(t), older.Content)
	if err != nil || blocked.State != "blocked" || !slices.Contains(blocked.Blockers, "WEEKLY_WALLET_HISTORY_CHANGED") {
		t.Fatal("restore would discard recorded weekly debt", blocked.State, blocked.Blockers, err)
	}
	previous := export(t, f, true)
	exec(t, f.pool, `UPDATE weekly_wallet_groups SET snapshot='{"offsets":[{"amount":"3"}],"target":"25"}'`)
	blocked, err = f.s.Preflight(ctx, f.token, id(t), previous.Content)
	if err != nil || blocked.State != "blocked" || !slices.Contains(blocked.Blockers, "WEEKLY_WALLET_HISTORY_CHANGED") {
		t.Fatal("restore would alter confirmed wallet offsets", blocked.State, blocked.Blockers, err)
	}
	current := preflight(t, f, export(t, f, true))
	out, err := f.s.Apply(ctx, f.token, ApplyInput{current.ID, current.TargetBasis, true})
	if err != nil || out.State != "applied" {
		t.Fatal("restore of equivalent weekly history failed", out.State, out.Reason, err)
	}
	var enabled, automatic, revision, debt string
	err = f.pool.QueryRow(ctx, `SELECT p.weekly_wallet->>'enabled',p.weekly_wallet->>'auto_allocate',
	 p.weekly_wallet->>'revision',w.snapshot->>'debt' FROM pools p JOIN weekly_wallet_allocations w ON p.id=w.pool_id`).
		Scan(&enabled, &automatic, &revision, &debt)
	if err != nil || enabled != "false" || automatic != "false" || revision != "3" || debt != "5" {
		t.Fatal("restore changed weekly debt or retained execution authority", enabled, automatic, revision, debt, err)
	}
	var offset, target string
	err = f.pool.QueryRow(ctx, `SELECT snapshot #>> '{offsets,0,amount}',snapshot->>'target' FROM weekly_wallet_groups`).Scan(&offset, &target)
	if err != nil || offset != "3" || target != "25" {
		t.Fatal("restore changed confirmed wallet offsets", offset, target, err)
	}
}

func TestHistoricalProtectedMetadataStaysDownloadableAcrossBusinessSchemaGrowth(t *testing.T) {
	f := setup(t)
	file := export(t, f, true)
	old, err := f.s.unprotect(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	old.Tables = nil
	current := currentSnapshot(t, f)
	for _, table := range current.Tables {
		switch table.Name {
		case "announcement_reads", "dispatch_events", "dispatch_pauses", "native_weekly_estimates", "pricing_changes", "pricing_events",
			"wallet_application_results", "wallet_automation", "temporary_rounds", "temporary_exits", "temporary_events", "weekly_wallet_allocations", "usage_day_jobs", "upstream_write_grants", "weekly_wallet_groups", nativeFinancialSamples:
		default:
			old.Tables = append(old.Tables, table)
		}
	}
	delete(old.Migrations, "migrations/019_help.sql")
	delete(old.Migrations, "migrations/020_dispatch.sql")
	delete(old.Migrations, "migrations/021_pricing_control.sql")
	delete(old.Migrations, "migrations/023_wallet_application_results.sql")
	delete(old.Migrations, "migrations/024_wallet_automation.sql")
	delete(old.Migrations, "migrations/025_temporary_rounds.sql")
	delete(old.Migrations, "migrations/026_financial_notifications.sql")
	delete(old.Migrations, "migrations/027_temporary_settlement_audit.sql")
	delete(old.Migrations, "migrations/028_retained_estimates.sql")
	delete(old.Migrations, "migrations/029_native_wallet_adjustments.sql")
	delete(old.Migrations, "migrations/030_native_weekly_estimates.sql")
	delete(old.Migrations, "migrations/031_weekly_wallet_allocations.sql")
	delete(old.Migrations, "migrations/033_usage_days.sql")
	delete(old.Migrations, "migrations/034_upstream_write_grants.sql")
	delete(old.Migrations, "migrations/035_weekly_wallet_groups.sql")
	delete(old.Migrations, nativeFinancialMigration)
	old.SchemaFingerprint = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	archived, err := f.s.saveArchive(context.Background(), tx, f.admin, id(t), "export", old)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.GetFile(context.Background(), f.token, archived.ID)
	if err != nil || len(got.Archive.Tables) != 39 || got.Content == "" {
		t.Fatal("historical protected file became inaccessible", err)
	}
	if _, err = f.s.Preflight(context.Background(), f.token, id(t), got.Content); err != ErrSchema {
		t.Fatal("historical schema silently accepted for current restore", err)
	}
}
