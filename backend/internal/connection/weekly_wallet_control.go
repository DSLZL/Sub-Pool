package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type weeklyWalletControl struct {
	Revision           string `json:"revision"`
	Enabled            bool   `json:"enabled"`
	AutoAllocate       bool   `json:"auto_allocate"`
	AccountID          string `json:"account_id"`
	SourceID           string `json:"source_id"`
	ConnectionRevision string `json:"connection_revision"`
	ActorID            string `json:"actor_id"`
	ActorRevision      string `json:"actor_revision"`
}

type weeklyWalletPool struct {
	ID, Revision, ContractID, AccountID string
	AccountIDs                          []string
	ReadyAccounts                       []string
	Control                             weeklyWalletControl
	Eligible                            bool
	actorID                             string
}

type WeeklyWalletStatus struct {
	PoolID       string              `json:"pool_id"`
	PoolRevision string              `json:"pool_revision"`
	Revision     string              `json:"revision"`
	Enabled      bool                `json:"enabled"`
	AutoAllocate bool                `json:"auto_allocate"`
	State        string              `json:"state"`
	Entries      []weeklyWalletEntry `json:"entries"`
	Groups       []weeklyWalletEntry `json:"groups,omitempty"`
}

type WeeklyWalletInput struct {
	ID           string `json:"id"`
	PoolRevision string `json:"pool_revision"`
	Revision     string `json:"revision"`
	Enabled      bool   `json:"enabled"`
	AutoAllocate bool   `json:"auto_allocate"`
}

type weeklyWalletRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type weeklyWalletProposal struct {
	Basis     *string                `json:"basis"`
	PeriodEnd time.Time              `json:"period_end"`
	Plan      *weeklyWalletPlan      `json:"plan,omitempty"`
	Delta     string                 `json:"delta"`
	Group     *weeklyWalletGroupPlan `json:"group,omitempty"`
}

type WeeklyWalletRecommendation struct {
	ParticipantID string                   `json:"participant_id"`
	Pool          *weeklyWalletRef         `json:"pool"`
	State         string                   `json:"state"`
	Proposal      *weeklyWalletProposal    `json:"proposal"`
	Issues        []weeklyWalletGroupIssue `json:"issues,omitempty"`
}

type WeeklyWalletApplyInput struct {
	PoolID        string     `json:"pool_id"`
	ParticipantID string     `json:"participant_id"`
	Basis         string     `json:"basis"`
	Cutoff        *time.Time `json:"cutoff,omitempty"`
}

func (s *Service) GetWeeklyWalletRecommendation(ctx context.Context, token, participant string) (WeeklyWalletRecommendation, error) {
	out := WeeklyWalletRecommendation{ParticipantID: participant, State: "unavailable"}
	if !managedID(participant) {
		return out, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return out, err
	}
	grouped, err := weeklyWalletGroupRequired(ctx, tx, participant)
	if err != nil {
		return out, err
	}
	if grouped {
		return s.weeklyWalletGroupRecommendation(ctx, tx, token, out)
	}
	var pool weeklyWalletRef
	err = tx.QueryRow(ctx, `SELECT min(p.id),min(p.name) FROM pools p
	 JOIN pool_contracts c ON c.pool_id=p.id AND c.revision=p.revision
	 JOIN pool_contract_participants m ON m.contract_id=c.id
	 WHERE p.enabled AND c.enabled AND m.participant_id=$1 HAVING count(*)=1`, participant).Scan(&pool.ID, &pool.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, tx.Commit(ctx)
	}
	if err != nil {
		return out, err
	}
	out.Pool = &pool
	manual := &weeklyWalletManual{token: token, preview: true}
	p, c, member, a, policy, ok, err := s.weeklyWalletEnvironment(ctx, tx, pool.ID, participant, manual)
	if err != nil {
		return out, err
	}
	if ok {
		pending, err := weeklyWalletPending(ctx, tx, participant)
		if err != nil {
			return out, err
		}
		if pending {
			out.State = "pending"
		} else {
			entry, delta, grantID, err := s.weeklyWalletCandidate(ctx, tx, p, member, a, c, policy, manual)
			if err != nil {
				return out, err
			}
			out.State = entry.State
			if entry.State == "ready" {
				out.Proposal = &weeklyWalletProposal{PeriodEnd: entry.PeriodEnd, Plan: &entry.Plan, Delta: delta}
				if grantID != "" {
					basis := weeklyWalletBasis(p, c, member, entry)
					out.Proposal.Basis = &basis
				}
			}
		}
	}
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Service) ApplyWeeklyWallet(ctx context.Context, token string, input WeeklyWalletApplyInput) (weeklyWalletEntry, error) {
	if !managedID(input.PoolID) || !managedID(input.ParticipantID) || len(input.Basis) != sha256.Size*2 {
		return weeklyWalletEntry{}, auth.ErrInput
	}
	if _, err := hex.DecodeString(input.Basis); err != nil {
		return weeklyWalletEntry{}, auth.ErrInput
	}
	return s.applyWeeklyWallet(ctx, input.PoolID, input.ParticipantID, &weeklyWalletManual{token: token, basis: input.Basis, cutoff: input.Cutoff})
}

