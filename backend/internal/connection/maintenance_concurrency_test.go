package connection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func awaitMaintenanceLock(t *testing.T, db *pgxpool.Pool, table string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := db.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON a.pid=l.pid WHERE a.wait_event_type='Lock' AND l.relation=to_regclass($1))`, table).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("maintenance never reached the observed %s lock", table)
}

func TestMaintenancePreflightAndWorkerRejectAuthorityExpiringDuringActualDatabaseWait(t *testing.T) {
	for _, stage := range []string{"preflight-account", "worker-audit"} {
		t.Run(stage, func(t *testing.T) {
			s, db, token, c, calls := maintenanceFixture(t)
			ctx := context.Background()
			a := passiveManaged(t, s, token, c, 1)
			fact := collectObservation(t, s, db, token, c, a)
			in := maintenanceInputFor(a, fact, fact)
			var p MaintenancePlan
			if stage == "worker-audit" {
				var err error
				p, err = s.PreflightMaintenance(ctx, token, in)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
					t.Fatal(err)
				}
			}
			before, requests := maintenanceFactSnapshot(t, db), calls.Load()
			blocker, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			table := "managed_accounts"
			if stage == "preflight-account" {
				_, err = blocker.Exec(ctx, `SELECT id FROM managed_accounts WHERE id=$1 FOR UPDATE`, a.ID)
			} else {
				table = "management_events"
				_, err = blocker.Exec(ctx, `LOCK TABLE management_events IN ACCESS EXCLUSIVE MODE`)
			}
			if err != nil {
				t.Fatal(err)
			}
			var expires time.Time
			if err = db.QueryRow(ctx, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '900 milliseconds' RETURNING idle_expires_at`).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if stage == "preflight-account" {
					_, err := s.PreflightMaintenance(ctx, token, in)
					done <- err
					return
				}
				conn, err := db.Acquire(ctx)
				if err == nil {
					defer conn.Release()
					_, err = s.processMaintenance(ctx, conn)
				}
				done <- err
			}()
			awaitMaintenanceLock(t, db, table)
			if delay := time.Until(expires.Add(25 * time.Millisecond)); delay > 0 {
				time.Sleep(delay)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("maintenance did not finish after the real lock was released")
			}
			if stage == "preflight-account" {
				if !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatal("expired preflight authority survived an account lock", err)
				}
				var count int
				if err = db.QueryRow(ctx, `SELECT count(*) FROM maintenance_plans`).Scan(&count); err != nil || count != 0 {
					t.Fatal("expired authority left a preflight artifact", count, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				p, err = scanMaintenance(db.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1`, p.ID))
				if err != nil || p.State != "failed" || p.Reason != "ACTOR_UNAVAILABLE" || p.ResultID != nil {
					t.Fatal("late audit wait retained an unauthorized applied result", p, err)
				}
				var succeeded int
				if err = db.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE action='maintenance.apply' AND outcome='succeeded'`).Scan(&succeeded); err != nil || succeeded != 0 {
					t.Fatal("rolled-back application retained a successful audit", succeeded, err)
				}
			}
			if requests != calls.Load() || before != maintenanceFactSnapshot(t, db) {
				t.Fatal("expired maintenance authority changed facts or caused external effects")
			}
		})
	}
}

func TestMaintenanceCollectorLeaderRunsQueuedLocalWorkWithoutCollection(t *testing.T) {
	s, db, token, c, calls := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	p, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	before, requests := maintenanceFactSnapshot(t, db), calls.Load()
	worker, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.RunCollector(worker) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, err = s.GetMaintenancePlan(ctx, token, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.State == "applied" {
			if p.ResultID == nil || requests != calls.Load() || before != maintenanceFactSnapshot(t, db) {
				t.Fatal("real leader did not keep maintenance local", p)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("actual collector leader did not process the durable local queue", p)
}

func TestMaintenancePlanReadRefreshesLifecycleAfterActualAccountWait(t *testing.T) {
	s, db, token, c, _ := maintenanceFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	p, err := s.PreflightMaintenance(ctx, token, maintenanceInputFor(a, fact, fact))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyMaintenancePlan(ctx, token, p.ID, p.Basis.Fingerprint, true); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `LOCK TABLE management_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	worker := make(chan error, 1)
	go func() {
		conn, err := db.Acquire(ctx)
		if err == nil {
			defer conn.Release()
			_, err = s.processMaintenance(ctx, conn)
		}
		worker <- err
	}()
	awaitMaintenanceLock(t, db, "management_events")
	type reply struct {
		plan MaintenancePlan
		err  error
	}
	read := make(chan reply, 1)
	go func() { plan, err := s.GetMaintenancePlan(ctx, token, p.ID); read <- reply{plan, err} }()
	awaitMaintenanceLock(t, db, "managed_accounts")
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-worker:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not complete after releasing the actual audit lock")
	}
	select {
	case result := <-read:
		if result.err != nil || result.plan.State != "applied" || result.plan.ResultID == nil || *result.plan.ResultID != p.ID {
			t.Fatal("post-wait read returned an obsolete lifecycle", result.plan, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plan read did not complete after the actual account lock")
	}
}
