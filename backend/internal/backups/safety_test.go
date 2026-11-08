package backups

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestFailedReplacementKeepsEveryCurrentRowAndSequence(t *testing.T) {
	f := setup(t)
	seedEveryBusinessTable(t, f)
	exec(t, f.pool, `UPDATE auth_api_keys SET revision=9223372036854775807`)
	file := export(t, f, true)
	exec(t, f.pool, `UPDATE system_settings SET timezone='Asia/Taipei'`)
	p := preflight(t, f, file)
	before := currentSnapshot(t, f)
	out, err := f.s.Apply(context.Background(), f.token, ApplyInput{p.ID, p.TargetBasis, true})
	if err != nil || out.State != "failed" || out.Reason != "RESTORE_FAILED" {
		t.Fatal("injected native overflow was not a persisted failed replacement", out.State, out.Reason, err)
	}
	after := currentSnapshot(t, f)
	for i, want := range before.Tables {
		if want.Name == "management_events" {
			continue
		}
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(after.Tables[i])
		if !reflect.DeepEqual(a, b) {
			t.Fatal("failed replacement changed current rows", want.Name)
		}
	}
	for i, want := range before.Sequences {
		if want.Table != "management_events" && want != after.Sequences[i] {
			t.Fatal("failed replacement changed a live sequence", want.Table)
		}
	}
	if _, err = f.identity.Current(context.Background(), f.token, false); err != nil {
		t.Fatal("failed replacement revoked current sessions", err)
	}
	if _, err = f.s.GetFile(context.Background(), f.token, *p.BeforeArchiveID); err != nil {
		t.Fatal("failed replacement lost before-image", err)
	}
	if err = f.s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, err := f.s.GetPlan(context.Background(), f.token, p.ID)
	if err != nil || again.State != "failed" {
		t.Fatal("failed restore was replayed", err)
	}
}

func TestFinalAuthorizationExpiryAfterNativeTruncateWaitRollsBack(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	file := export(t, f, false)
	exec(t, f.pool, `UPDATE system_settings SET timezone='Asia/Taipei'`)
	p := preflight(t, f, file)
	var expiresAt time.Time
	if err := f.pool.QueryRow(ctx, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '2 seconds' RETURNING idle_expires_at`).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	lock, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(ctx, `LOCK TABLE system_settings IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		plan Plan
		err  error
	}
	done := make(chan result, 1)
	go func() { p, e := f.s.Apply(ctx, f.token, ApplyInput{p.ID, p.TargetBasis, true}); done <- result{p, e} }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='system_settings'::regclass AND mode='AccessExclusiveLock' AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual replacement never waited on truncate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		var expired bool
		if err = f.pool.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, expiresAt).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture session did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.plan.State != "failed" || got.plan.Reason != "AUTHORIZATION_EXPIRED" {
		t.Fatal("expired authority committed replacement", got.plan.State, got.plan.Reason, got.err)
	}
	var timezone string
	var sessions int
	if err = f.pool.QueryRow(ctx, `SELECT timezone,(SELECT count(*) FROM auth_sessions) FROM system_settings`).Scan(&timezone, &sessions); err != nil || timezone != "Asia/Taipei" || sessions != 1 {
		t.Fatal("expired replacement changed current rows", timezone, sessions, err)
	}
}

func TestActiveWorkAndUnconfirmedRemoteIntentBlockBothSides(t *testing.T) {
	f := setup(t)
	seedEveryBusinessTable(t, f)
	file := export(t, f, true)
	ctx := context.Background()
	exec(t, f.pool, `UPDATE notifications SET state='queued',reason='QUEUED',started_at=NULL,finished_at=NULL`)
	p, err := f.s.Preflight(ctx, f.token, id(t), file.Content)
	if err != nil || p.State != "blocked" || p.BeforeArchiveID != nil || !contains(p.Blockers, "MAIL_ACTIVE") {
		t.Fatal("active target mail was not blocked", p.State, p.Blockers, err)
	}
	busyFile := export(t, f, true)
	exec(t, f.pool, `UPDATE notifications SET state='unknown',reason='WORKER_INTERRUPTED',started_at=clock_timestamp(),finished_at=clock_timestamp()`)
	p, err = f.s.Preflight(ctx, f.token, id(t), busyFile.Content)
	if err != nil || p.State != "blocked" || !contains(p.Blockers, "BACKUP_MAIL_ACTIVE") || contains(p.Blockers, "MAIL_ACTIVE") {
		t.Fatal("active imported mail was not blocked", p.Blockers, err)
	}
	exec(t, f.pool, `UPDATE collection_jobs SET state='running',reason='RUNNING',finished_at=NULL`)
	exec(t, f.pool, `UPDATE maintenance_plans SET state='queued',reason='QUEUED',queued_actor_id=actor_id,queued_actor_revision=1,queued_digest=(SELECT digest FROM auth_sessions LIMIT 1),queued_at=clock_timestamp()`)
	exec(t, f.pool, `UPDATE wallet_application_requests SET snapshot=snapshot||'{"dispatch_state":"unknown"}'::jsonb`)
	p, err = f.s.Preflight(ctx, f.token, id(t), file.Content)
	if err != nil || p.State != "blocked" || !contains(p.Blockers, "COLLECTION_ACTIVE") || !contains(p.Blockers, "MAINTENANCE_ACTIVE") || !contains(p.Blockers, "REMOTE_WRITE_UNCONFIRMED") {
		t.Fatal("unsafe work/intent was not blocked", p.Blockers, err)
	}
}

