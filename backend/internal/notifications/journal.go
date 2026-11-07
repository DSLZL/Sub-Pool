package notifications

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

const enqueueLock int64 = 1380991565
const columns = `n.id::text,n.event_key,n.kind,n.subject_type,n.subject_id,n.participant_id,n.source_id,n.connection_revision::text,n.subject_revision::text,n.config_revision::text,n.channel,n.from_address,n.recipient_address,n.subject,n.body,n.event_reason,n.actor_id,n.actor_revision::text,n.actor_digest,n.request_key,n.created_at,n.started_at,n.finished_at,n.state,n.reason,n.provider_id`

type Details struct {
	ConfigRevision string  `json:"config_revision"`
	Channel        string  `json:"channel"`
	From           string  `json:"from"`
	Recipient      string  `json:"recipient"`
	Subject        string  `json:"subject"`
	Body           string  `json:"body"`
	EventReason    string  `json:"event_reason"`
	EventKey       string  `json:"event_key"`
	ActorID        *string `json:"actor_id"`
	ProviderID     *string `json:"provider_id"`
}
type Notification struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	SubjectType   string     `json:"subject_type"`
	SubjectID     string     `json:"subject_id"`
	ParticipantID *string    `json:"participant_id"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	State         string     `json:"state"`
	Reason        string     `json:"reason"`
	Visibility    string     `json:"visibility"`
	Details       *Details   `json:"details"`
}
type storedNotification struct {
	Notification
	details                                                                  Details
	sourceID, connectionRevision, subjectRevision, actorRevision, requestKey *string
	digest                                                                   []byte
}

func scan(row pgx.Row) (storedNotification, error) {
	var n storedNotification
	e := row.Scan(&n.ID, &n.details.EventKey, &n.Kind, &n.SubjectType, &n.SubjectID, &n.ParticipantID, &n.sourceID, &n.connectionRevision, &n.subjectRevision, &n.details.ConfigRevision, &n.details.Channel, &n.details.From, &n.details.Recipient, &n.details.Subject, &n.details.Body, &n.details.EventReason, &n.details.ActorID, &n.actorRevision, &n.digest, &n.requestKey, &n.CreatedAt, &n.StartedAt, &n.FinishedAt, &n.State, &n.Reason, &n.details.ProviderID)
	n.CreatedAt = n.CreatedAt.UTC()
	n.StartedAt = utc(n.StartedAt)
	n.FinishedAt = utc(n.FinishedAt)
	return n, e
}
func project(n storedNotification, admin bool) Notification {
	n.Visibility = "administrator"
	n.Details = &n.details
	if !admin {
		n.Visibility = "subject_summary"
		n.Details = nil
		n.Reason = strings.ToUpper(n.State)
	}
	return n.Notification
}
func recordAudit(ctx context.Context, tx pgx.Tx, n storedNotification, action string) error {
	outcome := "pending"
	switch n.State {
	case "accepted":
		outcome = "succeeded"
	case "failed":
		outcome = "failed"
	case "unknown":
		outcome = "unknown"
	case "skipped":
		outcome = "denied"
	}
	task := "notification:" + n.ID
	entry := audit.Entry{OperationID: task, TaskID: &task, Action: action, Outcome: outcome, TargetType: "notification", TargetID: n.ID, Revision: &n.details.ConfigRevision, SourceID: n.sourceID}
	if action == "notification.request" && n.details.ActorID != nil {
		entry.ActorID = n.details.ActorID
		entry.TaskID = nil
	}
	_, e := audit.Record(ctx, tx, entry)
	return e
}
func insert(ctx context.Context, tx pgx.Tx, n storedNotification, c storedConfig) (storedNotification, error) {
	n.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	if n.State != "queued" {
		n.FinishedAt = &n.CreatedAt
	}
	n.details.ConfigRevision = c.Revision
	n.details.Channel = c.Channel
	n.details.From = c.From
	n.details.Recipient = c.Recipient
	n, e := scan(tx.QueryRow(ctx, `INSERT INTO notifications(event_key,kind,subject_type,subject_id,participant_id,source_id,connection_revision,subject_revision,config_revision,channel,from_address,recipient_address,subject,body,event_reason,actor_id,actor_revision,actor_digest,request_key,created_at,finished_at,state,reason) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23) RETURNING `+strings.ReplaceAll(columns, "n.", ""), n.details.EventKey, n.Kind, n.SubjectType, n.SubjectID, n.ParticipantID, n.sourceID, n.connectionRevision, n.subjectRevision, c.Revision, c.Channel, c.From, c.Recipient, n.details.Subject, n.details.Body, n.details.EventReason, n.details.ActorID, n.actorRevision, n.digest, n.requestKey, n.CreatedAt, n.FinishedAt, n.State, n.Reason))
	if e == nil {
		e = recordAudit(ctx, tx, n, "notification.request")
	}
	return n, e
}

type TestInput struct {
	RequestID string `json:"request_id"`
	Revision  string `json:"revision"`
}

func (s *Service) RequestTest(ctx context.Context, token string, in TestInput) (Notification, error) {
	if !validID(in.RequestID) || !validRevision(in.Revision) {
		return Notification{}, auth.ErrInput
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Notification{}, e
	}
	defer tx.Rollback(ctx)
	actor, e := s.auth.AuthorizeAdmin(ctx, tx, token)
	if e != nil {
		return Notification{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, enqueueLock); e != nil {
		return Notification{}, e
	}
	n, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM notifications n WHERE request_key=$1`, in.RequestID))
	if e == nil {
		if n.details.ActorID == nil || *n.details.ActorID != actor.ID || n.details.ConfigRevision != in.Revision {
			return Notification{}, auth.ErrConflict
		}
	} else {
		if !errors.Is(e, pgx.ErrNoRows) {
			return Notification{}, e
		}
		c, err := readConfig(ctx, tx, false)
		if err != nil {
			return Notification{}, err
		}
		if c.Revision != in.Revision {
			return Notification{}, auth.ErrConflict
		}
		n = storedNotification{Notification: Notification{Kind: "test", SubjectType: "system", SubjectID: "test", State: "queued", Reason: "QUEUED"}, details: Details{EventKey: "test:" + in.RequestID, Subject: "Sub-Pool test notification", Body: "This is an explicitly requested Sub-Pool channel test. Server acceptance does not prove inbox delivery or reading.", EventReason: "EXPLICIT_ADMINISTRATOR_TEST", ActorID: &actor.ID}, actorRevision: &actor.Revision, requestKey: &in.RequestID}
		digest := sha256.Sum256([]byte(token))
		n.digest = digest[:]
		if !ready(c) {
			n.State, n.Reason = "skipped", "CHANNEL_INCOMPLETE"
		}
		if _, e = s.auth.AuthorizeAdmin(ctx, tx, token); e != nil {
			return Notification{}, e
		}
		n, e = insert(ctx, tx, n, c)
	}
	if e == nil {
		_, e = s.auth.AuthorizeAdmin(ctx, tx, token)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return Notification{}, e
	}
	return project(n, true), nil
}

