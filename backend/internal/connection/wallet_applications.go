package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

type WalletApplicationInput struct {
	WalletExpected
	RequestID        string                `json:"request_id"`
	RecommendationID string                `json:"recommendation_id"`
	Simulation       *SimulationProvenance `json:"simulation,omitempty"`
	NativeDelta      string                `json:"native_delta,omitempty"`
}

type WalletApplicationDetails struct {
	ActorID                 string                       `json:"actor_id"`
	ActorAPIKeyID           *string                      `json:"actor_api_key_id,omitempty"`
	Input                   WalletApplicationInput       `json:"input"`
	UpstreamUserID          string                       `json:"upstream_user_id"`
	EvaluatedBasisHash      string                       `json:"evaluated_basis_hash"`
	RecommendationBasisHash *string                      `json:"recommendation_basis_hash"`
	Reasons                 []string                     `json:"reasons"`
	SimulationWrite         *simulationWalletWriteIntent `json:"simulation_write,omitempty"`
	SimulationProof         *simulationWalletWriteProof  `json:"simulation_proof,omitempty"`
	Automation              *WalletAutomationTag         `json:"automation,omitempty"`
	NativeAdjustment        *nativeWalletAdjustment      `json:"native_adjustment,omitempty"`
}

type WalletApplicationSnapshot struct {
	ParticipantName string                    `json:"participant_name"`
	State           string                    `json:"state"`
	DispatchState   string                    `json:"dispatch_state"`
	Reason          string                    `json:"reason"`
	Target          *estimation.Amount        `json:"target"`
	Unit            string                    `json:"unit"`
	Details         *WalletApplicationDetails `json:"details"`
	Simulation      *SimulationProvenance     `json:"simulation,omitempty"`
	UpdatedAt       *time.Time                `json:"updated_at,omitempty"`
}

type WalletApplication struct {
	ID            string    `json:"id"`
	ParticipantID string    `json:"participant_id"`
	AttemptedAt   time.Time `json:"attempted_at"`
	Visibility    string    `json:"visibility"`
	WalletApplicationSnapshot
}

const walletApplicationSnapshotSQL = `COALESCE((SELECT x.snapshot FROM wallet_application_results x WHERE x.application_id=r.id ORDER BY x.id DESC LIMIT 1),r.snapshot)`
const walletApplicationColumns = `r.id::text,r.participant_id,r.attempted_at,` + walletApplicationSnapshotSQL

const walletApplicationResolvedSQL = `COALESCE((SELECT x.resolved AND (
	(r.snapshot->>'unit'='simulated_wallet_usd' AND r.snapshot->'simulation'=x.snapshot->'simulation')
	OR (r.snapshot->>'unit'='native_wallet' AND x.snapshot->>'unit'='native_wallet'
		AND r.snapshot #>> '{details,input,native_delta}' IS NOT NULL
		AND r.snapshot #> '{details,input}'=x.snapshot #> '{details,input}')
) FROM wallet_application_results x WHERE x.application_id=r.id ORDER BY x.id DESC LIMIT 1),r.snapshot->>'state'='rejected' AND r.snapshot->>'dispatch_state'='not_dispatched') IS TRUE`

func scanWalletApplication(row pgx.Row) (WalletApplication, error) {
	var out WalletApplication
	var body []byte
	err := row.Scan(&out.ID, &out.ParticipantID, &out.AttemptedAt, &body)
	if err == nil {
		err = json.Unmarshal(body, &out.WalletApplicationSnapshot)
	}
	out.AttemptedAt = out.AttemptedAt.UTC()
	out.Visibility = "administrator"
	return out, err
}

