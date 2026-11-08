package connection

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type UsageAttribution struct {
	ParticipantID   *string `json:"participant_id"`
	BindingID       *string `json:"binding_id"`
	ParticipantName *string `json:"participant_name"`
	Basis           string  `json:"basis"`
}

type recordedBinding struct {
	id, participant, user, name string
	start                       time.Time
	end                         *time.Time
}

func attributeUsage(ctx context.Context, tx pgx.Tx, captureID, sourceID string, facts []UsageFact) error {
	users, seen := []int64{}, map[string]bool{}
	for _, f := range facts {
		if f.UserID != nil && !seen[*f.UserID] {
			n, _ := strconv.ParseInt(*f.UserID, 10, 64)
			users, seen[*f.UserID] = append(users, n), true
		}
	}
	bindings := map[string][]recordedBinding{}
	if len(users) > 0 {
		rows, err := tx.Query(ctx, `SELECT b.id::text,b.participant_id,b.upstream_user_id::text,p.name,b.started_at,b.ended_at FROM participant_bindings b JOIN participants p ON p.id=b.participant_id WHERE b.source_id=$1 AND b.upstream_user_id=ANY($2::bigint[]) ORDER BY b.id FOR SHARE OF b`, sourceID, users)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b recordedBinding
			if err = rows.Scan(&b.id, &b.participant, &b.user, &b.name, &b.start, &b.end); err != nil {
				rows.Close()
				return err
			}
			bindings[b.user] = append(bindings[b.user], b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	type row struct {
		UsageAttribution
		UpstreamID string `json:"upstream_id"`
	}
	items := []row{}
	for _, f := range facts {
		a := row{UpstreamID: f.ID, UsageAttribution: UsageAttribution{Basis: "missing_user_id"}}
		if f.UserID != nil {
			a.Basis = "outside_recorded_binding"
			for _, b := range bindings[*f.UserID] {
				if !f.CreatedAt.Before(b.start) && (b.end == nil || f.CreatedAt.Before(*b.end)) {
					if a.ParticipantID != nil {
						return auth.ErrConflict
					}
					participant, binding, name := b.participant, b.id, b.name
					a.ParticipantID, a.BindingID, a.ParticipantName, a.Basis = &participant, &binding, &name, "local_binding_at_log_time"
				}
			}
		}
		items = append(items, a)
	}
	body, err := json.Marshal(items)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO usage_attributions(capture_id,upstream_id,participant_id,binding_id,participant_name,basis)
			SELECT $1,(v->>'upstream_id')::bigint,v->>'participant_id',(v->>'binding_id')::bigint,v->>'participant_name',v->>'basis' FROM jsonb_array_elements($2::jsonb) v`, captureID, body)
	}
	return err
}

type UsageSelection struct {
	ID         string    `json:"id"`
	AccountID  string    `json:"account_id"`
	StartAt    time.Time `json:"start_at"`
	EndAt      time.Time `json:"end_at"`
	CapturedAt time.Time `json:"captured_at"`
}

type UsageSelectionPage struct {
	Captures  []UsageSelection `json:"captures"`
	NextAfter *string          `json:"next_after"`
}

func visibleCaptureSQL(access auth.Access, position int) (string, []any, error) {
	if access.AllData {
		return "true", nil, nil
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"c", "account_id"}, pgx.Identifier{"a", "participant_id"}, position)
	return `EXISTS(SELECT 1 FROM usage_attributions a WHERE a.capture_id=c.id AND ` + scope + `)`, args, err
}

func (s *Service) ListAttributedCaptures(ctx context.Context, token, account, after string, limit int) (UsageSelectionPage, error) {
	if !managedID(account) || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return UsageSelectionPage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return UsageSelectionPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err == nil {
		err = access.CheckAccount(account)
	}
	if err != nil {
		return UsageSelectionPage{}, err
	}
	scope, args, err := visibleCaptureSQL(access, 4)
	if err != nil {
		return UsageSelectionPage{}, err
	}
	args = append([]any{account, after, limit + 1}, args...)
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.account_id,c.snapshot->>'start_at',c.snapshot->>'end_at',c.snapshot->>'captured_at' FROM usage_captures c WHERE c.account_id=$1 AND c.id>$2::bigint AND `+scope+` ORDER BY c.id LIMIT $3`, args...)
	if err != nil {
		return UsageSelectionPage{}, err
	}
	result := UsageSelectionPage{Captures: []UsageSelection{}}
	for rows.Next() {
		var item UsageSelection
		var start, end, captured string
		if err = rows.Scan(&item.ID, &item.AccountID, &start, &end, &captured); err == nil {
			item.CapturedAt, err = time.Parse(time.RFC3339Nano, captured)
		}
		if err != nil {
			rows.Close()
			return UsageSelectionPage{}, err
		}
		item.StartAt, item.EndAt, _ = usageRange(start, end)
		result.Captures = append(result.Captures, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return UsageSelectionPage{}, err
	}
	if len(result.Captures) > limit {
		result.Captures = result.Captures[:limit]
		result.NextAfter = &result.Captures[limit-1].ID
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "usage"); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return UsageSelectionPage{}, err
	}
	return result, nil
}

type AttributedUsageQuery struct {
	CaptureIDs    []string `json:"capture_ids"`
	ParticipantID string   `json:"participant_id"`
	Model         string   `json:"model"`
	APIKeyID      string   `json:"api_key_id"`
	Grain         string   `json:"grain"`
	After         string   `json:"after"`
	Limit         int      `json:"limit"`
	StartAt       string   `json:"start_at"`
	EndAt         string   `json:"end_at"`
}

type AttributedUsageRecord struct {
	CaptureID   string           `json:"capture_id"`
	AccountID   string           `json:"account_id"`
	Attribution UsageAttribution `json:"attribution"`
	Fact        UsageFact        `json:"fact"`
}

type AttributedUsageGroup struct {
	AccountID     string  `json:"account_id"`
	ParticipantID *string `json:"participant_id"`
	Basis         string  `json:"basis"`
	UsageSummary
}

type AttributedUsageView struct {
	History            *UsageHistory           `json:"history,omitempty"`
	StartAt            time.Time               `json:"start_at"`
	EndAt              time.Time               `json:"end_at"`
	QueryStartAt       *time.Time              `json:"query_start_at,omitempty"`
	QueryEndAt         *time.Time              `json:"query_end_at,omitempty"`
	OldestCaptureAt    time.Time               `json:"oldest_capture_at"`
	NewestCaptureAt    time.Time               `json:"newest_capture_at"`
	Summary            UsageSummary            `json:"summary"`
	Assigned           UsageSummary            `json:"assigned"`
	Unassigned         *UsageSummary           `json:"unassigned"`
	UnassignedVisible  bool                    `json:"unassigned_visible"`
	Groups             []AttributedUsageGroup  `json:"groups"`
	Grain              string                  `json:"grain"`
	Buckets            []UsageBucket           `json:"buckets"`
	Records            []AttributedUsageRecord `json:"records"`
	NextAfter          *string                 `json:"next_after"`
	Complete           bool                    `json:"complete"`
	SettlementEligible bool                    `json:"settlement_eligible"`
	Currency           string                  `json:"currency"`
	TimestampBasis     string                  `json:"timestamp_basis"`
	Limitations        []string                `json:"limitations"`
}

type UsageHistory struct {
	Basis             string           `json:"basis"`
	Timezone          string           `json:"timezone"`
	SettingsRevision  string           `json:"settings_revision"`
	AuthorityRevision string           `json:"authority_revision"`
	Selections        []UsageSelection `json:"selections"`
}

func attributionCursor(value string) (int64, int64, bool) {
	if value == "0" {
		return 0, 0, true
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || !validRevision(parts[0]) || !validRevision(parts[1]) {
		return 0, 0, false
	}
	capture, _ := strconv.ParseInt(parts[0], 10, 64)
	fact, _ := strconv.ParseInt(parts[1], 10, 64)
	return capture, fact, true
}

func (s *Service) GetAttributedUsage(ctx context.Context, token string, input AttributedUsageQuery) (AttributedUsageView, error) {
	return s.getAttributedUsage(ctx, token, input, false)
}

func (s *Service) GetUsageHistory(ctx context.Context, token string, input AttributedUsageQuery) (AttributedUsageView, error) {
	return s.getAttributedUsage(ctx, token, input, true)
}

func (s *Service) getAttributedUsage(ctx context.Context, token string, input AttributedUsageQuery, historical bool) (AttributedUsageView, error) {
	windowed := input.StartAt != "" || input.EndAt != ""
	start, end, rangeValid := usageRange(input.StartAt, input.EndAt)
	if historical {
		start, end, rangeValid = usageRangeDays(input.StartAt, input.EndAt, 366)
	}
	if (windowed || historical) && !rangeValid {
		return AttributedUsageView{}, auth.ErrInput
	}
	grainValid := input.Grain == "hour" || input.Grain == "day"
	if historical {
		grainValid = input.Grain == "day" || input.Grain == "month"
	}
	afterCapture, afterFact, valid := attributionCursor(input.After)
	if !valid || input.Limit < 1 || input.Limit > 100 || len(input.CaptureIDs) < 1 || len(input.CaptureIDs) > 20 || input.ParticipantID != "" && !managedID(input.ParticipantID) || input.Model != "" && !safeText(input.Model, 512, "") || input.APIKeyID != "" && !validRevision(input.APIKeyID) || !grainValid {
		return AttributedUsageView{}, auth.ErrInput
	}
	ids, seen := []int64{}, map[string]bool{}
	for _, id := range input.CaptureIDs {
		if !validRevision(id) || seen[id] {
			return AttributedUsageView{}, auth.ErrInput
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		ids, seen[id] = append(ids, n), true
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return AttributedUsageView{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err == nil && input.ParticipantID != "" {
		err = access.CheckParticipant(input.ParticipantID)
	}
	if err != nil {
		return AttributedUsageView{}, err
	}
	location := time.UTC
	var system Settings
	if historical {
		system, err = settings(ctx, tx, false)
		if err == nil {
			location, err = time.LoadLocation(system.Timezone)
		}
		if err != nil {
			return AttributedUsageView{}, err
		}
	}
	scope, args, err := visibleCaptureSQL(access, 2)
	if err != nil {
		return AttributedUsageView{}, err
	}
	args = append([]any{ids}, args...)
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.account_id,c.snapshot FROM usage_captures c WHERE c.id=ANY($1::bigint[]) AND `+scope+` ORDER BY c.id`, args...)
	if err != nil {
		return AttributedUsageView{}, err
	}
	result := AttributedUsageView{Currency: "USD", TimestampBasis: "native_created_at", Grain: input.Grain, UnassignedVisible: access.AllData, Groups: []AttributedUsageGroup{}, Records: []AttributedUsageRecord{}, Limitations: []string{"RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "REQUEST_START_TIME_UNAVAILABLE", "ONLY_RECORDED_BINDING_INTERVALS", "ONLY_SELECTED_CAPTURES"}}
	if historical {
		result.History = &UsageHistory{Basis: "selected_captures", Timezone: system.Timezone, SettingsRevision: system.Revision, AuthorityRevision: access.User.Revision, Selections: []UsageSelection{}}
	}
	if windowed {
		result.QueryStartAt, result.QueryEndAt = &start, &end
	}
	accounts, count := map[string]bool{}, 0
	for rows.Next() {
		var id, account string
		var body []byte
		var capture UsageCapture
		if err = rows.Scan(&id, &account, &body); err == nil {
			err = json.Unmarshal(body, &capture)
		}
		if err != nil {
			break
		}
		start, end, ok := usageRange(capture.StartAt.Format(time.RFC3339Nano), capture.EndAt.Format(time.RFC3339Nano))
		if !ok || !historical && (accounts[account] || count > 0 && (!start.Equal(result.StartAt) || !end.Equal(result.EndAt))) {
			err = auth.ErrInput
			break
		}
		if historical {
			for _, previous := range result.History.Selections {
				if previous.AccountID == account && start.Before(previous.EndAt) && end.After(previous.StartAt) {
					err = auth.ErrInput
					break
				}
			}
			if err != nil {
				break
			}
			result.History.Selections = append(result.History.Selections, UsageSelection{ID: id, AccountID: account, StartAt: start, EndAt: end, CapturedAt: capture.CapturedAt})
		}
		if count == 0 || start.Before(result.StartAt) {
			result.StartAt = start
		}
		if count == 0 || end.After(result.EndAt) {
			result.EndAt = end
		}
		accounts[account], count = true, count+1
		if result.OldestCaptureAt.IsZero() || capture.CapturedAt.Before(result.OldestCaptureAt) {
			result.OldestCaptureAt = capture.CapturedAt
		}
		if capture.CapturedAt.After(result.NewestCaptureAt) {
			result.NewestCaptureAt = capture.CapturedAt
		}
		limitations := append([]string{}, capture.Limitations...)
		if capture.State != "exhausted" {
			limitations = append(limitations, "CAPTURE_"+strings.ToUpper(capture.State))
		}
		for _, reason := range limitations {
			if reason == "PARTICIPANT_MAPPING_UNAVAILABLE" {
				continue
			}
			found := false
			for _, existing := range result.Limitations {
				found = found || existing == reason
			}
			if !found {
				result.Limitations = append(result.Limitations, reason)
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return AttributedUsageView{}, err
	}
	if count != len(ids) {
		return AttributedUsageView{}, auth.ErrNotFound
	}
	if historical && result.EndAt.Sub(result.StartAt) > 366*24*time.Hour {
		return AttributedUsageView{}, auth.ErrInput
	}
	scope, args, err = access.ScopeSQL(pgx.Identifier{"c", "account_id"}, pgx.Identifier{"a", "participant_id"}, 2)
	if err != nil {
		return AttributedUsageView{}, err
	}
	n := len(args) + 2
	args = append([]any{ids}, args...)
	args = append(args, input.ParticipantID, input.Model, input.APIKeyID)
	rows, err = tx.Query(ctx, `SELECT c.id::text,c.account_id,a.participant_id,a.binding_id::text,a.participant_name,a.basis,f.fact
		FROM usage_capture_facts f JOIN usage_captures c ON c.id=f.capture_id JOIN usage_attributions a ON a.capture_id=f.capture_id AND a.upstream_id=f.upstream_id
		WHERE c.id=ANY($1::bigint[]) AND `+scope+` AND ($`+strconv.Itoa(n)+`='' OR a.participant_id=$`+strconv.Itoa(n)+`) AND ($`+strconv.Itoa(n+1)+`='' OR f.fact->>'model'=$`+strconv.Itoa(n+1)+`) AND ($`+strconv.Itoa(n+2)+`='' OR f.fact->>'api_key_id'=$`+strconv.Itoa(n+2)+`) ORDER BY c.id,f.upstream_id`, args...)
	if err != nil {
		return AttributedUsageView{}, err
	}
	facts, assigned, unassigned := []UsageFact{}, []UsageFact{}, []UsageFact{}
	groups, groupFacts := map[string]AttributedUsageGroup{}, map[string][]UsageFact{}
	for rows.Next() {
		var r AttributedUsageRecord
		var body []byte
		if err = rows.Scan(&r.CaptureID, &r.AccountID, &r.Attribution.ParticipantID, &r.Attribution.BindingID, &r.Attribution.ParticipantName, &r.Attribution.Basis, &body); err == nil {
			err = json.Unmarshal(body, &r.Fact)
		}
		if err != nil {
			break
		}
		// PostgreSQL timestamps lose native nanoseconds; filter decoded facts before every aggregate and page.
		if windowed && (r.Fact.CreatedAt.Before(start) || !r.Fact.CreatedAt.Before(end)) {
			continue
		}
		facts = append(facts, r.Fact)
		participant := ""
		if r.Attribution.ParticipantID == nil {
			unassigned = append(unassigned, r.Fact)
		} else {
			participant = *r.Attribution.ParticipantID
			assigned = append(assigned, r.Fact)
		}
		key := r.AccountID + ":" + participant + ":" + r.Attribution.Basis
		groups[key] = AttributedUsageGroup{AccountID: r.AccountID, ParticipantID: r.Attribution.ParticipantID, Basis: r.Attribution.Basis}
		groupFacts[key] = append(groupFacts[key], r.Fact)
		capID, _ := strconv.ParseInt(r.CaptureID, 10, 64)
		factID, _ := strconv.ParseInt(r.Fact.ID, 10, 64)
		if (capID > afterCapture || capID == afterCapture && factID > afterFact) && len(result.Records) <= input.Limit {
			result.Records = append(result.Records, r)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return AttributedUsageView{}, err
	}
	result.Summary, result.Assigned = summarizeUsage(facts), summarizeUsage(assigned)
	if access.AllData {
		sum := summarizeUsage(unassigned)
		result.Unassigned = &sum
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		g := groups[key]
		g.UsageSummary = summarizeUsage(groupFacts[key])
		result.Groups = append(result.Groups, g)
	}
	bucketStart, bucketEnd := result.StartAt, result.EndAt
	if windowed {
		if start.After(bucketStart) {
			bucketStart = start
		}
		if end.Before(bucketEnd) {
			bucketEnd = end
		}
	}
	result.Buckets = usageBuckets(facts, bucketStart, bucketEnd, input.Grain, location)
	if len(result.Records) > input.Limit {
		result.Records = result.Records[:input.Limit]
		last := result.Records[input.Limit-1]
		next := last.CaptureID + "." + last.Fact.ID
		result.NextAfter = &next
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "usage"); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return AttributedUsageView{}, err
	}
	return result, nil
}
