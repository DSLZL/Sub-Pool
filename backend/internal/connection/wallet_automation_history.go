package connection

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type WalletAutomationCounts struct {
	Total   int `json:"total"`
	Applied int `json:"applied"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Unknown int `json:"unknown"`
	Running int `json:"running"`
}

type WalletAutomationRun struct {
	ID               string                  `json:"id"`
	ParticipantID    string                  `json:"participant_id"`
	ParticipantName  string                  `json:"participant_name"`
	RecommendationID string                  `json:"recommendation_id"`
	ControlRevision  string                  `json:"control_revision"`
	Binding          WalletAutomationBinding `json:"binding"`
	State            string                  `json:"state"`
	Reason           string                  `json:"reason"`
	AttemptedAt      time.Time               `json:"attempted_at"`
	UpdatedAt        *time.Time              `json:"updated_at"`
	Counts           WalletAutomationCounts  `json:"counts"`
}

type WalletAutomationPage struct {
	Items     []WalletAutomationRun `json:"items"`
	NextAfter *string               `json:"next_after"`
}

func listWalletAutomationRuns(ctx context.Context, tx pgx.Tx, after string, limit int) (WalletAutomationPage, error) {
	out := WalletAutomationPage{Items: []WalletAutomationRun{}}
	rows, err := tx.Query(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.snapshot #>> '{details,automation,control_revision}' IS NOT NULL AND ($1::bigint=0 OR r.id<$1::bigint) ORDER BY r.id DESC LIMIT $2`, after, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		application, err := scanWalletApplication(rows)
		if err != nil {
			return out, err
		}
		if application.Details == nil || application.Details.Automation == nil || application.Simulation == nil {
			return out, auth.ErrConflict
		}
		run := WalletAutomationRun{ID: application.ID, ParticipantID: application.ParticipantID, ParticipantName: application.ParticipantName, RecommendationID: application.Details.Input.RecommendationID, ControlRevision: application.Details.Automation.ControlRevision, Binding: WalletAutomationBinding{application.Details.Input.SourceID, application.Details.Input.ConnectionRevision, *application.Simulation}, State: application.State, Reason: application.Reason, AttemptedAt: application.AttemptedAt, UpdatedAt: application.UpdatedAt, Counts: WalletAutomationCounts{Total: 1}}
		switch application.State {
		case "pending":
			run.Counts.Running = 1
		case "applied":
			run.Counts.Applied = 1
		case "failed":
			run.Counts.Failed = 1
		case "rejected":
			run.Counts.Skipped = 1
		case "unknown":
			run.Counts.Unknown = 1
		default:
			return out, auth.ErrConflict
		}
		out.Items = append(out.Items, run)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextAfter = &out.Items[limit-1].ID
	}
	return out, rows.Err()
}

func (s *Service) ListWalletAutomation(ctx context.Context, token, after string, limit int) (WalletAutomationPage, error) {
	if !validUsageAfter(after) || limit < 1 || limit > 100 {
		return WalletAutomationPage{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WalletAutomationPage{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return WalletAutomationPage{}, err
	}
	out, err := listWalletAutomationRuns(ctx, tx, after, limit)
	if err == nil {
		_, err = s.auth.AuthorizeAdminSession(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}
