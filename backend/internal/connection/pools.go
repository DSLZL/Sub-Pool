package connection

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

const shareTotal int64 = 100000000

var sharePattern = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,2})(?:\.[0-9]{1,6})?$`)

func shareUnits(value string) (int64, bool) {
	if !sharePattern.MatchString(value) {
		return 0, false
	}
	whole, fraction, _ := strings.Cut(value, ".")
	a, _ := strconv.ParseInt(whole, 10, 64)
	b, _ := strconv.ParseInt(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	units := a*1000000 + b
	return units, units <= shareTotal
}

func sharePercent(units int64) string {
	if units%1000000 == 0 {
		return strconv.FormatInt(units/1000000, 10)
	}
	fraction := strconv.FormatInt(1000000+units%1000000, 10)[1:]
	return strconv.FormatInt(units/1000000, 10) + "." + strings.TrimRight(fraction, "0")
}

type PoolMemberInput struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type PoolShareInput struct {
	PoolMemberInput
	SharePercent string `json:"share_percent"`
}
type PoolInput struct {
	SourceID           string            `json:"source_id"`
	ConnectionRevision string            `json:"connection_revision"`
	Name               string            `json:"name"`
	Enabled            bool              `json:"enabled"`
	Accounts           []PoolMemberInput `json:"accounts"`
	Participants       []PoolShareInput  `json:"participants"`
}
type PoolUpdate struct {
	PoolInput
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

func validPoolInput(input PoolInput) bool {
	if !managedID(input.SourceID) || !validRevision(input.ConnectionRevision) || !localAccountInput(input.Name, "passive") ||
		len(input.Accounts) < 1 || len(input.Accounts) > 100 || input.Participants == nil || len(input.Participants) > 500 {
		return false
	}
	accounts, participants := map[string]bool{}, map[string]bool{}
	var total int64
	for _, a := range input.Accounts {
		if !managedID(a.ID) || !validRevision(a.Revision) || accounts[a.ID] {
			return false
		}
		accounts[a.ID] = true
	}
	for _, p := range input.Participants {
		units, ok := shareUnits(p.SharePercent)
		if !managedID(p.ID) || !validRevision(p.Revision) || participants[p.ID] || !ok {
			return false
		}
		participants[p.ID], total = true, total+units
	}
	return total <= shareTotal
}

type PoolAccount struct {
	ID              string `json:"id"`
	Revision        string `json:"revision"`
	Name            string `json:"name"`
	SourceID        string `json:"source_id"`
	UpstreamID      string `json:"upstream_id"`
	IdentityCurrent bool   `json:"identity_current"`
	fingerprint     []byte
}
type PoolParticipant struct {
	ID             string `json:"id"`
	Revision       string `json:"revision"`
	Name           string `json:"name"`
	BindingID      string `json:"binding_id"`
	SharePercent   string `json:"share_percent"`
	Enabled        bool   `json:"enabled"`
	BindingCurrent bool   `json:"binding_current"`
	units          int64
}
type PoolSummary struct {
	ID                 string     `json:"id"`
	PoolID             string     `json:"pool_id"`
	Revision           string     `json:"revision"`
	Name               string     `json:"name"`
	Enabled            bool       `json:"enabled"`
	SourceID           string     `json:"source_id"`
	ConnectionRevision string     `json:"connection_revision"`
	EffectiveAt        time.Time  `json:"effective_at"`
	EndedAt            *time.Time `json:"ended_at"`
	CurrentRevision    *string    `json:"current_revision"`
	IsCurrent          bool       `json:"is_current"`
	SourceCurrent      bool       `json:"source_current"`
	Visibility         string     `json:"visibility"`
}
type PoolContract struct {
	PoolSummary
	Accounts           []PoolAccount     `json:"accounts"`
	Participants       []PoolParticipant `json:"participants"`
	UnallocatedPercent *string           `json:"unallocated_percent"`
}

func lockPool(ctx context.Context, tx pgx.Tx, id, revision string) error {
	var current string
	err := tx.QueryRow(ctx, `SELECT revision::text FROM pools WHERE id=$1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	if err == nil && current != revision {
		return auth.ErrConflict
	}
	return err
}

