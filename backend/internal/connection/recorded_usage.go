package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type RecordedUsageQuery struct {
	AttributedUsageQuery
	AccountIDs []string `json:"account_ids"`
	Basis      string   `json:"basis"`
}

type RecordedUsageView struct {
	View            *AttributedUsageView `json:"view"`
	Basis           string               `json:"basis"`
	MissingAccounts []string             `json:"missing_accounts"`
}

type recordedCapture struct {
	id       string
	capture  UsageCapture
	complete bool
}

func (s *Service) GetRecordedUsage(ctx context.Context, token string, input RecordedUsageQuery) (RecordedUsageView, error) {
	out := RecordedUsageView{MissingAccounts: []string{}}
	start, end, rangeValid := usageRangeDays(input.StartAt, input.EndAt, 366)
	_, _, cursorValid := attributionCursor(input.After)
	if !rangeValid || !cursorValid || len(input.AccountIDs) < 1 || len(input.AccountIDs) > 20 ||
		input.Limit < 0 || input.Limit > 100 || input.Limit == 0 && input.After != "0" || input.Grain != "day" && input.Grain != "month" ||
		input.ParticipantID != "" && !managedID(input.ParticipantID) || input.Model != "" && !safeText(input.Model, 512, "") ||
		input.APIKeyID != "" && !validRevision(input.APIKeyID) || input.After != "0" && input.Basis == "" {
		return out, auth.ErrInput
	}
	if input.Basis != "" {
		if decoded, err := hex.DecodeString(input.Basis); err != nil || len(decoded) != sha256.Size {
			return out, auth.ErrInput
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, id := range input.AccountIDs {
		if !managedID(id) || seen[id] {
			return out, auth.ErrInput
		}
		seen[id] = true
		if err = access.CheckAccount(id); err != nil {
			return out, err
		}
	}
	if input.ParticipantID != "" {
		if err = access.CheckParticipant(input.ParticipantID); err != nil {
			return out, err
		}
	}
	settings, err := settings(ctx, tx, false)
	if err != nil {
		return out, err
	}
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return out, err
	}
	config, err := s.config(ctx, tx, false)
	if err != nil {
		return out, err
	}
	selections, missing, incomplete, err := recordedUsageSelections(ctx, tx, access, config, input.AccountIDs, start, end)
	if err != nil {
		return out, err
	}
	out.MissingAccounts = missing
	basis, err := recordedUsageBasis(input, config.Revision, access.User.Revision, settings.Revision, selections, missing, incomplete)
	if err != nil {
		return out, err
	}
	out.Basis = basis
	if input.Basis != "" && input.Basis != basis {
		return out, auth.ErrConflict
	}
	if len(selections) > 0 {
		view := AttributedUsageView{
			Currency: "USD", TimestampBasis: "native_created_at", Grain: input.Grain,
			UnassignedVisible: access.AllData, Groups: []AttributedUsageGroup{}, Records: []AttributedUsageRecord{},
			QueryStartAt: &start, QueryEndAt: &end,
			History: &UsageHistory{Basis: "selected_captures", Timezone: settings.Timezone, SettingsRevision: settings.Revision,
				AuthorityRevision: access.User.Revision, Selections: selections},
			Limitations: []string{"RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN",
				"REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"},
		}
		if incomplete {
			view.Limitations = append(view.Limitations, "DAILY_COVERAGE_INCOMPLETE")
		}
		ids := make([]int64, 0, len(selections))
		windows := make(map[string][]UsageSelection, len(selections))
		view.StartAt, view.EndAt = selections[0].StartAt, selections[0].EndAt
		view.OldestCaptureAt, view.NewestCaptureAt = selections[0].CapturedAt, selections[0].CapturedAt
		for _, item := range selections {
			if _, ok := windows[item.ID]; !ok {
				id, err := strconv.ParseInt(item.ID, 10, 64)
				if err != nil {
					return out, err
				}
				ids = append(ids, id)
			}
			windows[item.ID] = append(windows[item.ID], item)
			if item.StartAt.Before(view.StartAt) {
				view.StartAt = item.StartAt
			}
			view.EndAt = maxTime(view.EndAt, item.EndAt)
			if item.CapturedAt.Before(view.OldestCaptureAt) {
				view.OldestCaptureAt = item.CapturedAt
			}
			view.NewestCaptureAt = maxTime(view.NewestCaptureAt, item.CapturedAt)
		}
		view, err = readAttributedUsageFacts(ctx, tx, access, input.AttributedUsageQuery, ids, location, view, windows)
		if err != nil {
			return out, err
		}
		out.View = &view
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "usage"); err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}

func recordedUsageBasis(input RecordedUsageQuery, connection, authority, settings string, selections []UsageSelection, missing []string, incomplete bool) (string, error) {
	input.Basis, input.After, input.Limit = "", "", 0
	material, err := json.Marshal([]any{input, connection, authority, settings, selections, missing, incomplete})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:]), nil
}

