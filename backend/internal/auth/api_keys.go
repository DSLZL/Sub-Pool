package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/telemetry"
)

const apiKeyPrefix = "spk_live_"

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Mode       string     `json:"mode"`
	Prefix     string     `json:"prefix"`
	Revision   string     `json:"revision"`
	CreatedAt  time.Time  `json:"created_at"`
	RotatedAt  *time.Time `json:"rotated_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type APIKeyPage struct {
	Keys      []APIKey `json:"keys"`
	NextAfter *string  `json:"next_after"`
}

type APIKeyIssuance struct {
	Key    APIKey `json:"key"`
	Secret string `json:"secret"`
}

func apiKeyDigest(token string) (string, []byte, error) {
	if len(token) != len(apiKeyPrefix)+22+1+43 || !strings.HasPrefix(token, apiKeyPrefix) {
		return "", nil, ErrUnauthorized
	}
	id := token[len(apiKeyPrefix) : len(apiKeyPrefix)+22]
	if !validID(id) || token[len(apiKeyPrefix)+22] != '_' {
		return "", nil, ErrUnauthorized
	}
	if _, err := tokenDigest(token[len(apiKeyPrefix)+23:]); err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256([]byte(token))
	return id, digest[:], nil
}

const apiKeyColumns = `id,name,mode,revision::text,created_at,rotated_at,last_used_at,revoked_at`

func scanAPIKey(row pgx.Row) (APIKey, error) {
	var key APIKey
	err := row.Scan(&key.ID, &key.Name, &key.Mode, &key.Revision, &key.CreatedAt, &key.RotatedAt, &key.LastUsedAt, &key.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	key.Prefix = apiKeyPrefix + key.ID
	key.CreatedAt = key.CreatedAt.UTC()
	for _, at := range []*time.Time{key.RotatedAt, key.LastUsedAt, key.RevokedAt} {
		if at != nil {
			*at = at.UTC()
		}
	}
	return key, err
}

func (s *Service) keyOwner(ctx context.Context, tx pgx.Tx, token string) (Identity, error) {
	if _, ok := ctx.Value(machineKey{}).(machineCredential); ok {
		return Identity{}, ErrForbidden
	}
	return s.AuthorizeSession(ctx, tx, token)
}

func (s *Service) ListAPIKeys(ctx context.Context, token, after string, limit int) (APIKeyPage, error) {
	page := APIKeyPage{Keys: []APIKey{}}
	if after != "" && !validID(after) || limit < 1 || limit > 100 {
		return page, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	owner, err := s.keyOwner(ctx, tx, token)
	if err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT `+apiKeyColumns+` FROM auth_api_keys WHERE user_id=$1 AND id>$2 ORDER BY id LIMIT $3`, owner.ID, after, limit+1)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		key, readErr := scanAPIKey(rows)
		if readErr != nil {
			rows.Close()
			return page, readErr
		}
		page.Keys = append(page.Keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Keys) > limit {
		page.Keys = page.Keys[:limit]
		page.NextAfter = &page.Keys[limit-1].ID
	}
	if _, err = s.keyOwner(ctx, tx, token); err == nil {
		err = tx.Commit(ctx)
	}
	return page, err
}

