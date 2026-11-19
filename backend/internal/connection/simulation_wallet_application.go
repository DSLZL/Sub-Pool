package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

const simulationWalletQuantum = "0.00000001"

type simulationWalletWriteContract struct {
	SimulationProvenance
	UserID        string `json:"user_id"`
	Quantum       string `json:"quantum"`
	WriteAllowed  bool   `json:"write_allowed"`
	LookupAllowed bool   `json:"lookup_allowed"`
}

type simulationWalletWriteRequest struct {
	SimulationProvenance
	OperationID            string    `json:"operation_id"`
	UserID                 string    `json:"user_id"`
	ExpectedWalletVersion  string    `json:"expected_wallet_version"`
	ExpectedLedgerRevision string    `json:"expected_ledger_revision"`
	ExpectedClosedThrough  time.Time `json:"expected_closed_through"`
	SourceAccountIDs       []string  `json:"source_account_ids"`
	Target                 string    `json:"target"`
	Quantum                string    `json:"quantum"`
}

type simulationWalletWriteIntent struct {
	Request        simulationWalletWriteRequest `json:"request"`
	Before         simulationWalletScope        `json:"before"`
	OriginalTarget estimation.Amount            `json:"original_target"`
	Remainder      estimation.Amount            `json:"remainder"`
}

type simulationWalletWriteProof struct {
	SimulationProvenance
	OperationID string                       `json:"operation_id"`
	UserID      string                       `json:"user_id"`
	RequestHash string                       `json:"request_hash"`
	Request     simulationWalletWriteRequest `json:"request"`
	State       string                       `json:"state"`
	Reason      string                       `json:"reason"`
	Before      simulationWalletScope        `json:"before"`
	After       simulationWalletScope        `json:"after"`
	OccurredAt  time.Time                    `json:"occurred_at"`
}

func simulationWalletTarget(original *estimation.Amount, quantum string) (*estimation.Amount, *estimation.Amount, string, string) {
	if original == nil || quantum != simulationWalletQuantum {
		return nil, nil, "", "SIMULATION_TARGET_UNAVAILABLE"
	}
	value, valid := original.Rational()
	if !valid || value.Sign() < 0 {
		return nil, nil, "", "SIMULATION_TARGET_UNAVAILABLE"
	}
	q, _ := simulationDecimal(quantum)
	scaled := new(big.Rat).Quo(value, q)
	units := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	target := new(big.Rat).Mul(new(big.Rat).SetInt(units), q)
	remainder := new(big.Rat).Sub(value, target)
	decimal := target.FloatString(8)
	if _, ok := simulationDecimal(decimal); !ok {
		return nil, nil, "", "SIMULATION_TARGET_UNAVAILABLE"
	}
	if target.Sign() == 0 {
		return nil, estimation.Exact(remainder), "", "ZERO_TARGET_PENDING"
	}
	return estimation.Exact(target), estimation.Exact(remainder), decimal, ""
}

