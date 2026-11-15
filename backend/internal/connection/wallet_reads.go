package connection

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

type WalletSummary struct {
	ParticipantID       string `json:"participant_id"`
	Name                string `json:"name"`
	ParticipantRevision string `json:"participant_revision"`
	BindingID           string `json:"binding_id"`
	SourceID            string `json:"source_id"`
	UpstreamUserID      string `json:"upstream_user_id"`
	Enabled             bool   `json:"enabled"`
	SourceCurrent       bool   `json:"source_current"`
}

type WalletView struct {
	WalletSummary
	ConnectionRevision    string                `json:"connection_revision"`
	CheckedAt             time.Time             `json:"checked_at"`
	LatestRead            *WalletRead           `json:"latest_read"`
	LastConfirmedRead     *WalletRead           `json:"last_confirmed_read"`
	ReadReferencesCurrent bool                  `json:"read_references_current"`
	LatestRecommendation  *WalletRecommendation `json:"latest_recommendation"`
	Contributions         []WalletContribution  `json:"contributions"`
	Visibility            string                `json:"visibility"`
	CurrentState          string                `json:"current_state"`
	NeedsRecalculation    *bool                 `json:"needs_recalculation"`
	CurrentTarget         *estimation.Amount    `json:"current_target"`
	Applicable            bool                  `json:"applicable"`
	Difference            *estimation.Amount    `json:"difference"`
	LastApplication       *string               `json:"last_application"`
	ApplicationState      string                `json:"application_state"`
}

func walletSummary(p Participant) WalletSummary {
	return WalletSummary{ParticipantID: p.ID, Name: p.Name, ParticipantRevision: p.Revision, BindingID: p.Binding.ID, SourceID: p.Binding.SourceID, UpstreamUserID: p.Binding.UpstreamUserID, Enabled: p.Enabled, SourceCurrent: p.SourceCurrent}
}

func (s *Service) beginWalletRead(ctx context.Context, token string, admin bool) (pgx.Tx, auth.Access, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, auth.Access{}, err
	}
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "balances")
	if err == nil && admin && !access.AllData {
		err = auth.ErrForbidden
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, auth.Access{}, err
	}
	return tx, access, nil
}