// RecordCollectionFailure shares the actual job-result transaction, so alerts cannot outlive rolled-back failures.
func RecordCollectionFailure(ctx context.Context, tx pgx.Tx, jobID string) error {
	if !validRevision(jobID) {
		return auth.ErrInput
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, enqueueLock); e != nil {
		return e
	}
	var n storedNotification
	var rawReason string
	e := tx.QueryRow(ctx, `SELECT account_id,source_id,connection_revision::text,account_revision::text,reason FROM collection_jobs WHERE id=$1 AND state='failed' AND source_mode='passive'`, jobID).Scan(&n.SubjectID, &n.sourceID, &n.connectionRevision, &n.subjectRevision, &rawReason)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE event_key=$1)`, "collection:"+jobID).Scan(&exists); e != nil || exists {
		return e
	}
	c, e := readConfig(ctx, tx, false)
	if e != nil {
		return e
	}
	n.Kind, n.SubjectType, n.State, n.Reason = "collection_failed", "account", "queued", "QUEUED"
	n.details.EventKey = "collection:" + jobID
	n.details.Subject = "Sub-Pool collection failed"
	n.details.EventReason = "COLLECTION_READ_FAILED"
	switch rawReason {
	case "CONNECTION_UNAVAILABLE", "INVALID_RESPONSE":
		n.details.EventReason = rawReason
	case "UNAUTHORIZED":
		n.details.EventReason = "UPSTREAM_UNAUTHORIZED"
	case "FORBIDDEN":
		n.details.EventReason = "UPSTREAM_FORBIDDEN"
	case "RATE_LIMITED":
		n.details.EventReason = "UPSTREAM_RATE_LIMITED"
	case "TIMEOUT":
		n.details.EventReason = "UPSTREAM_TIMEOUT"
	case "NETWORK_ERROR", "UPSTREAM_ERROR", "ROUTE_UNAVAILABLE":
		n.details.EventReason = "UPSTREAM_UNAVAILABLE"
	}
	n.details.Body = fmt.Sprintf("Collection job %s for local account %s failed (%s). No new observation was recorded by this attempt. This does not establish a permanent account failure.", jobID, n.SubjectID, n.details.EventReason)
	switch {
	case !c.Enabled:
		n.State, n.Reason = "skipped", "CHANNEL_DISABLED"
	case !c.Preferences.CollectionFailed:
		n.State, n.Reason = "skipped", "EVENT_DISABLED"
	case !ready(c):
		n.State, n.Reason = "skipped", "CHANNEL_INCOMPLETE"
	default:
		var duplicate bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE kind=$1 AND subject_type=$2 AND subject_id=$3 AND state IN ('queued','dispatching','accepted','unknown') AND created_at>=clock_timestamp()-make_interval(secs=>$4))`, n.Kind, n.SubjectType, n.SubjectID, c.DedupeSeconds).Scan(&duplicate)
		if e != nil {
			return e
		}
		if duplicate {
			n.State, n.Reason = "skipped", "DUPLICATE_WINDOW"
		}
	}
	_, e = insert(ctx, tx, n, c)
	return e
}

