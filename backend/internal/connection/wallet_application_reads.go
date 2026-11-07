package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

func projectWalletApplication(out WalletApplication, access auth.Access) WalletApplication {
	if !access.AllData {
		out.Details = nil
		out.Visibility = "participant_summary"
	}
	return out
}

func (s *Service) GetWalletApplication(ctx context.Context, token, id string) (WalletApplication, error) {
	if !validRevision(id) {
		return WalletApplication{}, auth.ErrInput
	}
	tx, access, err := s.beginWalletRead(ctx, token, false)
	if err != nil {
		return WalletApplication{}, err
	}
	defer tx.Rollback(ctx)
	scope, args, err := access.ScopeSQL(nil, pgx.Identifier{"r", "participant_id"}, 2)
	if err != nil {
		return WalletApplication{}, err
	}
	args = append([]any{id}, args...)
	out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1 AND `+scope, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err != nil {
		return WalletApplication{}, err
	}
	if err = s.finishWalletRead(ctx, tx, token); err != nil {
		return WalletApplication{}, err
	}
	return projectWalletApplication(out, access), nil
}

type WalletApplicationQuery struct {
	ParticipantID string `json:"participant_id"`
	StartAt       string `json:"start_at"`
	EndAt         string `json:"end_at"`
	After         string `json:"after"`
	Limit         int    `json:"limit"`
}

type WalletApplicationPage struct {
	ParticipantID string              `json:"participant_id"`
	StartAt       string              `json:"start_at"`
	EndAt         string              `json:"end_at"`
	Requests      []WalletApplication `json:"requests"`
	NextAfter     *string             `json:"next_after"`
}

func (s *Service) ListWalletApplications(ctx context.Context, token string, in WalletApplicationQuery) (WalletApplicationPage, error) {
	var start, end *time.Time
	if in.StartAt != "" || in.EndAt != "" {
		a, b, valid := usageRange(in.StartAt, in.EndAt)
		if !valid {
			return WalletApplicationPage{}, auth.ErrInput
		}
		// Stored receipts are whole microseconds; ceiling both bounds preserves [start,end).
		a, b = ceilMicrosecond(a), ceilMicrosecond(b)
		start, end = &a, &b
	}
	if in.ParticipantID != "" && !managedID(in.ParticipantID) || !validUsageAfter(in.After) || in.Limit < 1 || in.Limit > 100 {
		return WalletApplicationPage{}, auth.ErrInput
	}
	tx, access, err := s.beginWalletRead(ctx, token, false)
	if err != nil {
		return WalletApplicationPage{}, err
	}
	defer tx.Rollback(ctx)
	if in.ParticipantID != "" {
		if err = access.CheckParticipant(in.ParticipantID); err != nil {
			return WalletApplicationPage{}, err
		}
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM participants WHERE id=$1)`, in.ParticipantID).Scan(&exists); err != nil {
			return WalletApplicationPage{}, err
		}
		if !exists {
			return WalletApplicationPage{}, auth.ErrNotFound
		}
	}
	scope, args, err := access.ScopeSQL(nil, pgx.Identifier{"r", "participant_id"}, 6)
	if err != nil {
		return WalletApplicationPage{}, err
	}
	args = append([]any{in.ParticipantID, in.After, in.Limit + 1, start, end}, args...)
	rows, err := tx.Query(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE ($1='' OR r.participant_id=$1) AND r.id>$2::bigint AND ($4::timestamptz IS NULL OR r.attempted_at >= $4) AND ($5::timestamptz IS NULL OR r.attempted_at < $5) AND `+scope+` ORDER BY r.id LIMIT $3`, args...)
	if err != nil {
		return WalletApplicationPage{}, err
	}
	out := WalletApplicationPage{ParticipantID: in.ParticipantID, StartAt: in.StartAt, EndAt: in.EndAt, Requests: []WalletApplication{}}
	for rows.Next() {
		r, e := scanWalletApplication(rows)
		if e != nil {
			err = e
			break
		}
		out.Requests = append(out.Requests, projectWalletApplication(r, access))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return WalletApplicationPage{}, err
	}
	if len(out.Requests) > in.Limit {
		out.Requests = out.Requests[:in.Limit]
		out.NextAfter = &out.Requests[in.Limit-1].ID
	}
	if err = s.finishWalletRead(ctx, tx, token); err != nil {
		return WalletApplicationPage{}, err
	}
	return out, nil
}