func recordedUsageSelections(ctx context.Context, tx pgx.Tx, access auth.Access, config storedConfig,
	accounts []string, start, end time.Time) ([]UsageSelection, []string, bool, error) {
	first, last := start.Truncate(24*time.Hour), end.Add(-time.Nanosecond).Truncate(24*time.Hour)
	daily := map[string]map[string]recordedCapture{}
	if config.SourceID != nil {
		scope, args, err := scopedCaptureSQL(access, 6)
		if err != nil {
			return nil, nil, false, err
		}
		values := append([]any{accounts, *config.SourceID, config.Revision, first, last}, args...)
		rows, err := tx.Query(ctx, `SELECT c.id::text,c.snapshot,j.state='complete' AND j.connection_revision=$3::bigint FROM usage_day_jobs j
			JOIN managed_accounts m ON m.id=j.account_id AND m.resource_fingerprint=j.fingerprint AND m.source_id=j.source_id
			JOIN usage_captures c ON c.id=CASE WHEN j.state<>'blocked' AND j.connection_revision=$3::bigint AND m.observed_connection_revision=$3::bigint
				THEN COALESCE(j.work_capture_id,j.published_capture_id) ELSE j.published_capture_id END
			WHERE j.account_id=ANY($1) AND j.source_id=$2
				AND j.day BETWEEN $4 AND $5 AND `+scope, values...)
		if err != nil {
			return nil, nil, false, err
		}
		for rows.Next() {
			var item recordedCapture
			if err = rows.Scan(&item.id, &item.capture, &item.complete); err != nil {
				rows.Close()
				return nil, nil, false, err
			}
			if daily[item.capture.AccountID] == nil {
				daily[item.capture.AccountID] = map[string]recordedCapture{}
			}
			daily[item.capture.AccountID][item.capture.StartAt.Format(weeklyDate)] = item
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, false, err
		}
	}
	scope, args, err := scopedCaptureSQL(access, 2)
	if err != nil {
		return nil, nil, false, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (c.account_id) c.id::text,c.snapshot
		FROM usage_captures c WHERE c.account_id=ANY($1) AND c.collection_day IS NULL AND NOT c.staging AND `+scope+
		` ORDER BY c.account_id,c.captured_at DESC,c.id DESC`, append([]any{accounts}, args...)...)
	if err != nil {
		return nil, nil, false, err
	}
	legacy := map[string]recordedCapture{}
	for rows.Next() {
		var item recordedCapture
		if err = rows.Scan(&item.id, &item.capture); err != nil {
			rows.Close()
			return nil, nil, false, err
		}
		legacy[item.capture.AccountID] = item
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, false, err
	}
	selections, missing := []UsageSelection{}, []string{}
	incomplete := false
	for _, account := range accounts {
		matched := false
		for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
			item, ok := daily[account][day.Format(weeklyDate)]
			if !ok {
				item = legacy[account]
			}
			from := maxTime(maxTime(start, day), item.capture.StartAt)
			to := day.AddDate(0, 0, 1)
			if end.Before(to) {
				to = end
			}
			if item.capture.EndAt.Before(to) {
				to = item.capture.EndAt
			}
			incomplete = incomplete || !item.complete
			if item.id == "" || !to.After(from) {
				continue
			}
			matched = true
			selections = append(selections, UsageSelection{ID: item.id, AccountID: account, StartAt: from, EndAt: to, CapturedAt: item.capture.CapturedAt})
		}
		if !matched {
			missing = append(missing, account)
		}
	}
	sort.Slice(selections, func(i, j int) bool {
		a, b := selections[i], selections[j]
		if a.ID == b.ID {
			return a.StartAt.Before(b.StartAt)
		}
		return len(a.ID) < len(b.ID) || len(a.ID) == len(b.ID) && a.ID < b.ID
	})
	return selections, missing, incomplete, nil
}
