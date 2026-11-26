package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"sub-pool/backend/internal/audit"
)

var (
	ErrNotFound = &Failure{Status: 404, Code: "NOT_FOUND"}
	ErrConflict = &Failure{Status: 409, Code: "CONFLICT"}
)

// These grants permit reads only; they do not declare domain capabilities available.
var readFeatures = []string{"overview", "accounts", "pools", "usage", "observations", "estimates", "balances", "temporary", "pricing", "dispatch", "notifications", "security"}

type Grants struct {
	Features       []string `json:"features"`
	AccountIDs     []string `json:"account_ids"`
	ParticipantIDs []string `json:"participant_ids"`
}

type Access struct {
	User    Identity `json:"user"`
	AllData bool     `json:"all_data"`
	Grants
}

func validID(id string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(raw) == 16 && base64.RawURLEncoding.EncodeToString(raw) == id
}

func validRevision(revision string) bool {
	value, err := strconv.ParseInt(revision, 10, 64)
	return err == nil && value > 0 && strconv.FormatInt(value, 10) == revision
}

func validGrants(g Grants) bool {
	for i, values := range [][]string{g.Features, g.AccountIDs, g.ParticipantIDs} {
		limit := 500
		if i == 0 {
			limit = len(readFeatures)
		}
		if values == nil || len(values) > limit {
			return false
		}
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if seen[value] || i == 0 && !slices.Contains(readFeatures, value) || i > 0 && !validID(value) {
				return false
			}
			seen[value] = true
		}
	}
	return true
}

func currentAccess(ctx context.Context, tx pgx.Tx, token string) (Access, error) {
	if credential, ok := ctx.Value(machineKey{}).(machineCredential); ok {
		return currentAPIKeyAccess(ctx, tx, token, credential)
	}
	var access Access
	digest, err := tokenDigest(token)
	if err != nil {
		return access, err
	}
	session, enabled, revoked, err := lockedSession(ctx, tx, digest, false)
	if err != nil {
		return access, err
	}
	if !live(session, enabled, revoked) {
		return access, ErrUnauthorized
	}
	if session.MustChangePassword {
		return access, ErrPasswordChangeRequired
	}
	access.User = session.User
	access.AllData = session.User.Role == "admin"
	err = tx.QueryRow(ctx, `SELECT read_features,account_ids,participant_ids FROM auth_users WHERE id=$1`, access.User.ID).
		Scan(&access.Features, &access.AccountIDs, &access.ParticipantIDs)
	return access, err
}

// Keep the caller's transaction open through the protected query or mutation.
func (s *Service) AuthorizeSession(ctx context.Context, tx pgx.Tx, token string) (Identity, error) {
	access, err := currentAccess(ctx, tx, token)
	return access.User, err
}

func (s *Service) AuthorizeAdmin(ctx context.Context, tx pgx.Tx, token string) (Identity, error) {
	access, err := currentAccess(ctx, tx, token)
	if err == nil && access.User.Role != "admin" {
		err = ErrForbidden
	}
	return access.User, err
}

