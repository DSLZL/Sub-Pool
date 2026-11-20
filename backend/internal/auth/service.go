package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/telemetry"
)

const (
	IdleLifetime       = 8 * time.Hour
	AbsoluteLifetime   = 7 * 24 * time.Hour
	loginWindow        = 15 * time.Minute
	identifierFailures = 5
	sourceFailures     = 25
)

type Failure struct {
	Status  int
	Code    string
	RetryAt *time.Time
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInput              = &Failure{Status: 400, Code: "BAD_REQUEST"}
	ErrUnauthorized       = &Failure{Status: 401, Code: "UNAUTHORIZED"}
	ErrForbidden          = &Failure{Status: 403, Code: "FORBIDDEN"}
	ErrAlreadyInitialized = &Failure{Status: 409, Code: "CONFLICT"}
)

type Identity struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Revision string `json:"revision"`
}

type Session struct {
	User              Identity  `json:"user"`
	CreatedAt         time.Time `json:"created_at"`
	IdleExpiresAt     time.Time `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time `json:"absolute_expires_at"`
	ServerTime        time.Time `json:"server_time"`
}

type Service struct {
	pool *pgxpool.Pool
	kdf  chan struct{}
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, kdf: make(chan struct{}, 2)}
}

func limited(until time.Time) *Failure {
	until = until.UTC()
	return &Failure{Status: 429, Code: "TOO_MANY_REQUESTS", RetryAt: &until}
}

func (s *Service) acquireKDF(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.kdf <- struct{}{}:
		return nil
	default:
		return limited(time.Now().Add(time.Second))
	}
}

func event(ctx context.Context, tx pgx.Tx, action, outcome, source string, userID *string) error {
	ip, _ := parseIP(source)
	observed := sourceContext(ctx, ip)
	if observed.IP != nil {
		source = *observed.IP
	} else if source == "local" || source == "session" {
		observed.Kind = source
	}
	_, err := tx.Exec(ctx, `INSERT INTO auth_events(occurred_at,action,outcome,user_id,source_ip,peer_ip,source_kind) VALUES ($1,$2,$3,$4,$5,$6,$7)`, time.Now().UTC(), action, outcome, userID, source, observed.PeerIP, observed.Kind)
	if err == nil && action != "login" && userID != nil {
		result := "failed"
		if outcome == "success" {
			result = "succeeded"
		}
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: userID, Action: action, Outcome: result, TargetType: "user", TargetID: *userID})
	}
	return err
}

func (s *Service) Bootstrap(ctx context.Context, username, password string) (Identity, error) {
	name, err := NormalizeUsername(username)
	if err != nil || !ValidPassword(password) {
		return Identity{}, ErrInput
	}
	return s.bootstrap(ctx, name, password)
}

func (s *Service) EnsureDefaultAdmin(ctx context.Context) error {
	_, err := s.bootstrap(ctx, "admin", "admin")
	if errors.Is(err, ErrAlreadyInitialized) {
		return nil
	}
	return err
}

func (s *Service) bootstrap(ctx context.Context, name, password string) (Identity, error) {
	var result Identity
	if err := s.acquireKDF(ctx); err != nil {
		return result, err
	}
	defer func() { <-s.kdf }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	// Different requested usernames must also serialize the first-admin decision.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7210461102)`); err != nil {
		return result, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users WHERE role='admin')`).Scan(&exists); err != nil {
		return result, err
	}
	if exists {
		return result, ErrAlreadyInitialized
	}
	hash, err := derivePasswordHash(password)
	if err != nil {
		return result, err
	}
	id, err := randomValue(16)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_users(id,username,role,password_hash,created_at) VALUES ($1,$2,'admin',$3,$4)`, id, name, hash, time.Now().UTC()); err != nil {
		return result, err
	}
	if err = event(ctx, tx, "bootstrap", "success", "local", &id); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return Identity{ID: id, Username: name, Role: "admin", Revision: "1"}, nil
}

func bucketKey(kind, value string) string {
	return fmt.Sprintf("%s:%x", kind, sha256.Sum256([]byte(value)))
}