func TestKnownOperationIDsBindFileActorAndSingleBeforeImage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	requestID := id(t)
	first, err := f.s.Export(ctx, f.token, requestID, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.Export(ctx, f.token, requestID, false)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("export identity replay created different material", err)
	}
	if _, err = f.s.Export(ctx, f.token, requestID, true); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("same export key accepted different policy", err)
	}
	file, err := f.s.GetFile(ctx, f.token, requestID)
	if err != nil {
		t.Fatal(err)
	}
	planID := id(t)
	p, err := f.s.Preflight(ctx, f.token, planID, file.Content)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.Preflight(ctx, f.token, planID, file.Content)
	if err != nil || !reflect.DeepEqual(p, again) {
		t.Fatal("preflight identity created another before-image", err)
	}
	if _, err = f.s.Preflight(ctx, f.token, planID, file.Content+" "); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("same plan key accepted different file", err)
	}
	var beforeImages, audits int
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM backup_archives WHERE kind='pre_restore'),(SELECT count(*) FROM management_events WHERE action='backup.preflight' AND target_id=$1)`, planID).Scan(&beforeImages, &audits); err != nil || beforeImages != 1 || audits != 1 {
		t.Fatal("known-key replay duplicated side effects", beforeImages, audits, err)
	}
}

func TestOwnedSchemaMigrationAndInterruptedIntentRecovery(t *testing.T) {
	source := setup(t)
	file := export(t, source, false)
	source.s.runtime.Close()
	source.pool.Close()
	target := setup(t)
	p := preflight(t, target, file)
	exec(t, target.pool, `UPDATE restore_plans SET state='applying',reason='APPLYING',started_at=clock_timestamp() WHERE id=$1`, p.ID)
	if err := target.s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := target.s.GetPlan(context.Background(), target.token, p.ID)
	if err != nil || got.State != "failed" || got.Reason != "WORKER_INTERRUPTED" {
		t.Fatal("abandoned restore was not failed without replay", got.State, err)
	}
	p = preflight(t, target, file)
	got, err = target.s.Apply(context.Background(), target.token, ApplyInput{p.ID, p.TargetBasis, true})
	if err != nil || got.State != "applied" {
		t.Fatal("compatible independently migrated schema failed", got.State, got.Reason, err)
	}
	target.token = login(t, target, "administrator")
	current, err := target.identity.Current(context.Background(), target.token, false)
	if err != nil || current.User.ID != source.admin.ID {
		t.Fatal("source identity was not migrated", err)
	}
}

func TestRestoreRechecksCurrentAuthorityAfterDraining(t *testing.T) {
	for _, test := range []struct {
		name, update string
		want         error
	}{
		{"demoted", `UPDATE auth_users SET role='user',revision=revision+1`, auth.ErrForbidden},
		{"disabled", `UPDATE auth_users SET enabled=false,revision=revision+1`, auth.ErrUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			file := export(t, f, false)
			p := preflight(t, f, file)
			release, err := f.s.runtime.Enter(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				_, e := f.s.Apply(context.Background(), f.token, ApplyInput{p.ID, p.TargetBasis, true})
				done <- e
			}()
			<-started
			exec(t, f.pool, test.update)
			release()
			if err = <-done; !errors.Is(err, test.want) {
				t.Fatal("old administrator authority survived drain", err)
			}
			var state string
			if err = f.pool.QueryRow(context.Background(), `SELECT state FROM restore_plans WHERE id=$1`, p.ID).Scan(&state); err != nil || state != "ready" {
				t.Fatal("unauthorized replacement began", state, err)
			}
		})
	}
}

func TestExpiredPlanAndDamagedBeforeImageNeverBeginReplacement(t *testing.T) {
	f := setup(t)
	file := export(t, f, false)
	p := preflight(t, f, file)
	ctx := context.Background()
	exec(t, f.pool, `UPDATE backup_archives SET file=file||' ' WHERE id=$1`, *p.BeforeArchiveID)
	if _, err := f.s.Apply(ctx, f.token, ApplyInput{p.ID, p.TargetBasis, true}); !errors.Is(err, ErrInvalid) {
		t.Fatal("restore began without valid before-image", err)
	}
	exec(t, f.pool, `UPDATE restore_plans SET expires_at=created_at+interval '1 microsecond' WHERE id=$1`, p.ID)
	projected, err := f.s.GetPlan(ctx, f.token, p.ID)
	if err != nil || projected.State != "expired" {
		t.Fatal("expired plan presented as ready", err)
	}
	out, err := f.s.Apply(ctx, f.token, ApplyInput{p.ID, p.TargetBasis, true})
	if err != nil || out.State != "expired" || out.Reason != "PLAN_EXPIRED" || out.StartedAt != nil {
		t.Fatal("expired preflight began replacement", out.State, out.Reason, err)
	}
	if _, err = f.identity.Current(ctx, f.token, false); err != nil {
		t.Fatal("rejected restore revoked current session", err)
	}
}