func existingWalletApplication(ctx context.Context, tx pgx.Tx, actor, key, digest string) (*WalletApplication, error) {
	var id, previous string
	err := tx.QueryRow(ctx, `SELECT id::text,request_digest FROM wallet_application_requests WHERE actor_id=$1 AND request_id=$2`, actor, key).Scan(&id, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if digest != previous {
		return nil, auth.ErrConflict
	}
	out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1`, id))
	return &out, err
}

func (s *Service) RequestWalletApplication(ctx context.Context, token string, in WalletApplicationInput) (WalletApplication, error) {
	return s.requestWalletApplication(ctx, token, in, nil)
}

func (s *Service) requestWalletApplication(ctx context.Context, token string, in WalletApplicationInput, automatic *walletAutomationAuthority) (WalletApplication, error) {
	if !validWalletExpected(in.WalletExpected) || !managedID(in.RequestID) || !validUsageAfter(in.RecommendationID) || in.Simulation != nil && (in.Simulation.Protocol != simulationProtocol || in.Simulation.Environment != "simulated" || !managedID(in.Simulation.Epoch)) {
		return WalletApplication{}, auth.ErrInput
	}
	native := in.NativeDelta != ""
	if native && (in.Simulation != nil || automatic != nil || !validNativeDelta(in.NativeDelta)) {
		return WalletApplication{}, auth.ErrInput
	}
	if native && auth.APIKeyActor(ctx) != nil {
		return WalletApplication{}, auth.ErrForbidden
	}
	body, err := json.Marshal(in)
	if err != nil {
		return WalletApplication{}, err
	}
	hash := sha256.Sum256(body)
	digest := hex.EncodeToString(hash[:])
	var tx pgx.Tx
	var actor auth.Identity
	var conn *pgxpool.Conn
	sessionLocked := false
	if in.Simulation != nil || native {
		conn, err = s.pool.Acquire(ctx)
		if err != nil {
			return WalletApplication{}, err
		}
		defer conn.Release()
		defer func() {
			if sessionLocked {
				unlockWalletSession(conn, in.ParticipantID)
			}
		}()
		tx, err = conn.Begin(ctx)
		if err == nil {
			actor, err = s.walletApplicationActor(ctx, tx, token, automatic)
		}
	} else {
		tx, actor, err = s.begin(ctx, token, true)
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback(ctx)
		}
		return WalletApplication{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockWallet(ctx, tx, in.ParticipantID); err != nil {
		return WalletApplication{}, err
	}
	if conn != nil {
		sessionLocked = true
		_, err = tx.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 1380991564))`, in.ParticipantID)
		if err != nil {
			return WalletApplication{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, temporaryLock); err != nil {
			return WalletApplication{}, err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, unlockErr := conn.Exec(cleanup, `SELECT pg_advisory_unlock_shared($1)`, temporaryLock); unlockErr != nil {
				_ = conn.Conn().Close(cleanup)
			}
		}()
	}
	if _, err = s.walletApplicationActor(ctx, tx, token, automatic); err != nil {
		return WalletApplication{}, err
	}
	previous, err := existingWalletApplication(ctx, tx, actor.ID, in.RequestID, digest)
	if err != nil {
		return WalletApplication{}, err
	}
	if previous != nil {
		if err = s.finishWalletApplication(ctx, tx, token, automatic); err != nil {
			return WalletApplication{}, err
		}
		return *previous, nil
	}
	c, p, err := s.walletReferences(ctx, tx, in.WalletExpected)
	if err != nil {
		return WalletApplication{}, err
	}
	if automatic != nil {
		if err = s.checkWalletAutomation(ctx, tx, c, *automatic); err != nil {
			return WalletApplication{}, err
		}
		var considered bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests WHERE snapshot #>> '{details,input,recommendation_id}'=$1 AND snapshot #>> '{details,automation,control_revision}' IS NOT NULL)`, in.RecommendationID).Scan(&considered); err != nil {
			return WalletApplication{}, err
		}
		if considered {
			return WalletApplication{}, auth.ErrConflict
		}
	}
	read, err := latestWalletRead(ctx, tx, p.ID, p.Binding.ID, false)
	if err != nil {
		return WalletApplication{}, err
	}
	recommendation, err := latestWalletRecommendation(ctx, tx, p.ID, p.Binding.ID)
	if err != nil {
		return WalletApplication{}, err
	}
	head := "0"
	if recommendation != nil {
		head = recommendation.ID
	}
	if walletReadID(read) != in.LatestID || head != in.RecommendationID {
		return WalletApplication{}, auth.ErrConflict
	}
	basis, err := s.walletBasis(ctx, tx, p.ID)
	if err != nil {
		return WalletApplication{}, err
	}
	out := WalletApplication{ParticipantID: p.ID, AttemptedAt: time.Now().UTC().Truncate(time.Microsecond), Visibility: "administrator"}
	current := walletRecommendation(basis, p.ID, out.AttemptedAt)
	details := &WalletApplicationDetails{ActorID: actor.ID, ActorAPIKeyID: auth.APIKeyActor(ctx), Input: in, UpstreamUserID: p.Binding.UpstreamUserID, EvaluatedBasisHash: current.BasisHash, Reasons: append([]string{}, current.Global.Limitations...)}
	if automatic != nil {
		details.Automation = &WalletAutomationTag{ControlRevision: automatic.Revision}
	}
	details.Reasons = append(details.Reasons, "WALLET_WRITE_CAPABILITY_UNVERIFIED", "TARGET_UNAVAILABLE")
	if recommendation == nil {
		details.Reasons = append(details.Reasons, "RECOMMENDATION_NOT_GENERATED")
	} else {
		details.RecommendationBasisHash = &recommendation.BasisHash
		if recommendation.BasisHash != current.BasisHash || recommendation.ExpiresAt != nil && !out.AttemptedAt.Before(*recommendation.ExpiresAt) {
			details.Reasons = append(details.Reasons, "RECOMMENDATION_STALE")
		}
	}
	// A recommendation alone does not authorize an adjustment to a native wallet.
	out.WalletApplicationSnapshot = WalletApplicationSnapshot{ParticipantName: p.Name, State: "rejected", DispatchState: "not_dispatched", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", Unit: "native_wallet", Details: details}
	if native {
		if err = s.prepareNativeWalletApplication(ctx, tx, c, p, read, &out); err != nil {
			return WalletApplication{}, err
		}
	} else if in.Simulation != nil {
		if err = s.prepareSimulationWalletApplication(ctx, tx, c, p, basis, read, recommendation, &out); err != nil {
			return WalletApplication{}, err
		}
	}
	body, err = json.Marshal(out.WalletApplicationSnapshot)
	if err == nil {
		err = tx.QueryRow(ctx, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(actor_id,request_id) DO NOTHING RETURNING id::text`, actor.ID, in.RequestID, digest, p.ID, p.Binding.ID, out.AttemptedAt, body).Scan(&out.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = auth.ErrConflict
		}
	}
	if err == nil {
		outcome := "denied"
		if out.State == "pending" {
			outcome = "pending"
		}
		entry := audit.Entry{OperationID: "wallet-application:" + out.ID, ActorID: &actor.ID, Action: "wallet.application.request", Outcome: outcome, TargetType: "participant", TargetID: p.ID, Revision: &p.Revision, SourceID: &p.Binding.SourceID}
		if automatic != nil {
			task := "wallet_automation"
			entry.ActorID, entry.TaskID = nil, &task
		}
		_, err = audit.Record(ctx, tx, entry)
	}
	if err == nil && details.ActorAPIKeyID != nil {
		outcome := "denied"
		if out.State == "pending" {
			outcome = "pending"
		}
		_, err = audit.Record(ctx, tx, audit.Entry{OperationID: "wallet-application:" + out.ID, ActorID: &actor.ID, Action: "api_key.wallet.application", Outcome: outcome, TargetType: "api_key", TargetID: *details.ActorAPIKeyID})
	}
	if err == nil {
		err = s.finishWalletApplication(ctx, tx, token, automatic)
	}
	if err != nil {
		return WalletApplication{}, err
	}
	if out.State == "pending" {
		if native {
			return s.executeNativeWalletApplication(ctx, conn, token, out)
		}
		return s.executeSimulationWalletApplication(ctx, conn, token, out.ID, automatic)
	}
	return out, nil
}
