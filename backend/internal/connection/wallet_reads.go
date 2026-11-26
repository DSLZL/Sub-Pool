package connection

import (
	"context"
	"encoding/json"
	"strings"
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
	if access.AllData {
		out.Visibility = "all_recorded_sources"
		needsRecalculation := false
		out.NeedsRecalculation = &needsRecalculation
		out.LatestRecommendation, err = latestWalletRecommendation(ctx, tx, id, p.Binding.ID)
		if err != nil {
			return WalletView{}, err
		}
	}
	if err = s.finishWalletRead(ctx, tx, token); err != nil {
		return WalletView{}, err
	}
	out.CheckedAt = time.Now().UTC()
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

func walletTimeBounds(start, end string) (*time.Time, *time.Time, error) {
	var bounds [2]*time.Time
	for i, value := range []string{start, end} {
		if value == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || !strings.HasSuffix(value, "Z") || !observationTimestamp.MatchString(value) {
			return nil, nil, auth.ErrInput
		}
		bounds[i] = &at
	}
	if bounds[0] != nil && bounds[1] != nil && !bounds[0].Before(*bounds[1]) {
		return nil, nil, auth.ErrInput
	}
	// Database timestamps have microsecond resolution. Round both bounds up for [start, end).
	for _, at := range bounds {
		if at != nil {
			*at = ceilMicrosecond(*at)
		}
	}
	return bounds[0], bounds[1], nil
}

func (s *Service) ListWalletHistory(ctx context.Context, token, id, kind, after string, limit int) (WalletHistory, error) {
	return s.listWalletHistory(ctx, token, id, kind, after, limit, "", "")
}

func (s *Service) listWalletHistory(ctx context.Context, token, id, kind, after string, limit int, startAt, endAt string) (WalletHistory, error) {
	if !managedID(id) || kind != "wallet" && kind != "recommendation" || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return WalletHistory{}, auth.ErrInput
	}
	start, end, err := walletTimeBounds(startAt, endAt)
	if err != nil {
		return WalletHistory{}, err
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
	table, columns, when := "wallet_reads", walletReadColumns, "attempted_at"
	if kind == "recommendation" {
		table, columns, when = "wallet_recommendations", walletRecommendationColumns, "generated_at"
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM `+table+` WHERE participant_id=$1 AND id>$2::bigint
		AND ($4::timestamptz IS NULL OR `+when+` >= $4) AND ($5::timestamptz IS NULL OR `+when+` < $5)
		ORDER BY `+table+`.id LIMIT $3`, id, after, limit+1, start, end)
	if err != nil {
		return WalletHistory{}, err
	}
	var historyBytes int
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
			// A page of large snapshots must fit through the same-origin proxy.
			body, e := json.Marshal(r)
			if e != nil {
				err = e
				break
			}
			if count := len(out.Recommendations); count > 0 && historyBytes+len(body) > 24*1024*1024 {
				out.NextAfter = &out.Recommendations[count-1].ID
				break
			}
			historyBytes += len(body)
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
