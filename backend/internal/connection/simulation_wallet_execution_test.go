package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

type simulatedApplicationFixture struct {
	*simulatedWalletFixture
	input                         WalletApplicationInput
	writes, lookups               atomic.Int32
	proofs                        map[string]simulationWalletWriteProof
	lookupDenied, loseAck, reject bool
	arrive, release               chan struct{}
}

func simulatedApplication(t *testing.T) *simulatedApplicationFixture {
	t.Helper()
	f := &simulatedApplicationFixture{simulatedWalletFixture: simulatedWallet(t), proofs: map[string]simulationWalletWriteProof{}}
	read := f.refresh(t, "0")
	view, err := f.s.GetWallet(context.Background(), f.token, f.p.ID)
	if err != nil || view.CurrentTarget == nil {
		t.Fatal("application prerequisite", err)
	}
	f.input = WalletApplicationInput{WalletExpected: walletExpected(f.p, f.c, read.ID), RequestID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 16)), RecommendationID: view.LatestRecommendation.ID, Simulation: view.LatestRecommendation.Simulation}
	previous := f.serve
	f.serve = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == simulationPrefix+"/wallets/21/write-contract" {
			respond(w, map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": simulationTestEpoch, "user_id": "21", "quantum": "0.00000001", "write_allowed": true, "lookup_allowed": true})
			return true
		}
		if strings.HasPrefix(r.URL.Path, simulationPrefix+"/wallets/21/applications/") {
			f.lookups.Add(1)
			f.mu.RLock()
			proof, found := f.proofs[strings.TrimPrefix(r.URL.Path, simulationPrefix+"/wallets/21/applications/")]
			denied := f.lookupDenied
			f.mu.RUnlock()
			if !found || denied {
				http.NotFound(w, r)
			} else {
				respond(w, proof)
			}
			return true
		}
		return previous(w, r)
	}
	f.write = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "POST" || r.URL.Path != simulationPrefix+"/wallets/21/applications" {
			return false
		}
		f.writes.Add(1)
		var request simulationWalletWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad", 400)
			return true
		}
		f.mu.Lock()
		if _, exists := f.proofs[request.OperationID]; exists {
			f.mu.Unlock()
			t.Error("application POST was replayed")
			http.Error(w, "duplicate", 409)
			return true
		}
		raw, _ := json.Marshal(f.scope)
		var before simulationWalletScope
		_ = json.Unmarshal(raw, &before)
		body, _ := json.Marshal(request)
		var canonical map[string]json.RawMessage
		_ = json.Unmarshal(body, &canonical)
		body, _ = json.Marshal(canonical)
		digest := sha256.Sum256(body)
		at := time.Now().UTC()
		proof := simulationWalletWriteProof{SimulationProvenance: before.SimulationProvenance, OperationID: request.OperationID, UserID: "21", RequestHash: hex.EncodeToString(digest[:]), Request: request, State: "rejected", Reason: "PRECONDITION_CHANGED", Before: before, After: before, OccurredAt: at}
		if f.reject {
			proof.Reason = "WRITE_DENIED"
		} else if request.ExpectedWalletVersion == before.WalletVersion && request.ExpectedLedgerRevision == before.LedgerRevision && request.ExpectedClosedThrough.Equal(before.ClosedThrough) && reflect.DeepEqual(request.SourceAccountIDs, before.SourceAccountIDs) && before.FrozenBalance == "0" && before.Pending == "0" {
			proof.State, proof.Reason = "applied", "APPLIED"
			proof.After.Balance = request.Target
			version, _ := strconv.Atoi(before.WalletVersion)
			proof.After.WalletVersion = strconv.Itoa(version + 1)
			version, _ = strconv.Atoi(before.LedgerRevision)
			proof.After.LedgerRevision = strconv.Itoa(version + 1)
			proof.After.ClosedThrough = at
			f.scope["balance"], f.scope["wallet_version"], f.scope["ledger_revision"], f.scope["closed_through"] = proof.After.Balance, proof.After.WalletVersion, proof.After.LedgerRevision, at.Format(time.RFC3339Nano)
		}
		f.proofs[request.OperationID] = proof
		lose, arrive, release := f.loseAck, f.arrive, f.release
		f.mu.Unlock()
		if arrive != nil {
			close(arrive)
			<-release
		}
		if lose {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = conn.Close()
			}
			return true
		}
		respond(w, map[string]any{"accepted": true})
		return true
	}
	return f
}