func simulationWriteHash(in simulationWalletWriteRequest) string {
	body, _ := json.Marshal(in)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	// Sorted ASCII keys give the independent source the same exact request identity.
	canonical, _ := json.Marshal(fields)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func simulationWalletAccounts(a, b []string) bool {
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}

func simulationWalletClosed(scope simulationWalletScope) bool {
	frozen, valid := simulationDecimal(scope.FrozenBalance)
	return valid && frozen.Sign() == 0 && scope.Pending == "0" && scope.ScopeComplete && scope.BillingType == "standard_wallet" && scope.LedgerClosed
}

func simulationWalletPrecondition(scope simulationWalletScope, request simulationWalletWriteRequest) bool {
	return simulationWalletClosed(scope) && scope.SimulationProvenance == request.SimulationProvenance && scope.UserID == request.UserID && scope.WalletVersion == request.ExpectedWalletVersion && scope.LedgerRevision == request.ExpectedLedgerRevision && scope.ClosedThrough.Equal(request.ExpectedClosedThrough) && simulationWalletAccounts(scope.SourceAccountIDs, request.SourceAccountIDs)
}

func simulationMoneyEqual(a, b string) bool {
	x, okX := simulationDecimal(a)
	y, okY := simulationDecimal(b)
	return okX && okY && x.Cmp(y) == 0
}

func simulationVersionCompare(a, b string) (int, bool) {
	if !validRevision(a) || !validRevision(b) {
		return 0, false
	}
	x, _ := new(big.Int).SetString(a, 10)
	y, _ := new(big.Int).SetString(b, 10)
	return x.Cmp(y), true
}

func simulationVersionNext(after, before string) bool {
	if _, ok := simulationVersionCompare(after, before); !ok {
		return false
	}
	x, _ := new(big.Int).SetString(after, 10)
	y, _ := new(big.Int).SetString(before, 10)
	return x.Cmp(y.Add(y, big.NewInt(1))) == 0
}

func inspectSimulationWalletProof(body json.RawMessage, intent simulationWalletWriteIntent) (*simulationWalletWriteProof, bool) {
	var proof simulationWalletWriteProof
	if !simulationObject(body, "protocol environment epoch operation_id user_id request_hash request state reason before after occurred_at", &proof) {
		return nil, false
	}
	fields, _ := usageObject(body)
	var request simulationWalletWriteRequest
	if !simulationObject(fields["request"], "protocol environment epoch operation_id user_id expected_wallet_version expected_ledger_revision expected_closed_through source_account_ids target quantum", &request) || !reflect.DeepEqual(request, intent.Request) {
		return nil, false
	}
	before, okB := inspectSimulationWallet(fields["before"], request.SimulationProvenance, request.UserID)
	after, okA := inspectSimulationWallet(fields["after"], request.SimulationProvenance, request.UserID)
	if !okB || !okA || proof.SimulationProvenance != request.SimulationProvenance || proof.OperationID != request.OperationID || proof.UserID != request.UserID || proof.RequestHash != simulationWriteHash(request) || !simulationTime(fields["occurred_at"]) || proof.OccurredAt.After(time.Now()) || proof.OccurredAt.Before(before.ClosedThrough) {
		return nil, false
	}
	switch proof.State {
	case "applied":
		if proof.Reason != "APPLIED" || !simulationWalletPrecondition(before, request) || !simulationMoneyEqual(before.Balance, intent.Before.Balance) || !simulationWalletClosed(after) || !simulationWalletAccounts(after.SourceAccountIDs, before.SourceAccountIDs) || !simulationVersionNext(after.WalletVersion, before.WalletVersion) || !simulationVersionNext(after.LedgerRevision, before.LedgerRevision) || !simulationMoneyEqual(after.Balance, request.Target) || !after.ClosedThrough.Equal(proof.OccurredAt) {
			return nil, false
		}
	case "rejected":
		if proof.Reason != "PRECONDITION_CHANGED" && proof.Reason != "WRITE_DENIED" || !reflect.DeepEqual(before, after) {
			return nil, false
		}
	default:
		return nil, false
	}
	return &proof, true
}

func simulationWalletConfirms(current simulationWalletScope, proof simulationWalletWriteProof) bool {
	wallet, okW := simulationVersionCompare(current.WalletVersion, proof.After.WalletVersion)
	ledger, okL := simulationVersionCompare(current.LedgerRevision, proof.After.LedgerRevision)
	if !okW || !okL || wallet < 0 || ledger < 0 || current.SimulationProvenance != proof.SimulationProvenance || current.UserID != proof.UserID {
		return false
	}
	// A later debit does not undo a separately proved operation.
	return wallet > 0 || simulationMoneyEqual(current.Balance, proof.After.Balance) && simulationMoneyEqual(current.FrozenBalance, proof.After.FrozenBalance)
}

func unlockWalletSession(conn *pgxpool.Conn, participant string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, 1380991564))`, participant).Scan(&unlocked); err != nil || !unlocked {
		_ = conn.Conn().Close(ctx)
	}
}

func (s *Service) prepareSimulationWalletApplication(ctx context.Context, tx pgx.Tx, c storedConfig, p Participant, basis walletBasis, read *WalletRead, recommendation *WalletRecommendation, out *WalletApplication) error {
	expected := SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: s.simulationEpoch(c)}
	if expected.Epoch == "" || out.Details.Input.Simulation == nil || *out.Details.Input.Simulation != expected {
		return auth.ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	out.Simulation, out.Unit, out.UpdatedAt = &expected, "simulated_wallet_usd", &now
	out.Reason = "SIMULATION_TARGET_UNAVAILABLE"
	out.Details.Reasons = []string{"CONDITIONAL_SIMULATION_ONLY", out.Reason}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=$1 AND NOT (`+walletApplicationResolvedSQL+`))`, p.ID).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		if out.Details.Automation != nil {
			out.Reason = "SIMULATION_UNRESOLVED_APPLICATION"
			out.Details.Reasons[1] = out.Reason
			return nil
		}
		return auth.ErrConflict
	}
	current := s.simulationWalletCurrent(c, basis, p.ID, now, recommendation)
	if !current.Complete || current.Target == nil || current.ExpiresAt == nil || read == nil || read.SimulationScope == nil || read.Read.State != "available" || read.FieldAuthority.State != "available" || read.SimulationScope.SimulationProvenance != expected || !simulationWalletClosed(*read.SimulationScope) {
		return nil
	}
	available, err := s.simulationWalletWriteAvailable(ctx, c, p.Binding.UpstreamUserID)
	if err != nil {
		return err
	}
	if !available {
		out.Reason = "SIMULATION_WRITE_UNAVAILABLE"
		out.Details.Reasons[1] = out.Reason
		return nil
	}
	fresh, err := s.walletBasis(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if walletRecommendation(fresh, p.ID, time.Now().UTC()).BasisHash != current.BasisHash || !time.Now().Before(*current.ExpiresAt) {
		out.Reason = "RECOMMENDATION_STALE"
		out.Details.Reasons[1] = out.Reason
		return nil
	}
	target, remainder, decimal, reason := simulationWalletTarget(current.Target, simulationWalletQuantum)
	if remainder == nil {
		out.Reason = reason
		out.Details.Reasons[1] = reason
		return nil
	}
	if decimal == "" {
		decimal = "0.00000000"
	}
	digest := sha256.Sum256([]byte("wallet-application:" + out.Details.ActorID + ":" + out.Details.Input.RequestID))
	accounts := append([]string{}, read.SimulationScope.SourceAccountIDs...)
	sort.Strings(accounts)
	request := simulationWalletWriteRequest{SimulationProvenance: expected, OperationID: hex.EncodeToString(digest[:]), UserID: p.Binding.UpstreamUserID, ExpectedWalletVersion: read.SimulationScope.WalletVersion, ExpectedLedgerRevision: read.SimulationScope.LedgerRevision, ExpectedClosedThrough: read.SimulationScope.ClosedThrough, SourceAccountIDs: accounts, Target: decimal, Quantum: simulationWalletQuantum}
	out.Details.SimulationWrite = &simulationWalletWriteIntent{Request: request, Before: *read.SimulationScope, OriginalTarget: *current.Target, Remainder: *remainder}
	if reason != "" {
		out.Reason = reason
		out.Details.Reasons[1] = reason
		return nil
	}
	out.State, out.DispatchState, out.Reason, out.Target = "pending", "pending", "SIMULATED_APPLY_PENDING", target
	out.Details.Reasons = []string{"CONDITIONAL_SIMULATION_ONLY", out.Reason}
	return nil
}

