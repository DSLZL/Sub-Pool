package auth

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
)

var (
	ErrSourceBlocked       = &Failure{Status: 403, Code: "SOURCE_BLOCKED"}
	ErrSelfSource          = &Failure{Status: 409, Code: "SELF_SOURCE_PROTECTED"}
	ErrDiagnosticsDisabled = &Failure{Status: 409, Code: "NETWORK_DIAGNOSTICS_DISABLED"}
)

type SecurityConfig struct {
	Revision                  string `json:"revision"`
	NetworkDiagnosticsEnabled bool   `json:"network_diagnostics_enabled"`
}

func securityConfig(ctx context.Context, tx pgx.Tx, write bool) (SecurityConfig, error) {
	var c SecurityConfig
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT revision::text,network_diagnostics_enabled FROM security_config WHERE singleton`+lock).Scan(&c.Revision, &c.NetworkDiagnosticsEnabled)
	return c, err
}

func (s *Service) GetSecurityConfig(ctx context.Context, token string) (SecurityConfig, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SecurityConfig{}, err
	}
	defer tx.Rollback(ctx)
	c, err := securityConfig(ctx, tx, false)
	if err == nil {
		_, err = s.AuthorizeAdmin(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return c, err
}

func (s *Service) UpdateSecurityConfig(ctx context.Context, token, revision string, enabled bool) (SecurityConfig, error) {
	if !validRevision(revision) {
		return SecurityConfig{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SecurityConfig{}, err
	}
	defer tx.Rollback(ctx)
	c, err := securityConfig(ctx, tx, true)
	if err != nil {
		return SecurityConfig{}, err
	}
	actor, err := s.AuthorizeAdmin(ctx, tx, token)
	if err != nil {
		return SecurityConfig{}, err
	}
	if c.Revision != revision {
		return SecurityConfig{}, ErrConflict
	}
	err = tx.QueryRow(ctx, `UPDATE security_config SET revision=revision+1,network_diagnostics_enabled=$1 WHERE singleton RETURNING revision::text,network_diagnostics_enabled`, enabled).Scan(&c.Revision, &c.NetworkDiagnosticsEnabled)
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "security.diagnostics.update", Outcome: "succeeded", TargetType: "network_diagnostics", TargetID: "global", Revision: &c.Revision})
	}
	if err == nil {
		_, err = s.AuthorizeAdmin(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return c, err
}

type NetworkComparison struct {
	ReportedIP string        `json:"reported_ip"`
	Trusted    bool          `json:"trusted"`
	Source     RequestSource `json:"server_source"`
}

func (s *Service) CompareNetwork(ctx context.Context, token, reported string, consent bool) (NetworkComparison, error) {
	ip, valid := parseIP(reported)
	if !consent || !valid || ip.String() != reported {
		return NetworkComparison{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NetworkComparison{}, err
	}
	defer tx.Rollback(ctx)
	c, err := securityConfig(ctx, tx, false)
	if err == nil {
		_, err = s.AuthorizeSession(ctx, tx, token)
	}
	if err != nil {
		return NetworkComparison{}, err
	}
	if !c.NetworkDiagnosticsEnabled {
		return NetworkComparison{}, ErrDiagnosticsDisabled
	}
	result := NetworkComparison{ReportedIP: reported, Source: sourceContext(ctx, netip.Addr{})}
	if _, err = s.AuthorizeSession(ctx, tx, token); err == nil {
		err = tx.Commit(ctx)
	}
	return result, err
}

type SourceRule struct {
	IP       string    `json:"ip"`
	Revision string    `json:"revision"`
	Blocked  bool      `json:"blocked"`
	SavedAt  time.Time `json:"saved_at"`
	ActorID  string    `json:"actor_id"`
}

type SourcePage struct {
	Rules     []SourceRule `json:"rules"`
	NextAfter *string      `json:"next_after"`
}

func scanSource(row pgx.Row) (SourceRule, error) {
	var rule SourceRule
	err := row.Scan(&rule.IP, &rule.Revision, &rule.Blocked, &rule.SavedAt, &rule.ActorID)
	if ip, valid := parseIP(rule.IP); valid {
		rule.IP = ip.String()
	}
	rule.SavedAt = rule.SavedAt.UTC()
	return rule, err
}

func (s *Service) ListSources(ctx context.Context, token, after, state string, limit int) (SourcePage, error) {
	if after != "" {
		ip, valid := parseIP(after)
		if !valid || ip.String() != after {
			return SourcePage{}, ErrInput
		}
	}
	if limit < 1 || limit > 100 || state != "all" && state != "blocked" && state != "unblocked" {
		return SourcePage{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SourcePage{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.AuthorizeRead(ctx, tx, token, "security"); err != nil {
		return SourcePage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT host(ip),revision::text,blocked,saved_at,actor_id FROM source_blocks WHERE (NULLIF($1,'')::inet IS NULL OR ip>NULLIF($1,'')::inet) AND ($2='all' OR blocked=($2='blocked')) ORDER BY ip LIMIT $3`, after, state, limit+1)
	if err != nil {
		return SourcePage{}, err
	}
	page := SourcePage{Rules: []SourceRule{}}
	for rows.Next() {
		rule, scanErr := scanSource(rows)
		if scanErr != nil {
			rows.Close()
			return SourcePage{}, scanErr
		}
		page.Rules = append(page.Rules, rule)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return SourcePage{}, err
	}
	if len(page.Rules) > limit {
		page.Rules = page.Rules[:limit]
		page.NextAfter = &page.Rules[limit-1].IP
	}
	if _, err = s.AuthorizeRead(ctx, tx, token, "security"); err == nil {
		err = tx.Commit(ctx)
	}
	return page, err
}

