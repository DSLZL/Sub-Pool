package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

const (
	writePricing     = "pricing"
	writeScheduling  = "scheduling"
	writeWallet      = "wallet"
	writeGrantNeeded = "SOURCE_WRITE_GRANT_REQUIRED"
	writesDisabled   = "UPSTREAM_WRITES_DISABLED"

	writeGrantConfirmation = "Sub2API 写入接口缺少原子版本校验。其他写入者可能在前置读取与实际写入之间修改数据。请确认 Sub-Pool 是这项能力的唯一写入者，并在派发结果不明时先核对上游。"
)

var writeCapabilities = []string{writePricing, writeScheduling, writeWallet}
var writeGrantConfirmationHash = fmt.Sprintf("%x", sha256.Sum256([]byte(writeGrantConfirmation)))

type WriteGrant struct {
	ID                 string     `json:"id"`
	GrantedBy          string     `json:"granted_by"`
	GrantedAt          time.Time  `json:"granted_at"`
	ConfirmationSHA256 string     `json:"confirmation_sha256"`
	RevokedBy          *string    `json:"revoked_by"`
	RevokedAt          *time.Time `json:"revoked_at"`
}

type WriteGrantStatus struct {
	Capability string      `json:"capability"`
	Grant      *WriteGrant `json:"grant"`
}

type WriteGrantView struct {
	SourceID               *string            `json:"source_id"`
	ConnectionRevision     string             `json:"connection_revision"`
	UpstreamWritesDisabled bool               `json:"upstream_writes_disabled"`
	ConfirmationText       string             `json:"confirmation_text"`
	ConfirmationSHA256     string             `json:"confirmation_sha256"`
	Grants                 []WriteGrantStatus `json:"grants"`
}

type WriteGrantUpdate struct {
	SourceID           string `json:"source_id"`
	ConnectionRevision string `json:"connection_revision"`
	Capability         string `json:"capability"`
	ExpectedGrantID    string `json:"expected_grant_id"`
	Enabled            *bool  `json:"enabled"`
	ConfirmationSHA256 string `json:"confirmation_sha256"`
	CurrentPassword    string `json:"current_password"`
}

func activeWriteGrant(ctx context.Context, tx pgx.Tx, sourceID, capability string, lock bool) (string, error) {
	query := `SELECT id::text FROM upstream_write_grants WHERE source_id=$1 AND capability=$2 AND revoked_at IS NULL`
	if lock {
		query += " FOR SHARE"
	}
	var id string
	err := tx.QueryRow(ctx, query, sourceID, capability).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return id, err
}

func (s *Service) writeGrant(ctx context.Context, tx pgx.Tx, c storedConfig, capability string, lock bool) (id, reason string, err error) {
	if s.UpstreamWritesDisabled {
		return "", writesDisabled, nil
	}
	if c.SourceID != nil {
		id, err = activeWriteGrant(ctx, tx, *c.SourceID, capability, lock)
	}
	if err == nil && id == "" {
		reason = writeGrantNeeded
	}
	return
}

func (s *Service) checkWriteDispatch(ctx context.Context, tx pgx.Tx, c storedConfig, capability, expectedID string) (string, error) {
	// Revocation and the last outbound write check contend on the same row.
	id, reason, err := s.writeGrant(ctx, tx, c, capability, true)
	if err == nil && reason == "" && id != expectedID {
		reason = writeGrantNeeded
	}
	return reason, err
}