type Query struct {
	StartAt       string `json:"start_at"`
	EndAt         string `json:"end_at"`
	Kind          string `json:"kind"`
	ParticipantID string `json:"participant_id"`
	SubjectType   string `json:"subject_type"`
	SubjectID     string `json:"subject_id"`
	State         string `json:"state"`
	After         string `json:"after"`
	Limit         int    `json:"limit"`
}
type Page struct {
	Notifications []Notification `json:"notifications"`
	NextAfter     *string        `json:"next_after"`
}

func validQuery(q Query) (*time.Time, *time.Time, error) {
	if q.After != "0" && !validRevision(q.After) || q.Limit < 1 || q.Limit > 100 || !slices.Contains([]string{"", "test", "collection_failed"}, q.Kind) || !slices.Contains([]string{"", "system", "account", "participant"}, q.SubjectType) || !slices.Contains([]string{"", "queued", "dispatching", "accepted", "failed", "unknown", "skipped"}, q.State) || q.ParticipantID != "" && !validID(q.ParticipantID) || q.SubjectID != "" && (!validID(q.SubjectID) && !(q.SubjectType == "system" && q.SubjectID == "test")) || q.SubjectID != "" && q.SubjectType == "" {
		return nil, nil, auth.ErrInput
	}
	if q.StartAt == "" && q.EndAt == "" {
		return nil, nil, nil
	}
	start, e := time.Parse(time.RFC3339Nano, q.StartAt)
	end, f := time.Parse(time.RFC3339Nano, q.EndAt)
	if e != nil || f != nil || !strings.HasSuffix(q.StartAt, "Z") || !strings.HasSuffix(q.EndAt, "Z") || !end.After(start) || end.Sub(start) > 31*24*time.Hour {
		return nil, nil, auth.ErrInput
	}
	ceil := func(t time.Time) time.Time {
		v := t.Truncate(time.Microsecond)
		if v.Before(t) {
			v = v.Add(time.Microsecond)
		}
		return v
	}
	a, b := ceil(start), ceil(end)
	return &a, &b, nil
}
func scope(access auth.Access) (string, []any) {
	if access.AllData {
		return "true", nil
	}
	return `(n.kind='collection_failed' AND n.subject_type='account' AND n.subject_id=ANY($1::text[]))`, []any{access.AccountIDs}
}
func (s *Service) Get(ctx context.Context, token, id string) (Notification, error) {
	if !validRevision(id) {
		return Notification{}, auth.ErrInput
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Notification{}, e
	}
	defer tx.Rollback(ctx)
	access, e := s.auth.AuthorizeRead(ctx, tx, token, "notifications")
	if e != nil {
		return Notification{}, e
	}
	predicate, args := scope(access)
	args = append(args, id)
	n, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM notifications n WHERE `+predicate+fmt.Sprintf(` AND n.id=$%d`, len(args)), args...))
	if errors.Is(e, pgx.ErrNoRows) {
		e = auth.ErrNotFound
	}
	if e == nil {
		_, e = s.auth.AuthorizeRead(ctx, tx, token, "notifications")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return Notification{}, e
	}
	return project(n, access.AllData), nil
}
func (s *Service) List(ctx context.Context, token string, q Query) (Page, error) {
	start, end, e := validQuery(q)
	if e != nil {
		return Page{}, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Page{}, e
	}
	defer tx.Rollback(ctx)
	access, e := s.auth.AuthorizeRead(ctx, tx, token, "notifications")
	if e != nil {
		return Page{}, e
	}
	if !access.AllData && (q.ParticipantID != "" && !slices.Contains(access.ParticipantIDs, q.ParticipantID) || q.SubjectType == "account" && q.SubjectID != "" && !slices.Contains(access.AccountIDs, q.SubjectID) || q.SubjectType == "participant" && q.SubjectID != "" && !slices.Contains(access.ParticipantIDs, q.SubjectID)) {
		return Page{}, auth.ErrNotFound
	}
	predicate, args := scope(access)
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	afterParam, startParam, endParam := bind(q.After), bind(start), bind(end)
	predicate += " AND n.id>" + afterParam + " AND (" + startParam + "::timestamptz IS NULL OR n.created_at>=" + startParam + "::timestamptz) AND (" + endParam + "::timestamptz IS NULL OR n.created_at<" + endParam + "::timestamptz)"
	for _, f := range []struct{ column, value string }{{"kind", q.Kind}, {"participant_id", q.ParticipantID}, {"subject_type", q.SubjectType}, {"subject_id", q.SubjectID}, {"state", q.State}} {
		if f.value != "" {
			predicate += " AND n." + f.column + "=" + bind(f.value)
		}
	}
	rows, e := tx.Query(ctx, `SELECT `+columns+` FROM notifications n WHERE `+predicate+` ORDER BY n.id LIMIT `+bind(q.Limit+1), args...)
	if e != nil {
		return Page{}, e
	}
	page := Page{Notifications: []Notification{}}
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			e = err
			break
		}
		page.Notifications = append(page.Notifications, project(n, access.AllData))
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e == nil && len(page.Notifications) > q.Limit {
		page.Notifications = page.Notifications[:q.Limit]
		id := page.Notifications[q.Limit-1].ID
		page.NextAfter = &id
	}
	if e == nil {
		_, e = s.auth.AuthorizeRead(ctx, tx, token, "notifications")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return Page{}, e
	}
	return page, nil
}
