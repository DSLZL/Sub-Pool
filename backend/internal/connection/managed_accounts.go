package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type ManagedAccount struct {
	ID                         string          `json:"id"`
	Revision                   string          `json:"revision"`
	SourceID                   string          `json:"source_id"`
	UpstreamID                 string          `json:"upstream_id"`
	Name                       string          `json:"name"`
	Monitoring                 bool            `json:"monitoring"`
	SourceMode                 string          `json:"source_mode"`
	SourceCurrent              bool            `json:"source_current"`
	ConnectionAvailable        bool            `json:"connection_available"`
	ObservedConnectionRevision string          `json:"observed_connection_revision"`
	IdentityState              string          `json:"identity_state"`
	CollectionState            string          `json:"collection_state"`
	Snapshot                   AccountSnapshot `json:"snapshot"`
	Read                       Check           `json:"read"`
	LastAttemptAt              time.Time       `json:"last_attempt_at"`
	ObservedAt                 time.Time       `json:"observed_at"`
	CreatedAt                  time.Time       `json:"created_at"`
	SavedAt                    time.Time       `json:"saved_at"`
}

type storedAccount struct {
	ManagedAccount
	fingerprint []byte
}

type ManagedView struct {
	Account            ManagedAccount `json:"account"`
	ConnectionRevision string         `json:"connection_revision"`
}

type ManagedPage struct {
	Accounts           []ManagedAccount `json:"accounts"`
	NextAfter          *string          `json:"next_after"`
	ConnectionRevision string           `json:"connection_revision"`
}

const managedColumns = `id,revision,source_id,upstream_id,name,monitoring,source_mode,identity_state,resource_fingerprint,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at`

func scanManaged(row pgx.Row, c storedConfig) (storedAccount, error) {
	var a storedAccount
	var revision, upstream, observedRevision int64
	var snapshot, check []byte
	err := row.Scan(&a.ID, &revision, &a.SourceID, &upstream, &a.Name, &a.Monitoring, &a.SourceMode, &a.IdentityState, &a.fingerprint, &snapshot, &check, &a.LastAttemptAt, &a.ObservedAt, &observedRevision, &a.CreatedAt, &a.SavedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, auth.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(snapshot, &a.Snapshot)
	}
	if err == nil {
		err = json.Unmarshal(check, &a.Read)
	}
	a.Revision, a.UpstreamID = strconv.FormatInt(revision, 10), strconv.FormatInt(upstream, 10)
	a.SourceCurrent = c.SourceID != nil && a.SourceID == *c.SourceID
	a.ConnectionAvailable = c.CredentialSet && c.SecretStorageAvailable
	a.ObservedConnectionRevision = strconv.FormatInt(observedRevision, 10)
	a.LastAttemptAt, a.ObservedAt, a.CreatedAt, a.SavedAt = a.LastAttemptAt.UTC(), a.ObservedAt.UTC(), a.CreatedAt.UTC(), a.SavedAt.UTC()
	switch {
	case !a.Monitoring:
		a.CollectionState = "paused"
	case !a.SourceCurrent:
		a.CollectionState = "source_changed"
	case !a.ConnectionAvailable:
		a.CollectionState = "connection_unavailable"
	case a.Read.State != "available":
		a.CollectionState = "read_unavailable"
	case a.ObservedConnectionRevision != c.Revision:
		a.CollectionState = "connection_changed"
	case a.IdentityState == "verified":
		a.CollectionState = "awaiting_collector"
	default:
		a.CollectionState = a.IdentityState
	}
	return a, err
}

func readManaged(ctx context.Context, tx pgx.Tx, id string, c storedConfig, write bool) (storedAccount, error) {
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	return scanManaged(tx.QueryRow(ctx, `SELECT `+managedColumns+` FROM managed_accounts WHERE id=$1`+lock, id), c)
}

func managedID(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 16 && base64.RawURLEncoding.EncodeToString(raw) == value
}

func localAccountInput(name, mode string) bool {
	return strings.TrimSpace(name) == name && name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 128 && !strings.ContainsFunc(name, unicode.IsControl) && (mode == "active" || mode == "passive")
}