func (s *Service) UpdateSource(ctx context.Context, token, address, revision string, blocked bool) (SourceRule, error) {
	ip, valid := parseIP(address)
	if !valid || ip.String() != address || ip.IsUnspecified() || ip.IsMulticast() || revision != "0" && !validRevision(revision) {
		return SourceRule{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SourceRule{}, err
	}
	defer tx.Rollback(ctx)
	// Login and security writes acquire this lock before any identity/session locks.
	if _, err = securityConfig(ctx, tx, true); err != nil {
		return SourceRule{}, err
	}
	actor, err := s.AuthorizeAdmin(ctx, tx, token)
	if err != nil {
		return SourceRule{}, err
	}
	source := sourceContext(ctx, netip.Addr{})
	if blocked && (source.IP == nil || source.PeerIP == nil || *source.IP == address || *source.PeerIP == address) {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "security.source.block", Outcome: "denied", TargetType: "source_ip", TargetID: address})
		if err == nil {
			_, err = s.AuthorizeAdmin(ctx, tx, token)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			return SourceRule{}, err
		}
		return SourceRule{}, ErrSelfSource
	}
	current := "0"
	err = tx.QueryRow(ctx, `SELECT revision::text FROM source_blocks WHERE ip=$1::inet FOR UPDATE`, address).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SourceRule{}, err
	}
	if current != revision {
		return SourceRule{}, ErrConflict
	}
	rule, err := scanSource(tx.QueryRow(ctx, `INSERT INTO source_blocks(ip,revision,blocked,saved_at,actor_id) VALUES ($1::inet,1,$2,$3,$4) ON CONFLICT (ip) DO UPDATE SET revision=source_blocks.revision+1,blocked=EXCLUDED.blocked,saved_at=EXCLUDED.saved_at,actor_id=EXCLUDED.actor_id RETURNING host(ip),revision::text,blocked,saved_at,actor_id`, address, blocked, time.Now().UTC(), actor.ID))
	if err == nil {
		action := "security.source.unblock"
		if blocked {
			action = "security.source.block"
		}
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: action, Outcome: "succeeded", TargetType: "source_ip", TargetID: address, Revision: &rule.Revision})
	}
	if err == nil {
		_, err = s.AuthorizeAdmin(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return rule, err
}

type AuditRange struct {
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
	After string    `json:"after"`
	Limit int       `json:"limit"`
}

func validAuditRange(input AuditRange) bool {
	return !input.From.IsZero() && !input.Until.IsZero() && input.Until.After(input.From) && input.Until.Sub(input.From) <= 31*24*time.Hour && (input.After == "0" || validRevision(input.After)) && input.Limit >= 1 && input.Limit <= 100
}

type LoginEvent struct {
	ID         string        `json:"id"`
	OccurredAt time.Time     `json:"occurred_at"`
	Outcome    string        `json:"outcome"`
	UserID     *string       `json:"user_id"`
	Username   *string       `json:"username"`
	Source     RequestSource `json:"source"`
}

type LoginCounts struct {
	Success string `json:"success"`
	Failure string `json:"failure"`
	Limited string `json:"limited"`
	Blocked string `json:"blocked"`
}

type LoginPage struct {
	Events    []LoginEvent `json:"events"`
	NextAfter *string      `json:"next_after"`
	Counts    LoginCounts  `json:"counts"`
}

func (s *Service) LoginAudit(ctx context.Context, token string, input AuditRange, outcome string) (LoginPage, error) {
	if !validAuditRange(input) || outcome != "all" && outcome != "success" && outcome != "failure" && outcome != "limited" && outcome != "blocked" {
		return LoginPage{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LoginPage{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.AuthorizeRead(ctx, tx, token, "security"); err != nil {
		return LoginPage{}, err
	}
	page := LoginPage{Events: []LoginEvent{}}
	rows, err := tx.Query(ctx, `SELECT e.id::text,e.occurred_at,e.outcome,e.user_id,u.username,e.source_ip,e.peer_ip,e.source_kind FROM auth_events e LEFT JOIN auth_users u ON u.id=e.user_id WHERE e.action='login' AND e.occurred_at>=$1 AND e.occurred_at<$2 AND e.id>$3 AND ($4='all' OR e.outcome=$4) ORDER BY e.id LIMIT $5`, input.From, input.Until, input.After, outcome, input.Limit+1)
	if err != nil {
		return LoginPage{}, err
	}
	for rows.Next() {
		var event LoginEvent
		var source string
		if err = rows.Scan(&event.ID, &event.OccurredAt, &event.Outcome, &event.UserID, &event.Username, &source, &event.Source.PeerIP, &event.Source.Kind); err != nil {
			rows.Close()
			return LoginPage{}, err
		}
		if ip, valid := parseIP(source); valid {
			value := ip.String()
			event.Source.IP = &value
		}
		event.OccurredAt = event.OccurredAt.UTC()
		page.Events = append(page.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return LoginPage{}, err
	}
	if len(page.Events) > input.Limit {
		page.Events = page.Events[:input.Limit]
		page.NextAfter = &page.Events[input.Limit-1].ID
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE outcome='success')::text,count(*) FILTER (WHERE outcome='failure')::text,count(*) FILTER (WHERE outcome='limited')::text,count(*) FILTER (WHERE outcome='blocked')::text FROM auth_events WHERE action='login' AND occurred_at>=$1 AND occurred_at<$2 AND ($3='all' OR outcome=$3)`, input.From, input.Until, outcome).Scan(&page.Counts.Success, &page.Counts.Failure, &page.Counts.Limited, &page.Counts.Blocked)
	if err == nil {
		_, err = s.AuthorizeRead(ctx, tx, token, "security")
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return page, err
}

type ManagementEvent struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operation_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	ActorID     *string   `json:"actor_id"`
	TaskID      *string   `json:"task_id"`
	Action      string    `json:"action"`
	Outcome     string    `json:"outcome"`
	TargetType  string    `json:"target_type"`
	TargetID    string    `json:"target_id"`
	Revision    *string   `json:"target_revision"`
	SourceID    *string   `json:"source_id"`
}

type ManagementPage struct {
	Events    []ManagementEvent `json:"events"`
	NextAfter *string           `json:"next_after"`
}

func (s *Service) ManagementAudit(ctx context.Context, token string, input AuditRange) (ManagementPage, error) {
	if !validAuditRange(input) {
		return ManagementPage{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagementPage{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.AuthorizeAdmin(ctx, tx, token); err != nil {
		return ManagementPage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,operation_id,occurred_at,actor_id,task_id,action,outcome,target_type,target_id,target_revision::text,source_id FROM management_events WHERE occurred_at>=$1 AND occurred_at<$2 AND id>$3 ORDER BY id LIMIT $4`, input.From, input.Until, input.After, input.Limit+1)
	if err != nil {
		return ManagementPage{}, err
	}
	page := ManagementPage{Events: []ManagementEvent{}}
	for rows.Next() {
		var event ManagementEvent
		if err = rows.Scan(&event.ID, &event.OperationID, &event.OccurredAt, &event.ActorID, &event.TaskID, &event.Action, &event.Outcome, &event.TargetType, &event.TargetID, &event.Revision, &event.SourceID); err != nil {
			rows.Close()
			return ManagementPage{}, err
		}
		event.OccurredAt = event.OccurredAt.UTC()
		page.Events = append(page.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ManagementPage{}, err
	}
	if len(page.Events) > input.Limit {
		page.Events = page.Events[:input.Limit]
		page.NextAfter = &page.Events[input.Limit-1].ID
	}
	if _, err = s.AuthorizeAdmin(ctx, tx, token); err == nil {
		err = tx.Commit(ctx)
	}
	return page, err
}