func (s *Service) IssueAPIKey(ctx context.Context, token, name, mode string) (APIKeyIssuance, error) {
	var out APIKeyIssuance
	if name == "" || name != strings.TrimSpace(name) || len(name) > 256 || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) || mode != "read" && mode != "management" {
		return out, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	owner, err := s.keyOwner(ctx, tx, token)
	if err != nil {
		return out, err
	}
	if mode == "management" && owner.Role != "admin" {
		return out, ErrForbidden
	}
	id, err := randomValue(16)
	if err != nil {
		return out, err
	}
	secret, err := randomValue(32)
	if err != nil {
		return out, err
	}
	issued := apiKeyPrefix + id + "_" + secret
	_, digest, err := apiKeyDigest(issued)
	if err != nil {
		return out, err
	}
	out.Key, err = scanAPIKey(tx.QueryRow(ctx, `INSERT INTO auth_api_keys(id,user_id,name,mode,digest,created_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+apiKeyColumns, id, owner.ID, name, mode, digest, time.Now().UTC().Truncate(time.Microsecond)))
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &owner.ID, Action: "api_key.create", Outcome: "succeeded", TargetType: "api_key", TargetID: id, Revision: &out.Key.Revision})
	}
	if err == nil {
		_, err = s.keyOwner(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return APIKeyIssuance{}, err
	}
	out.Secret = issued
	return out, nil
}

func (s *Service) RotateAPIKey(ctx context.Context, token, id, revision string) (APIKeyIssuance, error) {
	return s.changeAPIKey(ctx, token, id, revision, true)
}

func (s *Service) RevokeAPIKey(ctx context.Context, token, id, revision string) (APIKey, error) {
	out, err := s.changeAPIKey(ctx, token, id, revision, false)
	return out.Key, err
}

func (s *Service) changeAPIKey(ctx context.Context, token, id, revision string, rotate bool) (APIKeyIssuance, error) {
	var out APIKeyIssuance
	if !validID(id) || !validRevision(revision) {
		return out, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	owner, err := s.keyOwner(ctx, tx, token)
	if err != nil {
		return out, err
	}
	out.Key, err = scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM auth_api_keys WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, owner.ID))
	if err != nil {
		return out, err
	}
	if out.Key.Revision != revision || rotate && out.Key.RevokedAt != nil {
		return out, ErrConflict
	}
	if out.Key.Mode == "management" && owner.Role != "admin" && rotate {
		return out, ErrForbidden
	}
	action := "api_key.revoke"
	issued := ""
	if rotate {
		secret, randomErr := randomValue(32)
		if randomErr != nil {
			return out, randomErr
		}
		issued = apiKeyPrefix + id + "_" + secret
		_, digest, digestErr := apiKeyDigest(issued)
		if digestErr != nil {
			return out, digestErr
		}
		out.Key, err = scanAPIKey(tx.QueryRow(ctx, `UPDATE auth_api_keys SET digest=$2,revision=revision+1,rotated_at=$3,last_used_at=NULL WHERE id=$1 RETURNING `+apiKeyColumns, id, digest, time.Now().UTC().Truncate(time.Microsecond)))
		action = "api_key.rotate"
	} else if out.Key.RevokedAt == nil {
		out.Key, err = scanAPIKey(tx.QueryRow(ctx, `UPDATE auth_api_keys SET revoked_at=$2,revision=revision+1 WHERE id=$1 RETURNING `+apiKeyColumns, id, time.Now().UTC().Truncate(time.Microsecond)))
	}
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &owner.ID, Action: action, Outcome: "succeeded", TargetType: "api_key", TargetID: id, Revision: &out.Key.Revision})
	}
	if err == nil {
		_, err = s.keyOwner(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return APIKeyIssuance{}, err
	}
	out.Secret = issued
	return out, nil
}

func revokeAPIKeys(ctx context.Context, tx pgx.Tx, userID string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE auth_api_keys SET revoked_at=$2,revision=revision+1 WHERE user_id=$1 AND revoked_at IS NULL`, userID, at)
	return err
}

func currentAPIKeyAccess(ctx context.Context, tx pgx.Tx, token string, credential machineCredential) (Access, error) {
	var access Access
	id, digest, err := apiKeyDigest(token)
	if err != nil || id != credential.ID {
		return access, ErrUnauthorized
	}
	var owner string
	err = tx.QueryRow(ctx, `SELECT user_id FROM auth_api_keys WHERE id=$1 AND digest=$2`, id, digest).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return access, ErrUnauthorized
	}
	if err != nil {
		return access, err
	}
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT id,username,role,revision::text,enabled,read_features,account_ids,participant_ids FROM auth_users WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, owner).
		Scan(&access.User.ID, &access.User.Username, &access.User.Role, &access.User.Revision, &enabled, &access.Features, &access.AccountIDs, &access.ParticipantIDs)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !enabled {
		return access, ErrUnauthorized
	}
	if err != nil {
		return access, err
	}
	var mode string
	var revoked bool
	// Take the key's write lock directly: SHARE followed by last-used UPDATE can deadlock parallel readers.
	err = tx.QueryRow(ctx, `SELECT mode,revoked_at IS NOT NULL FROM auth_api_keys WHERE id=$1 AND digest=$2 FOR UPDATE`, id, digest).Scan(&mode, &revoked)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (revoked || mode == "management" && access.User.Role != "admin") {
		return access, ErrUnauthorized
	}
	if err != nil {
		return access, err
	}
	telemetry.SetActor(ctx, access.User.ID)
	if credential.Management && mode != "management" {
		return access, ErrForbidden
	}
	access.AllData = access.User.Role == "admin"
	_, err = tx.Exec(ctx, `UPDATE auth_api_keys SET last_used_at=$2 WHERE id=$1`, id, time.Now().UTC().Truncate(time.Microsecond))
	return access, err
}