func (s *Service) writeGrantView(ctx context.Context, tx pgx.Tx, c storedConfig) (WriteGrantView, error) {
	out := WriteGrantView{
		SourceID: c.SourceID, ConnectionRevision: c.Revision, UpstreamWritesDisabled: s.UpstreamWritesDisabled,
		ConfirmationText: writeGrantConfirmation, ConfirmationSHA256: writeGrantConfirmationHash,
	}
	latest := make(map[string]*WriteGrant, len(writeCapabilities))
	if c.SourceID != nil {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (capability) capability,id::text,granted_by,granted_at,confirmation_sha256,revoked_by,revoked_at
			FROM upstream_write_grants WHERE source_id=$1 ORDER BY capability,(revoked_at IS NULL) DESC,id DESC`, *c.SourceID)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var capability string
			var grant WriteGrant
			if err = rows.Scan(&capability, &grant.ID, &grant.GrantedBy, &grant.GrantedAt, &grant.ConfirmationSHA256, &grant.RevokedBy, &grant.RevokedAt); err != nil {
				return out, err
			}
			grant.GrantedAt = grant.GrantedAt.UTC()
			if grant.RevokedAt != nil {
				utc := grant.RevokedAt.UTC()
				grant.RevokedAt = &utc
			}
			latest[capability] = &grant
		}
		if err = rows.Err(); err != nil {
			return out, err
		}
	}
	for _, capability := range writeCapabilities {
		out.Grants = append(out.Grants, WriteGrantStatus{Capability: capability, Grant: latest[capability]})
	}
	return out, nil
}

func (s *Service) GetWriteGrants(ctx context.Context, token string) (WriteGrantView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteGrantView{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return WriteGrantView{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WriteGrantView{}, err
	}
	out, err := s.writeGrantView(ctx, tx, c)
	if err == nil {
		_, err = s.auth.AuthorizeAdminSession(ctx, tx, token)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}

func (s *Service) UpdateWriteGrant(ctx context.Context, token string, in WriteGrantUpdate) (WriteGrantView, error) {
	if !managedID(in.SourceID) || !validRevision(in.ConnectionRevision) || !slices.Contains(writeCapabilities, in.Capability) ||
		in.Enabled == nil || (in.ExpectedGrantID != "0" && !validRevision(in.ExpectedGrantID)) {
		return WriteGrantView{}, auth.ErrInput
	}
	if in.ConfirmationSHA256 != writeGrantConfirmationHash {
		return WriteGrantView{}, auth.ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteGrantView{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.ConfirmAdminPassword(ctx, tx, token, in.CurrentPassword)
	if err != nil {
		return WriteGrantView{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return WriteGrantView{}, err
	}
	if c.SourceID == nil || *c.SourceID != in.SourceID || c.Revision != in.ConnectionRevision {
		return WriteGrantView{}, auth.ErrConflict
	}
	var id string
	action := "connection.write_grant.revoke"
	if *in.Enabled && in.ExpectedGrantID == "0" {
		action = "connection.write_grant.enable"
		err = tx.QueryRow(ctx, `INSERT INTO upstream_write_grants(source_id,capability,granted_by,confirmation_sha256) VALUES ($1,$2,$3,$4)
			ON CONFLICT (source_id,capability) WHERE revoked_at IS NULL DO NOTHING RETURNING id::text`,
			in.SourceID, in.Capability, actor.ID, writeGrantConfirmationHash).Scan(&id)
	} else if !*in.Enabled && in.ExpectedGrantID != "0" {
		err = tx.QueryRow(ctx, `UPDATE upstream_write_grants SET revoked_by=$4,revoked_at=GREATEST(clock_timestamp(),granted_at)
			WHERE source_id=$1 AND capability=$2 AND id=$3::text::bigint AND revoked_at IS NULL RETURNING id::text`,
			in.SourceID, in.Capability, in.ExpectedGrantID, actor.ID).Scan(&id)
	} else {
		return WriteGrantView{}, auth.ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteGrantView{}, auth.ErrConflict
	}
	if err != nil {
		return WriteGrantView{}, err
	}
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return WriteGrantView{}, err
	}
	if _, err = audit.Record(ctx, tx, audit.Entry{
		ActorID: &actor.ID, Action: action, Outcome: "succeeded", TargetType: "upstream_write_grant",
		TargetID: id, Revision: &c.Revision, SourceID: c.SourceID,
	}); err != nil {
		return WriteGrantView{}, err
	}
	out, err := s.writeGrantView(ctx, tx, c)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}