func (s *Service) simulationWalletApplicationCurrent(ctx context.Context, tx pgx.Tx, c storedConfig, out WalletApplication) (Participant, bool, error) {
	var p Participant
	if out.Details == nil || out.Details.SimulationWrite == nil || out.Simulation == nil || c.SourceID == nil || *c.SourceID != out.Details.Input.SourceID || out.Simulation.Epoch != s.simulationEpoch(c) || c.Revision != out.Details.Input.ConnectionRevision {
		return p, false, nil
	}
	var err error
	p, err = readParticipant(ctx, tx, out.ParticipantID, c, false, false)
	if errors.Is(err, auth.ErrNotFound) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	in := out.Details.Input
	if !p.Enabled || !p.SourceCurrent || p.Revision != in.ParticipantRevision || p.Binding.ID != in.BindingID || p.Binding.EndedAt != nil || p.Binding.UpstreamUserID != out.Details.SimulationWrite.Request.UserID {
		return p, false, nil
	}
	read, err := latestWalletRead(ctx, tx, p.ID, p.Binding.ID, false)
	if err != nil {
		return p, false, err
	}
	recommendation, err := latestWalletRecommendation(ctx, tx, p.ID, p.Binding.ID)
	if err != nil {
		return p, false, err
	}
	if walletReadID(read) != in.LatestID || recommendation == nil || recommendation.ID != in.RecommendationID {
		return p, false, nil
	}
	basis, err := s.walletBasis(ctx, tx, p.ID)
	if err != nil {
		return p, false, err
	}
	current := s.simulationWalletCurrent(c, basis, p.ID, time.Now().UTC(), recommendation)
	return p, current.Complete && current.Target != nil && current.BasisHash == out.Details.EvaluatedBasisHash && reflect.DeepEqual(*current.Target, out.Details.SimulationWrite.OriginalTarget), nil
}

