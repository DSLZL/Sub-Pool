package connection

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func maintenanceWithRemoteSource(t *testing.T, remote, credential string) (*Service, *pgxpool.Pool, string, Configuration, MaintenanceInput) {
	t.Helper()
	s, db, token, c, _ := maintenanceFixture(t)
	a := passiveManaged(t, s, token, c, 1)
	fact := collectObservation(t, s, db, token, c, a)
	// Retained facts also need the global gate when the current source is different.
	saved, err := s.Save(context.Background(), token, SaveInput{Revision: c.Revision, BaseURL: remote, CredentialAction: "replace", Credential: credential, Confirmation: "更换地址"})
	if err != nil {
		t.Fatal(err)
	}
	return s, db, token, saved.Configuration, maintenanceInputFor(a, fact, fact)
}

func maintenancePricingFixture(t *testing.T) (*pricingFixture, MaintenanceInput) {
	t.Helper()
	f := newPricingFixture(t)
	s, db, token, c, in := maintenanceWithRemoteSource(t, f.c.BaseURL, "Bearer controlled-pricing-jwt")
	f.s, f.db, f.token, f.c = s, db, token, c
	setTestWriteGrant(t, s, token, writePricing, true)
	return f, in
}

func maintenanceDispatchFixture(t *testing.T) (*dispatchFixture, MaintenanceInput) {
	t.Helper()
	f := newDispatchFixture(t, true)
	s, db, token, c, in := maintenanceWithRemoteSource(t, f.c.BaseURL, "Bearer controlled-test-jwt")
	f.s, f.db, f.token, f.c = s, db, token, c
	f.a = adoptManaged(t, s, token, c, 1, "Current controlled account")
	setTestWriteGrant(t, s, token, writeScheduling, true)
	return f, in
}

func maintenanceRemoteStage(t *testing.T, stage string, s *Service, db *pgxpool.Pool, token string, in MaintenanceInput, unknown, review func()) {
	t.Helper()
	ctx := context.Background()
	plan, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil || plan.State != "ready" {
		t.Fatal("initial plan unavailable", plan, err)
	}
	if stage == "worker" {
		if _, err = s.ApplyMaintenancePlan(ctx, token, plan.ID, plan.Basis.Fingerprint, true); err != nil {
			t.Fatal(err)
		}
	}
	unknown()
	before := maintenanceFactSnapshot(t, db)
	switch stage {
	case "preflight":
		blocked, err := s.PreflightMaintenance(ctx, token, in)
		if err != nil || blocked.State != "blocked" || blocked.ResultID != nil {
			t.Fatal("unresolved native operation did not block preflight", blocked, err)
		}
		found := false
		for _, issue := range blocked.Issues {
			if issue.Code == "REMOTE_OPERATION_UNRESOLVED" && issue.Blocking && issue.ObservationID == nil && issue.WindowKind == nil {
				found = true
			}
		}
		if !found {
			t.Fatal("missing explicit global unresolved-operation issue", blocked.Issues)
		}
		if _, err = s.ApplyMaintenancePlan(ctx, token, blocked.ID, blocked.Basis.Fingerprint, true); !errors.Is(err, auth.ErrConflict) {
			t.Fatal("blocked plan queued", err)
		}
	case "confirm":
		current, err := s.GetMaintenancePlan(ctx, token, plan.ID)
		if err != nil || current.Validity != "basis_changed" {
			t.Fatal("ready plan remained applicable after an unknown native result", current, err)
		}
		if _, err = s.ApplyMaintenancePlan(ctx, token, plan.ID, plan.Basis.Fingerprint, true); !errors.Is(err, auth.ErrConflict) {
			t.Fatal("unknown native result passed confirmation", err)
		}
	case "worker":
		processMaintenanceForTest(t, s)
	}
	if stage != "preflight" {
		terminal, err := s.GetMaintenancePlan(ctx, token, plan.ID)
		if err != nil || terminal.State != "expired" || terminal.Reason != "BASIS_CHANGED" || terminal.ResultID != nil {
			t.Fatal("unknown native outcome produced an applied index", terminal, err)
		}
	}
	if maintenanceFactSnapshot(t, db) != before {
		t.Fatal("blocked maintenance altered source facts")
	}
	review()
	ready, err := s.PreflightMaintenance(ctx, token, in)
	if err != nil || ready.State != "ready" {
		t.Fatal("reviewed unknown outcome still blocked new explicit maintenance", ready, err)
	}
	if maintenanceFactSnapshot(t, db) != before {
		t.Fatal("manual review or new preview changed source facts")
	}
}

