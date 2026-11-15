package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type WalletAutomationTag struct {
	ControlRevision string `json:"control_revision"`
}

type WalletAutomationBinding struct {
	SourceID           string               `json:"source_id"`
	ConnectionRevision string               `json:"connection_revision"`
	Simulation         SimulationProvenance `json:"simulation"`
}

type WalletAutomationControl struct {
	Revision         string                   `json:"revision"`
	Binding          *WalletAutomationBinding `json:"binding"`
	AvailableBinding *WalletAutomationBinding `json:"available_binding"`
	UpdatedAt        *time.Time               `json:"updated_at"`
	LatestRun        *WalletAutomationRun     `json:"latest_run"`
}

type WalletAutomation struct {
	Enabled        bool                     `json:"enabled"`
	CanEnable      bool                     `json:"can_enable"`
	State          string                   `json:"state"`
	Reason         string                   `json:"reason"`
	ExecutionState string                   `json:"execution_state"`
	Control        *WalletAutomationControl `json:"control,omitempty"`
}

type WalletAutomationInput struct {
	Revision           string                `json:"revision"`
	SourceID           string                `json:"source_id"`
	ConnectionRevision string                `json:"connection_revision"`
	Simulation         *SimulationProvenance `json:"simulation"`
}

type walletAutomationAuthority struct {
	Revision, ActorID, ActorRevision string
	Enabled                          bool
	Binding                          *WalletAutomationBinding
	UpdatedAt                        *time.Time
}

func readWalletAutomation(ctx context.Context, tx pgx.Tx) (walletAutomationAuthority, error) {
	var out walletAutomationAuthority
	var actor, actorRevision, source, connection, epoch *string
	err := tx.QueryRow(ctx, `SELECT revision::text,enabled,actor_id,actor_revision::text,source_id,connection_revision::text,simulation_epoch,updated_at FROM wallet_automation WHERE singleton`).
		Scan(&out.Revision, &out.Enabled, &actor, &actorRevision, &source, &connection, &epoch, &out.UpdatedAt)
	if actor != nil && actorRevision != nil {
		out.ActorID, out.ActorRevision = *actor, *actorRevision
	}
	if source != nil && connection != nil && epoch != nil {
		out.Binding = &WalletAutomationBinding{*source, *connection, SimulationProvenance{simulationProtocol, "simulated", *epoch}}
	}
	out.UpdatedAt = utc(out.UpdatedAt)
	return out, err
}

func (s *Service) walletAutomationBinding(c storedConfig) *WalletAutomationBinding {
	if epoch := s.simulationEpoch(c); epoch != "" && c.SourceID != nil {
		return &WalletAutomationBinding{*c.SourceID, c.Revision, SimulationProvenance{simulationProtocol, "simulated", epoch}}
	}
	return nil
}

func (s *Service) checkWalletAutomation(ctx context.Context, tx pgx.Tx, c storedConfig, expected walletAutomationAuthority) error {
	current, err := readWalletAutomation(ctx, tx)
	if err != nil {
		return err
	}
	binding := s.walletAutomationBinding(c)
	if !current.Enabled || current.Revision != expected.Revision || current.ActorID != expected.ActorID || current.ActorRevision != expected.ActorRevision || binding == nil || current.Binding == nil || expected.Binding == nil || *current.Binding != *binding || *current.Binding != *expected.Binding {
		return auth.ErrConflict
	}
	return nil
}

func (s *Service) walletApplicationActor(ctx context.Context, tx pgx.Tx, token string, automatic *walletAutomationAuthority) (auth.Identity, error) {
	if automatic != nil {
		return s.auth.AuthorizeAutomationAdmin(ctx, tx, automatic.ActorID, automatic.ActorRevision)
	}
	return s.authorize(ctx, tx, token, true)
}