func (s *Service) Login(ctx context.Context, username, password string, source netip.Addr) (Session, string, error) {
	var result Session
	name, err := NormalizeUsername(username)
	if err != nil || password == "" || len(password) > 128 {
		return result, "", ErrInput
	}
	if err = s.acquireKDF(ctx); err != nil {
		return result, "", err
	}
	defer func() { <-s.kdf }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, "", err
	}
	defer tx.Rollback(ctx)
	ip := "unknown"
	if source.IsValid() {
		ip = source.Unmap().String()
	}
	if _, err = securityConfig(ctx, tx, false); err != nil {
		return result, "", err
	}
	if source.IsValid() {
		var blocked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_blocks WHERE ip=$1::inet AND blocked)`, ip).Scan(&blocked); err != nil {
			return result, "", err
		}
		if blocked {
			if err = event(ctx, tx, "login", "blocked", ip, nil); err != nil {
				return result, "", err
			}
			if err = tx.Commit(ctx); err != nil {
				return result, "", err
			}
			return result, "", ErrSourceBlocked
		}
	}
	identityKey, sourceKey := bucketKey("identity", name), bucketKey("source", ip)
	keys := []string{identityKey, sourceKey}
	sort.Strings(keys)
	var retryAt time.Time
	for _, key := range keys {
		if _, err = tx.Exec(ctx, `INSERT INTO auth_login_limits(bucket,window_start) VALUES ($1,$2) ON CONFLICT DO NOTHING`, key, time.Now().UTC()); err != nil {
			return result, "", err
		}
		var start time.Time
		var failures int
		if err = tx.QueryRow(ctx, `SELECT window_start,failures FROM auth_login_limits WHERE bucket=$1 FOR UPDATE`, key).Scan(&start, &failures); err != nil {
			return result, "", err
		}
		now := time.Now().UTC()
		if !now.Before(start.Add(loginWindow)) {
			if _, err = tx.Exec(ctx, `UPDATE auth_login_limits SET window_start=$2,failures=0 WHERE bucket=$1`, key, now); err != nil {
				return result, "", err
			}
			failures = 0
		}
		limit := sourceFailures
		if key == identityKey {
			limit = identifierFailures
		}
		if failures >= limit && start.Add(loginWindow).After(retryAt) {
			retryAt = start.Add(loginWindow)
		}
	}
	if !retryAt.IsZero() {
		if err = event(ctx, tx, "login", "limited", ip, nil); err != nil {
			return result, "", err
		}
		if err = tx.Commit(ctx); err != nil {
			return result, "", err
		}
		return result, "", limited(retryAt)
	}
	var hash string
	var enabled bool
	var revision int64
	err = tx.QueryRow(ctx, `SELECT id,username,role,revision,password_hash,enabled FROM auth_users WHERE username=$1 FOR UPDATE`, name).Scan(&result.User.ID, &result.User.Username, &result.User.Role, &revision, &hash, &enabled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, "", err
	}
	if !enabled {
		hash = ""
	}
	if !verifyPassword(password, hash) {
		if _, err = tx.Exec(ctx, `UPDATE auth_login_limits SET failures=failures+1 WHERE bucket=ANY($1::text[])`, keys); err != nil {
			return result, "", err
		}
		var id *string
		if result.User.ID != "" {
			id = &result.User.ID
		}
		if err = event(ctx, tx, "login", "failure", ip, id); err != nil {
			return result, "", err
		}
		if err = tx.Commit(ctx); err != nil {
			return result, "", err
		}
		return Session{}, "", ErrUnauthorized
	}
	token, err := randomValue(32)
	if err != nil {
		return result, "", err
	}
	digest, err := tokenDigest(token)
	if err != nil {
		return result, "", err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	result.CreatedAt, result.ServerTime = now, now
	result.IdleExpiresAt, result.AbsoluteExpiresAt = now.Add(IdleLifetime), now.Add(AbsoluteLifetime)
	result.User.Revision = strconv.FormatInt(revision, 10)
	if _, err = tx.Exec(ctx, `INSERT INTO auth_sessions(digest,user_id,created_at,idle_expires_at,absolute_expires_at) VALUES ($1,$2,$3,$4,$5)`, digest, result.User.ID, now, result.IdleExpiresAt, result.AbsoluteExpiresAt); err != nil {
		return result, "", err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_login_limits WHERE bucket=$1`, identityKey); err != nil {
		return result, "", err
	}
	if err = event(ctx, tx, "login", "success", ip, &result.User.ID); err != nil {
		return result, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, "", err
	}
	telemetry.SetActor(ctx, result.User.ID)
	return result, token, nil
}

