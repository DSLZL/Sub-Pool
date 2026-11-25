package connection

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type ObservationUsageCapture struct {
	ID          string       `json:"id"`
	CapturedAt  time.Time    `json:"captured_at"`
	State       string       `json:"state"`
	Summary     UsageSummary `json:"summary"`
	Limitations []string     `json:"limitations"`
}

type ObservationUsageWindow struct {
	Kind    string                   `json:"kind"`
	StartAt *time.Time               `json:"start_at"`
	EndAt   *time.Time               `json:"end_at"`
	Reason  string                   `json:"reason"`
	Capture *ObservationUsageCapture `json:"capture"`
}

type ObservationUsageEvidence struct {
	ObservationID      string                   `json:"observation_id"`
	AccountID          string                   `json:"account_id"`
	Scope              string                   `json:"scope"`
	Basis              string                   `json:"basis"`
	Currency           string                   `json:"currency"`
	TimestampBasis     string                   `json:"timestamp_basis"`
	Complete           bool                     `json:"complete"`
	SettlementEligible bool                     `json:"settlement_eligible"`
	Windows            []ObservationUsageWindow `json:"windows"`
}

func observationUsageBounds(snapshot QuotaSnapshot, window QuotaWindow) (time.Time, time.Time, bool) {
	if snapshot.SourceAt == nil || snapshot.SourceAt.After(snapshot.ReceivedAt) || window.ResetAt == nil || window.DurationSeconds == nil {
		return time.Time{}, time.Time{}, false
	}
	seconds, err := strconv.ParseInt(*window.DurationSeconds, 10, 64)
	if err != nil || seconds <= 0 || seconds > 31*24*60*60 {
		return time.Time{}, time.Time{}, false
	}
	start, end := window.ResetAt.Add(-time.Duration(seconds)*time.Second).UTC(), snapshot.SourceAt.UTC()
	_, _, valid := usageRange(start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	return start, end, valid && end.Before(*window.ResetAt)
}

func (s *Service) GetObservationUsageEvidence(ctx context.Context, token, id string) (ObservationUsageEvidence, error) {
	if !validRevision(id) {
		return ObservationUsageEvidence{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationUsageEvidence{}, err
	}
	defer tx.Rollback(ctx)
	observations, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err != nil {
		return ObservationUsageEvidence{}, err
	}
	usage, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err != nil {
		return ObservationUsageEvidence{}, err
	}
	item, err := readObservation(ctx, tx, id, "", observations)
	if err != nil {
		return ObservationUsageEvidence{}, err
	}
	if err = usage.CheckAccount(item.AccountID); err != nil {
		return ObservationUsageEvidence{}, err
	}
	result, err := projectObservationUsage(ctx, tx, item, usage)
	if err != nil {
		return ObservationUsageEvidence{}, err
	}
	for _, feature := range []string{"observations", "usage"} {
		access, e := s.auth.AuthorizeRead(ctx, tx, token, feature)
		if e != nil {
			return ObservationUsageEvidence{}, e
		}
		if e = access.CheckAccount(item.AccountID); e != nil {
			return ObservationUsageEvidence{}, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ObservationUsageEvidence{}, err
	}
	return result, nil
}

func projectObservationUsage(ctx context.Context, tx pgx.Tx, item ObservationRecord, usage auth.Access) (ObservationUsageEvidence, error) {
	result := ObservationUsageEvidence{ObservationID: item.ID, AccountID: item.AccountID, Scope: "authorized_participants", Basis: "native_window_interval", Currency: "USD", TimestampBasis: "native_created_at", Windows: []ObservationUsageWindow{}}
	if usage.AllData {
		result.Scope = "account"
	}
	for _, window := range item.Snapshot.Windows {
		entry := ObservationUsageWindow{Kind: window.Kind, Reason: "NATIVE_WINDOW_UNVERIFIED"}
		start, end, valid := observationUsageBounds(item.Snapshot, window)
		if valid {
			entry.StartAt, entry.EndAt, entry.Reason = &start, &end, "NO_RECORDED_WINDOW_CAPTURE"
			var err error
			entry.Capture, err = readObservationUsage(ctx, tx, item, usage, start, end)
			if err != nil {
				return ObservationUsageEvidence{}, err
			}
			if entry.Capture != nil {
				entry.Reason = "RECORDED_WINDOW_USAGE"
			}
		}
		result.Windows = append(result.Windows, entry)
	}
	return result, nil
}

func readObservationUsage(ctx context.Context, tx pgx.Tx, item ObservationRecord, access auth.Access, start, end time.Time) (*ObservationUsageCapture, error) {
	scope, args, err := visibleCaptureSQL(access, 7)
	if err != nil {
		return nil, err
	}
	args = append([]any{item.AccountID, item.Snapshot.SourceID, item.Snapshot.ConnectionRevision, item.Snapshot.AccountRevision, ceilMicrosecond(start), end.Truncate(time.Microsecond)}, args...)
	// SQL bounds only shortlist; decoded timestamps retain the native nanosecond interval.
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.snapshot FROM usage_captures c WHERE c.account_id=$1
 AND c.snapshot->>'source_id'=$2 AND c.snapshot->>'connection_revision'=$3 AND c.snapshot->>'account_revision'=$4
 AND (c.snapshot->>'start_at')::timestamptz<=$5 AND (c.snapshot->>'end_at')::timestamptz>=$6 AND `+scope+` ORDER BY c.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	var capture UsageCapture
	found := false
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&capture.ID, &body); err == nil {
			id := capture.ID
			err = json.Unmarshal(body, &capture)
			capture.ID = id
		}
		if err != nil {
			break
		}
		if !capture.StartAt.After(start) && !capture.EndAt.Before(end) {
			found = true
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || !found {
		return nil, err
	}
	scope, args, err = access.ScopeSQL(pgx.Identifier{"c", "account_id"}, pgx.Identifier{"a", "participant_id"}, 2)
	if err != nil {
		return nil, err
	}
	args = append([]any{capture.ID}, args...)
	rows, err = tx.Query(ctx, `SELECT f.fact FROM usage_capture_facts f JOIN usage_captures c ON c.id=f.capture_id
 LEFT JOIN usage_attributions a ON a.capture_id=f.capture_id AND a.upstream_id=f.upstream_id WHERE c.id=$1 AND NOT c.staging AND `+scope, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := []UsageFact{}
	for rows.Next() {
		var body []byte
		var fact UsageFact
		if err = rows.Scan(&body); err == nil {
			err = json.Unmarshal(body, &fact)
		}
		if err != nil {
			return nil, err
		}
		if !fact.CreatedAt.Before(start) && fact.CreatedAt.Before(end) {
			facts = append(facts, fact)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	limitations := []string{"RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_SELECTED_CAPTURE", "QUOTA_CAUSALITY_UNVERIFIED"}
	for _, reason := range capture.Limitations {
		if reason != "PARTICIPANT_MAPPING_UNAVAILABLE" && !slices.Contains(limitations, reason) {
			limitations = append(limitations, reason)
		}
	}
	if !access.AllData {
		limitations = append(limitations, "ONLY_AUTHORIZED_PARTICIPANTS")
	}
	return &ObservationUsageCapture{ID: capture.ID, CapturedAt: capture.CapturedAt.UTC(), State: capture.State, Summary: summarizeUsage(facts), Limitations: limitations}, nil
}