func (s *Service) finishWalletApplication(ctx context.Context, tx pgx.Tx, token string, automatic *walletAutomationAuthority) error {
	if automatic == nil {
		return s.finish(ctx, tx, token, true)
	}
	if _, err := s.walletApplicationActor(ctx, tx, token, automatic); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) walletAutomationStatus(ctx context.Context, tx pgx.Tx, c storedConfig, saved walletAutomationAuthority) (WalletAutomation, error) {
	out := WalletAutomation{Enabled: saved.Enabled, State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", ExecutionState: "not_implemented"}
	available := s.walletAutomationBinding(c)
	if available == nil && saved.Binding == nil {
		return out, nil
	}
	out.CanEnable, out.ExecutionState = available != nil, "disabled"
	out.Reason = "CONDITIONAL_SIMULATION_ONLY"
	if available != nil {
		out.State = "available"
	}
	out.Control = &WalletAutomationControl{Revision: saved.Revision, Binding: saved.Binding, AvailableBinding: available, UpdatedAt: saved.UpdatedAt}
	if saved.Enabled {
		out.ExecutionState = "waiting"
		if available == nil || saved.Binding == nil || *saved.Binding != *available {
			out.State, out.ExecutionState, out.Reason = "paused", "paused", "SIMULATION_BINDING_CHANGED"
		} else if _, err := s.auth.AuthorizeAutomationAdmin(ctx, tx, saved.ActorID, saved.ActorRevision); errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) {
			out.State, out.ExecutionState, out.Reason = "paused", "paused", "AUTOMATION_ACTOR_UNAVAILABLE"
		} else if err != nil {
			return WalletAutomation{}, err
		}
	}
	runs, err := listWalletAutomationRuns(ctx, tx, "0", 1)
	if err == nil && len(runs.Items) > 0 {
		out.Control.LatestRun = &runs.Items[0]
	}
	return out, err
}

func (s *Service) GetWalletAutomation(ctx context.Context, token string) (WalletAutomation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WalletAutomation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return WalletAutomation{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WalletAutomation{}, err
	}
	saved, err := readWalletAutomation(ctx, tx)
	if err != nil {
		return WalletAutomation{}, err
	}
	out, err := s.walletAutomationStatus(ctx, tx, c, saved)
	if err == nil {
		_, err = s.auth.AuthorizeAdminSession(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}

func (s *Service) ConfigureWalletAutomation(ctx context.Context, token string, in WalletAutomationInput, enabled bool) (WalletAutomation, error) {
	if !validRevision(in.Revision) || enabled && (!managedID(in.SourceID) || !validRevision(in.ConnectionRevision) || in.Simulation == nil || in.Simulation.Protocol != simulationProtocol || in.Simulation.Environment != "simulated" || !managedID(in.Simulation.Epoch)) {
		return WalletAutomation{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WalletAutomation{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return WalletAutomation{}, err
	}
	// Share the dispatch producer lock so a committed disable fences subsequent sends.
	c, err := s.config(ctx, tx, true)
	if err != nil {
		return WalletAutomation{}, err
	}
	var revision string
	if err = tx.QueryRow(ctx, `SELECT revision::text FROM wallet_automation WHERE singleton FOR UPDATE`).Scan(&revision); err != nil {
		return WalletAutomation{}, err
	}
	if revision != in.Revision {
		return WalletAutomation{}, auth.ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if enabled {
		binding := s.walletAutomationBinding(c)
		if binding == nil || binding.SourceID != in.SourceID || binding.ConnectionRevision != in.ConnectionRevision || binding.Simulation != *in.Simulation {
			return WalletAutomation{}, auth.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE wallet_automation SET revision=revision+1,enabled=true,actor_id=$1,actor_revision=$2,source_id=$3,connection_revision=$4,simulation_epoch=$5,updated_at=$6 WHERE singleton`, actor.ID, actor.Revision, in.SourceID, in.ConnectionRevision, in.Simulation.Epoch, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE wallet_automation SET revision=revision+1,enabled=false,updated_at=$1 WHERE singleton`, now)
	}
	action := "wallet.automation.disable"
	if enabled {
		action = "wallet.automation.enable"
	}
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: action, Outcome: "succeeded", TargetType: "wallet_automation", TargetID: "singleton", Revision: &in.Revision, SourceID: c.SourceID})
	}
	if err != nil {
		return WalletAutomation{}, err
	}
	saved, err := readWalletAutomation(ctx, tx)
	if err != nil {
		return WalletAutomation{}, err
	}
	out, err := s.walletAutomationStatus(ctx, tx, c, saved)
	if err == nil {
		_, err = s.auth.AuthorizeAdminSession(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}
