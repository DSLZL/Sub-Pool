package connection

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type ObservationRecord struct {
	ID                 string        `json:"id"`
	JobID              string        `json:"job_id"`
	AccountID          string        `json:"account_id"`
	Snapshot           QuotaSnapshot `json:"snapshot"`
	ManagementRevision string        `json:"management_revision"`
	Excluded           bool          `json:"excluded"`
	ManualStart        bool          `json:"manual_start"`
	ReviewReason       *string       `json:"review_reason"`
	ReviewedAt         *time.Time    `json:"reviewed_at"`
}

type ObservationQuery struct {
	AccountID  string `json:"account_id"`
	SourceID   string `json:"source_id"`
	SourceMode string `json:"source_mode"`
	Validity   string `json:"validity"`
	StartAt    string `json:"start_at"`
	EndAt      string `json:"end_at"`
	After      string `json:"after"`
	Limit      int    `json:"limit"`
}

type ObservationPage struct {
	Records      []ObservationRecord `json:"records"`
	NextAfter    *string             `json:"next_after"`
	QueryStartAt time.Time           `json:"query_start_at"`
	QueryEndAt   time.Time           `json:"query_end_at"`
	TimeBasis    string              `json:"time_basis"`
}

type ObservationUsagePage struct {
	ObservationPage
	UsageEvidence []ObservationUsageEvidence `json:"usage_evidence"`
}

const observationColumns = `q.id::text,q.job_id::text,q.account_id,q.snapshot,COALESCE(m.revision,1)::text,COALESCE(v.action='exclude',false),COALESCE(s.action='start' AND s.start_observation_id=q.id,false),v.reason,v.occurred_at`
const observationTables = `quota_observations q LEFT JOIN observation_management m ON m.account_id=q.account_id
 LEFT JOIN LATERAL (SELECT action,reason,occurred_at FROM observation_changes WHERE observation_id=q.id ORDER BY observation_changes.id DESC LIMIT 1) v ON true
 LEFT JOIN LATERAL (SELECT action,start_observation_id FROM observation_changes WHERE account_id=q.account_id AND action IN ('start','revoke') ORDER BY observation_changes.id DESC LIMIT 1) s ON true`

func scanObservation(row pgx.Row) (ObservationRecord, error) {
	var item ObservationRecord
	var body []byte
	err := row.Scan(&item.ID, &item.JobID, &item.AccountID, &body, &item.ManagementRevision, &item.Excluded, &item.ManualStart, &item.ReviewReason, &item.ReviewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, auth.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(body, &item.Snapshot)
	}
	item.ReviewedAt = utc(item.ReviewedAt)
	return item, err
}

var observationTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z$`)

func observationRange(from, until string) (time.Time, time.Time, bool) {
	start, e1 := time.Parse(time.RFC3339Nano, from)
	end, e2 := time.Parse(time.RFC3339Nano, until)
	return start, end, e1 == nil && e2 == nil && observationTimestamp.MatchString(from) && observationTimestamp.MatchString(until) && start.Year() >= 1970 && end.Year() < 9999 && end.After(start)
}

func (s *Service) finishObservationRead(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeRead(ctx, tx, token, "observations"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ListObservations(ctx context.Context, token string, input ObservationQuery) (ObservationPage, error) {
	result, err := s.listObservations(ctx, token, input, false)
	return result.ObservationPage, err
}

func (s *Service) ListObservationUsage(ctx context.Context, token string, input ObservationQuery) (ObservationUsagePage, error) {
	return s.listObservations(ctx, token, input, true)
}

func (s *Service) listObservations(ctx context.Context, token string, input ObservationQuery, withUsage bool) (ObservationUsagePage, error) {
	start, end, valid := observationRange(input.StartAt, input.EndAt)
	if !valid || input.AccountID != "" && !managedID(input.AccountID) || input.SourceID != "" && !managedID(input.SourceID) || input.SourceMode != "all" && input.SourceMode != "active" && input.SourceMode != "passive" || input.Validity != "all" && input.Validity != "included" && input.Validity != "excluded" && input.Validity != "manual_start" || !validUsageAfter(input.After) || input.Limit < 1 || input.Limit > 100 {
		return ObservationUsagePage{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationUsagePage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err != nil {
		return ObservationUsagePage{}, err
	}
	var usage auth.Access
	if withUsage {
		usage, err = s.auth.AuthorizeRead(ctx, tx, token, "usage")
		if err != nil {
			return ObservationUsagePage{}, err
		}
	}
	if input.AccountID != "" {
		if err = access.CheckAccount(input.AccountID); err != nil {
			return ObservationUsagePage{}, err
		}
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"q", "account_id"}, nil, 9)
	if err != nil {
		return ObservationUsagePage{}, err
	}
	args = append([]any{input.AccountID, input.SourceID, input.SourceMode, input.Validity, ceilMicrosecond(start), ceilMicrosecond(end), input.After, input.Limit + 1}, args...)
	rows, err := tx.Query(ctx, `SELECT `+observationColumns+` FROM `+observationTables+` WHERE `+scope+`
 AND ($1='' OR q.account_id=$1) AND ($2='' OR q.snapshot->>'source_id'=$2)
	 AND ($3='all' OR q.snapshot->>'source_mode'=$3)
	 AND ($4='all' OR $4='included' AND NOT COALESCE(v.action='exclude',false) OR $4='excluded' AND COALESCE(v.action='exclude',false)
	 OR $4='manual_start' AND COALESCE(s.action='start' AND s.start_observation_id=q.id,false))
 AND q.received_at>=$5 AND q.received_at<$6 AND q.id>$7::bigint ORDER BY q.id LIMIT $8`, args...)
	if err != nil {
		return ObservationUsagePage{}, err
	}
	result := ObservationUsagePage{ObservationPage: ObservationPage{Records: []ObservationRecord{}, QueryStartAt: start, QueryEndAt: end, TimeBasis: "received_at"}, UsageEvidence: []ObservationUsageEvidence{}}
	for rows.Next() {
		item, e := scanObservation(rows)
		if e != nil {
			err = e
			break
		}
		result.Records = append(result.Records, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return ObservationUsagePage{}, err
	}
	if len(result.Records) > input.Limit {
		result.Records = result.Records[:input.Limit]
		result.NextAfter = &result.Records[input.Limit-1].ID
	}
	if withUsage {
		for _, item := range result.Records {
			if err = usage.CheckAccount(item.AccountID); err != nil {
				return ObservationUsagePage{}, err
			}
			evidence, e := projectObservationUsage(ctx, tx, item, usage)
			if e != nil {
				return ObservationUsagePage{}, e
			}
			result.UsageEvidence = append(result.UsageEvidence, evidence)
		}
		if _, err = s.auth.AuthorizeRead(ctx, tx, token, "usage"); err != nil {
			return ObservationUsagePage{}, err
		}
	}
	if err = s.finishObservationRead(ctx, tx, token); err != nil {
		return ObservationUsagePage{}, err
	}
	return result, nil
}

func readObservation(ctx context.Context, tx pgx.Tx, id, account string, access auth.Access) (ObservationRecord, error) {
	scope, args, err := access.ScopeSQL(pgx.Identifier{"q", "account_id"}, nil, 3)
	if err != nil {
		return ObservationRecord{}, err
	}
	args = append([]any{id, account}, args...)
	return scanObservation(tx.QueryRow(ctx, `SELECT `+observationColumns+` FROM `+observationTables+` WHERE q.id=$1 AND ($2='' OR q.account_id=$2) AND `+scope, args...))
}

type ObservationMember struct {
	ParticipantID string `json:"participant_id"`
	Name          string `json:"name"`
	SharePercent  string `json:"share_percent"`
}

type ObservationBasis struct {
	ContractID         *string             `json:"contract_id"`
	Members            []ObservationMember `json:"members"`
	UnallocatedPercent *string             `json:"unallocated_percent"`
	TimeBasis          string              `json:"time_basis"`
	UsageState         string              `json:"usage_state"`
	EstimateState      string              `json:"estimate_state"`
}

type ObservationDetail struct {
	Observation ObservationRecord `json:"observation"`
	Basis       ObservationBasis  `json:"basis"`
}

func readObservationBasis(ctx context.Context, tx pgx.Tx, item ObservationRecord, access auth.Access) (ObservationBasis, error) {
	result := ObservationBasis{Members: []ObservationMember{}, TimeBasis: "unavailable", UsageState: "unavailable", EstimateState: "unavailable"}
	at := item.Snapshot.SourceAt
	if at == nil || at.After(item.Snapshot.ReceivedAt) {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text FROM pool_contracts c JOIN pool_contract_accounts a ON a.contract_id=c.id
 WHERE a.account_id=$1 AND c.source_id=$2 AND c.effective_at<=$3 AND (c.ended_at IS NULL OR c.ended_at>$3)
 AND c.effective_at<=$4 ORDER BY c.id`, item.AccountID, item.Snapshot.SourceID, *at, item.Snapshot.ReceivedAt)
	if err != nil {
		return result, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) != 1 {
		return result, auth.ErrConflict
	}
	rows, err = tx.Query(ctx, `SELECT participant_id,name,share_units FROM pool_contract_participants
 WHERE contract_id=$1 AND ($2 OR participant_id=ANY($3::text[])) ORDER BY participant_id`, ids[0], access.AllData, access.ParticipantIDs)
	if err != nil {
		return result, err
	}
	var total int64
	for rows.Next() {
		var member ObservationMember
		var units int64
		if err = rows.Scan(&member.ParticipantID, &member.Name, &units); err != nil {
			break
		}
		total += units
		member.SharePercent = sharePercent(units)
		result.Members = append(result.Members, member)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if access.AllData || len(result.Members) > 0 {
		result.ContractID = &ids[0]
		result.TimeBasis = "recorded_local_contract_at_source_time"
	}
	if access.AllData {
		remaining := sharePercent(shareTotal - total)
		result.UnallocatedPercent = &remaining
	}
	return result, nil
}

