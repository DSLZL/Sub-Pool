package connection

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
)

var ErrSecretStorage = &auth.Failure{Status: 503, Code: "SECRET_STORAGE_UNAVAILABLE"}
var ErrNotConfigured = &auth.Failure{Status: 409, Code: "CONNECTION_NOT_CONFIGURED"}

type Service struct {
	simulationBinding      string
	dispatchControlBinding string
	pricingControlBinding  string
	Runtime                *database.Runtime
	pool                   *pgxpool.Pool
	auth                   *auth.Service
	aead                   cipher.AEAD
	allowHTTP              bool
	upstream               *upstreamClient
}

func New(pool *pgxpool.Pool, identity *auth.Service, key []byte, allowLoopbackHTTP bool) (*Service, error) {
	s := &Service{pool: pool, auth: identity, allowHTTP: allowLoopbackHTTP, upstream: newUpstreamClient()}
	if len(key) != 0 {
		if len(key) != 32 {
			return nil, errors.New("secret storage key must contain exactly 32 bytes")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		s.aead, err = cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

type Configuration struct {
	Revision               string      `json:"revision"`
	SourceID               *string     `json:"source_id"`
	BaseURL                string      `json:"base_url"`
	CredentialSet          bool        `json:"credential_set"`
	CredentialKind         *string     `json:"credential_kind"`
	SecretStorageAvailable bool        `json:"secret_storage_available"`
	SavedAt                *time.Time  `json:"saved_at"`
	Diagnostic             *Diagnostic `json:"diagnostic"`
}

type storedConfig struct {
	Configuration
	secret                 []byte
	reverificationRequired bool
}

func (s *Service) begin(ctx context.Context, token string, admin bool) (pgx.Tx, auth.Identity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, auth.Identity{}, err
	}
	actor, err := s.authorize(ctx, tx, token, admin)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, auth.Identity{}, err
	}
	return tx, actor, nil
}

func (s *Service) authorize(ctx context.Context, tx pgx.Tx, token string, admin bool) (auth.Identity, error) {
	if admin {
		return s.auth.AuthorizeAdmin(ctx, tx, token)
	}
	return s.auth.AuthorizeSession(ctx, tx, token)
}

func (s *Service) config(ctx context.Context, tx pgx.Tx, write bool) (storedConfig, error) {
	var c storedConfig
	var revision int64
	var diagnostic []byte
	lock := " FOR SHARE OF c"
	if write {
		lock = " FOR UPDATE OF c"
	}
	err := tx.QueryRow(ctx, `SELECT c.revision,c.source_id,COALESCE(s.base_url,''),c.credential_kind,c.secret,c.saved_at,c.diagnostic,c.reverification_required FROM connection_config c LEFT JOIN connection_sources s ON s.id=c.source_id WHERE c.singleton`+lock).
		Scan(&revision, &c.SourceID, &c.BaseURL, &c.CredentialKind, &c.secret, &c.SavedAt, &diagnostic, &c.reverificationRequired)
	c.Revision = strconv.FormatInt(revision, 10)
	c.CredentialSet = len(c.secret) > 0
	c.SecretStorageAvailable = s.aead != nil
	if c.SavedAt != nil {
		utc := c.SavedAt.UTC()
		c.SavedAt = &utc
	}
	if err == nil && diagnostic != nil {
		err = json.Unmarshal(diagnostic, &c.Diagnostic)
	}
	return c, err
}

func record(ctx context.Context, tx pgx.Tx, actor string, source *string, revision, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO system_events(occurred_at,actor_id,source_id,revision,action) VALUES ($1,$2,$3,$4,$5)`, time.Now().UTC(), actor, source, revision, action)
	if err == nil {
		kind, target := "connection", "global"
		if action == "settings.update" {
			kind = "settings"
		} else if source != nil {
			target = *source
		}
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor, Action: action, Outcome: "succeeded", TargetType: kind, TargetID: target, Revision: &revision, SourceID: source})
	}
	return err
}

func (s *Service) finish(ctx context.Context, tx pgx.Tx, token string, admin bool) error {
	if _, err := s.authorize(ctx, tx, token, admin); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Get(ctx context.Context, token string) (Configuration, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return Configuration{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return Configuration{}, err
	}
	return c.Configuration, nil
}

func validRevision(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}

func (s *Service) normalizeURL(value string) (string, error) {
	if len(value) > 2048 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\r\n\t ") {
		return "", auth.ErrInput
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return "", auth.ErrInput
	}
	ip, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme != "https" && !(s.allowHTTP && u.Scheme == "http" && ip.IsLoopback()) {
		return "", auth.ErrInput
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", auth.ErrInput
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", auth.ErrInput
		}
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func credential(value string) (string, string, error) {
	if value == "" || len(value) > 8192 {
		return "", "", auth.ErrInput
	}
	kind := "api_key"
	if strings.HasPrefix(value, "Bearer ") {
		kind, value = "bearer", strings.TrimPrefix(value, "Bearer ")
	}
	if value == "" {
		return "", "", auth.ErrInput
	}
	for _, char := range value {
		if char < 33 || char > 126 {
			return "", "", auth.ErrInput
		}
	}
	return kind, value, nil
}

func (s *Service) encrypt(source, kind, value string) ([]byte, error) {
	if s.aead == nil {
		return nil, ErrSecretStorage
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(value), []byte(source+":"+kind)), nil
}

func (s *Service) decrypt(c storedConfig) (string, string, error) {
	if c.reverificationRequired {
		return "", "", &auth.Failure{Status: 409, Code: "CONNECTION_REVERIFICATION_REQUIRED"}
	}
	if c.SourceID == nil || c.CredentialKind == nil || len(c.secret) == 0 {
		return "", "", ErrNotConfigured
	}
	if s.aead == nil || len(c.secret) < s.aead.NonceSize() {
		return "", "", ErrSecretStorage
	}
	n := s.aead.NonceSize()
	value, err := s.aead.Open(nil, c.secret[:n], c.secret[n:], []byte(*c.SourceID+":"+*c.CredentialKind))
	if err != nil {
		return "", "", ErrSecretStorage
	}
	return *c.CredentialKind, string(value), nil
}

type SaveInput struct {
	Revision         string `json:"revision"`
	BaseURL          string `json:"base_url"`
	CredentialAction string `json:"credential_action"`
	Credential       string `json:"credential"`
	Confirmation     string `json:"confirmation"`
}

type SaveResult struct {
	Configuration Configuration `json:"configuration"`
	SourceChanged bool          `json:"source_changed"`
}

func (s *Service) Save(ctx context.Context, token string, input SaveInput) (SaveResult, error) {
	base, err := s.normalizeURL(input.BaseURL)
	if err != nil || !validRevision(input.Revision) || input.CredentialAction != "keep" && input.CredentialAction != "replace" && input.CredentialAction != "clear" || input.CredentialAction != "replace" && input.Credential != "" {
		return SaveResult{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, true)
	if err != nil {
		return SaveResult{}, err
	}
	if c.Revision != input.Revision {
		return SaveResult{}, auth.ErrConflict
	}
	changed := c.BaseURL != base
	if changed && c.SourceID != nil && input.Confirmation != "更换地址" || changed && input.CredentialAction == "keep" && c.CredentialSet {
		return SaveResult{}, auth.ErrInput
	}
	if changed && c.SourceID != nil {
		var pending bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r JOIN participant_bindings b ON b.id=r.binding_id WHERE b.source_id=$1 AND NOT (`+walletApplicationResolvedSQL+`))`, *c.SourceID).Scan(&pending); err != nil {
			return SaveResult{}, err
		}
		if pending {
			return SaveResult{}, auth.ErrConflict
		}
	}
	if changed {
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return SaveResult{}, err
		}
		source := base64.RawURLEncoding.EncodeToString(id)
		c.SourceID, c.BaseURL = &source, base
		if _, err = tx.Exec(ctx, `INSERT INTO connection_sources(id,base_url,created_at) VALUES ($1,$2,$3)`, source, base, time.Now().UTC()); err != nil {
			return SaveResult{}, err
		}
	}
	switch input.CredentialAction {
	case "replace":
		kind, value, err := credential(input.Credential)
		if err != nil {
			return SaveResult{}, err
		}
		c.CredentialKind = &kind
		c.secret, err = s.encrypt(*c.SourceID, kind, value)
		if err != nil {
			return SaveResult{}, err
		}
	case "clear":
		c.CredentialKind, c.secret = nil, nil
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return SaveResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_config SET revision=revision+1,source_id=$1,credential_kind=$2,secret=$3,saved_at=$4,diagnostic=NULL WHERE singleton`, c.SourceID, c.CredentialKind, c.secret, time.Now().UTC()); err != nil {
		return SaveResult{}, err
	}
	c, err = s.config(ctx, tx, true)
	if err == nil {
		err = record(ctx, tx, actor.ID, c.SourceID, c.Revision, "connection.save")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Configuration: c.Configuration, SourceChanged: changed}, nil
}

type Settings struct {
	Revision string     `json:"revision"`
	Timezone string     `json:"timezone"`
	SavedAt  *time.Time `json:"saved_at"`
}

func settings(ctx context.Context, tx pgx.Tx, write bool) (Settings, error) {
	var result Settings
	var revision int64
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT revision,timezone,saved_at FROM system_settings WHERE singleton`+lock).Scan(&revision, &result.Timezone, &result.SavedAt)
	result.Revision = strconv.FormatInt(revision, 10)
	if result.SavedAt != nil {
		utc := result.SavedAt.UTC()
		result.SavedAt = &utc
	}
	return result, err
}

func (s *Service) GetSettings(ctx context.Context, token string) (Settings, error) {
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(ctx)
	result, err := settings(ctx, tx, false)
	if err == nil {
		err = s.finish(ctx, tx, token, false)
	}
	if err != nil {
		return Settings{}, err
	}
	return result, nil
}

func (s *Service) UpdateSettings(ctx context.Context, token, revision, timezone string) (Settings, error) {
	if !validRevision(revision) || len(timezone) > 64 || timezone == "" || timezone == "Local" {
		return Settings{}, auth.ErrInput
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return Settings{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback(ctx)
	result, err := settings(ctx, tx, true)
	if err != nil {
		return Settings{}, err
	}
	if result.Revision != revision {
		return Settings{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return Settings{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE system_settings SET revision=revision+1,timezone=$1,saved_at=$2 WHERE singleton`, timezone, time.Now().UTC()); err != nil {
		return Settings{}, err
	}
	result, err = settings(ctx, tx, true)
	if err == nil {
		err = record(ctx, tx, actor.ID, nil, result.Revision, "settings.update")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return Settings{}, err
	}
	return result, nil
}