func accountFailure(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return auth.ErrConflict
	}
	return err
}

func accountEvent(ctx context.Context, tx pgx.Tx, actor, id, revision, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO account_events(occurred_at,actor_id,account_id,revision,action) VALUES ($1,$2,$3,$4,$5)`, time.Now().UTC(), actor, id, revision, action)
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor, Action: action, Outcome: "succeeded", TargetType: "account", TargetID: id, Revision: &revision})
	}
	return err
}

func (s *Service) finishAccountRead(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeRead(ctx, tx, token, "accounts"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ListManaged(ctx context.Context, token, after, search string, limit int) (ManagedPage, error) {
	if after != "" && !managedID(after) || limit < 1 || limit > 100 || len(search) > 256 || !utf8.ValidString(search) || strings.ContainsFunc(search, unicode.IsControl) {
		return ManagedPage{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "accounts")
	if err != nil {
		return ManagedPage{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ManagedPage{}, err
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"id"}, nil, 1)
	if err != nil {
		return ManagedPage{}, err
	}
	first := len(args) + 1
	// Literal substring search: wildcard characters never broaden the visible scope.
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
	args = append(args, after, pattern, limit+1)
	query := `SELECT ` + managedColumns + ` FROM managed_accounts WHERE ` + scope + ` AND id>$` + strconv.Itoa(first) + ` AND name ILIKE $` + strconv.Itoa(first+1) + ` ORDER BY id LIMIT $` + strconv.Itoa(first+2)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return ManagedPage{}, err
	}
	page := ManagedPage{Accounts: []ManagedAccount{}, ConnectionRevision: c.Revision}
	for rows.Next() {
		a, scanErr := scanManaged(rows, c)
		if scanErr != nil {
			rows.Close()
			return ManagedPage{}, scanErr
		}
		page.Accounts = append(page.Accounts, a.ManagedAccount)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ManagedPage{}, err
	}
	if len(page.Accounts) > limit {
		page.Accounts = page.Accounts[:limit]
		next := page.Accounts[limit-1].ID
		page.NextAfter = &next
	}
	if err = s.finishAccountRead(ctx, tx, token); err != nil {
		return ManagedPage{}, err
	}
	return page, nil
}

func (s *Service) GetManaged(ctx context.Context, token, id string) (ManagedView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedView{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "accounts")
	if err == nil {
		err = access.CheckAccount(id)
	}
	if err != nil {
		return ManagedView{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ManagedView{}, err
	}
	a, err := readManaged(ctx, tx, id, c, false)
	if err == nil {
		err = s.finishAccountRead(ctx, tx, token)
	}
	if err != nil {
		return ManagedView{}, err
	}
	return ManagedView{Account: a.ManagedAccount, ConnectionRevision: c.Revision}, nil
}

type AdoptInput struct {
	SourceID           string `json:"source_id"`
	ConnectionRevision string `json:"connection_revision"`
	UpstreamID         string `json:"upstream_id"`
	Name               string `json:"name"`
	Monitoring         bool   `json:"monitoring"`
	SourceMode         string `json:"source_mode"`
}

type Adoption struct {
	Account            *ManagedAccount `json:"account"`
	Read               Check           `json:"read"`
	ConnectionRevision string          `json:"connection_revision"`
}

func (s *Service) Adopt(ctx context.Context, token string, input AdoptInput) (Adoption, error) {
	if !validRevision(input.ConnectionRevision) || !validRevision(input.UpstreamID) || !managedID(input.SourceID) || !localAccountInput(input.Name, input.SourceMode) {
		return Adoption{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return Adoption{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return Adoption{}, err
	}
	if c.SourceID == nil || *c.SourceID != input.SourceID || c.Revision != input.ConnectionRevision {
		return Adoption{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return Adoption{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return Adoption{}, err
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+input.UpstreamID, kind, secret, "accounts.read", true)
	result := Adoption{Read: check, ConnectionRevision: c.Revision}
	observation, valid := inspectAccount(body, input.UpstreamID, secret)
	if check.State == "available" && !valid {
		result.Read = invalid(check)
	}
	if result.Read.State == "available" {
		if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
			return Adoption{}, err
		}
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return Adoption{}, err
		}
		localID := base64.RawURLEncoding.EncodeToString(id)
		snapshot, err := json.Marshal(observation.AccountSnapshot)
		if err != nil {
			return Adoption{}, err
		}
		read, _ := json.Marshal(check)
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err = tx.Exec(ctx, `INSERT INTO managed_accounts(id,source_id,upstream_id,name,monitoring,source_mode,identity_state,resource_fingerprint,snapshot,read_check,last_attempt_at,observed_at,observed_connection_revision,created_at,saved_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12,$11,$11)`, localID, input.SourceID, input.UpstreamID, input.Name, input.Monitoring, input.SourceMode, observation.identity, observation.fingerprint, snapshot, read, now, c.Revision); err != nil {
			return Adoption{}, accountFailure(err)
		}
		a, err := readManaged(ctx, tx, localID, c, false)
		if err != nil {
			return Adoption{}, err
		}
		if err = accountEvent(ctx, tx, actor.ID, a.ID, a.Revision, "account.adopt"); err != nil {
			return Adoption{}, err
		}
		result.Account = &a.ManagedAccount
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return Adoption{}, err
	}
	return result, nil
}

type ManagedUpdate struct {
	ID         string `json:"id"`
	Revision   string `json:"revision"`
	Name       string `json:"name"`
	Monitoring bool   `json:"monitoring"`
	SourceMode string `json:"source_mode"`
}

func (s *Service) UpdateManaged(ctx context.Context, token string, input ManagedUpdate) (ManagedView, error) {
	if !managedID(input.ID) || !validRevision(input.Revision) || !localAccountInput(input.Name, input.SourceMode) {
		return ManagedView{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return ManagedView{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ManagedView{}, err
	}
	a, err := readManaged(ctx, tx, input.ID, c, true)
	if err != nil {
		return ManagedView{}, err
	}
	if a.Revision != input.Revision || input.Monitoring && !a.SourceCurrent {
		return ManagedView{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return ManagedView{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_accounts SET name=$2,monitoring=$3,source_mode=$4,revision=revision+1,saved_at=$5 WHERE id=$1`, a.ID, input.Name, input.Monitoring, input.SourceMode, time.Now().UTC()); err != nil {
		return ManagedView{}, err
	}
	a, err = readManaged(ctx, tx, a.ID, c, true)
	if err == nil {
		err = accountEvent(ctx, tx, actor.ID, a.ID, a.Revision, "account.update")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return ManagedView{}, err
	}
	return ManagedView{Account: a.ManagedAccount, ConnectionRevision: c.Revision}, nil
}

