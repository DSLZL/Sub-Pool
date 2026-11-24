package connection

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/auth"
)

const poolSummaryColumns = `c.id::text,c.pool_id,c.revision::text,c.name,c.enabled,c.source_id,c.connection_revision::text,c.effective_at,c.ended_at,p.revision::text`
const poolSummaryJoin = ` FROM pool_contracts c JOIN pools p ON p.id=c.pool_id`

func poolVisibility(access auth.Access, position int) (string, []any, error) {
	if access.AllData {
		return "true", nil, nil
	}
	accounts, accountArgs, err := access.ScopeSQL(pgx.Identifier{"ca", "account_id"}, nil, position)
	if err != nil {
		return "", nil, err
	}
	participants, participantArgs, err := access.ScopeSQL(nil, pgx.Identifier{"cp", "participant_id"}, position+len(accountArgs))
	return `EXISTS(SELECT 1 FROM pool_contract_accounts ca WHERE ca.contract_id=c.id AND ` + accounts + `)
		AND EXISTS(SELECT 1 FROM pool_contract_participants cp WHERE cp.contract_id=c.id AND ` + participants + `)`, append(accountArgs, participantArgs...), err
}

func scanPoolSummary(row pgx.Row, c storedConfig, all bool) (PoolSummary, error) {
	var p PoolSummary
	var current string
	err := row.Scan(&p.ID, &p.PoolID, &p.Revision, &p.Name, &p.Enabled, &p.SourceID, &p.ConnectionRevision, &p.EffectiveAt, &p.EndedAt, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	p.EffectiveAt, p.EndedAt = p.EffectiveAt.UTC(), utc(p.EndedAt)
	p.IsCurrent = p.Revision == current
	p.SourceCurrent = c.SourceID != nil && p.SourceID == *c.SourceID
	p.Visibility = "scoped_members"
	if all {
		p.Visibility = "all_members"
		p.CurrentRevision = &current
	}
	return p, err
}

func readPoolContract(ctx context.Context, tx pgx.Tx, id string, c storedConfig, access auth.Access) (PoolContract, error) {
	return readPoolContractRange(ctx, tx, id, c, access, "", 0)
}

func readPoolContractRange(ctx context.Context, tx pgx.Tx, id string, c storedConfig, access auth.Access, after string, limit int) (PoolContract, error) {
	visible, args, err := poolVisibility(access, 2)
	if err != nil {
		return PoolContract{}, err
	}
	args = append([]any{id}, args...)
	summary, err := scanPoolSummary(tx.QueryRow(ctx, `SELECT `+poolSummaryColumns+poolSummaryJoin+` WHERE c.id=$1 AND `+visible+` FOR SHARE OF p`, args...), c, access.AllData)
	if err != nil {
		return PoolContract{}, err
	}
	result := PoolContract{PoolSummary: summary, Accounts: []PoolAccount{}, Participants: []PoolParticipant{}}
	source := ""
	if c.SourceID != nil {
		source = *c.SourceID
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"ca", "account_id"}, nil, 4)
	if err != nil {
		return PoolContract{}, err
	}
	args = append([]any{id, source, c.Revision}, args...)
	rows, err := tx.Query(ctx, `SELECT ca.account_id,ca.revision::text,ca.name,ca.source_id,ca.upstream_id::text,ca.resource_fingerprint,
        a.source_id=$2 AND a.identity_state='verified' AND a.resource_fingerprint=ca.resource_fingerprint AND a.observed_connection_revision=$3::bigint AND a.read_check->>'state'='available'
        FROM pool_contract_accounts ca JOIN managed_accounts a ON a.id=ca.account_id WHERE ca.contract_id=$1 AND `+scope+` ORDER BY ca.account_id`, args...)
	if err != nil {
		return PoolContract{}, err
	}
	for rows.Next() {
		var a PoolAccount
		if err = rows.Scan(&a.ID, &a.Revision, &a.Name, &a.SourceID, &a.UpstreamID, &a.fingerprint, &a.IdentityCurrent); err != nil {
			break
		}
		result.Accounts = append(result.Accounts, a)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return PoolContract{}, err
	}
	scope, args, err = access.ScopeSQL(nil, pgx.Identifier{"cp", "participant_id"}, 5)
	if err != nil {
		return PoolContract{}, err
	}
	args = append([]any{id, source, after, limit}, args...)
	rows, err = tx.Query(ctx, `SELECT cp.participant_id,cp.revision::text,cp.name,cp.binding_id::text,cp.share_units,
        p.enabled AND p.deleted_at IS NULL,b.ended_at IS NULL AND b.source_id=$2
        FROM pool_contract_participants cp JOIN participants p ON p.id=cp.participant_id JOIN participant_bindings b ON b.id=cp.binding_id
        WHERE cp.contract_id=$1 AND ($3='' OR cp.participant_id>$3) AND `+scope+`
		ORDER BY cp.participant_id LIMIT NULLIF($4,0)`, args...)
	if err != nil {
		return PoolContract{}, err
	}
	for rows.Next() {
		var p PoolParticipant
		if err = rows.Scan(&p.ID, &p.Revision, &p.Name, &p.BindingID, &p.units, &p.Enabled, &p.BindingCurrent); err != nil {
			break
		}
		p.SharePercent = sharePercent(p.units)
		result.Participants = append(result.Participants, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return PoolContract{}, err
	}
	if access.AllData {
		var total int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(share_units),0) FROM pool_contract_participants WHERE contract_id=$1`, id).Scan(&total); err != nil {
			return PoolContract{}, err
		}
		remainder := sharePercent(shareTotal - total)
		result.UnallocatedPercent = &remainder
	}
	return result, nil
}

func (s *Service) finishPoolRead(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeRead(ctx, tx, token, "pools"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) GetPool(ctx context.Context, token, id string) (PoolContract, error) {
	view, err := s.getPoolContractView(ctx, token, id, true, "", 0)
	return view.PoolContract, err
}
func (s *Service) GetPoolContract(ctx context.Context, token, id string) (PoolContract, error) {
	view, err := s.getPoolContractView(ctx, token, id, false, "", 0)
	return view.PoolContract, err
}

type PoolMembersPage struct {
	PoolContract
	NextAfter     *string `json:"next_after"`
	ScopeRevision string  `json:"scope_revision"`
}

func (s *Service) ListPoolMembers(ctx context.Context, token, id, after string, limit int) (PoolMembersPage, error) {
	if after != "" && !managedID(after) || limit < 1 || limit > 100 {
		return PoolMembersPage{}, auth.ErrInput
	}
	page, err := s.getPoolContractView(ctx, token, id, false, after, limit+1)
	if err != nil {
		return PoolMembersPage{}, err
	}
	if len(page.Participants) > limit {
		page.Participants = page.Participants[:limit]
		page.NextAfter = &page.Participants[limit-1].ID
	}
	return page, nil
}

func (s *Service) getPoolContractView(ctx context.Context, token, id string, current bool, after string, limit int) (PoolMembersPage, error) {
	if current && !managedID(id) || !current && !validRevision(id) {
		return PoolMembersPage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return PoolMembersPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "pools")
	if err != nil {
		return PoolMembersPage{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return PoolMembersPage{}, err
	}
	if current {
		var locked string
		err = tx.QueryRow(ctx, `SELECT id FROM pools WHERE id=$1 FOR SHARE`, id).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			err = auth.ErrNotFound
		}
		if err != nil {
			return PoolMembersPage{}, err
		}
		visible, args, err := poolVisibility(access, 2)
		if err != nil {
			return PoolMembersPage{}, err
		}
		args = append([]any{id}, args...)
		err = tx.QueryRow(ctx, `SELECT c.id::text`+poolSummaryJoin+` WHERE p.id=$1 AND c.revision=p.revision AND `+visible, args...).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = auth.ErrNotFound
		}
		if err != nil {
			return PoolMembersPage{}, err
		}
	}
	result, err := readPoolContractRange(ctx, tx, id, c, access, after, limit)
	if err == nil {
		err = s.finishPoolRead(ctx, tx, token)
	}
	if err != nil {
		return PoolMembersPage{}, err
	}
	return PoolMembersPage{PoolContract: result, ScopeRevision: access.User.Revision}, nil
}

type PoolPage struct {
	Pools     []PoolSummary `json:"pools"`
	NextAfter *string       `json:"next_after"`
}
type PoolHistory struct {
	Contracts []PoolSummary `json:"contracts"`
	NextAfter *string       `json:"next_after"`
}

func (s *Service) ListPools(ctx context.Context, token, after, search string, limit int) (PoolPage, error) {
	if after != "" && !managedID(after) || limit < 1 || limit > 100 || len(search) > 256 || !utf8.ValidString(search) || strings.ContainsFunc(search, unicode.IsControl) {
		return PoolPage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return PoolPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "pools")
	if err != nil {
		return PoolPage{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return PoolPage{}, err
	}
	visible, args, err := poolVisibility(access, 4)
	if err != nil {
		return PoolPage{}, err
	}
	args = append([]any{after, search, limit + 1}, args...)
	rows, err := tx.Query(ctx, `SELECT `+poolSummaryColumns+poolSummaryJoin+` WHERE c.revision=p.revision AND p.id>$1 AND ($2='' OR strpos(lower(c.name),lower($2))>0) AND `+visible+` ORDER BY p.id LIMIT $3`, args...)
	if err != nil {
		return PoolPage{}, err
	}
	result := PoolPage{Pools: []PoolSummary{}}
	for rows.Next() {
		p, e := scanPoolSummary(rows, c, access.AllData)
		if e != nil {
			err = e
			break
		}
		result.Pools = append(result.Pools, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return PoolPage{}, err
	}
	if len(result.Pools) > limit {
		result.Pools = result.Pools[:limit]
		result.NextAfter = &result.Pools[limit-1].PoolID
	}
	if err = s.finishPoolRead(ctx, tx, token); err != nil {
		return PoolPage{}, err
	}
	return result, nil
}

func (s *Service) ListPoolHistory(ctx context.Context, token, pool, after string, limit int) (PoolHistory, error) {
	if pool != "" && !managedID(pool) || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return PoolHistory{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return PoolHistory{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "pools")
	if err != nil {
		return PoolHistory{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return PoolHistory{}, err
	}
	visible, args, err := poolVisibility(access, 4)
	if err != nil {
		return PoolHistory{}, err
	}
	args = append([]any{pool, after, limit + 1}, args...)
	if pool != "" {
		// Guard explicit resources so hidden and absent histories both return not-found.
		check, checkArgs, err := poolVisibility(access, 2)
		if err != nil {
			return PoolHistory{}, err
		}
		checkArgs = append([]any{pool}, checkArgs...)
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+poolSummaryJoin+` WHERE p.id=$1 AND `+check+`)`, checkArgs...).Scan(&exists)
		if err != nil {
			return PoolHistory{}, err
		}
		if !exists {
			return PoolHistory{}, auth.ErrNotFound
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+poolSummaryColumns+poolSummaryJoin+` WHERE ($1='' OR p.id=$1) AND c.id>$2::bigint AND `+visible+` ORDER BY c.id LIMIT $3`, args...)
	if err != nil {
		return PoolHistory{}, err
	}
	result := PoolHistory{Contracts: []PoolSummary{}}
	for rows.Next() {
		p, e := scanPoolSummary(rows, c, access.AllData)
		if e != nil {
			err = e
			break
		}
		result.Contracts = append(result.Contracts, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return PoolHistory{}, err
	}
	if len(result.Contracts) > limit {
		result.Contracts = result.Contracts[:limit]
		result.NextAfter = &result.Contracts[limit-1].ID
	}
	if err = s.finishPoolRead(ctx, tx, token); err != nil {
		return PoolHistory{}, err
	}
	return result, nil
}