func (s *Service) readWeeklyWalletPool(ctx context.Context, tx pgx.Tx, c storedConfig, id string) (weeklyWalletPool, error) {
	out := weeklyWalletPool{ID: id, Control: weeklyWalletControl{Revision: "1"}}
	var raw []byte
	var active bool
	err := tx.QueryRow(ctx, `SELECT p.revision::text,pc.id::text,p.weekly_wallet,
	 COALESCE(p.enabled AND pc.enabled AND pc.source_id=$2 AND pc.connection_revision=$3,false),
	 candidates.ids,candidates.ready
	 FROM pools p JOIN pool_contracts pc ON pc.pool_id=p.id AND pc.revision=p.revision
	 CROSS JOIN LATERAL (
	  SELECT COALESCE(array_agg(ca.account_id ORDER BY ca.account_id),'{}') AS ids,
	   COALESCE(array_agg(ca.account_id ORDER BY ca.account_id) FILTER(WHERE ca.current AND ca.source_id=$2 AND a.source_id=ca.source_id
	    AND a.resource_fingerprint=ca.resource_fingerprint AND a.identity_state='verified' AND a.monitoring
	    AND a.source_mode='passive' AND a.snapshot #>> '{account,platform}'='openai' AND a.snapshot #>> '{account,type}'='oauth'),'{}') AS ready
	  FROM pool_contract_accounts ca JOIN managed_accounts a ON a.id=ca.account_id WHERE ca.contract_id=pc.id
	 ) candidates
	 WHERE p.id=$1 FOR SHARE OF p`, id, c.SourceID, c.Revision).Scan(&out.Revision, &out.ContractID, &raw, &active, &out.AccountIDs, &out.ReadyAccounts)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, auth.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if raw != nil {
		if err = json.Unmarshal(raw, &out.Control); err != nil {
			return out, err
		}
	}
	if !active || !c.CredentialSet || !c.SecretStorageAvailable || c.reverificationRequired || s.simulationEpoch(c) != "" {
		out.ReadyAccounts = nil
		return out, nil
	}
	if len(out.AccountIDs) == 1 {
		out.AccountID = out.AccountIDs[0]
	}
	out.Eligible = len(out.AccountIDs) > 0 && len(out.AccountIDs) == len(out.ReadyAccounts)
	return out, nil
}

func (p weeklyWalletPool) current(c storedConfig) bool {
	return p.Eligible && (p.Control.Enabled || p.Control.AutoAllocate) && p.Control.AccountID == strings.Join(p.AccountIDs, ",") && c.SourceID != nil &&
		p.Control.SourceID == *c.SourceID && p.Control.ConnectionRevision == c.Revision
}

func (s *Service) weeklyWalletStatus(ctx context.Context, tx pgx.Tx, c storedConfig, p weeklyWalletPool) (WeeklyWalletStatus, error) {
	out := WeeklyWalletStatus{
		PoolID: p.ID, PoolRevision: p.Revision, Revision: p.Control.Revision, Enabled: p.Control.Enabled,
		AutoAllocate: p.Control.AutoAllocate, State: "unavailable", Entries: []weeklyWalletEntry{},
	}
	if p.Eligible {
		out.State = "disabled"
	}
	if p.Control.Enabled || p.Control.AutoAllocate {
		out.State = "paused"
		if p.current(c) {
			_, err := s.auth.AuthorizeAutomationAdmin(ctx, tx, p.Control.ActorID, p.Control.ActorRevision)
			if err == nil {
				out.State = "enabled"
			} else if !errors.Is(err, auth.ErrUnauthorized) && !errors.Is(err, auth.ErrForbidden) {
				return out, err
			}
		}
	}
	var err error
	out.Entries, err = listWeeklyWalletEntries(ctx, tx, p.ID)
	if err == nil {
		out.Groups, err = listWeeklyWalletGroups(ctx, tx, p.ID)
	}
	return out, err
}

