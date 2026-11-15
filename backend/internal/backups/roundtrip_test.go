package backups

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// Native row fixtures deliberately keep opaque business JSON intact instead of recalculating it.
func seedEveryBusinessTable(t *testing.T, f fixture) {
	t.Helper()
	c := savedConnection(t, f)
	prefix := `WITH f AS (SELECT $1::text actor,$2::text source,$3::text account,$4::text participant,$5::jsonb fact) `
	fact := `{"upstream_id":9007199254740993,"actual_cost":12345678901234567890.1234567890123456789,"source_at":"2026-10-01T00:00:00.123456789Z","zero":0,"unknown":null}`
	statements := []string{
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
		`INSERT INTO pool_events(occurred_at,actor_id,pool_id,contract_id,action) SELECT clock_timestamp(),actor,account,(SELECT min(id) FROM pool_contracts),'pool.create' FROM f`,
		`INSERT INTO usage_attributions(capture_id,upstream_id,participant_id,binding_id,participant_name,basis,pool_contract_id,pool_basis) SELECT (SELECT min(id) FROM usage_captures),9007199254740993,participant,(SELECT min(id) FROM participant_bindings),'owned participant','local_binding_at_log_time',(SELECT min(id) FROM pool_contracts),'recorded_contract' FROM f`,
		`INSERT INTO observation_management(account_id,revision) SELECT account,2 FROM f`,
		`INSERT INTO observation_changes(account_id,revision,action,observation_id,reason,actor_id,occurred_at) SELECT account,2,'exclude',(SELECT min(id) FROM quota_observations),'owned native fixture',actor,clock_timestamp() FROM f`,
		`INSERT INTO estimate_config(account_id,configuration,saved_at) SELECT account,fact,clock_timestamp() FROM f`,
		`INSERT INTO estimate_results(account_id,generated_at,snapshot) SELECT account,clock_timestamp(),fact FROM f`,
		`INSERT INTO wallet_reads(participant_id,binding_id,source_id,connection_revision,attempted_at,snapshot) SELECT participant,(SELECT min(id) FROM participant_bindings),source,2,clock_timestamp(),fact FROM f`,
		`INSERT INTO wallet_recommendations(participant_id,binding_id,generated_at,snapshot) SELECT participant,(SELECT min(id) FROM participant_bindings),clock_timestamp(),fact FROM f`,
		`UPDATE wallet_automation SET revision=3,enabled=true,actor_id=f.actor,actor_revision=1,source_id=f.source,connection_revision=2,simulation_epoch='AAAAAAAAAAAAAAAAAAAAAA',updated_at=clock_timestamp() FROM f`,
		`INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) SELECT actor,account,repeat('a',64),participant,(SELECT min(id) FROM participant_bindings),clock_timestamp(),fact||'{"state":"rejected","dispatch_state":"not_dispatched"}'::jsonb FROM f`,
		`INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) SELECT actor,participant,repeat('b',64),participant,(SELECT min(id) FROM participant_bindings),clock_timestamp(),fact||'{"state":"pending","dispatch_state":"pending","unit":"simulated_wallet_usd","simulation":{"protocol":"sub-pool-simulator-v1","environment":"simulated","epoch":"AAAAAAAAAAAAAAAAAAAAAA"}}'::jsonb FROM f`,
		`INSERT INTO wallet_application_results(application_id,recorded_at,resolved,snapshot) SELECT (SELECT max(id) FROM wallet_application_requests),clock_timestamp(),true,fact||'{"state":"applied","dispatch_state":"confirmed","unit":"simulated_wallet_usd","simulation":{"protocol":"sub-pool-simulator-v1","environment":"simulated","epoch":"AAAAAAAAAAAAAAAAAAAAAA"}}'::jsonb FROM f`,
		`INSERT INTO notifications(event_key,kind,subject_type,subject_id,source_id,connection_revision,subject_revision,config_revision,channel,from_address,recipient_address,subject,body,event_reason,created_at,started_at,finished_at,state,reason) SELECT 'owned-unknown','collection_failed','account',account,source,2,1,1,'smtp','sender@fixture.test','admin@fixture.test','owned unknown','not replayed','OWNED',clock_timestamp(),clock_timestamp(),clock_timestamp(),'unknown','WORKER_INTERRUPTED' FROM f`,
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
	changed := map[string]bool{"auth_api_keys": true, "connection_config": true, "management_events": true, "monitoring_config": true, "notification_config": true, "maintenance_plans": true, "wallet_automation": true}
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
		if table.Name != "announcement_reads" && table.Name != "dispatch_events" && table.Name != "dispatch_pauses" && table.Name != "pricing_changes" && table.Name != "pricing_events" && table.Name != "wallet_application_results" && table.Name != "wallet_automation" && table.Name != "temporary_rounds" && table.Name != "temporary_exits" && table.Name != "temporary_events" {
			old.Tables = append(old.Tables, table)
		}
	}
	delete(old.Migrations, "migrations/019_help.sql")
	delete(old.Migrations, "migrations/020_dispatch.sql")
	delete(old.Migrations, "migrations/021_pricing_control.sql")
	delete(old.Migrations, "migrations/023_wallet_application_results.sql")
	delete(old.Migrations, "migrations/024_wallet_automation.sql")
	delete(old.Migrations, "migrations/025_temporary_rounds.sql")
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
