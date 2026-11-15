package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type TemporaryRestoration struct {
	ParticipantID string  `json:"participant_id"`
	AdviceState   string  `json:"advice_state"`
	RemoteState   string  `json:"remote_state"`
	ApplicationID *string `json:"application_id"`
}
type TemporaryStatus struct {
	CanStart           bool                   `json:"can_start"`
	State              string                 `json:"state"`
	Reason             string                 `json:"reason"`
	RoundState         string                 `json:"round_state"`
	RightsState        string                 `json:"rights_state"`
	RemindersAvailable bool                   `json:"reminders_available"`
	Simulation         *SimulationProvenance  `json:"simulation,omitempty"`
	Round              *TemporaryRound        `json:"round,omitempty"`
	Restorations       []TemporaryRestoration `json:"restorations,omitempty"`
	Settlements        []TemporarySettlement  `json:"settlements,omitempty"`
	Reminders          *TemporaryReminders    `json:"reminders,omitempty"`
}

func (s *Service) GetTemporaryStatus(ctx context.Context, token string) (TemporaryStatus, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryStatus{}, err
	}
	defer tx.Rollback(ctx)
	out := TemporaryStatus{State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", RoundState: "not_implemented", RightsState: "not_implemented"}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return TemporaryStatus{}, err
	}
	epoch := s.simulationEpoch(c)
	if epoch != "" {
		out.Simulation = &SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: epoch}
		out.State, out.Reason, out.RoundState, out.RightsState = "idle", "TEMPORARY_PREFLIGHT_REQUIRED", "idle", "none"
		r, err := temporaryLatest(ctx, tx)
		if err != nil {
			return TemporaryStatus{}, err
		}
		if r != nil {
			exit, err := temporaryEffectiveExit(ctx, tx, *r, time.Now().UTC())
			if err != nil {
				return TemporaryStatus{}, err
			}
			r.Exit = exit
			out.Round = r
			out.State, out.RoundState, out.Reason = "active", "active", "CONDITIONAL_SIMULATION_ONLY"
			if exit != nil {
				out.State, out.RoundState = "exited", "exited"
				out.RightsState = "pending_evidence"
			}
			for _, a := range r.Scope.Accounts {
				settlement, err := temporarySettlement(ctx, tx, r.ID, a.AccountID)
				if err != nil {
					return TemporaryStatus{}, err
				}
				if settlement != nil {
					out.Settlements = append(out.Settlements, *settlement)
				}
			}
			if len(out.Settlements) == len(r.Scope.Accounts) {
				done := true
				for _, row := range out.Settlements {
					done = done && temporarySettlementDone(row.State)
				}
				if done {
					out.RightsState = "processed"
				}
			}
			reminders, err := temporaryReminders(ctx, tx, r.ID)
			if err != nil {
				return TemporaryStatus{}, err
			}
			out.Reminders = &reminders
			out.RemindersAvailable = exit == nil && r.Scope.Simulation == *out.Simulation && r.Scope.ConnectionRevision == c.Revision
			for _, wallet := range r.Scope.Wallets {
				row := TemporaryRestoration{ParticipantID: wallet.ParticipantID, AdviceState: "temporary_active", RemoteState: "temporary_active"}
				if exit != nil {
					row.AdviceState, row.RemoteState = "recalculation_required", "remote_restore_pending"
					basis, err := s.walletBasis(ctx, tx, wallet.ParticipantID)
					if errors.Is(err, auth.ErrNotFound) {
						out.Restorations = append(out.Restorations, row)
						continue
					}
					if err != nil {
						return TemporaryStatus{}, err
					}
					latest, err := latestWalletRecommendation(ctx, tx, wallet.ParticipantID, wallet.BindingID)
					if err != nil {
						return TemporaryStatus{}, err
					}
					current := s.simulationWalletCurrent(c, basis, wallet.ParticipantID, time.Now().UTC(), latest)
					if current.Complete {
						row.AdviceState = "ordinary_restored"
					}
					var changed bool
					if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=$1 AND r.attempted_at >= $2 AND r.attempted_at < $3 AND (r.snapshot->>'state'='pending'))`, wallet.ParticipantID, r.StartedAt, exit.ExitedAt).Scan(&changed); err != nil {
						return TemporaryStatus{}, err
					}
					if !changed {
						row.RemoteState = "not_required"
					} else {
						application, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.participant_id=$1 AND r.attempted_at >= $2 ORDER BY r.id DESC LIMIT 1`, wallet.ParticipantID, exit.ExitedAt))
						if err != nil && !errors.Is(err, pgx.ErrNoRows) {
							return TemporaryStatus{}, err
						}
						read, readErr := latestWalletRead(ctx, tx, wallet.ParticipantID, wallet.BindingID, true)
						if readErr != nil {
							return TemporaryStatus{}, readErr
						}
						if err == nil && temporaryRemoteRestored(application, current, basis, read) {
							row.RemoteState = "remote_restored"
							row.ApplicationID = &application.ID
						}
					}
				}
				out.Restorations = append(out.Restorations, row)
			}
		}
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return TemporaryStatus{}, err
	}
	return out, nil
}

func (s *Service) GetTemporaryRound(ctx context.Context, token, id string) (TemporaryRoundDetails, error) {
	if !managedID(id) {
		return TemporaryRoundDetails{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	defer tx.Rollback(ctx)
	r, err := temporaryRound(ctx, tx, id)
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	r.Exit, err = temporaryEffectiveExit(ctx, tx, r, time.Now().UTC())
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	out, err := temporaryRoundDetails(ctx, tx, r)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryRoundDetails{}, err
	}
	return out, nil
}
func (s *Service) GetTemporaryRights(ctx context.Context, token, round, account string) (TemporaryRights, error) {
	if !managedID(round) || !managedID(account) {
		return TemporaryRights{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRights{}, err
	}
	defer tx.Rollback(ctx)
	r, err := temporaryRound(ctx, tx, round)
	if err != nil {
		return TemporaryRights{}, err
	}
	var frozen *TemporaryAccount
	for i := range r.Scope.Accounts {
		if r.Scope.Accounts[i].AccountID == account {
			frozen = &r.Scope.Accounts[i]
		}
	}
	if frozen == nil || len(frozen.Members) == 0 {
		return TemporaryRights{}, auth.ErrNotFound
	}
	basis, err := s.walletBasis(ctx, tx, frozen.Members[0].ParticipantID)
	if err != nil {
		return TemporaryRights{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return TemporaryRights{}, err
	}
	if r.Scope.Simulation.Epoch != s.simulationEpoch(c) || r.Scope.SourceID != basis.SourceID {
		return TemporaryRights{}, auth.ErrConflict
	}
	out := TemporaryRights{RoundID: round, AccountID: account, Adjustments: map[string]string{}}
	for _, source := range basis.Sources {
		if source.AccountID != account || source.Estimate == nil {
			continue
		}
		for _, window := range source.Estimate.Windows {
			if window.Kind == frozen.SettlementPeriod.Kind {
				out.Period = window.Period
			}
		}
		out.Adjustments, err = temporaryCurrentRights(ctx, tx, s.simulationEpoch(c), basis, source)
		if err != nil {
			return TemporaryRights{}, err
		}
	}
	out.Revision, err = temporaryRightsRevision(ctx, tx, account)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryRights{}, err
	}
	return out, nil
}
