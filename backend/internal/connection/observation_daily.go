package connection

import (
	"context"
	"sort"
	"strconv"
	"time"

	"sub-pool/backend/internal/auth"
)

type DailyObservationQuery struct {
	AccountID        string `json:"account_id"`
	StartAt          string `json:"start_at"`
	EndAt            string `json:"end_at"`
	SettingsRevision string `json:"settings_revision"`
}

type DailyObservation struct {
	Day          string                   `json:"day"`
	ReceiptCount string                   `json:"receipt_count"`
	Observation  ObservationRecord        `json:"observation"`
	Usage        ObservationUsageEvidence `json:"usage"`
}

type DailyObservationHistory struct {
	RulesVersion       string             `json:"rules_version"`
	AccountID          string             `json:"account_id"`
	Timezone           string             `json:"timezone"`
	SettingsRevision   string             `json:"settings_revision"`
	AuthorityRevision  string             `json:"authority_revision"`
	QueryStartAt       time.Time          `json:"query_start_at"`
	QueryEndAt         time.Time          `json:"query_end_at"`
	TimeBasis          string             `json:"time_basis"`
	Complete           bool               `json:"complete"`
	SettlementEligible bool               `json:"settlement_eligible"`
	Records            []DailyObservation `json:"records"`
}

func (s *Service) GetDailyObservationHistory(ctx context.Context, token string, input DailyObservationQuery) (DailyObservationHistory, error) {
	start, end, valid := observationRange(input.StartAt, input.EndAt)
	if !valid || end.Sub(start) > 31*24*time.Hour || !managedID(input.AccountID) || !validRevision(input.SettingsRevision) {
		return DailyObservationHistory{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DailyObservationHistory{}, err
	}
	defer tx.Rollback(ctx)
	observations, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err == nil {
		err = observations.CheckAccount(input.AccountID)
	}
	if err != nil {
		return DailyObservationHistory{}, err
	}
	usage, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err == nil {
		err = usage.CheckAccount(input.AccountID)
	}
	if err != nil {
		return DailyObservationHistory{}, err
	}
	system, err := settings(ctx, tx, false)
	if err != nil {
		return DailyObservationHistory{}, err
	}
	if system.Revision != input.SettingsRevision {
		return DailyObservationHistory{}, auth.ErrConflict
	}
	location, err := time.LoadLocation(system.Timezone)
	if err != nil {
		return DailyObservationHistory{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE id=$1)`, input.AccountID).Scan(&exists); err != nil {
		return DailyObservationHistory{}, err
	}
	if !exists {
		return DailyObservationHistory{}, auth.ErrNotFound
	}
	// Receipt dates are descriptive; late or repeated source timestamps never become a financial close.
	rows, err := tx.Query(ctx, `SELECT q.id::text,q.received_at FROM quota_observations q
 WHERE q.account_id=$1 AND q.received_at>=$2 AND q.received_at<$3 ORDER BY q.received_at,q.id LIMIT 50001`, input.AccountID, ceilMicrosecond(start), ceilMicrosecond(end))
	if err != nil {
		return DailyObservationHistory{}, err
	}
	type selected struct {
		id    string
		at    time.Time
		count int
	}
	items := map[string]selected{}
	total := 0
	for rows.Next() {
		var id string
		var received time.Time
		if err = rows.Scan(&id, &received); err != nil {
			break
		}
		day := received.In(location).Format("2006-01-02")
		items[day] = selected{id: id, at: received, count: items[day].count + 1}
		total++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return DailyObservationHistory{}, err
	}
	// shortcut: Bound one request to 50,000 receipts; add a persisted projection for denser histories.
	if total > 50000 || len(items) > 64 {
		return DailyObservationHistory{}, auth.ErrConflict
	}
	result := DailyObservationHistory{RulesVersion: "recorded-day-last-v1", AccountID: input.AccountID, Timezone: system.Timezone, SettingsRevision: system.Revision, AuthorityRevision: observations.User.Revision,
		QueryStartAt: start, QueryEndAt: end, TimeBasis: "received_at", Records: []DailyObservation{}}
	days := make([]string, 0, len(items))
	for day := range items {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		selected := items[day]
		item, e := readObservation(ctx, tx, selected.id, input.AccountID, observations)
		if e != nil {
			return DailyObservationHistory{}, e
		}
		if !item.Snapshot.ReceivedAt.Equal(selected.at) {
			return DailyObservationHistory{}, auth.ErrConflict
		}
		evidence, e := projectObservationUsage(ctx, tx, item, usage)
		if e != nil {
			return DailyObservationHistory{}, e
		}
		result.Records = append(result.Records, DailyObservation{Day: day, ReceiptCount: strconv.Itoa(selected.count), Observation: item, Usage: evidence})
	}
	for _, feature := range []string{"observations", "usage"} {
		access, e := s.auth.AuthorizeRead(ctx, tx, token, feature)
		if e != nil {
			return DailyObservationHistory{}, e
		}
		if e = access.CheckAccount(input.AccountID); e != nil {
			return DailyObservationHistory{}, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return DailyObservationHistory{}, err
	}
	return result, nil
}
