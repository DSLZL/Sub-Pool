package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

// shortcut: native group updates have no CAS; the bound deployment must enforce one pricing controller.
func (s *Service) ConfigurePricingControl(binding string) error {
	if binding != "" {
		parts := strings.Split(binding, ":")
		if len(parts) != 2 || !managedID(parts[0]) || !validRevision(parts[1]) {
			return errors.New("invalid SUB_POOL_PRICING_CONTROL_BINDING")
		}
	}
	s.pricingControlBinding = binding
	return nil
}

type PricingControlInput struct {
	Revision string `json:"revision"`
	SourceID string `json:"source_id"`
	GroupID  string `json:"group_id"`
}

type PricingControl struct {
	Configuration      *PricingConfiguration `json:"configuration"`
	Version            string                `json:"version"`
	SourceID           string                `json:"source_id"`
	ConnectionRevision string                `json:"connection_revision"`
	GroupID            string                `json:"group_id"`
	CanApply           bool                  `json:"can_apply"`
	Reason             string                `json:"reason"`
	Fingerprint        *string               `json:"fingerprint"`
	OwnedChangeID      *string               `json:"owned_change_id"`
	CheckedAt          time.Time             `json:"checked_at"`
}

type PricingApplyInput struct {
	PricingControlInput
	RequestID               string          `json:"request_id"`
	ExpectedFingerprint     string          `json:"expected_fingerprint"`
	AcknowledgeSharedImpact bool            `json:"acknowledge_shared_impact"`
	Changes                 []PricingChange `json:"changes"`
}