func (s *Service) AuthorizeAdminSession(ctx context.Context, tx pgx.Tx, token string) (Identity, error) {
	if _, ok := ctx.Value(machineKey{}).(machineCredential); ok {
		return Identity{}, ErrForbidden
	}
	if source := sourceContext(ctx, netip.Addr{}); source.IP != nil {
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_blocks WHERE ip=$1::inet AND `+SourceBlockActive+`)`, *source.IP).Scan(&blocked); err != nil {
			return Identity{}, err
		}
		if blocked {
			return Identity{}, ErrSourceBlocked
		}
	}
	return s.AuthorizeAdmin(ctx, tx, token)
}

// Account/participant SQL predicates must be applied before grouping or pagination.
func (s *Service) AuthorizeRead(ctx context.Context, tx pgx.Tx, token string, features ...string) (Access, error) {
	if len(features) == 0 {
		return Access{}, ErrInput
	}
	for _, feature := range features {
		if !slices.Contains(readFeatures, feature) {
			return Access{}, ErrInput
		}
	}
	access, err := currentAccess(ctx, tx, token)
	if err != nil || access.AllData {
		return access, err
	}
	for _, feature := range features {
		if !slices.Contains(access.Features, feature) {
			return access, ErrForbidden
		}
	}
	return access, nil
}

func (a Access) CheckAccount(id string) error {
	if !validID(id) || !a.AllData && !slices.Contains(a.AccountIDs, id) {
		return ErrNotFound
	}
	return nil
}

func (a Access) CheckParticipant(id string) error {
	if !validID(id) || !a.AllData && !slices.Contains(a.ParticipantIDs, id) {
		return ErrNotFound
	}
	return nil
}

// Unknown attribution must not expose a wider account or participant aggregate.
func (a Access) CheckAttribution(accountID, participantID string) error {
	if a.AllData {
		return nil
	}
	if err := a.CheckAccount(accountID); err != nil {
		return err
	}
	return a.CheckParticipant(participantID)
}

// Pass both columns for attributed facts; one column is only for that resource's own catalog.
func (a Access) ScopeSQL(account, participant pgx.Identifier, position int) (string, []any, error) {
	if position < 1 || position > 65534 || len(account)+len(participant) == 0 {
		return "", nil, ErrInput
	}
	var clauses []string
	args := []any{}
	for i, column := range []pgx.Identifier{account, participant} {
		if len(column) == 0 {
			continue
		}
		if len(column) > 2 || slices.Contains(column, "") {
			return "", nil, ErrInput
		}
		ids := a.AccountIDs
		if i == 1 {
			ids = a.ParticipantIDs
		}
		clauses = append(clauses, column.Sanitize()+"=ANY($"+strconv.Itoa(position+len(args))+"::text[])")
		args = append(args, ids)
	}
	if a.AllData {
		return "TRUE", []any{}, nil
	}
	return "(" + strings.Join(clauses, " AND ") + ")", args, nil
}

func (s *Service) CurrentAccess(ctx context.Context, token string) (Access, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Access{}, err
	}
	defer tx.Rollback(ctx)
	access, err := currentAccess(ctx, tx, token)
	if err != nil {
		return Access{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Access{}, err
	}
	return access, nil
}

type UserSummary struct {
	Identity
	Enabled          bool       `json:"enabled"`
	CreatedAt        time.Time  `json:"created_at"`
	FeatureCount     int        `json:"feature_count"`
	AccountCount     int        `json:"account_count"`
	ParticipantCount int        `json:"participant_count"`
	LastLoginAt      *time.Time `json:"last_login_at"`
}

type ManagedUser struct {
	UserSummary
	Grants
}

type UserPage struct {
	Users []UserSummary `json:"users"`
	Next  *string       `json:"next"`
}

const userColumns = "id,username,role,revision,enabled,created_at,cardinality(read_features),cardinality(account_ids),cardinality(participant_ids),(SELECT max(occurred_at) FROM auth_events WHERE user_id=auth_users.id AND action='login' AND outcome='success')"

func validUserRole(role string) bool {
	return role == "user" || role == "admin"
}

func readUser(ctx context.Context, tx pgx.Tx, id, role string, lock bool) (ManagedUser, error) {
	var user ManagedUser
	var revision int64
	sql := `SELECT ` + userColumns + `,read_features,account_ids,participant_ids FROM auth_users WHERE id=$1 AND ($2='' OR role=$2) AND deleted_at IS NULL`
	if lock {
		sql += " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, sql, id, role).Scan(&user.ID, &user.Username, &user.Role, &revision, &user.Enabled, &user.CreatedAt, &user.FeatureCount, &user.AccountCount, &user.ParticipantCount, &user.LastLoginAt, &user.Features, &user.AccountIDs, &user.ParticipantIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, ErrNotFound
	}
	user.Revision = strconv.FormatInt(revision, 10)
	user.CreatedAt = user.CreatedAt.UTC()
	if user.LastLoginAt != nil {
		value := user.LastLoginAt.UTC()
		user.LastLoginAt = &value
	}
	return user, err
}

func (s *Service) ListUsers(ctx context.Context, token, after, search string, limit int) (UserPage, error) {
	return s.listUsers(ctx, token, after, search, limit, "user")
}

func (s *Service) listUsers(ctx context.Context, token, after, search string, limit int, role string) (UserPage, error) {
	page := UserPage{Users: []UserSummary{}}
	search = strings.ToLower(strings.TrimSpace(search))
	if after != "" && !validID(after) || limit < 1 || limit > 100 || len(search) > 64 {
		return page, ErrInput
	}
	for _, r := range search {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return page, ErrInput
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT `+userColumns+` FROM auth_users WHERE role=$4 AND deleted_at IS NULL AND id>$1 AND left(username,length($2))=$2 ORDER BY id LIMIT $3`, after, search, limit+1, role)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var user UserSummary
		var revision int64
		if err = rows.Scan(&user.ID, &user.Username, &user.Role, &revision, &user.Enabled, &user.CreatedAt, &user.FeatureCount, &user.AccountCount, &user.ParticipantCount, &user.LastLoginAt); err != nil {
			return page, err
		}
		user.Revision, user.CreatedAt = strconv.FormatInt(revision, 10), user.CreatedAt.UTC()
		if user.LastLoginAt != nil {
			value := user.LastLoginAt.UTC()
			user.LastLoginAt = &value
		}
		page.Users = append(page.Users, user)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Users) > limit {
		page.Users = page.Users[:limit]
		page.Next = &page.Users[limit-1].ID
	}
	if err = tx.Commit(ctx); err != nil {
		return UserPage{}, err
	}
	return page, nil
}

