package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func enableWalletAutomation(t *testing.T, f *simulatedApplicationFixture) WalletAutomation {
	t.Helper()
	status, err := f.s.GetWalletAutomation(context.Background(), f.token)
	if err != nil || status.Control == nil || status.Control.AvailableBinding == nil || !status.CanEnable {
		t.Fatal("qualified control unavailable", status, err)
	}
	binding := status.Control.AvailableBinding
	status, err = f.s.ConfigureWalletAutomation(context.Background(), f.token, WalletAutomationInput{Revision: status.Control.Revision, SourceID: binding.SourceID, ConnectionRevision: binding.ConnectionRevision, Simulation: &binding.Simulation}, true)
	if err != nil || !status.Enabled || status.ExecutionState != "waiting" {
		t.Fatal("explicit enable failed", status, err)
	}
	return status
}

func TestWalletAutomationPersistsWithoutBrowserAndConsidersEachRecommendationOnce(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	if did, err := f.s.processWalletAutomation(ctx); err != nil || did || f.writes.Load() != 0 {
		t.Fatal("default-off dispatched", did, err)
	}
	status := enableWalletAutomation(t, f)
	f.loseAck = true
	exec(t, f.db, "UPDATE auth_sessions SET revoked_at=clock_timestamp()")
	replacement, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	bindSimulation(t, replacement, f.c)
	var group sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() { defer group.Done(); _, err := replacement.processWalletAutomation(ctx); results <- err }()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var intents, outcomes, taskEvents, humanEvents int
	err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM wallet_application_results),(SELECT count(*) FROM management_events WHERE task_id='wallet_automation'),(SELECT count(*) FROM management_events WHERE action LIKE 'wallet.application.%' AND actor_id IS NOT NULL)`).Scan(&intents, &outcomes, &taskEvents, &humanEvents)
	if err != nil || intents != 1 || outcomes != 1 || taskEvents != 2 || humanEvents != 0 || f.writes.Load() != 1 {
		t.Fatal("background concurrency duplicated or misattributed work", intents, outcomes, taskEvents, humanEvents, f.writes.Load(), err)
	}
	var body []byte
	if err = f.db.QueryRow(ctx, "SELECT snapshot FROM wallet_application_requests").Scan(&body); err != nil || !bytes.Contains(body, []byte(`"control_revision": "`+status.Control.Revision+`"`)) {
		t.Fatal("intent lacks persisted explicit control", err)
	}
	if err = replacement.recoverSimulationWalletIntents(ctx); err != nil {
		t.Fatal(err)
	}
	if did, err := replacement.processWalletAutomation(ctx); err != nil || did || f.writes.Load() != 1 {
		t.Fatal("replacement replayed considered recommendation", did, err)
	}
}

func TestWalletAutomationUnknownSkipsNewRecommendationAndHistoryUsesReadOnlyReconciliation(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	enableWalletAutomation(t, f)
	f.lookupDenied = true
	if did, err := f.s.processWalletAutomation(ctx); err != nil || !did {
		t.Fatal("unknown run missing", did, err)
	}
	page, err := f.s.ListWalletAutomation(ctx, f.token, "0", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "unknown" || page.Items[0].Counts.Unknown != 1 {
		t.Fatal("unknown history", page, err)
	}
	unknown := page.Items[0].ID
	f.refresh(t, f.input.LatestID)
	if did, err := f.s.processWalletAutomation(ctx); err != nil || !did || f.writes.Load() != 1 {
		t.Fatal("unresolved wallet was retried or lost skip receipt", did, err, f.writes.Load())
	}
	page, err = f.s.ListWalletAutomation(ctx, f.token, "0", 1)
	if err != nil || page.NextAfter == nil || page.Items[0].Counts.Skipped != 1 || page.Items[0].Reason != "SIMULATION_UNRESOLVED_APPLICATION" {
		t.Fatal("skip history", page, err)
	}
	older, err := f.s.ListWalletAutomation(ctx, f.token, *page.NextAfter, 1)
	if err != nil || len(older.Items) != 1 || older.Items[0].ID != unknown || older.NextAfter != nil {
		t.Fatal("numeric history cursor lost a run", older, err)
	}
	f.mu.Lock()
	f.lookupDenied = false
	f.mu.Unlock()
	if row, err := f.s.ReconcileWalletApplication(ctx, f.token, unknown); err != nil || row.State != "applied" || f.writes.Load() != 1 {
		t.Fatal("automation reconciliation replayed", row, err)
	}
	older, err = f.s.ListWalletAutomation(ctx, f.token, *page.NextAfter, 1)
	if err != nil || older.Items[0].Counts.Applied != 1 {
		t.Fatal("history failed to project latest append-only outcome", older, err)
	}
	status, err := f.s.GetWalletAutomation(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ConfigureWalletAutomation(ctx, f.token, WalletAutomationInput{Revision: status.Control.Revision}, false); err != nil {
		t.Fatal(err)
	}
	enableWalletAutomation(t, f)
	if did, err := f.s.processWalletAutomation(ctx); err != nil || did || f.writes.Load() != 1 {
		t.Fatal("re-enable replayed prior recommendations", did, err)
	}
}

func TestWalletAutomationPausesChangedOwnerOrSourceAndCanAlwaysDisable(t *testing.T) {
	for _, ownerChanged := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner", false: "source"}[ownerChanged], func(t *testing.T) {
			f := simulatedApplication(t)
			ctx := context.Background()
			status := enableWalletAutomation(t, f)
			if ownerChanged {
				exec(t, f.db, "UPDATE auth_users SET revision=revision+1 WHERE id=(SELECT actor_id FROM wallet_automation)")
			} else {
				saved, err := f.s.Save(ctx, f.token, SaveInput{Revision: f.c.Revision, BaseURL: f.c.BaseURL, CredentialAction: "keep"})
				if err != nil {
					t.Fatal(err)
				}
				bindSimulation(t, f.s, saved.Configuration)
				actual, err := f.s.GetWalletAutomation(ctx, f.token)
				if err != nil || !actual.Enabled || actual.ExecutionState != "paused" || actual.Reason != "SIMULATION_BINDING_CHANGED" {
					t.Fatal("source change silently rebound", actual, err)
				}
				if disabled, err := f.s.ConfigureWalletAutomation(ctx, f.token, WalletAutomationInput{Revision: status.Control.Revision}, false); err != nil || disabled.Enabled {
					t.Fatal("stale source prevented disabling", disabled, err)
				}
			}
			if did, err := f.s.processWalletAutomation(ctx); err != nil || did || f.writes.Load() != 0 {
				t.Fatal("stale authority dispatched", did, err)
			}
		})
	}
}

func TestWalletAutomationDisableSerializesWithActiveSend(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	status := enableWalletAutomation(t, f)
	f.arrive, f.release = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := f.s.processWalletAutomation(ctx); done <- err }()
	select {
	case <-f.arrive:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never reached owned source")
	}
	wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err := f.s.ConfigureWalletAutomation(wait, f.token, WalletAutomationInput{Revision: status.Control.Revision}, false)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("disable overtook an active financial send", err)
	}
	close(f.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	disabled, err := f.s.ConfigureWalletAutomation(ctx, f.token, WalletAutomationInput{Revision: status.Control.Revision}, false)
	if err != nil || disabled.Enabled {
		t.Fatal("disable failed after current send", disabled, err)
	}
	f.refresh(t, f.input.LatestID)
	if did, err := f.s.processWalletAutomation(ctx); err != nil || did || f.writes.Load() != 1 {
		t.Fatal("committed disable allowed a subsequent send", did, err)
	}
}

func TestWalletAutomationClosesUnsentIntentAfterAuthorityOrControlChanges(t *testing.T) {
	for _, ownerChanged := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner", false: "control"}[ownerChanged], func(t *testing.T) {
			f := simulatedApplication(t)
			ctx := context.Background()
			status := enableWalletAutomation(t, f)
			tx, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			control, err := readWalletAutomation(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			c, err := f.s.config(ctx, tx, false)
			if err != nil {
				t.Fatal(err)
			}
			basis, err := f.s.walletBasis(ctx, tx, f.p.ID)
			if err != nil {
				t.Fatal(err)
			}
			read, err := latestWalletRead(ctx, tx, f.p.ID, f.p.Binding.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			recommendation, err := latestWalletRecommendation(ctx, tx, f.p.ID, f.p.Binding.ID)
			if err != nil || recommendation == nil {
				t.Fatal(err)
			}
			out := WalletApplication{ParticipantID: f.p.ID, AttemptedAt: time.Now().UTC().Truncate(time.Microsecond), WalletApplicationSnapshot: WalletApplicationSnapshot{
				ParticipantName: f.p.Name, State: "rejected", DispatchState: "not_dispatched", Details: &WalletApplicationDetails{
					ActorID: control.ActorID, Input: f.input, UpstreamUserID: f.p.Binding.UpstreamUserID, EvaluatedBasisHash: recommendation.BasisHash,
					RecommendationBasisHash: &recommendation.BasisHash, Automation: &WalletAutomationTag{ControlRevision: control.Revision},
				},
			}}
			if err = f.s.prepareSimulationWalletApplication(ctx, tx, c, f.p, basis, read, recommendation, &out); err != nil || out.State != "pending" {
				t.Fatal("intent fixture", out, err)
			}
			body, _ := json.Marshal(out.WalletApplicationSnapshot)
			err = tx.QueryRow(ctx, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, control.ActorID, f.input.RequestID, strings.Repeat("a", 64), f.p.ID, f.p.Binding.ID, out.AttemptedAt, body).Scan(&out.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if ownerChanged {
				exec(t, f.db, "UPDATE auth_users SET revision=revision+1 WHERE id=$1", control.ActorID)
			} else if _, err = f.s.ConfigureWalletAutomation(ctx, f.token, WalletAutomationInput{Revision: status.Control.Revision}, false); err != nil {
				t.Fatal(err)
			}
			conn, err := f.db.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			actual, err := f.s.executeSimulationWalletApplication(ctx, conn, "", out.ID, &control)
			if err != nil || actual.State != "rejected" || actual.DispatchState != "not_dispatched" || actual.Reason != "SIMULATION_PRECONDITION_CHANGED" || f.writes.Load() != 0 {
				t.Fatal("known unsent intent became an unknown obligation or dispatched", actual, err)
			}
			var resolved bool
			if err = f.db.QueryRow(ctx, "SELECT resolved FROM wallet_application_results WHERE application_id=$1", out.ID).Scan(&resolved); err != nil || !resolved {
				t.Fatal("unsent intent was not resolved", resolved, err)
			}
		})
	}
}

func TestWalletAutomationControlAuditFailureRollsBackEnable(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	exec(t, f.db, `CREATE FUNCTION reject_automation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='wallet.automation.enable' THEN RAISE EXCEPTION 'fixture rejects control audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_automation_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_automation_audit()`)
	_, err := f.s.ConfigureWalletAutomation(ctx, f.token, WalletAutomationInput{Revision: "1", SourceID: *f.c.SourceID, ConnectionRevision: f.c.Revision, Simulation: f.input.Simulation}, true)
	if err == nil {
		t.Fatal("failed audit enabled automation")
	}
	status, err := f.s.GetWalletAutomation(ctx, f.token)
	if err != nil || status.Enabled || status.Control.Revision != "1" {
		t.Fatal("control audit did not roll back", status, err)
	}
	if did, err := f.s.processWalletAutomation(ctx); err != nil || did || f.writes.Load() != 0 {
		t.Fatal("rolled-back control dispatched", did, err)
	}
}