func lockPoolAccounts(ctx context.Context, tx pgx.Tx, pool string, inputs []PoolMemberInput, c storedConfig) (map[string]storedAccount, error) {
	ids := map[string]bool{}
	for _, a := range inputs {
		ids[a.ID] = true
	}
	if pool != "" {
		rows, err := tx.Query(ctx, `SELECT a.account_id FROM pool_contract_accounts a JOIN pool_contracts c ON c.id=a.contract_id WHERE c.pool_id=$1 AND a.current`, pool)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	result := map[string]storedAccount{}
	for _, id := range ordered {
		// Captures hold SHARE; exclusive account locks serialize even late historical reads.
		a, err := readManaged(ctx, tx, id, c, true)
		if err != nil {
			return nil, err
		}
		result[id] = a
	}
	return result, nil
}

func (s *Service) SavePool(ctx context.Context, token string, input PoolUpdate) (PoolContract, error) {
	creating := input.ID == ""
	if !validPoolInput(input.PoolInput) || !creating && (!managedID(input.ID) || !validRevision(input.Revision)) || creating && input.Revision != "" {
		return PoolContract{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return PoolContract{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return PoolContract{}, err
	}
	if !creating {
		if err = lockPool(ctx, tx, input.ID, input.Revision); err != nil {
			return PoolContract{}, err
		}
	}
	if c.SourceID == nil || *c.SourceID != input.SourceID || c.Revision != input.ConnectionRevision {
		return PoolContract{}, auth.ErrConflict
	}
	locked, err := lockPoolAccounts(ctx, tx, input.ID, input.Accounts, c)
	if err != nil {
		return PoolContract{}, err
	}
	accounts := []PoolAccount{}
	resources := map[string]bool{}
	for _, ref := range input.Accounts {
		a := locked[ref.ID]
		if a.Revision != ref.Revision || !a.SourceCurrent || !a.ConnectionAvailable || a.ObservedConnectionRevision != c.Revision ||
			a.Read.State != "available" || a.IdentityState != "verified" || len(a.fingerprint) != 32 || resources[string(a.fingerprint)] {
			return PoolContract{}, auth.ErrConflict
		}
		resources[string(a.fingerprint)] = true
		accounts = append(accounts, PoolAccount{ID: a.ID, Revision: a.Revision, Name: a.Name, SourceID: a.SourceID, UpstreamID: a.UpstreamID, fingerprint: a.fingerprint})
	}
	refs := append([]PoolShareInput{}, input.Participants...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	participants := []PoolParticipant{}
	for _, ref := range refs {
		p, err := readParticipant(ctx, tx, ref.ID, c, false, true)
		if err != nil {
			return PoolContract{}, err
		}
		if p.Revision != ref.Revision || !p.SourceCurrent || p.Binding.EndedAt != nil || input.Enabled && !p.Enabled {
			return PoolContract{}, auth.ErrConflict
		}
		units, _ := shareUnits(ref.SharePercent)
		participants = append(participants, PoolParticipant{ID: p.ID, Revision: p.Revision, Name: p.Name, BindingID: p.Binding.ID, SharePercent: sharePercent(units), units: units})
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return PoolContract{}, err
	}
	action := "pool.update"
	if creating {
		raw := make([]byte, 16)
		if _, err = rand.Read(raw); err != nil {
			return PoolContract{}, err
		}
		input.ID, action = base64.RawURLEncoding.EncodeToString(raw), "pool.create"
	}
	id, err := appendPoolContract(ctx, tx, actor.ID, input.ID, input.Revision, input.Name, input.Enabled, input.SourceID, input.ConnectionRevision, accounts, participants, action, time.Now())
	if err != nil {
		return PoolContract{}, accountFailure(err)
	}
	result, err := readPoolContract(ctx, tx, id, c, auth.Access{AllData: true})
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return PoolContract{}, err
	}
	return result, nil
}

func appendPoolContract(ctx context.Context, tx pgx.Tx, actor, pool, oldRevision, name string, enabled bool, source, connectionRevision string, accounts []PoolAccount, participants []PoolParticipant, action string, now time.Time) (string, error) {
	now = now.UTC().Truncate(time.Microsecond)
	var revision string
	var err error
	if oldRevision == "" {
		err = tx.QueryRow(ctx, `INSERT INTO pools(id,name,enabled,created_at,saved_at) VALUES($1,$2,$3,$4,$4) RETURNING revision::text`, pool, name, enabled, now).Scan(&revision)
	} else {
		_, err = tx.Exec(ctx, `UPDATE pool_contract_accounts SET current=false WHERE current AND contract_id IN (SELECT id FROM pool_contracts WHERE pool_id=$1)`, pool)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE pool_contracts SET ended_at=$2 WHERE pool_id=$1 AND ended_at IS NULL`, pool, now)
		}
		if err == nil {
			err = tx.QueryRow(ctx, `UPDATE pools SET revision=revision+1,name=$2,enabled=$3,saved_at=$4 WHERE id=$1 AND revision=$5 RETURNING revision::text`, pool, name, enabled, now, oldRevision).Scan(&revision)
		}
	}
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO pool_contracts(pool_id,revision,name,enabled,source_id,connection_revision,effective_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, pool, revision, name, enabled, source, connectionRevision, now).Scan(&id)
	for _, a := range accounts {
		if err != nil {
			break
		}
		_, err = tx.Exec(ctx, `INSERT INTO pool_contract_accounts(contract_id,account_id,revision,name,source_id,upstream_id,resource_fingerprint,current) VALUES($1,$2,$3,$4,$5,$6,$7,true)`, id, a.ID, a.Revision, a.Name, a.SourceID, a.UpstreamID, a.fingerprint)
	}
	for _, p := range participants {
		if err != nil {
			break
		}
		_, err = tx.Exec(ctx, `INSERT INTO pool_contract_participants(contract_id,participant_id,revision,binding_id,name,share_units) VALUES($1,$2,$3,$4,$5,$6)`, id, p.ID, p.Revision, p.BindingID, p.Name, p.units)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO pool_events(occurred_at,actor_id,pool_id,contract_id,action) VALUES($1,$2,$3,$4,$5)`, now, actor, pool, id, action)
	}
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor, Action: action, Outcome: "succeeded", TargetType: "pool", TargetID: pool, SourceID: &source, Revision: &revision})
	}
	return id, err
}