func (s *Service) GetWeeklyWallet(ctx context.Context, token, id string) (WeeklyWalletStatus, error) {
	return s.weeklyWalletControl(ctx, token, WeeklyWalletInput{ID: id}, false)
}

func (s *Service) ConfigureWeeklyWallet(ctx context.Context, token string, input WeeklyWalletInput) (WeeklyWalletStatus, error) {
	return s.weeklyWalletControl(ctx, token, input, true)
}

func (s *Service) weeklyWalletControl(ctx context.Context, token string, input WeeklyWalletInput, save bool) (WeeklyWalletStatus, error) {
	if !managedID(input.ID) || save && (!validRevision(input.Revision) || !validRevision(input.PoolRevision)) {
		return WeeklyWalletStatus{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WeeklyWalletStatus{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return WeeklyWalletStatus{}, err
	}
	c, err := s.config(ctx, tx, save)
	if err != nil {
		return WeeklyWalletStatus{}, err
	}
	p, err := s.readWeeklyWalletPool(ctx, tx, c, input.ID)
	if err != nil {
		return WeeklyWalletStatus{}, err
	}
	if save {
		active := input.Enabled || input.AutoAllocate
		if input.PoolRevision != p.Revision || input.Revision != p.Control.Revision || active && !p.Eligible {
			return WeeklyWalletStatus{}, auth.ErrConflict
		}
		next := p.Control
		next.Enabled, next.AutoAllocate = input.Enabled, input.AutoAllocate
		if active {
			next.AccountID, next.SourceID, next.ConnectionRevision = strings.Join(p.AccountIDs, ","), *c.SourceID, c.Revision
			next.ActorID, next.ActorRevision = actor.ID, actor.Revision
		}
		if err = tx.QueryRow(ctx, `SELECT ($1::bigint+1)::text`, next.Revision).Scan(&next.Revision); err != nil {
			return WeeklyWalletStatus{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE pools SET weekly_wallet=$2 WHERE id=$1`, p.ID, next); err != nil {
			return WeeklyWalletStatus{}, err
		}
		if _, err = audit.Record(ctx, tx, audit.Entry{
			ActorID: &actor.ID, Action: "pool.weekly_wallet.configure", Outcome: "succeeded",
			TargetType: "pool", TargetID: p.ID, Revision: &next.Revision, SourceID: c.SourceID,
		}); err != nil {
			return WeeklyWalletStatus{}, err
		}
		p.Control = next
	}
	out, err := s.weeklyWalletStatus(ctx, tx, c, p)
	if err == nil {
		_, err = s.auth.AuthorizeAdminSession(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}

func listWeeklyWalletEntries(ctx context.Context, tx pgx.Tx, pool string) ([]weeklyWalletEntry, error) {
	rows, err := tx.Query(ctx, `SELECT w.id::text,w.participant_id,p.name,w.phase,w.period_end,w.recorded_at,w.snapshot,
	 w.application_id::text,`+walletApplicationSnapshotSQL+`->>'state' FROM weekly_wallet_allocations w
	 JOIN participants p ON p.id=w.participant_id LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE w.pool_id=$1 ORDER BY w.id DESC LIMIT 100`, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []weeklyWalletEntry{}
	for rows.Next() {
		var entry weeklyWalletEntry
		var state *string
		if err = rows.Scan(&entry.ID, &entry.ParticipantID, &entry.Name, &entry.Phase, &entry.PeriodEnd, &entry.RecordedAt, &entry.Plan, &entry.ApplicationID, &state); err != nil {
			return nil, err
		}
		entry.PeriodEnd, entry.RecordedAt = entry.PeriodEnd.UTC(), entry.RecordedAt.UTC()
		entry.State = "applied"
		if state != nil {
			entry.State = *state
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

type weeklyWalletEntry struct {
	ID            string                 `json:"id"`
	ParticipantID string                 `json:"participant_id"`
	Name          string                 `json:"name"`
	Phase         string                 `json:"phase"`
	PeriodEnd     time.Time              `json:"period_end"`
	RecordedAt    time.Time              `json:"recorded_at"`
	Plan          weeklyWalletPlan       `json:"plan"`
	ApplicationID *string                `json:"application_id"`
	State         string                 `json:"state"`
	Group         *weeklyWalletGroupPlan `json:"group,omitempty"`
	basis         string
}

func (entry weeklyWalletEntry) MarshalJSON() ([]byte, error) {
	type output weeklyWalletEntry
	value := struct {
		output
		Plan *weeklyWalletPlan `json:"plan,omitempty"`
	}{output: output(entry)}
	if entry.Group == nil {
		value.Plan = &entry.Plan
	}
	return json.Marshal(value)
}