func TestSimulationWalletApplicationExecutesOnceWithIndependentProofAndPrivateSummary(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	f.loseAck = true
	results := make(chan WalletApplication, 4)
	errs := make(chan error, 4)
	for range 4 {
		go func() { row, err := f.s.RequestWalletApplication(ctx, f.token, f.input); results <- row; errs <- err }()
	}
	first := <-results
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if row := <-results; !reflect.DeepEqual(row, first) {
			t.Fatal("same key diverged", row, first)
		}
	}
	if first.State != "applied" || first.DispatchState != "confirmed" || first.Target == nil || first.Target.Numerator != "77" || first.Target.Denominator != "40" || first.Details.SimulationProof == nil || f.writes.Load() != 1 || f.lookups.Load() != 1 {
		t.Fatal("lost acknowledgement was not independently confirmed", first, f.writes.Load(), f.lookups.Load())
	}
	before := f.calls.Load()
	lookups := f.lookups.Load()
	retained, err := f.s.GetWalletApplication(ctx, f.token, first.ID)
	if err != nil || !reflect.DeepEqual(first, retained) || f.calls.Load() != before || f.lookups.Load() != lookups {
		t.Fatal("history changed or called source", err)
	}
	changed := f.input
	changed.LatestID = "999"
	if _, err = f.s.RequestWalletApplication(ctx, f.token, changed); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("same key changed input", err)
	}
	user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "simulation.application.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := f.s.GetWalletApplication(ctx, reader, first.ID)
	if err != nil || public.Target != nil || public.Details != nil || public.State != "applied" {
		t.Fatal("summary leaked global money or lost outcome", public, err)
	}
	for state, reason := range map[string]string{"rejected": "SIMULATED_APPLICATION_UNAVAILABLE", "failed": "SIMULATED_APPLICATION_FAILED", "unknown": "SIMULATED_WRITE_RESULT_UNKNOWN"} {
		private := first
		private.State = state
		private.Reason = "SENSITIVE_SOURCE_REASON"
		projected := projectWalletApplication(private, auth.Access{})
		if projected.Target != nil || projected.Details != nil || projected.Reason != reason || projected.Visibility != "participant_summary" {
			t.Fatal("ordinary projection leaked amount or private reason", projected)
		}
	}
	if _, err = f.s.ReconcileWalletApplication(ctx, reader, first.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary reader reconciled", err)
	}
	var intents, outcomes, audits int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_application_requests),(SELECT count(*) FROM wallet_application_results),(SELECT count(*) FROM management_events WHERE action LIKE 'wallet.application.%')`).Scan(&intents, &outcomes, &audits); err != nil || intents != 1 || outcomes != 1 || audits != 2 {
		t.Fatal("intent/result audit mismatch", intents, outcomes, audits, err)
	}
}

func TestSimulationWalletApplicationUnknownBlocksNewWritesAndReconcilesWithoutReplay(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	f.lookupDenied = true
	row, err := f.s.RequestWalletApplication(ctx, f.token, f.input)
	if err != nil || row.State != "unknown" || f.writes.Load() != 1 {
		t.Fatal("accepted-only response became success", row, err)
	}
	next := f.input
	next.RequestID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{10}, 16))
	if _, err = f.s.RequestWalletApplication(ctx, f.token, next); !errors.Is(err, auth.ErrConflict) || f.writes.Load() != 1 {
		t.Fatal("unknown intent allowed another write", err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = participantObligations(ctx, tx, f.p.ID)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, errParticipantExitBlocked) {
		t.Fatal("unknown wallet lost lifecycle obligation", err)
	}
	impact, err := f.s.GetConnectionImpact(ctx, f.token)
	if err != nil || impact.WalletUnconfirmed != "1" {
		t.Fatal("unknown application missing from source impact", impact, err)
	}
	if _, err = f.s.ExitParticipant(ctx, f.token, f.p.ID, f.p.Revision, f.p.Name); !errors.Is(err, errParticipantExitBlocked) {
		t.Fatal("unknown wallet allowed exit", err)
	}
	if _, err = f.s.Save(ctx, f.token, SaveInput{Revision: f.c.Revision, BaseURL: "https://changed-wallet.invalid", CredentialAction: "clear", Confirmation: "更换地址"}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("unknown wallet allowed source replacement", err)
	}
	rotated, err := f.s.Save(ctx, f.token, SaveInput{Revision: f.c.Revision, BaseURL: f.c.BaseURL, CredentialAction: "keep"})
	if err != nil || rotated.SourceChanged {
		t.Fatal("same-source revision prevented read-only recovery", err)
	}
	bindSimulation(t, f.s, rotated.Configuration)
	f.mu.Lock()
	f.lookupDenied = false
	f.mu.Unlock()
	row, err = f.s.ReconcileWalletApplication(ctx, f.token, row.ID)
	if err != nil || row.State != "applied" || f.writes.Load() != 1 {
		t.Fatal("read-only reconciliation failed or replayed", row, err)
	}
	tx, _ = f.db.Begin(ctx)
	err = participantObligations(ctx, tx, f.p.ID)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal("resolved result retained unknown-write obligation", err)
	}
	impact, err = f.s.GetConnectionImpact(ctx, f.token)
	if err != nil || impact.WalletUnconfirmed != "0" || impact.WalletRejected != "0" {
		t.Fatal("resolved application counted as an open source obligation", impact, err)
	}
	replaced, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := replaced.GetWalletApplication(ctx, f.token, row.ID)
	if err != nil || !reflect.DeepEqual(row, retained) || f.writes.Load() != 1 {
		t.Fatal("replacement rewrote outcome", err)
	}
}

func TestSimulationWalletApplicationRejectsChangedScopeAndIndependentlyConfirmsDenial(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			f := simulatedApplication(t)
			ctx := context.Background()
			if changed {
				f.mu.Lock()
				f.scope["wallet_version"] = "8"
				f.mu.Unlock()
			} else {
				f.reject = true
			}
			row, err := f.s.RequestWalletApplication(ctx, f.token, f.input)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				if row.State != "rejected" || row.DispatchState != "not_dispatched" || f.writes.Load() != 0 {
					t.Fatal("stale source precondition dispatched", row)
				}
			} else if row.State != "failed" || row.DispatchState != "confirmed_failed" || f.writes.Load() != 1 || row.Details.SimulationProof == nil {
				t.Fatal("explicit denial lacked proof", row)
			}
		})
	}
}

func TestSimulationWalletApplicationKnownZeroRetainsRemainderWithoutDispatch(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	zero := func(page map[string]any) {
		page["actual_cost"] = "0"
		for _, price := range page["prices"].([]any) {
			price.(map[string]any)["wallet_multiplier"] = "0"
		}
		for _, entry := range page["entries"].([]any) {
			entry.(map[string]any)["actual_cost"] = "0"
		}
	}
	f.mu.Lock()
	previous := f.change
	f.change = func(page map[string]any, r *http.Request) { previous(page, r); zero(page) }
	f.changeLedger = zero
	f.scope["balance"] = "100"
	f.mu.Unlock()
	current, err := f.s.GetEstimateCurrent(ctx, f.token, f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RecomputeEstimate(ctx, f.token, f.a.ID, current.Configuration.Revision, current.Latest.ID); err != nil {
		t.Fatal(err)
	}
	read := f.refresh(t, f.input.LatestID)
	view, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || view.CurrentTarget == nil || view.CurrentTarget.Numerator != "0" {
		t.Fatal("known-zero prerequisite", view, err)
	}
	input := f.input
	input.LatestID = read.ID
	input.RecommendationID = view.LatestRecommendation.ID
	row, err := f.s.RequestWalletApplication(ctx, f.token, input)
	if err != nil || row.State != "rejected" || row.Reason != "ZERO_TARGET_PENDING" || row.Target != nil || row.Details.SimulationWrite == nil || row.Details.SimulationWrite.Remainder.Numerator != "0" || f.writes.Load() != 0 {
		t.Fatal("known zero dispatched or lost exact remainder", row, err)
	}
}

func TestSimulationWalletApplicationRecoverySkipsLiveWriterAndNeverDispatches(t *testing.T) {
	f := simulatedApplication(t)
	ctx := context.Background()
	f.arrive = make(chan struct{})
	f.release = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := f.s.RequestWalletApplication(ctx, f.token, f.input); done <- err }()
	select {
	case <-f.arrive:
	case <-time.After(3 * time.Second):
		t.Fatal("writer never reached source")
	}
	if err := f.s.recoverSimulationWalletIntents(ctx); err != nil {
		close(f.release)
		t.Fatal(err)
	}
	var results int
	err := f.db.QueryRow(ctx, `SELECT count(*) FROM wallet_application_results`).Scan(&results)
	close(f.release)
	if final := <-done; final != nil || err != nil || results != 0 || f.writes.Load() != 1 {
		t.Fatal("recovery changed a live intent", results, err, final)
	}
}

func TestSimulationWalletApplicationAuditFailureRecoversUnknownAndUsesOnlyProofReads(t *testing.T) {
	for _, afterDispatch := range []bool{false, true} {
		t.Run(strconv.FormatBool(afterDispatch), func(t *testing.T) {
			f := simulatedApplication(t)
			ctx := context.Background()
			action := "wallet.application.request"
			if afterDispatch {
				action = "wallet.application.applied"
			}
			exec(t, f.db, `CREATE FUNCTION reject_application_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+action+`' THEN RAISE EXCEPTION 'fixture rejects application audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_application_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_application_audit()`)
			if row, err := f.s.RequestWalletApplication(ctx, f.token, f.input); err == nil || row.ID != "" {
				t.Fatal("audit failure returned protected success", row, err)
			}
			var id string
			err := f.db.QueryRow(ctx, `SELECT id::text FROM wallet_application_requests`).Scan(&id)
			if !afterDispatch {
				if err == nil || f.writes.Load() != 0 {
					t.Fatal("failed initial audit dispatched or persisted", err)
				}
				return
			}
			if err != nil || f.writes.Load() != 1 {
				t.Fatal("post-dispatch fixture failed", err)
			}
			exec(t, f.db, `DROP TRIGGER reject_application_audit ON management_events; DROP FUNCTION reject_application_audit()`)
			if err = f.s.recoverSimulationWalletIntents(ctx); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetWalletApplication(ctx, f.token, id)
			if err != nil || row.State != "unknown" || row.Reason != "PROCESS_INTERRUPTED_RESULT_UNKNOWN" || f.writes.Load() != 1 {
				t.Fatal("recovery lost uncertainty or replayed", row, err)
			}
			row, err = f.s.ReconcileWalletApplication(ctx, f.token, id)
			if err != nil || row.State != "applied" || f.writes.Load() != 1 {
				t.Fatal("recovered intent did not reconcile read-only", row, err)
			}
		})
	}
}
