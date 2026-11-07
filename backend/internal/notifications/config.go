package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

var ErrSecretStorage = &auth.Failure{Status: 503, Code: "SECRET_STORAGE_UNAVAILABLE"}
var localID = regexp.MustCompile(`^[A-Za-z0-9_-]{21}[AQgw]$`)
var hostName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var addressPattern = regexp.MustCompile("^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$")

type Service struct {
	pool          *pgxpool.Pool
	auth          *auth.Service
	aead          cipher.AEAD
	allowLoopback bool
	client        *http.Client
}

func New(pool *pgxpool.Pool, identity *auth.Service, key []byte, allowLoopback bool) (*Service, error) {
	s := &Service{pool: pool, auth: identity, allowLoopback: allowLoopback, client: newMailClient()}
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

type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
}
type Preferences struct {
	BalanceLow            bool `json:"balance_low"`
	RecommendationChanged bool `json:"recommendation_changed"`
	EstimateChanged       bool `json:"estimate_changed"`
	CollectionFailed      bool `json:"collection_failed"`
}
type Capability struct {
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	Effective bool   `json:"effective"`
	Reason    string `json:"reason"`
}
type Configuration struct {
	Revision               string       `json:"revision"`
	Enabled                bool         `json:"enabled"`
	Channel                string       `json:"channel"`
	From                   string       `json:"from"`
	Recipient              string       `json:"recipient"`
	SMTP                   SMTP         `json:"smtp"`
	SecretSet              bool         `json:"secret_set"`
	SecretStorageAvailable bool         `json:"secret_storage_available"`
	Preferences            Preferences  `json:"preferences"`
	DedupeSeconds          int          `json:"dedupe_seconds"`
	SavedAt                *time.Time   `json:"saved_at"`
	Capabilities           []Capability `json:"capabilities"`
}
type storedConfig struct {
	Configuration
	secret []byte
}
type SaveInput struct {
	Revision         string      `json:"revision"`
	Enabled          bool        `json:"enabled"`
	Channel          string      `json:"channel"`
	From             string      `json:"from"`
	Recipient        string      `json:"recipient"`
	SMTP             SMTP        `json:"smtp"`
	Preferences      Preferences `json:"preferences"`
	DedupeSeconds    int         `json:"dedupe_seconds"`
	CredentialAction string      `json:"credential_action"`
	Credential       string      `json:"credential"`
}