type PricingOperation struct {
	RequestID          string          `json:"request_id"`
	ID                 string          `json:"id"`
	Revision           string          `json:"revision"`
	ActorID            string          `json:"actor_id"`
	SourceID           string          `json:"source_id"`
	ConnectionRevision string          `json:"connection_revision"`
	GroupID            string          `json:"group_id"`
	GroupName          string          `json:"group_name"`
	Changes            []PricingChange `json:"changes"`
	State              string          `json:"state"`
	Reason             string          `json:"reason"`
	BeforeFingerprint  string          `json:"before_fingerprint"`
	AfterFingerprint   *string         `json:"after_fingerprint"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type PricingOperationInput struct {
	ID                      string `json:"id"`
	Revision                string `json:"revision"`
	AcknowledgeSharedImpact bool   `json:"acknowledge_shared_impact"`
}

type PricingReviewInput struct {
	ID                       string `json:"id"`
	Revision                 string `json:"revision"`
	AcknowledgeResultUnknown bool   `json:"acknowledge_result_unknown"`
}

type storedPricingOperation struct {
	PricingOperation
	before pricingNative
	target pricingNative
	after  *pricingNative
}

const pricingOperationColumns = `id::text,revision::text,actor_id,request_id,source_id,connection_revision::text,group_id::text,group_name,changes,state,reason,native_before,native_target,native_after,created_at,updated_at`

func scanPricingOperation(row pgx.Row) (storedPricingOperation, error) {
	var p storedPricingOperation
	var changes, before, target, after []byte
	err := row.Scan(&p.ID, &p.Revision, &p.ActorID, &p.RequestID, &p.SourceID, &p.ConnectionRevision, &p.GroupID, &p.GroupName, &changes, &p.State, &p.Reason, &before, &target, &after, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	for _, item := range []struct {
		body  []byte
		value any
	}{{changes, &p.Changes}, {before, &p.before}, {target, &p.target}} {
		if err == nil {
			err = json.Unmarshal(item.body, item.value)
		}
	}
	if err == nil && after != nil {
		err = json.Unmarshal(after, &p.after)
	}
	// jsonb may reorder rule objects; restore the same canonical representation used for native reads.
	for _, native := range []*pricingNative{&p.before, &p.target, p.after} {
		if err != nil || native == nil {
			continue
		}
		for name, raw := range native.Values {
			var ok bool
			native.Values[name], ok = canonicalPricing(raw, "")
			if !ok {
				err = errors.New("invalid stored pricing state")
				break
			}
		}
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	p.BeforeFingerprint = p.before.Fingerprint
	if p.after != nil {
		p.AfterFingerprint = &p.after.Fingerprint
	}
	return p, err
}

func readPricingOperation(ctx context.Context, tx pgx.Tx, id string) (storedPricingOperation, error) {
	return scanPricingOperation(tx.QueryRow(ctx, `SELECT `+pricingOperationColumns+` FROM pricing_changes WHERE id=$1 FOR UPDATE`, id))
}

func finishPricing(ctx context.Context, tx pgx.Tx, identity *auth.Service, token string) error {
	if _, err := identity.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) pricingGate(ctx context.Context, tx pgx.Tx, token string, in PricingControlInput) (storedConfig, string, string, pricingNative, string, error) {
	var native pricingNative
	if _, err := s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return storedConfig{}, "", "", native, "", err
	}
	c, kind, secret, err := s.pricingConfig(ctx, tx, token, in.Revision, in.SourceID)
	if err != nil {
		return c, kind, secret, native, "", err
	}
	if s.pricingControlBinding != in.SourceID+":"+in.Revision || kind != "bearer" || s.aead == nil {
		return c, kind, secret, native, "CONTROL_BINDING_REQUIRED", nil
	}
	mode, check := s.upstream.pricingMode(ctx, c.BaseURL, kind, secret)
	if check.State != "available" || mode == nil || *mode != "standard" {
		return c, kind, secret, native, "STANDARD_ADMIN_REQUIRED", nil
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/groups/"+in.GroupID, kind, secret, "pricing.group.read", true)
	var ok bool
	if check.State == "available" {
		native, ok = inspectPricingNative(body, in.GroupID, secret)
	}
	if !ok {
		return c, kind, secret, native, "TRUSTED_GROUP_STATE_REQUIRED", nil
	}
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return c, kind, secret, native, "", err
	}
	return c, kind, secret, native, "", nil
}

func validPricingControl(in PricingControlInput) bool {
	return validRevision(in.Revision) && managedID(in.SourceID) && validRevision(in.GroupID)
}

func (s *Service) GetPricingControl(ctx context.Context, token string, in PricingControlInput) (PricingControl, error) {
	if !validPricingControl(in) {
		return PricingControl{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingControl{}, err
	}
	defer tx.Rollback(ctx)
	_, _, _, native, reason, err := s.pricingGate(ctx, tx, token, in)
	if err != nil {
		return PricingControl{}, err
	}
	out := PricingControl{Version: "pricing-control-v1", SourceID: in.SourceID, ConnectionRevision: in.Revision, GroupID: in.GroupID, Reason: reason, CheckedAt: time.Now().UTC()}
	if reason == "" {
		out.Fingerprint = &native.Fingerprint
		body, _ := json.Marshal(native.Values)
		configuration, valid := projectPricingConfiguration(body, "")
		if !valid {
			return PricingControl{}, errors.New("invalid trusted pricing projection")
		}
		out.Configuration = &configuration
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text FROM pricing_changes WHERE source_id=$1 AND group_id=$2 AND state IN ('applying','applied','reverting','needs_review')`, in.SourceID, in.GroupID).Scan(&id)
		if err == nil {
			out.OwnedChangeID, out.Reason = &id, "OWNED_CHANGE_OPEN"
		} else if errors.Is(err, pgx.ErrNoRows) {
			out.CanApply, out.Reason, err = true, "CONTROLLED_ENVIRONMENT", nil
		}
	}
	if err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	return out, err
}