func (s *Service) GetUser(ctx context.Context, token, id string) (ManagedUser, error) {
	return s.getUser(ctx, token, id, "user")
}

func (s *Service) getUser(ctx context.Context, token, id, role string) (ManagedUser, error) {
	if !validID(id) {
		return ManagedUser{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedUser{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return ManagedUser{}, err
	}
	user, err := readUser(ctx, tx, id, role, false)
	if err != nil {
		return ManagedUser{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ManagedUser{}, err
	}
	return user, nil
}

func userEvent(ctx context.Context, tx pgx.Tx, actor, target, action string) error {
	source := sourceContext(ctx, netip.Addr{})
	ip := ""
	if source.IP != nil {
		ip = *source.IP
	}
	_, err := tx.Exec(ctx, `INSERT INTO auth_events(occurred_at,action,outcome,user_id,target_user_id,source_ip,peer_ip,source_kind) VALUES ($1,$2,'success',$3,$4,$5,$6,$7)`, time.Now().UTC(), action, actor, target, ip, source.PeerIP, source.Kind)
	if err == nil {
		var revision string
		err = tx.QueryRow(ctx, `SELECT revision::text FROM auth_users WHERE id=$1`, target).Scan(&revision)
		if err == nil {
			_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor, Action: action, Outcome: "succeeded", TargetType: "user", TargetID: target, Revision: &revision})
		}
	}
	return err
}

func usernameConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "auth_users_username_key" {
		return ErrConflict
	}
	return err
}

type UserCreate struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	Enabled         bool   `json:"enabled"`
	Role            string `json:"role,omitempty"`
	CurrentPassword string `json:"current_password,omitempty"`
	Grants
}

func (s *Service) CreateUser(ctx context.Context, token string, input UserCreate) (ManagedUser, error) {
	var user ManagedUser
	name, err := NormalizeUsername(input.Username)
	if input.Role == "" {
		input.Role = "user"
	}
	if err != nil || !validUserRole(input.Role) || !ValidPassword(input.Password) || !validGrants(input.Grants) {
		return user, ErrInput
	}
	if input.Role == "admin" && len(input.Features)+len(input.AccountIDs)+len(input.ParticipantIDs) != 0 {
		return user, ErrInput
	}
	tx, err := s.beginUserMutation(ctx)
	if err != nil {
		return user, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return user, err
	}
	if err = s.acquireKDF(ctx); err != nil {
		return user, err
	}
	defer func() { <-s.kdf }()
	if input.Role == "admin" {
		if err = confirmCurrentPassword(ctx, tx, actor.ID, input.CurrentPassword); err != nil {
			return user, err
		}
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		return user, err
	}
	id, err := randomValue(16)
	if err != nil {
		return user, err
	}
	if _, err = s.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return user, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_users(id,username,role,password_hash,created_at,enabled,read_features,account_ids,participant_ids) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, name, input.Role, hash, time.Now().UTC().Truncate(time.Microsecond), input.Enabled, input.Features, input.AccountIDs, input.ParticipantIDs); err != nil {
		return user, usernameConflict(err)
	}
	user, err = readUser(ctx, tx, id, input.Role, false)
	if err == nil {
		action := "user.create"
		if input.Role == "admin" {
			action = "user.create_admin"
		}
		err = userEvent(ctx, tx, actor.ID, id, action)
	}
	if err != nil {
		return ManagedUser{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ManagedUser{}, err
	}
	return user, nil
}