func (s *Service) DisablePool(ctx context.Context, token, id, revision string) (PoolContract, error) {
	if !managedID(id) || !validRevision(revision) {
		return PoolContract{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return PoolContract{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return PoolContract{}, err
	}
	if err = lockPool(ctx, tx, id, revision); err != nil {
		return PoolContract{}, err
	}
	var contract string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM pool_contracts WHERE pool_id=$1 AND ended_at IS NULL`, id).Scan(&contract); err != nil {
		return PoolContract{}, err
	}
	previous, err := readPoolContract(ctx, tx, contract, c, auth.Access{AllData: true})
	if err != nil {
		return PoolContract{}, err
	}
	if !previous.Enabled {
		return PoolContract{}, auth.ErrConflict
	}
	if _, err = lockPoolAccounts(ctx, tx, id, nil, c); err != nil {
		return PoolContract{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return PoolContract{}, err
	}
	next, err := appendPoolContract(ctx, tx, actor.ID, id, revision, previous.Name, false, previous.SourceID, previous.ConnectionRevision, previous.Accounts, previous.Participants, "pool.disable", time.Now())
	if err != nil {
		return PoolContract{}, accountFailure(err)
	}
	result, err := readPoolContract(ctx, tx, next, c, auth.Access{AllData: true})
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return PoolContract{}, err
	}
	return result, nil
}