func lockedSession(ctx context.Context, tx pgx.Tx, digest []byte, write bool) (Session, bool, bool, error) {
	var result Session
	var id string
	if err := tx.QueryRow(ctx, `SELECT user_id FROM auth_sessions WHERE digest=$1`, digest).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, false, false, ErrUnauthorized
		}
		return result, false, false, err
	}
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	var enabled, revoked bool
	var revision int64
	err := tx.QueryRow(ctx, `SELECT id,username,role,revision,enabled FROM auth_users WHERE id=$1`+lock, id).Scan(&result.User.ID, &result.User.Username, &result.User.Role, &revision, &enabled)
	if err != nil {
		return result, false, false, err
	}
	err = tx.QueryRow(ctx, `SELECT created_at,idle_expires_at,absolute_expires_at,revoked_at IS NOT NULL FROM auth_sessions WHERE digest=$1`+lock, digest).Scan(&result.CreatedAt, &result.IdleExpiresAt, &result.AbsoluteExpiresAt, &revoked)
	if err != nil {
		return result, false, false, err
	}
	result.User.Revision = strconv.FormatInt(revision, 10)
	result.CreatedAt = result.CreatedAt.UTC()
	result.IdleExpiresAt = result.IdleExpiresAt.UTC()
	result.AbsoluteExpiresAt = result.AbsoluteExpiresAt.UTC()
	result.ServerTime = time.Now().UTC().Truncate(time.Microsecond)
	if live(result, enabled, revoked) {
		telemetry.SetActor(ctx, result.User.ID)
	}
	return result, enabled, revoked, nil
}

func live(result Session, enabled, revoked bool) bool {
	return enabled && !revoked && result.ServerTime.Before(result.IdleExpiresAt) && result.ServerTime.Before(result.AbsoluteExpiresAt)
}

func (s *Service) Current(ctx context.Context, token string, renew bool) (Session, error) {
	var result Session
	digest, err := tokenDigest(token)
	if err != nil {
		return result, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	result, enabled, revoked, err := lockedSession(ctx, tx, digest, renew)
	if err != nil {
		return Session{}, err
	}
	if !live(result, enabled, revoked) {
		return Session{}, ErrUnauthorized
	}
	if renew {
		result.IdleExpiresAt = result.ServerTime.Add(IdleLifetime)
		if result.IdleExpiresAt.After(result.AbsoluteExpiresAt) {
			result.IdleExpiresAt = result.AbsoluteExpiresAt
		}
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest, result.IdleExpiresAt); err != nil {
			return Session{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return result, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	digest, err := tokenDigest(token)
	if err != nil {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, _, _, err := lockedSession(ctx, tx, digest, true)
	if errors.Is(err, ErrUnauthorized) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE digest=$1`, digest, time.Now().UTC()); err != nil {
		return err
	}
	if err = event(ctx, tx, "logout", "success", "session", &result.User.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ChangePassword(ctx context.Context, token, current, next string) error {
	if current == "" || len(current) > 128 || !ValidPassword(next) || current == next {
		return ErrInput
	}
	digest, err := tokenDigest(token)
	if err != nil {
		return err
	}
	if err = s.acquireKDF(ctx); err != nil {
		return err
	}
	defer func() { <-s.kdf }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, enabled, revoked, err := lockedSession(ctx, tx, digest, true)
	if err != nil {
		return err
	}
	if !live(result, enabled, revoked) {
		return ErrUnauthorized
	}
	var stored string
	if err = tx.QueryRow(ctx, `SELECT password_hash FROM auth_users WHERE id=$1`, result.User.ID).Scan(&stored); err != nil {
		return err
	}
	if !verifyPassword(current, stored) {
		if err = event(ctx, tx, "password", "failure", "session", &result.User.ID); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return ErrInput
	}
	hash, err := hashPassword(next)
	if err != nil {
		return err
	}
	result.ServerTime = time.Now().UTC()
	if !live(result, enabled, revoked) {
		return ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET password_hash=$2,revision=revision+1 WHERE id=$1`, result.User.ID, hash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE user_id=$1`, result.User.ID, result.ServerTime); err != nil {
		return err
	}
	if err = revokeAPIKeys(ctx, tx, result.User.ID, result.ServerTime); err != nil {
		return err
	}
	if err = event(ctx, tx, "password", "success", "session", &result.User.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