type UserUpdate struct {
	ID              string `json:"id"`
	Revision        string `json:"revision"`
	Username        string `json:"username"`
	Enabled         bool   `json:"enabled"`
	NewPassword     string `json:"new_password"`
	Role            string `json:"role,omitempty"`
	CurrentPassword string `json:"current_password,omitempty"`
	Grants
}

func (s *Service) UpdateUser(ctx context.Context, token string, input UserUpdate) (ManagedUser, error) {
	name, err := NormalizeUsername(input.Username)
	if err != nil || !validID(input.ID) || !validRevision(input.Revision) || !validGrants(input.Grants) ||
		input.NewPassword != "" && !ValidPassword(input.NewPassword) || input.Role != "" && !validUserRole(input.Role) {
		return ManagedUser{}, ErrInput
	}
	targetRole := ""
	if input.Role == "" {
		targetRole, input.Role = "user", "user"
	}
	tx, err := s.beginUserMutation(ctx)
	if err != nil {
		return ManagedUser{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return ManagedUser{}, err
	}
	user, err := readUser(ctx, tx, input.ID, targetRole, true)
	if err != nil {
		return ManagedUser{}, err
	}
	if user.Revision != input.Revision {
		return ManagedUser{}, ErrConflict
	}
	roleChanged := user.Role != input.Role
	if input.NewPassword != "" && (user.Role == "admin" || input.Role == "admin") {
		return ManagedUser{}, ErrInput
	}
	if roleChanged || user.Role == "admin" {
		input.Grants = user.Grants
	}
	var hash string
	if roleChanged || input.NewPassword != "" {
		if err = s.acquireKDF(ctx); err != nil {
			return ManagedUser{}, err
		}
		defer func() { <-s.kdf }()
		if roleChanged {
			err = confirmCurrentPassword(ctx, tx, actor.ID, input.CurrentPassword)
		} else {
			hash, err = hashPassword(input.NewPassword)
		}
		if err != nil {
			return ManagedUser{}, err
		}
	}
	if _, err = s.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return ManagedUser{}, err
	}
	if user.Role == "admin" && user.Enabled && (roleChanged || !input.Enabled) {
		if err = requireAnotherAdministrator(ctx, tx, user.ID); err != nil {
			return ManagedUser{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET username=$2,enabled=$3,read_features=$4,account_ids=$5,participant_ids=$6,password_hash=CASE WHEN $7='' THEN password_hash ELSE $7 END,role=$8,revision=revision+1 WHERE id=$1`, input.ID, name, input.Enabled, input.Features, input.AccountIDs, input.ParticipantIDs, hash, input.Role); err != nil {
		return ManagedUser{}, usernameConflict(err)
	}
	if roleChanged || !input.Enabled || input.NewPassword != "" {
		err = revokeUserCredentials(ctx, tx, input.ID, time.Now().UTC())
	}
	if err == nil {
		action := "user.update"
		switch {
		case roleChanged && input.Role == "admin":
			action = "user.promote"
		case roleChanged:
			action = "user.demote"
		case input.NewPassword != "":
			action = "user.update_password"
		}
		err = userEvent(ctx, tx, actor.ID, input.ID, action)
	}
	if err != nil {
		return ManagedUser{}, err
	}
	user, err = readUser(ctx, tx, input.ID, input.Role, false)
	if err != nil {
		return ManagedUser{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ManagedUser{}, err
	}
	return user, nil
}

func (s *Service) DeleteUser(ctx context.Context, token, id, revision, confirmation string) error {
	return s.deleteUser(ctx, token, id, revision, confirmation, "user")
}

func (s *Service) deleteUser(ctx context.Context, token, id, revision, confirmation, role string) error {
	if !validID(id) || !validRevision(revision) {
		return ErrInput
	}
	tx, err := s.beginUserMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	actor, err := s.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return err
	}
	user, err := readUser(ctx, tx, id, role, true)
	if err != nil {
		return err
	}
	if user.Revision != revision {
		return ErrConflict
	}
	if confirmation != user.Username {
		return ErrInput
	}
	if _, err = s.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return err
	}
	if user.Role == "admin" && user.Enabled {
		if err = requireAnotherAdministrator(ctx, tx, id); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET deleted_at=$2,role='user',enabled=false,password_hash='',read_features='{}',account_ids='{}',participant_ids='{}',revision=revision+1 WHERE id=$1`, id, now); err != nil {
		return err
	}
	if err = revokeUserCredentials(ctx, tx, id, now); err != nil {
		return err
	}
	if err = userEvent(ctx, tx, actor.ID, id, "user.delete"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
