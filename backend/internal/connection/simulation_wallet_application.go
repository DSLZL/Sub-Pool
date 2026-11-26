package connection

import (
	"context"

	"encoding/json"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

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

func unlockWalletSession(conn *pgxpool.Conn, participant string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, 1380991564))`, participant).Scan(&unlocked); err != nil || !unlocked {
		_ = conn.Conn().Close(ctx)
	}
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
	if !native {
		if out.Simulation == nil || out.Details == nil || out.Details.SimulationWrite == nil {
			return WalletApplication{}, auth.ErrConflict
		}
		return out, s.finish(ctx, tx, token, true)
	}
	if auth.APIKeyActor(ctx) != nil {
		return WalletApplication{}, auth.ErrForbidden
	}
	if out.State != "pending" && out.State != "unknown" {
		return out, s.finish(ctx, tx, token, true)
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WalletApplication{}, err
	}
	if c.SourceID == nil || *c.SourceID != out.Details.Input.SourceID {
		return WalletApplication{}, auth.ErrConflict
	}
	var weekly bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM weekly_wallet_issuances WHERE application_id=$1)
	 OR EXISTS(SELECT 1 FROM weekly_wallet_allocations WHERE application_id=$1)
	 OR EXISTS(SELECT 1 FROM weekly_wallet_groups WHERE application_id=$1)`, out.ID).Scan(&weekly)
	if err != nil {
		return WalletApplication{}, err
	}
	if weekly {
		if c.reverificationRequired {
			return WalletApplication{}, auth.ErrConflict
		}
		wallet := nativeWalletLockKey(*c.SourceID, out.Details.UpstreamUserID)
		if err = lockWallet(ctx, tx, wallet); err != nil {
			return WalletApplication{}, err
		}
		var recorded bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM participant_bindings
		 WHERE id=$1 AND participant_id=$2 AND source_id=$3 AND upstream_user_id::text=$4)`,
			out.Details.Input.BindingID, out.ParticipantID, *c.SourceID, out.Details.UpstreamUserID).Scan(&recorded)
		if err != nil {
			return WalletApplication{}, err
		}
		if !recorded {
			return WalletApplication{}, auth.ErrConflict
		}
	} else {
		p, err := readParticipant(ctx, tx, out.ParticipantID, c, false, false)
		if err != nil {
			return WalletApplication{}, err
		}
		if !p.SourceCurrent || p.Binding.ID != out.Details.Input.BindingID || p.Binding.UpstreamUserID != out.Details.UpstreamUserID {
			return WalletApplication{}, auth.ErrConflict
		}
	}
	s.observeNativeWalletApplication(ctx, c, &out)
	err = saveNativeWalletResult(ctx, tx, &out, actor.ID)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return WalletApplication{}, err
	}
	return out, nil
}