type AccountReadInput struct {
	ID                 string `json:"id"`
	Revision           string `json:"revision"`
	SourceID           string `json:"source_id"`
	ConnectionRevision string `json:"connection_revision"`
}

func accountReadMatches(input AccountReadInput, a storedAccount, c storedConfig) bool {
	return input.Revision == a.Revision && input.SourceID == a.SourceID && input.ConnectionRevision == c.Revision && a.SourceCurrent
}

func validAccountRead(input AccountReadInput) bool {
	return managedID(input.ID) && managedID(input.SourceID) && validRevision(input.Revision) && validRevision(input.ConnectionRevision)
}

func (s *Service) RefreshManaged(ctx context.Context, token string, input AccountReadInput) (ManagedView, error) {
	if !validAccountRead(input) {
		return ManagedView{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedView{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "accounts")
	if err == nil {
		err = access.CheckAccount(input.ID)
	}
	if err != nil {
		return ManagedView{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ManagedView{}, err
	}
	a, err := readManaged(ctx, tx, input.ID, c, true)
	if err != nil {
		return ManagedView{}, err
	}
	if !accountReadMatches(input, a, c) {
		return ManagedView{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return ManagedView{}, err
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "accounts"); err != nil {
		return ManagedView{}, err
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID, kind, secret, "accounts.read", true)
	observation, valid := inspectAccount(body, a.UpstreamID, secret)
	if check.State == "available" && !valid {
		check = invalid(check)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if check.State == "available" {
		state := observation.identity
		if a.IdentityState == "conflict" || a.Snapshot.Account.Platform != observation.Account.Platform || a.Snapshot.Account.Type != observation.Account.Type || len(a.fingerprint) > 0 && (state == "shadow" || len(observation.fingerprint) > 0 && !bytes.Equal(a.fingerprint, observation.fingerprint)) || a.IdentityState == "shadow" && state == "verified" {
			state = "conflict"
		}
		if state == "verified" {
			var duplicate bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE source_id=$1 AND resource_fingerprint=$2 AND id<>$3)`, a.SourceID, observation.fingerprint, a.ID).Scan(&duplicate)
			if err != nil {
				return ManagedView{}, err
			}
			if duplicate {
				state = "conflict"
			} else {
				a.fingerprint = observation.fingerprint
			}
		}
		a.IdentityState, a.Snapshot, a.ObservedAt = state, observation.AccountSnapshot, now
		a.ObservedConnectionRevision = c.Revision
	}
	snapshot, err := json.Marshal(a.Snapshot)
	if err != nil {
		return ManagedView{}, err
	}
	read, _ := json.Marshal(check)
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "accounts"); err != nil {
		return ManagedView{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE managed_accounts SET revision=revision+1,identity_state=$2,resource_fingerprint=$3,snapshot=$4,read_check=$5,last_attempt_at=$6,observed_at=$7,observed_connection_revision=$8 WHERE id=$1`, a.ID, a.IdentityState, a.fingerprint, snapshot, read, now, a.ObservedAt, a.ObservedConnectionRevision); err != nil {
		return ManagedView{}, accountFailure(err)
	}
	a, err = readManaged(ctx, tx, a.ID, c, true)
	if err == nil {
		err = accountEvent(ctx, tx, access.User.ID, a.ID, a.Revision, "account.refresh")
	}
	if err == nil {
		err = s.finishAccountRead(ctx, tx, token)
	}
	if err != nil {
		return ManagedView{}, err
	}
	return ManagedView{Account: a.ManagedAccount, ConnectionRevision: c.Revision}, nil
}

func (s *Service) ManagedModels(ctx context.Context, token string, input AccountReadInput) (AccountModels, error) {
	if !validAccountRead(input) {
		return AccountModels{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return AccountModels{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return AccountModels{}, err
	}
	a, err := readManaged(ctx, tx, input.ID, c, false)
	if err != nil {
		return AccountModels{}, err
	}
	if !accountReadMatches(input, a, c) {
		return AccountModels{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return AccountModels{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return AccountModels{}, err
	}
	result := AccountModels{AccountID: a.ID, SourceID: a.SourceID, ConnectionRevision: c.Revision, CheckedAt: time.Now().UTC().Truncate(time.Microsecond), Models: []CatalogModel{}, MappingMode: "unknown"}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID, kind, secret, "accounts.read", true)
	result.AccountRead = check
	if check.State == "available" {
		observation, ok := inspectAccount(body, a.UpstreamID, secret)
		mapping, passthrough, valid := modelConfiguration(body, secret)
		if !ok || !valid || observation.Account.Platform != a.Snapshot.Account.Platform || observation.Account.Type != a.Snapshot.Account.Type {
			result.AccountRead = invalid(check)
		} else {
			result.Mapping = mapping
			if observation.Account.Platform == "openai" {
				result.Passthrough = passthrough
			}
			switch {
			case result.Passthrough != nil && *result.Passthrough:
				result.MappingMode = "passthrough"
			case mapping != nil && len(mapping) == 0:
				result.MappingMode = "empty"
			case len(mapping) > 0:
				result.MappingMode = "configured"
			}
		}
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return AccountModels{}, err
	}
	body, check = s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID+"/models", kind, secret, "accounts.read", true)
	result.CatalogRead = check
	if check.State == "available" {
		models, valid := projectModels(body, secret)
		if !valid {
			result.CatalogRead = invalid(check)
		} else {
			result.Models = models
		}
	}
	if err = accountEvent(ctx, tx, actor.ID, a.ID, a.Revision, "account.models.read"); err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return AccountModels{}, err
	}
	return result, nil
}