func (s *Service) lockPricing(ctx context.Context, source, group string) (*pgxpool.Conn, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('pricing:' || $1 || ':' || $2,0))`, source, group).Scan(&locked)
	if err != nil || !locked {
		conn.Release()
		if err == nil {
			err = auth.ErrConflict
		}
		return nil, err
	}
	return conn, nil
}

func unlockPricing(conn *pgxpool.Conn, source, group string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended('pricing:' || $1 || ':' || $2,0))`, source, group).Scan(&unlocked); err != nil || !unlocked {
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

func pricingEvent(ctx context.Context, tx pgx.Tx, p PricingOperation, actor string) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO pricing_events(change_id,occurred_at,snapshot) VALUES ($1,$2,$3)`, p.ID, p.UpdatedAt, body); err != nil {
		return err
	}
	e := audit.Entry{OperationID: "pricing:change:" + p.ID, Action: "pricing." + p.State, TargetType: "pricing_group", TargetID: p.GroupID, SourceID: &p.SourceID, Revision: &p.Revision, Outcome: "succeeded"}
	if actor == "" {
		task := "pricing_recovery"
		e.TaskID = &task
	} else {
		e.ActorID = &actor
	}
	switch p.State {
	case "applying", "reverting":
		e.Outcome = "pending"
	case "needs_review", "reviewed":
		e.Outcome = "unknown"
	case "rejected":
		e.Outcome = "denied"
	case "conflict":
		e.Outcome = "conflict"
	}
	_, err = audit.Record(ctx, tx, e)
	return err
}

func savePricingOperation(ctx context.Context, tx pgx.Tx, p storedPricingOperation, actor string) (storedPricingOperation, error) {
	var after []byte
	if p.after != nil {
		after, _ = json.Marshal(p.after)
	}
	out, err := scanPricingOperation(tx.QueryRow(ctx, `UPDATE pricing_changes SET revision=revision+1,state=$2,reason=$3,native_after=$4,updated_at=$5 WHERE id=$1 RETURNING `+pricingOperationColumns, p.ID, p.State, p.Reason, after, time.Now().UTC().Truncate(time.Microsecond)))
	if err == nil {
		err = pricingEvent(ctx, tx, out.PricingOperation, actor)
	}
	return out, err
}

func (s *Service) ApplyPricing(ctx context.Context, token string, in PricingApplyInput) (PricingOperation, error) {
	if !validPricingControl(in.PricingControlInput) || !managedID(in.RequestID) || !in.AcknowledgeSharedImpact || len(in.ExpectedFingerprint) != 64 || !validPricingChanges(in.Changes) {
		return PricingOperation{}, auth.ErrInput
	}
	if _, err := hex.DecodeString(in.ExpectedFingerprint); err != nil {
		return PricingOperation{}, auth.ErrInput
	}
	// Authorize before acquiring a group lock, then recheck after it is acquired.
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingOperation{}, err
	}
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	_ = tx.Rollback(ctx)
	if err != nil {
		return PricingOperation{}, err
	}
	conn, err := s.lockPricing(ctx, in.SourceID, in.GroupID)
	if err != nil {
		return PricingOperation{}, err
	}
	defer unlockPricing(conn, in.SourceID, in.GroupID)
	tx, err = conn.Begin(ctx)
	if err != nil {
		return PricingOperation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return PricingOperation{}, err
	}
	body, _ := json.Marshal(in)
	hash := sha256.Sum256(body)
	digest := hex.EncodeToString(hash[:])
	var prior, priorDigest string
	err = tx.QueryRow(ctx, `SELECT id::text,request_digest FROM pricing_changes WHERE actor_id=$1 AND request_id=$2`, actor.ID, in.RequestID).Scan(&prior, &priorDigest)
	if err == nil {
		if priorDigest != digest {
			return PricingOperation{}, auth.ErrConflict
		}
		p, e := readPricingOperation(ctx, tx, prior)
		if e == nil {
			e = finishPricing(ctx, tx, s.auth, token)
		}
		return p.PricingOperation, e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PricingOperation{}, err
	}
	_, _, _, before, reason, err := s.pricingGate(ctx, tx, token, in.PricingControlInput)
	if err != nil {
		return PricingOperation{}, err
	}
	if reason != "" || before.Fingerprint != in.ExpectedFingerprint {
		return PricingOperation{}, auth.ErrConflict
	}
	target, _, err := pricingTarget(before, in.Changes)
	if err != nil {
		return PricingOperation{}, auth.ErrInput
	}
	changes, _ := json.Marshal(in.Changes)
	beforeBody, _ := json.Marshal(before)
	targetBody, _ := json.Marshal(target)
	p, err := scanPricingOperation(tx.QueryRow(ctx, `INSERT INTO pricing_changes(actor_id,request_id,request_digest,source_id,connection_revision,group_id,group_name,changes,native_before,native_target,state,reason,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'applying','APPLY_PENDING',$11,$11) RETURNING `+pricingOperationColumns, actor.ID, in.RequestID, digest, in.SourceID, in.Revision, in.GroupID, before.Name, changes, beforeBody, targetBody, time.Now().UTC().Truncate(time.Microsecond)))
	if err != nil {
		return PricingOperation{}, accountFailure(err)
	}
	if err = pricingEvent(ctx, tx, p.PricingOperation, actor.ID); err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	if err != nil {
		return PricingOperation{}, err
	}
	return s.executePricing(ctx, conn, token, actor.ID, p)
}

func pricingPatch(before, target pricingNative) []byte {
	patch := map[string]json.RawMessage{}
	for name, value := range target.Values {
		if !bytes.Equal(value, before.Values[name]) {
			patch[name] = value
		}
	}
	body, _ := json.Marshal(patch)
	return body
}

func (s *Service) executePricing(ctx context.Context, conn *pgxpool.Conn, token, actor string, intent storedPricingOperation) (PricingOperation, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return PricingOperation{}, err
	}
	defer tx.Rollback(ctx)
	p, err := readPricingOperation(ctx, tx, intent.ID)
	if err != nil || p.Revision != intent.Revision {
		if err == nil {
			err = auth.ErrConflict
		}
		return PricingOperation{}, err
	}
	reverting := p.State == "reverting"
	if !reverting && p.State != "applying" {
		return PricingOperation{}, auth.ErrConflict
	}
	c, kind, secret, current, reason, err := s.pricingGate(ctx, tx, token, PricingControlInput{Revision: p.ConnectionRevision, SourceID: p.SourceID, GroupID: p.GroupID})
	if err != nil {
		return PricingOperation{}, err
	}
	before, target := p.before, p.target
	if reverting {
		if p.after == nil {
			return PricingOperation{}, auth.ErrConflict
		}
		before, target = *p.after, p.before
	}
	if reason != "" || current.Fingerprint != before.Fingerprint {
		p.State, p.Reason = "conflict", "PRECONDITION_CHANGED"
		if reverting {
			p.State = "needs_review"
		}
	} else {
		body, write := s.upstream.request(ctx, http.MethodPut, c.BaseURL, "/api/v1/admin/groups/"+p.GroupID, kind, secret, "pricing.group.write", pricingPatch(before, target), true)
		p.State, p.Reason = "needs_review", "WRITE_RESULT_UNKNOWN"
		if write.State == "denied" {
			p.State, p.Reason = "rejected", "UPSTREAM_REJECTED"
			if reverting {
				p.State, p.Reason = "applied", "REVERT_REJECTED"
			}
		} else {
			ack, ackValid := inspectPricingNative(body, p.GroupID, secret)
			readBody, read := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/groups/"+p.GroupID, kind, secret, "pricing.group.read", true)
			after, valid := inspectPricingNative(readBody, p.GroupID, secret)
			if write.State == "available" && ackValid && read.State == "available" && valid && pricingValuesEqual(after, target) && after.UpdatedAt.After(before.UpdatedAt) && ack.Fingerprint == after.Fingerprint {
				p.State, p.Reason = "applied", "APPLIED"
				p.after = &after
				if reverting {
					p.State, p.Reason = "reverted", "REVERTED"
				}
			}
		}
	}
	p, err = savePricingOperation(ctx, tx, p, actor)
	if err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	if err != nil {
		return PricingOperation{}, err
	}
	return p.PricingOperation, nil
}

func (s *Service) RevertPricing(ctx context.Context, token string, in PricingOperationInput) (PricingOperation, error) {
	if !validRevision(in.ID) || !validRevision(in.Revision) || !in.AcknowledgeSharedImpact {
		return PricingOperation{}, auth.ErrInput
	}
	p, err := s.GetPricingOperation(ctx, token, in.ID)
	if err != nil {
		return PricingOperation{}, err
	}
	conn, err := s.lockPricing(ctx, p.SourceID, p.GroupID)
	if err != nil {
		return PricingOperation{}, err
	}
	defer unlockPricing(conn, p.SourceID, p.GroupID)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return PricingOperation{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return PricingOperation{}, err
	}
	saved, err := readPricingOperation(ctx, tx, in.ID)
	if err != nil {
		return PricingOperation{}, err
	}
	if saved.State == "reverted" {
		return saved.PricingOperation, finishPricing(ctx, tx, s.auth, token)
	}
	if saved.Revision != in.Revision || saved.State != "applied" {
		return PricingOperation{}, auth.ErrConflict
	}
	saved.State, saved.Reason = "reverting", "REVERT_PENDING"
	saved, err = savePricingOperation(ctx, tx, saved, actor.ID)
	if err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	if err != nil {
		return PricingOperation{}, err
	}
	return s.executePricing(ctx, conn, token, actor.ID, saved)
}

func (s *Service) GetPricingOperation(ctx context.Context, token, id string) (PricingOperation, error) {
	if !validRevision(id) {
		return PricingOperation{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingOperation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return PricingOperation{}, err
	}
	p, err := scanPricingOperation(tx.QueryRow(ctx, `SELECT `+pricingOperationColumns+` FROM pricing_changes WHERE id=$1`, id))
	if err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	return p.PricingOperation, err
}

type PricingRetryInput struct {
	ID                      string `json:"id"`
	Revision                string `json:"revision"`
	RequestID               string `json:"request_id"`
	AcknowledgeSharedImpact bool   `json:"acknowledge_shared_impact"`
}

// A retry is a new intent against the original precondition, never a replay of an unknown write.
func (s *Service) RetryPricing(ctx context.Context, token string, in PricingRetryInput) (PricingOperation, error) {
	if !validRevision(in.ID) || !validRevision(in.Revision) || !managedID(in.RequestID) || !in.AcknowledgeSharedImpact {
		return PricingOperation{}, auth.ErrInput
	}
	p, err := s.GetPricingOperation(ctx, token, in.ID)
	if err != nil {
		return PricingOperation{}, err
	}
	if p.State != "rejected" || p.Reason != "UPSTREAM_REJECTED" || p.Revision != in.Revision {
		return PricingOperation{}, auth.ErrConflict
	}
	return s.ApplyPricing(ctx, token, PricingApplyInput{
		PricingControlInput: PricingControlInput{Revision: p.ConnectionRevision, SourceID: p.SourceID, GroupID: p.GroupID},
		RequestID:           in.RequestID, ExpectedFingerprint: p.BeforeFingerprint,
		AcknowledgeSharedImpact: true, Changes: p.Changes,
	})
}

type PricingHistoryInput struct {
	SourceID string `json:"source_id"`
	GroupID  string `json:"group_id"`
	After    string `json:"after"`
	Limit    int    `json:"limit"`
}

type PricingHistory struct {
	Operations []PricingOperation `json:"operations"`
	NextAfter  *string            `json:"next_after"`
}

func (s *Service) ListPricingHistory(ctx context.Context, token string, in PricingHistoryInput) (PricingHistory, error) {
	if in.SourceID != "" && !managedID(in.SourceID) || in.GroupID != "" && !validRevision(in.GroupID) || in.After != "0" && !validRevision(in.After) || in.Limit < 1 || in.Limit > 100 {
		return PricingHistory{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingHistory{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return PricingHistory{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+pricingOperationColumns+` FROM pricing_changes WHERE ($1='' OR source_id=$1) AND ($2='' OR group_id=NULLIF($2,'')::bigint) AND id>$3 ORDER BY pricing_changes.id LIMIT $4`, in.SourceID, in.GroupID, in.After, in.Limit+1)
	if err != nil {
		return PricingHistory{}, err
	}
	out := PricingHistory{Operations: []PricingOperation{}}
	for rows.Next() {
		p, e := scanPricingOperation(rows)
		if e != nil {
			err = e
			break
		}
		out.Operations = append(out.Operations, p.PricingOperation)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err == nil && len(out.Operations) > in.Limit {
		out.Operations = out.Operations[:in.Limit]
		out.NextAfter = &out.Operations[in.Limit-1].ID
	}
	if err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	return out, err
}

func (s *Service) ReviewPricing(ctx context.Context, token string, in PricingReviewInput) (PricingOperation, error) {
	if !in.AcknowledgeResultUnknown || !validRevision(in.ID) || !validRevision(in.Revision) {
		return PricingOperation{}, auth.ErrInput
	}
	p, err := s.GetPricingOperation(ctx, token, in.ID)
	if err != nil {
		return PricingOperation{}, err
	}
	conn, err := s.lockPricing(ctx, p.SourceID, p.GroupID)
	if err != nil {
		return PricingOperation{}, err
	}
	defer unlockPricing(conn, p.SourceID, p.GroupID)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return PricingOperation{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		return PricingOperation{}, err
	}
	saved, err := readPricingOperation(ctx, tx, in.ID)
	if err != nil {
		return PricingOperation{}, err
	}
	if saved.Revision != in.Revision || saved.State != "needs_review" {
		return PricingOperation{}, auth.ErrConflict
	}
	saved.State, saved.Reason = "reviewed", "MANUALLY_REVIEWED_RESULT_UNKNOWN"
	saved, err = savePricingOperation(ctx, tx, saved, actor.ID)
	if err == nil {
		err = finishPricing(ctx, tx, s.auth, token)
	}
	return saved.PricingOperation, err
}

// Recovery records uncertainty without calling native endpoints or replaying an intent.
func (s *Service) recoverPricingIntents(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT id::text,source_id,group_id::text FROM pricing_changes WHERE state IN ('applying','reverting') ORDER BY pricing_changes.id LIMIT 100`)
	if err != nil {
		return err
	}
	type item struct{ id, source, group string }
	items := []item{}
	for rows.Next() {
		var p item
		if err = rows.Scan(&p.id, &p.source, &p.group); err != nil {
			break
		}
		items = append(items, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, candidate := range items {
		conn, e := s.lockPricing(ctx, candidate.source, candidate.group)
		if errors.Is(e, auth.ErrConflict) {
			continue
		}
		if e != nil {
			return e
		}
		err = func() error {
			defer unlockPricing(conn, candidate.source, candidate.group)
			tx, e := conn.Begin(ctx)
			if e != nil {
				return e
			}
			defer tx.Rollback(ctx)
			p, e := readPricingOperation(ctx, tx, candidate.id)
			if e != nil || p.State != "applying" && p.State != "reverting" {
				return e
			}
			p.State, p.Reason = "needs_review", "PROCESS_INTERRUPTED_RESULT_UNKNOWN"
			_, e = savePricingOperation(ctx, tx, p, "")
			if e == nil {
				e = tx.Commit(ctx)
			}
			return e
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