func saveSimulationWalletResult(ctx context.Context, tx pgx.Tx, out *WalletApplication, actor, task string) error {
	if out.Simulation == nil || out.Details == nil || out.Details.SimulationWrite == nil {
		return auth.ErrConflict
	}
	out.Details.Reasons = []string{"CONDITIONAL_SIMULATION_ONLY", out.Reason}
	if (out.State == "applied" || out.State == "failed") && out.Details.SimulationProof == nil {
		return auth.ErrConflict
	}
	return saveWalletApplicationResult(ctx, tx, out, actor, task)
}

func saveWalletApplicationResult(ctx context.Context, tx pgx.Tx, out *WalletApplication, actor, task string) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	out.UpdatedAt = &now
	resolved, outcome := false, "unknown"
	switch out.State {
	case "applied":
		resolved, outcome = true, "succeeded"
	case "failed":
		resolved, outcome = true, "failed"
	case "rejected":
		resolved, outcome = true, "denied"
	case "unknown":
	default:
		return auth.ErrConflict
	}
	body, err := json.Marshal(out.WalletApplicationSnapshot)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO wallet_application_results(application_id,recorded_at,resolved,snapshot) VALUES($1,$2,$3,$4)`, out.ID, now, resolved, body)
	}
	entry := audit.Entry{OperationID: "wallet-application:" + out.ID, Action: "wallet.application." + out.State, Outcome: outcome, TargetType: "participant", TargetID: out.ParticipantID, SourceID: &out.Details.Input.SourceID, Revision: &out.Details.Input.ParticipantRevision}
	if actor == "" {
		entry.TaskID = &task
	} else {
		entry.ActorID = &actor
	}
	if err == nil {
		_, err = audit.Record(ctx, tx, entry)
	}
	if key := auth.APIKeyActor(ctx); err == nil && actor != "" && key != nil {
		_, err = audit.Record(ctx, tx, audit.Entry{OperationID: entry.OperationID, ActorID: &actor, Action: "api_key.wallet.application", Outcome: outcome, TargetType: "api_key", TargetID: *key})
	}
	return err
}

func (s *Service) observeSimulationWalletApplication(ctx context.Context, c storedConfig, p Participant, out *WalletApplication) {
	out.State, out.DispatchState, out.Reason = "unknown", "unknown", "SIMULATED_WRITE_RESULT_UNKNOWN"
	network, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return
	}
	intent := out.Details.SimulationWrite
	body, check := s.upstream.get(network, c.BaseURL, simulationPrefix+"/wallets/"+intent.Request.UserID+"/applications/"+intent.Request.OperationID, kind, secret, "users.balance.operation.read", true)
	proof, valid := inspectSimulationWalletProof(body, *intent)
	if check.State != "available" || !valid {
		return
	}
	out.Details.SimulationProof = proof
	var read WalletRead
	s.readSimulationWallet(network, c, p, &read)
	if read.Read.State != "available" || read.FieldAuthority.State != "available" || read.SimulationScope == nil || !simulationWalletConfirms(*read.SimulationScope, *proof) {
		return
	}
	if proof.State == "applied" {
		out.State, out.DispatchState, out.Reason = "applied", "confirmed", "SIMULATED_APPLIED"
	} else {
		out.State, out.DispatchState, out.Reason = "failed", "confirmed_failed", "SIMULATED_"+proof.Reason
	}
}

func (s *Service) executeSimulationWalletApplication(ctx context.Context, conn *pgxpool.Conn, token, id string, automatic *walletAutomationAuthority) (WalletApplication, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return WalletApplication{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.walletApplicationActor(ctx, tx, token, automatic)
	authorityLost := automatic != nil && (errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden))
	if err != nil && !authorityLost {
		return WalletApplication{}, err
	}
	out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1`, id))
	if err != nil {
		return WalletApplication{}, err
	}
	if err = lockWallet(ctx, tx, out.ParticipantID); err != nil {
		return WalletApplication{}, err
	}
	if automatic != nil && (out.Details == nil || out.Details.Automation == nil || out.Details.ActorID != automatic.ActorID || out.Details.Automation.ControlRevision != automatic.Revision) {
		return WalletApplication{}, auth.ErrConflict
	}
	if out.State != "pending" {
		if authorityLost {
			return out, tx.Commit(ctx)
		}
		return out, s.finishWalletApplication(ctx, tx, token, automatic)
	}
	// The producer lock serializes writes with participant exit and disabling automation.
	c, err := s.config(ctx, tx, true)
	if err != nil {
		return WalletApplication{}, err
	}
	p, current, err := s.simulationWalletApplicationCurrent(ctx, tx, c, out)
	if err != nil {
		return WalletApplication{}, err
	}
	if authorityLost {
		current = false
	}
	if current && automatic != nil {
		err = s.checkWalletAutomation(ctx, tx, c, *automatic)
		if errors.Is(err, auth.ErrConflict) {
			current, err = false, nil
		}
		if err != nil {
			return WalletApplication{}, err
		}
	}
	if current {
		var read WalletRead
		network, cancel := context.WithTimeout(ctx, 2*time.Second)
		s.readSimulationWallet(network, c, p, &read)
		cancel()
		intent := out.Details.SimulationWrite
		current = read.Read.State == "available" && read.FieldAuthority.State == "available" && read.SimulationScope != nil && simulationWalletPrecondition(*read.SimulationScope, intent.Request) && simulationMoneyEqual(read.SimulationScope.Balance, intent.Before.Balance)
		if current {
			p, current, err = s.simulationWalletApplicationCurrent(ctx, tx, c, out)
			if err != nil {
				return WalletApplication{}, err
			}
		}
	}
	if !current {
		out.State, out.DispatchState, out.Reason = "rejected", "not_dispatched", "SIMULATION_PRECONDITION_CHANGED"
	} else {
		kind, secret, err := s.decrypt(c)
		if err != nil {
			return WalletApplication{}, err
		}
		if _, err = s.walletApplicationActor(ctx, tx, token, automatic); err != nil {
			return WalletApplication{}, err
		}
		body, _ := json.Marshal(out.Details.SimulationWrite.Request)
		network, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, _ = s.upstream.request(network, http.MethodPost, c.BaseURL, simulationPrefix+"/wallets/"+p.Binding.UpstreamUserID+"/applications", kind, secret, "users.balance.simulated_write", body, true)
		cancel()
		s.observeSimulationWalletApplication(ctx, c, p, &out)
	}
	actorID, task := actor.ID, ""
	if automatic != nil {
		actorID, task = "", "wallet_automation"
	}
	if err = saveSimulationWalletResult(ctx, tx, &out, actorID, task); err == nil {
		if authorityLost {
			err = tx.Commit(ctx)
		} else {
			err = s.finishWalletApplication(ctx, tx, token, automatic)
		}
	}
	if err != nil {
		return WalletApplication{}, err
	}
	return out, nil
}

