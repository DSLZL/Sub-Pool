package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

// ConfigureDispatchControl must run before serving requests or starting workers.
// shortcut: native scheduling has no CAS; enable only after enforcing one scheduling controller for this binding.
func (s *Service) ConfigureDispatchControl(binding string) error {
	if binding != "" {
		parts := strings.Split(binding, ":")
		if len(parts) != 2 || !managedID(parts[0]) || !validRevision(parts[1]) {
			return errors.New("invalid SUB_POOL_SCHEDULING_CONTROL_BINDING")
		}
	}
	s.dispatchControlBinding = binding
	return nil
}

type DispatchControl struct {
	Version                   string  `json:"version"`
	SourceID                  *string `json:"source_id"`
	ConnectionRevision        string  `json:"connection_revision"`
	CanPause                  bool    `json:"can_pause"`
	CanRestore                bool    `json:"can_restore"`
	AutomaticRestoreAvailable bool    `json:"automatic_restore_available"`
	Reason                    string  `json:"reason"`
}

func (s *Service) dispatchControlled(c storedConfig) bool {
	return c.SourceID != nil && s.dispatchControlBinding == *c.SourceID+":"+c.Revision && c.CredentialSet && c.CredentialKind != nil && *c.CredentialKind == "bearer" && s.aead != nil && !c.reverificationRequired
}

func (s *Service) GetDispatchControl(ctx context.Context, token string) (DispatchControl, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DispatchControl{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "dispatch")
	if err != nil {
		return DispatchControl{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return DispatchControl{}, err
	}
	out := DispatchControl{Version: "dispatch-control-v1", SourceID: c.SourceID, ConnectionRevision: c.Revision, Reason: "CONTROL_BINDING_REQUIRED"}
	if s.dispatchControlled(c) {
		out.AutomaticRestoreAvailable = true
		if access.User.Role == "admin" {
			out.CanPause, out.CanRestore, out.Reason = true, true, "CONTROLLED_ENVIRONMENT"
		} else {
			out.Reason = "ADMIN_REQUIRED"
		}
	}
	return out, s.finishDispatchRead(ctx, tx, token)
}

type DispatchPause struct {
	ID                  string    `json:"id"`
	Revision            string    `json:"revision"`
	AccountID           string    `json:"account_id"`
	SourceID            string    `json:"source_id"`
	ConnectionRevision  string    `json:"connection_revision"`
	AccountRevision     string    `json:"account_revision"`
	State               string    `json:"state"`
	Reason              string    `json:"reason"`
	PreviousSchedulable bool      `json:"previous_schedulable"`
	NativeUpdatedAt     time.Time `json:"native_updated_at"`
	RestoreAt           time.Time `json:"restore_at"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type DispatchPauseInput struct {
	AccountReadInput
	RequestID              string    `json:"request_id"`
	RestoreAt              time.Time `json:"restore_at"`
	AcknowledgeAccountWide bool      `json:"acknowledge_account_wide"`
}

type DispatchChangeInput struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type DispatchRescheduleInput struct {
	DispatchChangeInput
	RestoreAt time.Time `json:"restore_at"`
}

type DispatchReviewInput struct {
	DispatchChangeInput
	AcknowledgeResultUnknown bool `json:"acknowledge_result_unknown"`
}

const dispatchColumns = `id::text,revision::text,account_id,source_id,connection_revision::text,account_revision::text,state,reason,previous_schedulable,native_updated_at,restore_at,created_at,updated_at`

func dispatchUTC(p DispatchPause) DispatchPause {
	p.NativeUpdatedAt, p.RestoreAt = p.NativeUpdatedAt.UTC(), p.RestoreAt.UTC()
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p
}

func scanDispatch(row pgx.Row) (DispatchPause, error) {
	var p DispatchPause
	err := row.Scan(&p.ID, &p.Revision, &p.AccountID, &p.SourceID, &p.ConnectionRevision, &p.AccountRevision, &p.State, &p.Reason, &p.PreviousSchedulable, &p.NativeUpdatedAt, &p.RestoreAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return dispatchUTC(p), err
}

func readDispatch(ctx context.Context, tx pgx.Tx, id string) (DispatchPause, error) {
	return scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM dispatch_pauses WHERE id=$1 FOR UPDATE`, id))
}

