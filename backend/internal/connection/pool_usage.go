package connection

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/auth"
)

type recordedPoolContract struct {
	id      string
	start   time.Time
	end     *time.Time
	members map[string]string
}

func attributePoolUsage(ctx context.Context, tx pgx.Tx, capture string, facts []UsageFact) error {
	type assignment struct{ participant, binding *string }
	assignments := map[string]assignment{}
	participantIDs, seen := []string{}, map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT upstream_id::text,participant_id,binding_id::text FROM usage_attributions WHERE capture_id=$1`, capture)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var a assignment
		if err = rows.Scan(&id, &a.participant, &a.binding); err != nil {
			break
		}
		assignments[id] = a
		if a.participant != nil && !seen[*a.participant] {
			seen[*a.participant] = true
			participantIDs = append(participantIDs, *a.participant)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	// Pool changes lock this capture's managed account exclusively before closing an interval.
	rows, err = tx.Query(ctx, `SELECT c.id::text,c.effective_at,c.ended_at,cp.participant_id,cp.binding_id::text
        FROM pool_contracts c JOIN pool_contract_accounts ca ON ca.contract_id=c.id
		JOIN usage_captures u ON u.account_id=ca.account_id AND NOT u.staging AND u.snapshot->>'source_id'=c.source_id
        LEFT JOIN pool_contract_participants cp ON cp.contract_id=c.id AND cp.participant_id=ANY($2::text[])
        WHERE u.id=$1 ORDER BY c.id FOR SHARE OF c`, capture, participantIDs)
	if err != nil {
		return err
	}
	contracts := []recordedPoolContract{}
	for rows.Next() {
		var id string
		var start time.Time
		var end *time.Time
		var participant, binding *string
		if err = rows.Scan(&id, &start, &end, &participant, &binding); err != nil {
			break
		}
		if len(contracts) == 0 || contracts[len(contracts)-1].id != id {
			contracts = append(contracts, recordedPoolContract{id: id, start: start, end: end, members: map[string]string{}})
		}
		if participant != nil && binding != nil {
			contracts[len(contracts)-1].members[*participant] = *binding
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	type evidence struct {
		ID       string  `json:"id"`
		Contract *string `json:"contract_id"`
		Basis    string  `json:"basis"`
	}
	items := []evidence{}
	for _, f := range facts {
		a, ok := assignments[f.ID]
		if !ok {
			return auth.ErrConflict
		}
		e := evidence{ID: f.ID, Basis: "unassigned_participant"}
		if a.participant != nil {
			e.Basis = "no_recorded_contract"
			matched := false
			for _, c := range contracts {
				if f.CreatedAt.Before(c.start) || c.end != nil && !f.CreatedAt.Before(*c.end) {
					continue
				}
				if matched {
					return auth.ErrConflict
				}
				matched = true
				e.Basis = "contract_member_missing"
				if a.binding != nil && c.members[*a.participant] == *a.binding {
					id := c.id
					e.Contract, e.Basis = &id, "recorded_contract"
				}
			}
		}
		items = append(items, e)
	}
	body, err := json.Marshal(items)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE usage_attributions a SET pool_contract_id=(v->>'contract_id')::bigint,pool_basis=v->>'basis'
            FROM jsonb_array_elements($2::jsonb) v WHERE a.capture_id=$1 AND a.upstream_id=(v->>'id')::bigint`, capture, body)
	}
	return err
}

type PoolUsageEvidence struct {
	UpstreamID    string  `json:"upstream_id"`
	ParticipantID *string `json:"participant_id"`
	ContractID    *string `json:"contract_id"`
	Basis         string  `json:"basis"`
}
type PoolUsageEvidencePage struct {
	CaptureID string              `json:"capture_id"`
	Records   []PoolUsageEvidence `json:"records"`
	NextAfter *string             `json:"next_after"`
}

func (s *Service) GetPoolUsageEvidence(ctx context.Context, token, capture, after string, limit int) (PoolUsageEvidencePage, error) {
	if !validRevision(capture) || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return PoolUsageEvidencePage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	visible, args, err := visibleCaptureSQL(access, 2)
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	args = append([]any{capture}, args...)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usage_captures c WHERE c.id=$1 AND `+visible+`)`, args...).Scan(&exists)
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	if !exists {
		return PoolUsageEvidencePage{}, auth.ErrNotFound
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"c", "account_id"}, pgx.Identifier{"a", "participant_id"}, 4)
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	args = append([]any{capture, after, limit + 1}, args...)
	rows, err := tx.Query(ctx, `SELECT a.upstream_id::text,a.participant_id,a.pool_contract_id::text,a.pool_basis
        FROM usage_attributions a JOIN usage_captures c ON c.id=a.capture_id
        WHERE a.capture_id=$1 AND a.upstream_id>$2::bigint AND `+scope+` ORDER BY a.upstream_id LIMIT $3`, args...)
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	result := PoolUsageEvidencePage{CaptureID: capture, Records: []PoolUsageEvidence{}}
	for rows.Next() {
		var r PoolUsageEvidence
		if err = rows.Scan(&r.UpstreamID, &r.ParticipantID, &r.ContractID, &r.Basis); err != nil {
			break
		}
		result.Records = append(result.Records, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	if len(result.Records) > limit {
		result.Records = result.Records[:limit]
		result.NextAfter = &result.Records[limit-1].UpstreamID
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "usage"); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return PoolUsageEvidencePage{}, err
	}
	return result, nil
}