func (s *Service) GetObservation(ctx context.Context, token, id string) (ObservationDetail, error) {
	if !validRevision(id) {
		return ObservationDetail{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationDetail{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err != nil {
		return ObservationDetail{}, err
	}
	item, err := readObservation(ctx, tx, id, "", access)
	if err != nil {
		return ObservationDetail{}, err
	}
	basis, err := readObservationBasis(ctx, tx, item, access)
	if err != nil {
		return ObservationDetail{}, err
	}
	if err = s.finishObservationRead(ctx, tx, token); err != nil {
		return ObservationDetail{}, err
	}
	return ObservationDetail{Observation: item, Basis: basis}, nil
}

type ObservationEvent struct {
	ID                 string    `json:"id"`
	AccountID          string    `json:"account_id"`
	Revision           string    `json:"revision"`
	Action             string    `json:"action"`
	ObservationID      *string   `json:"observation_id"`
	StartObservationID *string   `json:"start_observation_id"`
	EndObservationID   *string   `json:"end_observation_id"`
	WindowKind         *string   `json:"window_kind"`
	Reason             string    `json:"reason"`
	ActorID            *string   `json:"actor_id"`
	OccurredAt         time.Time `json:"occurred_at"`
}

const observationEventColumns = `id::text,account_id,revision::text,action,observation_id::text,start_observation_id::text,end_observation_id::text,window_kind,reason,actor_id,occurred_at`

func scanObservationEvent(row pgx.Row, admin bool) (ObservationEvent, error) {
	var event ObservationEvent
	err := row.Scan(&event.ID, &event.AccountID, &event.Revision, &event.Action, &event.ObservationID, &event.StartObservationID, &event.EndObservationID, &event.WindowKind, &event.Reason, &event.ActorID, &event.OccurredAt)
	event.OccurredAt = event.OccurredAt.UTC()
	if !admin {
		event.ActorID = nil
	}
	return event, err
}

type ObservationEventPage struct {
	AccountID string             `json:"account_id"`
	Events    []ObservationEvent `json:"events"`
	NextAfter *string            `json:"next_after"`
}

func (s *Service) ListObservationEvents(ctx context.Context, token, account, after string, limit int) (ObservationEventPage, error) {
	if !managedID(account) || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return ObservationEventPage{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationEventPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err == nil {
		err = access.CheckAccount(account)
	}
	if err != nil {
		return ObservationEventPage{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE id=$1)`, account).Scan(&exists); err != nil {
		return ObservationEventPage{}, err
	}
	if !exists {
		return ObservationEventPage{}, auth.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+observationEventColumns+` FROM observation_changes WHERE account_id=$1 AND id>$2::bigint ORDER BY observation_changes.id LIMIT $3`, account, after, limit+1)
	if err != nil {
		return ObservationEventPage{}, err
	}
	result := ObservationEventPage{AccountID: account, Events: []ObservationEvent{}}
	for rows.Next() {
		event, e := scanObservationEvent(rows, access.AllData)
		if e != nil {
			err = e
			break
		}
		result.Events = append(result.Events, event)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return ObservationEventPage{}, err
	}
	if len(result.Events) > limit {
		result.Events = result.Events[:limit]
		result.NextAfter = &result.Events[limit-1].ID
	}
	if err = s.finishObservationRead(ctx, tx, token); err != nil {
		return ObservationEventPage{}, err
	}
	return result, nil
}

func observationReason(reason string) bool {
	return safeText(reason, 512, "") && strings.TrimSpace(reason) == reason && reason != ""
}