func dispatchDeadline(value time.Time) bool {
	now := time.Now()
	return value.After(now) && !value.After(now.Add(30*24*time.Hour)) && value.Year() <= 9999
}

func (s *Service) dispatchAdmin(ctx context.Context, token string) (string, error) {
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return "", err
	}
	return actor.ID, s.finish(ctx, tx, token, true)
}

func (s *Service) lockDispatch(ctx context.Context, accountID string) (*pgxpool.Conn, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('dispatch:' || $1,0))`, accountID).Scan(&locked)
	if err != nil || !locked {
		conn.Release()
		if err == nil {
			err = auth.ErrConflict
		}
		return nil, err
	}
	return conn, nil
}

func unlockDispatch(conn *pgxpool.Conn, accountID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended('dispatch:' || $1,0))`, accountID).Scan(&unlocked); err != nil || !unlocked {
		conn.Conn().Close(ctx)
	}
	conn.Release()
}

func dispatchEvent(ctx context.Context, tx pgx.Tx, p DispatchPause, actorID, action string) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	actor, id := "worker", any(nil)
	if actorID != "" {
		actor, id = "administrator", actorID
	}
	_, err = tx.Exec(ctx, `INSERT INTO dispatch_events(pause_id,account_id,actor_id,actor,action,occurred_at,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, p.AccountID, id, actor, action, p.UpdatedAt, body)
	if err == nil {
		entry := audit.Entry{OperationID: "dispatch." + p.ID, Action: "dispatch." + action, Outcome: "succeeded", TargetType: "dispatch_pause", TargetID: p.ID, Revision: &p.Revision, SourceID: &p.SourceID}
		if actorID != "" {
			entry.ActorID = &actorID
		} else {
			taskID := "dispatch." + p.ID
			entry.TaskID = &taskID
		}
		switch p.State {
		case "pausing", "restoring":
			entry.Outcome = "pending"
		case "needs_review":
			entry.Outcome = "conflict"
			if p.Reason == "WRITE_RESULT_UNKNOWN" || p.Reason == "PROCESS_INTERRUPTED_RESULT_UNKNOWN" {
				entry.Outcome = "unknown"
			}
		case "failed":
			entry.Outcome = "failed"
		}
		if p.Reason == "READ_RETRY_PENDING" {
			entry.Outcome = "failed"
		}
		_, err = audit.Record(ctx, tx, entry)
	}
	return err
}

func saveDispatch(ctx context.Context, tx pgx.Tx, p DispatchPause, actorID, action string) (DispatchPause, error) {
	out, err := scanDispatch(tx.QueryRow(ctx, `UPDATE dispatch_pauses SET revision=revision+1,state=$2,reason=$3,native_updated_at=$4,restore_at=$5,updated_at=$6 WHERE id=$1 RETURNING `+dispatchColumns, p.ID, p.State, p.Reason, p.NativeUpdatedAt, p.RestoreAt, time.Now().UTC().Truncate(time.Microsecond)))
	if err == nil {
		err = dispatchEvent(ctx, tx, out, actorID, action)
	}
	return out, err
}

type dispatchNative struct {
	schedulable bool
	updatedAt   time.Time
}

func inspectDispatchNative(body json.RawMessage, a storedAccount, secret string) (dispatchNative, bool) {
	observed, valid := inspectAccount(body, a.UpstreamID, secret)
	var fields struct {
		Schedulable *bool     `json:"schedulable"`
		UpdatedAt   time.Time `json:"updated_at"`
		Status      string    `json:"status"`
	}
	if !valid || decode(body, &fields) != nil || fields.Schedulable == nil || fields.UpdatedAt.IsZero() || fields.UpdatedAt.Year() > 9999 || fields.Status != "active" || a.IdentityState != "verified" || observed.identity != "verified" || len(a.fingerprint) == 0 || !bytes.Equal(a.fingerprint, observed.fingerprint) || a.Snapshot.Account.Platform != observed.Account.Platform || a.Snapshot.Account.Type != observed.Account.Type {
		return dispatchNative{}, false
	}
	return dispatchNative{*fields.Schedulable, fields.UpdatedAt}, true
}

// A reason means no write was attempted. Only transient read failures may be retried by the worker.
func (s *Service) dispatchGuard(ctx context.Context, tx pgx.Tx, token string, worker bool, p DispatchPause) (storedConfig, storedAccount, string, string, dispatchNative, string, bool, error) {
	var c storedConfig
	var a storedAccount
	var n dispatchNative
	if !worker {
		if _, err := s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
			return c, a, "", "", n, "", false, err
		}
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return c, a, "", "", n, "", false, err
	}
	if !s.dispatchControlled(c) || c.SourceID == nil || *c.SourceID != p.SourceID || c.Revision != p.ConnectionRevision {
		return c, a, "", "", n, "CONTROL_BINDING_REQUIRED", false, nil
	}
	a, err = readManaged(ctx, tx, p.AccountID, c, true)
	if err != nil {
		return c, a, "", "", n, "", false, err
	}
	if !a.SourceCurrent || a.SourceID != p.SourceID || a.Revision != p.AccountRevision {
		return c, a, "", "", n, "LOCAL_ACCOUNT_CHANGED", false, nil
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return c, a, "", "", n, "CONNECTION_UNAVAILABLE", false, nil
	}
	if !worker {
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
			return c, a, "", "", n, "", false, err
		}
	}
	mode, check := s.upstream.pricingMode(ctx, c.BaseURL, kind, secret)
	if check.State != "available" || mode == nil || *mode != "standard" {
		return c, a, kind, secret, n, "NATIVE_AUTHORITY_UNVERIFIED", check.Reason == "TIMEOUT" || check.Reason == "NETWORK_ERROR" || check.Reason == "RATE_LIMITED", nil
	}
	if !worker {
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
			return c, a, "", "", n, "", false, err
		}
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID, kind, secret, "dispatch.account.read", true)
	if check.State != "available" {
		return c, a, kind, secret, n, "NATIVE_READ_UNAVAILABLE", check.Reason == "TIMEOUT" || check.Reason == "NETWORK_ERROR" || check.Reason == "RATE_LIMITED", nil
	}
	n, valid := inspectDispatchNative(body, a, secret)
	if !valid {
		return c, a, kind, secret, n, "NATIVE_ACCOUNT_UNVERIFIED", false, nil
	}
	return c, a, kind, secret, n, "", false, nil
}

func (s *Service) PauseDispatch(ctx context.Context, token string, in DispatchPauseInput) (DispatchPause, error) {
	if !validAccountRead(in.AccountReadInput) || !managedID(in.RequestID) || !in.AcknowledgeAccountWide {
		return DispatchPause{}, auth.ErrInput
	}
	actorID, err := s.dispatchAdmin(ctx, token)
	if err != nil {
		return DispatchPause{}, err
	}
	in.RestoreAt = in.RestoreAt.UTC().Truncate(time.Microsecond)
	body, _ := json.Marshal(in)
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	conn, err := s.lockDispatch(ctx, in.ID)
	if err != nil {
		return DispatchPause{}, err
	}
	defer unlockDispatch(conn, in.ID)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return DispatchPause{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return DispatchPause{}, err
	}
	var priorID, priorHash string
	err = tx.QueryRow(ctx, `SELECT id::text,request_digest FROM dispatch_pauses WHERE actor_id=$1 AND request_id=$2`, actorID, in.RequestID).Scan(&priorID, &priorHash)
	if err == nil {
		if priorHash != hash {
			return DispatchPause{}, auth.ErrConflict
		}
		p, e := readDispatch(ctx, tx, priorID)
		if e == nil {
			e = s.finish(ctx, tx, token, true)
		}
		return p, e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DispatchPause{}, err
	}
	if !dispatchDeadline(in.RestoreAt) {
		return DispatchPause{}, auth.ErrInput
	}
	p := DispatchPause{AccountID: in.ID, AccountRevision: in.Revision, SourceID: in.SourceID, ConnectionRevision: in.ConnectionRevision}
	_, _, _, _, native, reason, _, err := s.dispatchGuard(ctx, tx, token, false, p)
	if err != nil {
		return DispatchPause{}, err
	}
	if reason != "" {
		return DispatchPause{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return DispatchPause{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	p, err = scanDispatch(tx.QueryRow(ctx, `INSERT INTO dispatch_pauses(account_id,actor_id,request_id,request_digest,source_id,connection_revision,account_revision,state,reason,previous_schedulable,native_updated_at,restore_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'pausing','PAUSE_PENDING',$8,$9,$10,$11,$11) RETURNING `+dispatchColumns, in.ID, actorID, in.RequestID, hash, in.SourceID, in.ConnectionRevision, in.Revision, native.schedulable, native.updatedAt, in.RestoreAt, now))
	if err != nil {
		return DispatchPause{}, accountFailure(err)
	}
	if err = dispatchEvent(ctx, tx, p, actorID, "pause"); err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return DispatchPause{}, err
	}
	return s.executeDispatch(ctx, conn, token, actorID, false, p)
}

func (s *Service) executeDispatch(ctx context.Context, conn *pgxpool.Conn, token, actorID string, worker bool, intent DispatchPause) (DispatchPause, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return DispatchPause{}, err
	}
	defer tx.Rollback(ctx)
	p, err := readDispatch(ctx, tx, intent.ID)
	if err != nil {
		return DispatchPause{}, err
	}
	if p.Revision != intent.Revision || p.State != intent.State || p.State != "pausing" && p.State != "restoring" {
		return DispatchPause{}, auth.ErrConflict
	}
	restoring := p.State == "restoring"
	action := "pause_result"
	if restoring {
		action = "restore_result"
	}
	c, a, kind, secret, native, reason, retryable, err := s.dispatchGuard(ctx, tx, token, worker, p)
	if err != nil {
		return DispatchPause{}, err
	}
	if reason != "" {
		p.State, p.Reason = "needs_review", reason
		if restoring && retryable {
			p.State, p.Reason, action = "paused", "READ_RETRY_PENDING", "retry"
		}
	} else if !native.updatedAt.Equal(p.NativeUpdatedAt) || native.schedulable != (!restoring && p.PreviousSchedulable) {
		p.State, p.Reason = "needs_review", "NATIVE_ACCOUNT_CHANGED"
	} else if !restoring && !p.RestoreAt.After(time.Now()) {
		p.State, p.Reason = "failed", "DEADLINE_PASSED"
	} else {
		if !worker {
			if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
				return DispatchPause{}, err
			}
		}
		target := restoring && p.PreviousSchedulable
		if native.schedulable == target {
			p.State, p.Reason = "paused", "PREVIOUSLY_DISABLED"
			if restoring {
				p.State, p.Reason = "restored", "PREVIOUS_STATE_RETAINED"
			}
		} else {
			payload := []byte(fmt.Sprintf(`{"schedulable":%t}`, target))
			body, check := s.upstream.request(ctx, http.MethodPost, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID+"/schedulable", kind, secret, "dispatch.scheduling.write", payload, true)
			written, valid := inspectDispatchNative(body, a, secret)
			p.State, p.Reason = "needs_review", "WRITE_RESULT_UNKNOWN"
			if check.State == "available" && valid && written.schedulable == target && written.updatedAt.After(native.updatedAt) {
				body, read := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID, kind, secret, "dispatch.account.read", true)
				verified, valid := inspectDispatchNative(body, a, secret)
				if read.State == "available" && valid && verified.schedulable == target && verified.updatedAt.Equal(written.updatedAt) {
					p.NativeUpdatedAt = verified.updatedAt
					p.State, p.Reason = "paused", "VERIFIED_PAUSE"
					if restoring {
						p.State, p.Reason = "restored", "VERIFIED_RESTORE"
					}
				}
			}
		}
	}
	if !worker {
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
			return DispatchPause{}, err
		}
	}
	p, err = saveDispatch(ctx, tx, p, actorID, action)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return p, err
}

func (s *Service) dispatchChangeLock(ctx context.Context, token string, in DispatchChangeInput) (*pgxpool.Conn, string, string, error) {
	if !validRevision(in.ID) || !validRevision(in.Revision) {
		return nil, "", "", auth.ErrInput
	}
	actorID, err := s.dispatchAdmin(ctx, token)
	if err != nil {
		return nil, "", "", err
	}
	var accountID string
	err = s.pool.QueryRow(ctx, `SELECT account_id FROM dispatch_pauses WHERE id=$1`, in.ID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err != nil {
		return nil, "", "", err
	}
	conn, err := s.lockDispatch(ctx, accountID)
	return conn, accountID, actorID, err
}

func (s *Service) RescheduleDispatch(ctx context.Context, token string, in DispatchRescheduleInput) (DispatchPause, error) {
	if !dispatchDeadline(in.RestoreAt) {
		return DispatchPause{}, auth.ErrInput
	}
	conn, accountID, actorID, err := s.dispatchChangeLock(ctx, token, in.DispatchChangeInput)
	if err != nil {
		return DispatchPause{}, err
	}
	defer unlockDispatch(conn, accountID)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return DispatchPause{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return DispatchPause{}, err
	}
	p, err := readDispatch(ctx, tx, in.ID)
	if err != nil {
		return DispatchPause{}, err
	}
	if p.Revision != in.Revision || p.State != "paused" {
		return DispatchPause{}, auth.ErrConflict
	}
	p.RestoreAt, p.Reason = in.RestoreAt.UTC().Truncate(time.Microsecond), "DEADLINE_CHANGED"
	p, err = saveDispatch(ctx, tx, p, actorID, "reschedule")
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return p, err
}

func (s *Service) RestoreDispatch(ctx context.Context, token string, in DispatchChangeInput) (DispatchPause, error) {
	conn, accountID, actorID, err := s.dispatchChangeLock(ctx, token, in)
	if err != nil {
		return DispatchPause{}, err
	}
	defer unlockDispatch(conn, accountID)
	return s.startDispatchRestore(ctx, conn, token, actorID, false, in)
}

func (s *Service) startDispatchRestore(ctx context.Context, conn *pgxpool.Conn, token, actorID string, worker bool, in DispatchChangeInput) (DispatchPause, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return DispatchPause{}, err
	}
	defer tx.Rollback(ctx)
	if !worker {
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
			return DispatchPause{}, err
		}
	}
	p, err := readDispatch(ctx, tx, in.ID)
	if err != nil {
		return DispatchPause{}, err
	}
	if p.Revision != in.Revision || p.State != "paused" || worker && p.RestoreAt.After(time.Now()) {
		return DispatchPause{}, auth.ErrConflict
	}
	p.State, p.Reason = "restoring", "RESTORE_PENDING"
	p, err = saveDispatch(ctx, tx, p, actorID, "restore")
	if err == nil {
		if worker {
			err = tx.Commit(ctx)
		} else {
			err = s.finish(ctx, tx, token, true)
		}
	}
	if err != nil {
		return DispatchPause{}, err
	}
	return s.executeDispatch(ctx, conn, token, actorID, worker, p)
}

func (s *Service) ReviewDispatch(ctx context.Context, token string, in DispatchReviewInput) (DispatchPause, error) {
	if !in.AcknowledgeResultUnknown {
		return DispatchPause{}, auth.ErrInput
	}
	conn, accountID, actorID, err := s.dispatchChangeLock(ctx, token, in.DispatchChangeInput)
	if err != nil {
		return DispatchPause{}, err
	}
	defer unlockDispatch(conn, accountID)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return DispatchPause{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return DispatchPause{}, err
	}
	p, err := readDispatch(ctx, tx, in.ID)
	if err != nil {
		return DispatchPause{}, err
	}
	if p.Revision != in.Revision || p.State != "needs_review" {
		return DispatchPause{}, auth.ErrConflict
	}
	p.State, p.Reason = "reviewed", "MANUALLY_REVIEWED"
	p, err = saveDispatch(ctx, tx, p, actorID, "review")
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return p, err
}

func (s *Service) GetDispatchPause(ctx context.Context, token, id string) (DispatchPause, error) {
	if !validRevision(id) {
		return DispatchPause{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DispatchPause{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "dispatch")
	if err != nil {
		return DispatchPause{}, err
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"account_id"}, nil, 2)
	if err != nil {
		return DispatchPause{}, err
	}
	p, err := scanDispatch(tx.QueryRow(ctx, `SELECT `+dispatchColumns+` FROM dispatch_pauses WHERE id=$1 AND (`+scope+`)`, append([]any{id}, args...)...))
	if err == nil {
		err = s.finishDispatchRead(ctx, tx, token)
	}
	return p, err
}

type DispatchHistoryInput struct {
	AccountID string `json:"account_id"`
	After     string `json:"after"`
	Limit     int    `json:"limit"`
}

type DispatchEvent struct {
	ID         string        `json:"id"`
	Actor      string        `json:"actor"`
	Action     string        `json:"action"`
	OccurredAt time.Time     `json:"occurred_at"`
	Pause      DispatchPause `json:"pause"`
}

type DispatchHistory struct {
	Events            []DispatchEvent `json:"events"`
	NextAfter         *string         `json:"next_after"`
	AuthorityRevision string          `json:"authority_revision"`
}

func (s *Service) ListDispatchHistory(ctx context.Context, token string, in DispatchHistoryInput) (DispatchHistory, error) {
	if in.AccountID != "" && !managedID(in.AccountID) || in.After != "" && !validRevision(in.After) || in.Limit < 1 || in.Limit > 100 {
		return DispatchHistory{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DispatchHistory{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "dispatch")
	if err == nil && in.AccountID != "" {
		err = access.CheckAccount(in.AccountID)
	}
	if err != nil {
		return DispatchHistory{}, err
	}
	if in.AccountID != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE id=$1)`, in.AccountID).Scan(&exists); err != nil {
			return DispatchHistory{}, err
		}
		if !exists {
			return DispatchHistory{}, auth.ErrNotFound
		}
	}
	after := in.After
	if after == "" {
		after = "0"
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"account_id"}, nil, 4)
	if err != nil {
		return DispatchHistory{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,actor,action,occurred_at,snapshot FROM dispatch_events WHERE ($1='' OR account_id=$1) AND id>$2 AND (`+scope+`) ORDER BY dispatch_events.id LIMIT $3`, append([]any{in.AccountID, after, in.Limit + 1}, args...)...)
	if err != nil {
		return DispatchHistory{}, err
	}
	out := DispatchHistory{Events: []DispatchEvent{}, AuthorityRevision: access.User.Revision}
	for rows.Next() {
		var event DispatchEvent
		var body []byte
		if err = rows.Scan(&event.ID, &event.Actor, &event.Action, &event.OccurredAt, &body); err == nil {
			err = json.Unmarshal(body, &event.Pause)
		}
		if err != nil {
			rows.Close()
			return DispatchHistory{}, err
		}
		event.OccurredAt, event.Pause = event.OccurredAt.UTC(), dispatchUTC(event.Pause)
		out.Events = append(out.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DispatchHistory{}, err
	}
	if len(out.Events) > in.Limit {
		out.Events = out.Events[:in.Limit]
		last := out.Events[len(out.Events)-1].ID
		out.NextAfter = &last
	}
	return out, s.finishDispatchRead(ctx, tx, token)
}

// Intents survive crashes, but are never replayed: the native result may already have taken effect.
func (s *Service) recoverDispatchIntents(ctx context.Context, startup bool) error {
	rows, err := s.pool.Query(ctx, `SELECT id::text,account_id FROM dispatch_pauses WHERE state IN ('pausing','restoring') AND ($1 OR updated_at<now()-interval '30 seconds') ORDER BY dispatch_pauses.id LIMIT 100`, startup)
	if err != nil {
		return err
	}
	type candidate struct{ id, account string }
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.id, &item.account); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		conn, lockErr := s.lockDispatch(ctx, item.account)
		if lockErr == auth.ErrConflict {
			continue
		}
		if lockErr != nil {
			return lockErr
		}
		err = func() error {
			defer unlockDispatch(conn, item.account)
			tx, e := conn.Begin(ctx)
			if e != nil {
				return e
			}
			defer tx.Rollback(ctx)
			p, e := readDispatch(ctx, tx, item.id)
			if e != nil {
				return e
			}
			if p.State != "pausing" && p.State != "restoring" {
				return nil
			}
			p.State, p.Reason = "needs_review", "PROCESS_INTERRUPTED_RESULT_UNKNOWN"
			_, e = saveDispatch(ctx, tx, p, "", "interrupted")
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

func (s *Service) processDispatchDue(ctx context.Context) (bool, error) {
	if err := s.recoverDispatchIntents(ctx, false); err != nil {
		return false, err
	}
	var id, revision, accountID string
	err := s.pool.QueryRow(ctx, `SELECT id::text,revision::text,account_id FROM dispatch_pauses WHERE state='paused' AND restore_at<=now() AND (reason<>'READ_RETRY_PENDING' OR updated_at<now()-interval '10 seconds') ORDER BY restore_at,dispatch_pauses.id LIMIT 1`).Scan(&id, &revision, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	conn, err := s.lockDispatch(ctx, accountID)
	if err == auth.ErrConflict {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unlockDispatch(conn, accountID)
	workCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	_, err = s.startDispatchRestore(workCtx, conn, "", "", true, DispatchChangeInput{ID: id, Revision: revision})
	if err == auth.ErrConflict {
		return false, nil
	}
	return true, err
}