// Reconciliation reads a unique operation proof; it never repeats the financial request.
func (s *Service) ReconcileWalletApplication(ctx context.Context, token, id string) (WalletApplication, error) {
	if !validRevision(id) {
		return WalletApplication{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return WalletApplication{}, err
	}
	defer tx.Rollback(ctx)
	out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err != nil {
		return WalletApplication{}, err
	}
	if err = lockWallet(ctx, tx, out.ParticipantID); err != nil {
		return WalletApplication{}, err
	}
	out, err = scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1`, id))
	if err != nil {
		return WalletApplication{}, err
	}
	native := out.Details != nil && out.Details.NativeAdjustment != nil
	if !native && (out.Simulation == nil || out.Details == nil || out.Details.SimulationWrite == nil) {
		return WalletApplication{}, auth.ErrConflict
	}
	if native && auth.APIKeyActor(ctx) != nil {
		return WalletApplication{}, auth.ErrForbidden
	}
	if out.State != "pending" && out.State != "unknown" {
		return out, s.finish(ctx, tx, token, true)
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WalletApplication{}, err
	}
	if c.SourceID == nil || *c.SourceID != out.Details.Input.SourceID ||
		(native && s.simulationEpoch(c) != "" || !native && s.simulationEpoch(c) != out.Simulation.Epoch) {
		return WalletApplication{}, auth.ErrConflict
	}
	p, err := readParticipant(ctx, tx, out.ParticipantID, c, false, false)
	if err != nil {
		return WalletApplication{}, err
	}
	if !p.SourceCurrent || p.Binding.ID != out.Details.Input.BindingID || p.Binding.UpstreamUserID != out.Details.UpstreamUserID {
		return WalletApplication{}, auth.ErrConflict
	}
	if native {
		s.observeNativeWalletApplication(ctx, c, &out)
		err = saveNativeWalletResult(ctx, tx, &out, actor.ID)
	} else {
		if p.Binding.UpstreamUserID != out.Details.SimulationWrite.Request.UserID {
			return WalletApplication{}, auth.ErrConflict
		}
		s.observeSimulationWalletApplication(ctx, c, p, &out)
		err = saveSimulationWalletResult(ctx, tx, &out, actor.ID, "")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return WalletApplication{}, err
	}
	return out, nil
}

func (s *Service) recoverSimulationWalletIntents(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT r.id::text,r.participant_id FROM wallet_application_requests r WHERE r.snapshot->'simulation'->>'environment'='simulated' AND r.snapshot->>'state'='pending' AND NOT EXISTS(SELECT 1 FROM wallet_application_results x WHERE x.application_id=r.id) ORDER BY r.id LIMIT 100`)
	if err != nil {
		return err
	}
	type candidate struct{ id, participant string }
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.id, &item.participant); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		err = func() error {
			tx, err := s.pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			var locked bool
			if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 1380991564))`, item.participant).Scan(&locked); err != nil || !locked {
				return err
			}
			out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1`, item.id))
			if err != nil || out.State != "pending" {
				return err
			}
			out.State, out.DispatchState, out.Reason = "unknown", "unknown", "PROCESS_INTERRUPTED_RESULT_UNKNOWN"
			if err = saveSimulationWalletResult(ctx, tx, &out, "", "wallet_application_recovery"); err == nil {
				err = tx.Commit(ctx)
			}
			return err
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) simulationWalletWriteAvailable(ctx context.Context, c storedConfig, user string) (bool, error) {
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return false, err
	}
	network, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, check := s.upstream.get(network, c.BaseURL, simulationPrefix+"/wallets/"+user+"/write-contract", kind, secret, "users.balance.write_contract", true)
	var contract simulationWalletWriteContract
	expected := SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: s.simulationEpoch(c)}
	return check.State == "available" && simulationObject(body, "protocol environment epoch user_id quantum write_allowed lookup_allowed", &contract) && contract.SimulationProvenance == expected && contract.UserID == user && contract.Quantum == simulationWalletQuantum && contract.WriteAllowed && contract.LookupAllowed, nil
}