func validRevision(v string) bool {
	n, e := strconv.ParseInt(v, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == v
}
func validID(v string) bool {
	b, e := base64.RawURLEncoding.DecodeString(v)
	return e == nil && len(b) == 16 && localID.MatchString(v)
}
func utc(v *time.Time) *time.Time {
	if v != nil {
		t := v.UTC()
		return &t
	}
	return nil
}
func mailbox(v string) bool {
	if len(v) > 254 || !addressPattern.MatchString(v) || strings.Contains(v, "..") {
		return false
	}
	for _, c := range v {
		if c < 33 || c > 126 {
			return false
		}
	}
	a, e := mail.ParseAddress(v)
	return e == nil && a.Address == v && a.Name == "" && strings.Count(v, "@") == 1
}
func readConfig(ctx context.Context, tx pgx.Tx, write bool) (storedConfig, error) {
	var c storedConfig
	var prefs []byte
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT revision::text,enabled,channel,from_address,recipient_address,smtp_host,smtp_port,smtp_tls,smtp_username,secret,preferences,dedupe_seconds,saved_at FROM notification_config WHERE singleton`+lock).Scan(&c.Revision, &c.Enabled, &c.Channel, &c.From, &c.Recipient, &c.SMTP.Host, &c.SMTP.Port, &c.SMTP.TLS, &c.SMTP.Username, &c.secret, &prefs, &c.DedupeSeconds, &c.SavedAt)
	if err == nil {
		err = json.Unmarshal(prefs, &c.Preferences)
	}
	c.SecretSet = len(c.secret) > 0
	c.SavedAt = utc(c.SavedAt)
	c.Capabilities = []Capability{{"balance_low", false, false, "FINANCIAL_BASIS_UNAVAILABLE"}, {"recommendation_changed", false, false, "FINANCIAL_BASIS_UNAVAILABLE"}, {"estimate_changed", false, false, "FINANCIAL_BASIS_UNAVAILABLE"}, {"collection_failed", true, c.Enabled && c.Preferences.CollectionFailed && ready(c), "CONFIRMED_COLLECTION_FAILURES_ONLY"}}
	return c, err
}
func ready(c storedConfig) bool {
	return mailbox(c.From) && mailbox(c.Recipient) && (c.Channel == "resend" && c.SecretSet || c.Channel == "smtp" && c.SMTP.Host != "" && (c.SMTP.Username == "" || c.SecretSet))
}
func secretContext(c Configuration) []byte {
	v := "notifications-v1:" + c.Channel
	if c.Channel == "smtp" {
		v += ":" + net.JoinHostPort(c.SMTP.Host, strconv.Itoa(c.SMTP.Port)) + ":" + c.SMTP.TLS + ":" + c.SMTP.Username
	}
	return []byte(v)
}
func (s *Service) encrypt(c Configuration, value string) ([]byte, error) {
	if s.aead == nil {
		return nil, ErrSecretStorage
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	return s.aead.Seal(nonce, nonce, []byte(value), secretContext(c)), nil
}
func (s *Service) decrypt(c storedConfig) (string, error) {
	if !c.SecretSet {
		return "", nil
	}
	if s.aead == nil || len(c.secret) < s.aead.NonceSize() {
		return "", ErrSecretStorage
	}
	n := s.aead.NonceSize()
	b, e := s.aead.Open(nil, c.secret[:n], c.secret[n:], secretContext(c.Configuration))
	if e != nil {
		return "", ErrSecretStorage
	}
	return string(b), nil
}
func (s *Service) validate(in SaveInput) bool {
	if !validRevision(in.Revision) || in.Channel != "smtp" && in.Channel != "resend" || in.DedupeSeconds < 60 || in.DedupeSeconds > 86400 || in.From != "" && !mailbox(in.From) || in.Recipient != "" && !mailbox(in.Recipient) {
		return false
	}
	if in.SMTP.Port < 1 || in.SMTP.Port > 65535 || len(in.SMTP.Username) > 256 || strings.ContainsAny(in.SMTP.Username, "\r\n\x00") {
		return false
	}
	if in.SMTP.Host != "" {
		ip, e := netip.ParseAddr(in.SMTP.Host)
		if len(in.SMTP.Host) > 253 || e == nil && ip.Zone() != "" || e != nil && !hostName.MatchString(in.SMTP.Host) {
			return false
		}
	}
	if in.SMTP.TLS != "tls" && in.SMTP.TLS != "starttls" {
		ip, e := netip.ParseAddr(in.SMTP.Host)
		if in.SMTP.TLS != "loopback_plain" || !s.allowLoopback || e != nil || !ip.IsLoopback() || in.SMTP.Username != "" {
			return false
		}
	}
	if in.CredentialAction != "keep" && in.CredentialAction != "replace" && in.CredentialAction != "clear" || in.CredentialAction != "replace" && in.Credential != "" {
		return false
	}
	if in.CredentialAction == "replace" && (in.Credential == "" || len(in.Credential) > 8192 || !utf8.ValidString(in.Credential) || strings.ContainsAny(in.Credential, "\r\n\x00")) {
		return false
	}
	if in.Channel == "resend" && in.CredentialAction == "replace" {
		for _, c := range in.Credential {
			if c < 33 || c > 126 {
				return false
			}
		}
	}
	return true
}
func (s *Service) GetConfiguration(ctx context.Context, token string) (Configuration, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Configuration{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = s.auth.AuthorizeAdmin(ctx, tx, token); e != nil {
		return Configuration{}, e
	}
	c, e := readConfig(ctx, tx, false)
	if e == nil {
		_, e = s.auth.AuthorizeAdmin(ctx, tx, token)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return Configuration{}, e
	}
	c.SecretStorageAvailable = s.aead != nil
	if c.SecretSet && !c.SecretStorageAvailable {
		c.Capabilities[3].Effective = false
	}
	return c.Configuration, nil
}
func (s *Service) SaveConfiguration(ctx context.Context, token string, in SaveInput) (Configuration, error) {
	if !s.validate(in) {
		return Configuration{}, auth.ErrInput
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Configuration{}, e
	}
	defer tx.Rollback(ctx)
	actor, e := s.auth.AuthorizeAdmin(ctx, tx, token)
	if e != nil {
		return Configuration{}, e
	}
	old, e := readConfig(ctx, tx, true)
	if e != nil {
		return Configuration{}, e
	}
	if old.Revision != in.Revision {
		return Configuration{}, auth.ErrConflict
	}
	c := storedConfig{Configuration: Configuration{Enabled: in.Enabled, Channel: in.Channel, From: in.From, Recipient: in.Recipient, SMTP: in.SMTP, Preferences: in.Preferences, DedupeSeconds: in.DedupeSeconds}, secret: old.secret}
	switch in.CredentialAction {
	case "keep":
		if old.SecretSet && string(secretContext(old.Configuration)) != string(secretContext(c.Configuration)) {
			return Configuration{}, auth.ErrInput
		}
	case "replace":
		c.secret, e = s.encrypt(c.Configuration, in.Credential)
		if e != nil {
			return Configuration{}, e
		}
	case "clear":
		c.secret = nil
	}
	c.SecretSet = len(c.secret) > 0
	if in.Enabled && !ready(c) {
		return Configuration{}, auth.ErrInput
	}
	prefs, e := json.Marshal(in.Preferences)
	if e != nil {
		return Configuration{}, e
	}
	if _, e = s.auth.AuthorizeAdmin(ctx, tx, token); e != nil {
		return Configuration{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE notification_config SET revision=revision+1,enabled=$1,channel=$2,from_address=$3,recipient_address=$4,smtp_host=$5,smtp_port=$6,smtp_tls=$7,smtp_username=$8,secret=$9,preferences=$10,dedupe_seconds=$11,saved_at=$12 WHERE singleton`, c.Enabled, c.Channel, c.From, c.Recipient, c.SMTP.Host, c.SMTP.Port, c.SMTP.TLS, c.SMTP.Username, c.secret, prefs, c.DedupeSeconds, time.Now().UTC())
	if e == nil {
		c, e = readConfig(ctx, tx, true)
	}
	if e == nil {
		_, e = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "notifications.config.update", Outcome: "succeeded", TargetType: "notifications", TargetID: "global", Revision: &c.Revision})
	}
	if e == nil {
		_, e = s.auth.AuthorizeAdmin(ctx, tx, token)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return Configuration{}, e
	}
	c.SecretStorageAvailable = s.aead != nil
	if c.SecretSet && !c.SecretStorageAvailable {
		c.Capabilities[3].Effective = false
	}
	return c.Configuration, nil
}