func TestMaintenanceRemotePricingUnknownBlocksPreflightConfirmationAndWorker(t *testing.T) {
	for _, stage := range []string{"preflight", "confirm", "worker"} {
		t.Run(stage, func(t *testing.T) {
			f, in := maintenancePricingFixture(t)
			var operation PricingOperation
			var reads int
			maintenanceRemoteStage(t, stage, f.s, f.db, f.token, in, func() {
				request := pricingInput(t, f, "1", fastPricing())
				f.mu.Lock()
				f.writeMode = "bad"
				f.mu.Unlock()
				var err error
				operation, err = f.s.ApplyPricing(context.Background(), f.token, request)
				if err != nil || operation.State != "needs_review" {
					t.Fatal("fixture did not produce a genuine uncertain native write", operation, err)
				}
				f.mu.Lock()
				reads = f.reads
				f.mu.Unlock()
			}, func() {
				p, err := f.s.ReviewPricing(context.Background(), f.token, PricingReviewInput{ID: operation.ID, Revision: operation.Revision, AcknowledgeResultUnknown: true})
				if err != nil || p.State != "reviewed" || p.Reason != "MANUALLY_REVIEWED_RESULT_UNKNOWN" {
					t.Fatal(p, err)
				}
			})
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 || f.reads != reads {
				t.Fatal("maintenance/review dispatched or replayed native traffic", f.writes, f.reads, reads)
			}
		})
	}
}

func TestMaintenanceRemoteDispatchUnknownBlocksPreflightConfirmationAndWorker(t *testing.T) {
	for _, stage := range []string{"preflight", "confirm", "worker"} {
		t.Run(stage, func(t *testing.T) {
			f, in := maintenanceDispatchFixture(t)
			var operation DispatchPause
			var reads int
			maintenanceRemoteStage(t, stage, f.s, f.db, f.token, in, func() {
				f.mu.Lock()
				f.postMode = "bad_response"
				f.mu.Unlock()
				var err error
				operation, err = f.s.PauseDispatch(context.Background(), f.token, dispatchRequest(t, f))
				if err != nil || operation.State != "needs_review" {
					t.Fatal("fixture did not retain uncertain scheduling result", operation, err)
				}
				f.mu.Lock()
				reads = f.reads
				f.mu.Unlock()
			}, func() {
				p, err := f.s.ReviewDispatch(context.Background(), f.token, DispatchReviewInput{DispatchChangeInput: DispatchChangeInput{ID: operation.ID, Revision: operation.Revision}, AcknowledgeResultUnknown: true})
				if err != nil || p.State != "reviewed" {
					t.Fatal(p, err)
				}
			})
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 1 || f.reads != reads {
				t.Fatal("maintenance/review dispatched or replayed native traffic", f.writes, f.reads, reads)
			}
		})
	}
}

func waitMaintenanceConnection(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := db.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM connection_config%' AND query NOT LIKE '%pg_stat_activity%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the actual connection-row wait was not observed")
}

func TestMaintenanceRemoteAndNativePricingSerializeBothOrders(t *testing.T) {
	for _, first := range []string{"maintenance", "native"} {
		t.Run(first, func(t *testing.T) {
			f, in := maintenancePricingFixture(t)
			ctx := context.Background()
			plan, err := f.s.PreflightMaintenance(ctx, f.token, in)
			if err != nil || plan.State != "ready" {
				t.Fatal(plan, err)
			}
			if _, err = f.s.ApplyMaintenancePlan(ctx, f.token, plan.ID, plan.Basis.Fingerprint, true); err != nil {
				t.Fatal(err)
			}
			request := pricingInput(t, f, "1", fastPricing())
			nativeDone, workerDone := make(chan error, 1), make(chan error, 1)
			startNative := func() {
				go func() {
					p, e := f.s.ApplyPricing(ctx, f.token, request)
					if e == nil && p.State != "applied" {
						e = errors.New("native write was not confirmed")
					}
					nativeDone <- e
				}()
			}
			startWorker := func() {
				go func() {
					conn, e := f.db.Acquire(ctx)
					if e == nil {
						defer conn.Release()
						_, e = f.s.processMaintenance(ctx, conn)
					}
					workerDone <- e
				}()
			}
			if first == "maintenance" {
				blocker, e := f.db.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer blocker.Rollback(ctx)
				if _, e = blocker.Exec(ctx, `LOCK TABLE management_events IN ACCESS EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
				startWorker()
				awaitMaintenanceLock(t, f.db, "management_events")
				startNative()
				waitMaintenanceConnection(t, f.db)
				f.mu.Lock()
				writes := f.writes
				f.mu.Unlock()
				if writes != 0 {
					t.Fatal("native write entered before the maintenance commit", writes)
				}
				if e = blocker.Commit(ctx); e != nil {
					t.Fatal(e)
				}
			} else {
				entered, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				f.mu.Lock()
				f.onWrite = func() { close(entered); <-release }
				f.mu.Unlock()
				startNative()
				select {
				case <-entered:
				case e := <-nativeDone:
					t.Fatal("native write did not enter", e)
				case <-time.After(5 * time.Second):
					t.Fatal("native write did not start")
				}
				startWorker()
				waitMaintenanceConnection(t, f.db)
				var state string
				if e := f.db.QueryRow(ctx, `SELECT state FROM maintenance_plans WHERE id=$1`, plan.ID).Scan(&state); e != nil || state != "running" {
					t.Fatal("maintenance passed an in-flight native write", state, e)
				}
				unblock()
			}
			for _, done := range []chan error{nativeDone, workerDone} {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("serialized operation did not finish")
				}
			}
			after, e := f.s.GetMaintenancePlan(ctx, f.token, plan.ID)
			if e != nil || after.State != "applied" || after.ResultID == nil {
				t.Fatal("confirmed native result blocked the independent retained-period index", after, e)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 {
				t.Fatal("native write replayed", f.writes)
			}
		})
	}
}