func (s *Service) finishWalletRead(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeRead(ctx, tx, token, "balances"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) GetWallet(ctx context.Context, token, id string) (WalletView, error) {
	if !managedID(id) {
		return WalletView{}, auth.ErrInput
	}
	tx, access, err := s.beginWalletRead(ctx, token, false)
	if err != nil {
		return WalletView{}, err
	}
	defer tx.Rollback(ctx)
	if err = access.CheckParticipant(id); err != nil {
		return WalletView{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WalletView{}, err
	}
	p, err := readParticipant(ctx, tx, id, c, false, false)
	if err != nil {
		return WalletView{}, err
	}
	out := WalletView{WalletSummary: walletSummary(p), ConnectionRevision: c.Revision, CheckedAt: time.Now().UTC(), Visibility: "authorized_sources", CurrentState: "unavailable", Contributions: []WalletContribution{}, ApplicationState: "not_implemented"}
	out.LatestRead, err = latestWalletRead(ctx, tx, id, p.Binding.ID, false)
	if err != nil {
		return WalletView{}, err
	}
	out.LastConfirmedRead, err = latestWalletRead(ctx, tx, id, p.Binding.ID, true)
	if err != nil {
		return WalletView{}, err
	}
	out.ReadReferencesCurrent = out.LatestRead != nil && out.SourceCurrent && out.LatestRead.ConnectionRevision == c.Revision
	if !access.AllData {
		for _, read := range []*WalletRead{out.LatestRead, out.LastConfirmedRead} {
			if read != nil {
				read.UsageSources = nil
				read.SimulationScope = nil
			}
		}
	}
	basis, err := s.walletBasis(ctx, tx, id)
	if err != nil {
		return WalletView{}, err
	}
	latest, err := latestWalletRecommendation(ctx, tx, id, p.Binding.ID)
	if err != nil {
		return WalletView{}, err
	}
	current := s.simulationWalletCurrent(c, basis, id, out.CheckedAt, latest)
	// Only the display projection uses account grants; the basis/qualification above is global.
	for _, contribution := range current.Contributions {
		if access.CheckAccount(contribution.AccountID) == nil {
			if !access.AllData {
				contribution.SimulationProof = nil
			}
			out.Contributions = append(out.Contributions, contribution)
		}
	}
	if access.AllData {
		out.Visibility = "all_recorded_sources"
		out.LatestRecommendation = latest
		needs := false
		out.CurrentState = "not_generated"
		if previous := out.LatestRecommendation; previous != nil {
			out.CurrentState = "unavailable"
			needs = previous.BasisHash != current.BasisHash || previous.ExpiresAt != nil && !out.CheckedAt.Before(*previous.ExpiresAt)
			if needs {
				out.CurrentState = "recalculation_required"
			} else if current.Complete && current.Target != nil {
				out.CurrentState, out.CurrentTarget = "scenario", current.Target
			}
		}
		out.NeedsRecalculation = &needs
	}
	if err = s.finishWalletRead(ctx, tx, token); err != nil {
		return WalletView{}, err
	}
	out.CheckedAt = time.Now().UTC()
	if current.ExpiresAt != nil && !out.CheckedAt.Before(*current.ExpiresAt) && access.AllData && latest != nil {
		out.CurrentState, out.CurrentTarget = "recalculation_required", nil
		needs := true
		out.NeedsRecalculation = &needs
	}
	for i := range out.Contributions {
		for _, row := range current.Contributions {
			if row.AccountID == out.Contributions[i].AccountID && row.SimulationProof != nil && !out.CheckedAt.Before(row.SimulationProof.ExpiresAt) {
				out.Contributions[i].State, out.Contributions[i].Amount, out.Contributions[i].Simulation, out.Contributions[i].SimulationProof = "evidence_incomplete", nil, nil, nil
			}
		}
	}
	return out, nil
}

type WalletPage struct {
	Participants []WalletSummary `json:"participants"`
	NextAfter    *string         `json:"next_after"`
}

func (s *Service) ListWallets(ctx context.Context, token, after string, limit int) (WalletPage, error) {
	if after != "" && !managedID(after) || limit < 1 || limit > 100 {
		return WalletPage{}, auth.ErrInput
	}
	tx, access, err := s.beginWalletRead(ctx, token, false)
	if err != nil {
		return WalletPage{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WalletPage{}, err
	}
	scope, args, err := access.ScopeSQL(nil, pgx.Identifier{"p", "id"}, 3)
	if err != nil {
		return WalletPage{}, err
	}
	args = append([]any{after, limit + 1}, args...)
	rows, err := tx.Query(ctx, `SELECT `+participantColumns+participantJoin+` WHERE p.deleted_at IS NULL AND p.id>$1 AND `+scope+` ORDER BY p.id LIMIT $2`, args...)
	if err != nil {
		return WalletPage{}, err
	}
	out := WalletPage{Participants: []WalletSummary{}}
	for rows.Next() {
		p, e := scanParticipant(rows, c, false)
		if e != nil {
			err = e
			break
		}
		out.Participants = append(out.Participants, walletSummary(p))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return WalletPage{}, err
	}
	if len(out.Participants) > limit {
		out.Participants = out.Participants[:limit]
		out.NextAfter = &out.Participants[limit-1].ParticipantID
	}
	if err = s.finishWalletRead(ctx, tx, token); err != nil {
		return WalletPage{}, err
	}
	return out, nil
}

type WalletHistory struct {
	ParticipantID   string                 `json:"participant_id"`
	Kind            string                 `json:"kind"`
	WalletReads     []WalletRead           `json:"wallet_reads"`
	Recommendations []WalletRecommendation `json:"recommendations"`
	NextAfter       *string                `json:"next_after"`
}

func (s *Service) ListWalletHistory(ctx context.Context, token, id, kind, after string, limit int) (WalletHistory, error) {
	if !managedID(id) || kind != "wallet" && kind != "recommendation" || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return WalletHistory{}, auth.ErrInput
	}
	tx, _, err := s.beginWalletRead(ctx, token, true)
	if err != nil {
		return WalletHistory{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM participants WHERE id=$1)`, id).Scan(&exists); err != nil {
		return WalletHistory{}, err
	}
	if !exists {
		return WalletHistory{}, auth.ErrNotFound
	}
	out := WalletHistory{ParticipantID: id, Kind: kind, WalletReads: []WalletRead{}, Recommendations: []WalletRecommendation{}}
	table, columns := "wallet_reads", walletReadColumns
	if kind == "recommendation" {
		table, columns = "wallet_recommendations", walletRecommendationColumns
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM `+table+` WHERE participant_id=$1 AND id>$2::bigint ORDER BY `+table+`.id LIMIT $3`, id, after, limit+1)
	if err != nil {
		return WalletHistory{}, err
	}
	for rows.Next() {
		if kind == "wallet" {
			r, e := scanWalletRead(rows)
			if e != nil {
				err = e
				break
			}
			out.WalletReads = append(out.WalletReads, r)
		} else {
			r, e := scanWalletRecommendation(rows)
			if e != nil {
				err = e
				break
			}
			out.Recommendations = append(out.Recommendations, r)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return WalletHistory{}, err
	}
	if len(out.WalletReads) > limit {
		out.WalletReads = out.WalletReads[:limit]
		out.NextAfter = &out.WalletReads[limit-1].ID
	}
	if len(out.Recommendations) > limit {
		out.Recommendations = out.Recommendations[:limit]
		out.NextAfter = &out.Recommendations[limit-1].ID
	}
	if err = s.finishWalletRead(ctx, tx, token); err != nil {
		return WalletHistory{}, err
	}
	return out, nil
}
