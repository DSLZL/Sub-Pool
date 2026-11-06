package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

const usagePageSize = 100
const usageMaxPages = 10

type UsageCaptureInput struct {
	AccountReadInput
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

type UsageCapture struct {
	ID                 string    `json:"id"`
	AccountID          string    `json:"account_id"`
	SourceID           string    `json:"source_id"`
	ConnectionRevision string    `json:"connection_revision"`
	AccountRevision    string    `json:"account_revision"`
	UpstreamAccountID  string    `json:"upstream_account_id"`
	StartAt            time.Time `json:"start_at"`
	EndAt              time.Time `json:"end_at"`
	StartedAt          time.Time `json:"started_at"`
	CapturedAt         time.Time `json:"captured_at"`
	SourceCurrent      bool      `json:"source_current"`
	ConnectionCurrent  bool      `json:"connection_current"`
	State              string    `json:"state"`
	Read               Check     `json:"read"`
	PagesRead          int       `json:"pages_read"`
	UpstreamDayTotal   *string   `json:"upstream_day_total"`
	ScannedCount       string    `json:"scanned_count"`
	ObservedCount      string    `json:"observed_count"`
	TimestampBasis     string    `json:"timestamp_basis"`
	Currency           string    `json:"currency"`
	Complete           bool      `json:"complete"`
	SettlementEligible bool      `json:"settlement_eligible"`
	Limitations        []string  `json:"limitations"`
}

func usageRange(start, end string) (time.Time, time.Time, bool) {
	a, errA := time.Parse(time.RFC3339Nano, start)
	b, errB := time.Parse(time.RFC3339Nano, end)
	return a, b, errA == nil && errB == nil && strings.HasSuffix(start, "Z") && strings.HasSuffix(end, "Z") && a.Year() >= 1970 && b.Year() < 9999 && b.After(a) && b.Sub(a) <= 31*24*time.Hour
}

func (s *Service) CaptureUsage(ctx context.Context, token string, input UsageCaptureInput) (UsageCapture, error) {
	start, end, valid := usageRange(input.StartAt, input.EndAt)
	if !validAccountRead(input.AccountReadInput) || !valid {
		return UsageCapture{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return UsageCapture{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return UsageCapture{}, err
	}
	a, err := readManaged(ctx, tx, input.ID, c, false)
	if err != nil {
		return UsageCapture{}, err
	}
	if !accountReadMatches(input.AccountReadInput, a, c) {
		return UsageCapture{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return UsageCapture{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return UsageCapture{}, err
	}
	result := UsageCapture{
		AccountID: a.ID, SourceID: a.SourceID, ConnectionRevision: c.Revision, AccountRevision: a.Revision, UpstreamAccountID: a.UpstreamID,
		StartAt: start, EndAt: end, StartedAt: time.Now().UTC(), State: "failed", SourceCurrent: true, ConnectionCurrent: true,
		ScannedCount: "0", ObservedCount: "0", TimestampBasis: "native_created_at", Currency: "USD",
		Limitations: []string{"RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "HISTORICAL_BILLING_PATH_UNKNOWN", "PARTICIPANT_MAPPING_UNAVAILABLE", "REQUEST_START_TIME_UNAVAILABLE"},
	}
	// Leave time for persistence and the final authority check after bounded network reads.
	network, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	facts, scanned, lastID := []UsageFact{}, 0, int64(0)
	query := url.Values{
		"account_id": {a.UpstreamID}, "start_date": {start.Format("2006-01-02")}, "end_date": {end.Add(-time.Nanosecond).Format("2006-01-02")},
		"timezone": {"UTC"}, "page_size": {strconv.Itoa(usagePageSize)}, "exact_total": {"true"}, "sort_by": {"id"}, "sort_order": {"asc"},
	}
	dayStart, _ := time.Parse("2006-01-02", query.Get("start_date"))
	dayEnd, _ := time.Parse("2006-01-02", query.Get("end_date"))
	dayEnd = dayEnd.AddDate(0, 0, 1)
	for page := 1; page <= usageMaxPages; page++ {
		if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
			return UsageCapture{}, err
		}
		query.Set("page", strconv.Itoa(page))
		body, check := s.upstream.get(network, c.BaseURL, "/api/v1/admin/usage?"+query.Encode(), kind, secret, "usage.read", true)
		result.Read = check
		if check.State != "available" {
			break
		}
		data, ok := parsePage(body, page, usagePageSize)
		if !ok {
			result.Read = invalid(check)
			break
		}
		total := strconv.FormatInt(*data.Total, 10)
		if result.UpstreamDayTotal != nil && *result.UpstreamDayTotal != total {
			result.Read.State, result.Read.Reason = "failed", "PAGINATION_CHANGED"
			break
		}
		projected, nextID := []UsageFact{}, lastID
		for _, item := range data.Items {
			fact, valid := projectUsage(item, a.UpstreamID, secret)
			id, _ := strconv.ParseInt(fact.ID, 10, 64)
			if !valid || id <= nextID || fact.CreatedAt.Before(dayStart) || !fact.CreatedAt.Before(dayEnd) {
				ok = false
				break
			}
			nextID = id
			if !fact.CreatedAt.Before(start) && fact.CreatedAt.Before(end) {
				projected = append(projected, fact)
			}
		}
		if !ok {
			result.Read = invalid(check)
			break
		}
		result.UpstreamDayTotal = &total
		result.PagesRead++
		scanned += len(data.Items)
		lastID = nextID
		facts = append(facts, projected...)
		if int64(scanned) == *data.Total {
			result.State = "exhausted"
			break
		}
		result.State = "partial"
		if page == usageMaxPages {
			result.Limitations = append(result.Limitations, "SCAN_LIMIT")
		}
	}
	cancel()
	result.CapturedAt = time.Now().UTC()
	result.ScannedCount, result.ObservedCount = strconv.Itoa(scanned), strconv.Itoa(len(facts))
	if result.Read.State != "available" {
		result.Limitations = append(result.Limitations, result.Read.Reason)
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return UsageCapture{}, err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return UsageCapture{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) VALUES ($1,$2,$3,$4) RETURNING id::text`, a.ID, actor.ID, result.CapturedAt, body).Scan(&result.ID)
	if err != nil {
		return UsageCapture{}, err
	}
	for _, fact := range facts {
		body, err = json.Marshal(fact)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) VALUES ($1,$2,$3)`, result.ID, fact.ID, body)
		}
		if err != nil {
			return UsageCapture{}, err
		}
	}
	if err = attributeUsage(ctx, tx, result.ID, result.SourceID, facts); err != nil {
		return UsageCapture{}, err
	}
	if err = attributePoolUsage(ctx, tx, result.ID, facts); err != nil {
		return UsageCapture{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO usage_events(occurred_at,actor_id,capture_id,action) VALUES ($1,$2,$3,'usage.capture')`, result.CapturedAt, actor.ID, result.ID)
	if err == nil {
		outcome := "succeeded"
		if result.State == "partial" {
			outcome = "unknown"
		} else if result.State == "failed" {
			outcome = "failed"
		}
		_, err = audit.Record(ctx, tx, audit.Entry{OperationID: "usage:capture:" + result.ID, ActorID: &actor.ID, Action: "usage.capture", Outcome: outcome, TargetType: "usage_capture", TargetID: result.ID, Revision: &result.AccountRevision, SourceID: &result.SourceID})
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return UsageCapture{}, err
	}
	return result, nil
}

func (s *Service) usageAccess(ctx context.Context, tx pgx.Tx, token, account string) error {
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err == nil {
		err = access.CheckAccount(account)
	}
	// Raw capture metadata is account-wide; ordinary readers use the scoped participants-v1 view.
	if err == nil && !access.AllData {
		err = auth.ErrNotFound
	}
	return err
}

func decodeUsageCapture(body []byte, id string, c storedConfig) (UsageCapture, error) {
	var result UsageCapture
	err := json.Unmarshal(body, &result)
	result.ID = id
	result.SourceCurrent = c.SourceID != nil && *c.SourceID == result.SourceID
	result.ConnectionCurrent = result.SourceCurrent && c.Revision == result.ConnectionRevision
	return result, err
}

type UsageCapturePage struct {
	Captures  []UsageCapture `json:"captures"`
	NextAfter *string        `json:"next_after"`
}

func validUsageAfter(after string) bool { return after == "0" || validRevision(after) }

func (s *Service) ListUsageCaptures(ctx context.Context, token, account, after string, limit int) (UsageCapturePage, error) {
	if !managedID(account) || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return UsageCapturePage{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UsageCapturePage{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.usageAccess(ctx, tx, token, account); err != nil {
		return UsageCapturePage{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return UsageCapturePage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,snapshot FROM usage_captures WHERE account_id=$1 AND id>$2::bigint ORDER BY usage_captures.id LIMIT $3`, account, after, limit+1)
	if err != nil {
		return UsageCapturePage{}, err
	}
	defer rows.Close()
	result := UsageCapturePage{Captures: []UsageCapture{}}
	for rows.Next() {
		var id string
		var body []byte
		if err = rows.Scan(&id, &body); err != nil {
			return UsageCapturePage{}, err
		}
		capture, err := decodeUsageCapture(body, id, c)
		if err != nil {
			return UsageCapturePage{}, err
		}
		result.Captures = append(result.Captures, capture)
	}
	if err = rows.Err(); err != nil {
		return UsageCapturePage{}, err
	}
	rows.Close()
	if len(result.Captures) > limit {
		result.Captures = result.Captures[:limit]
		result.NextAfter = &result.Captures[limit-1].ID
	}
	if err = s.usageAccess(ctx, tx, token, account); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return UsageCapturePage{}, err
	}
	return result, nil
}

type UsageQuery struct {
	ID       string `json:"id"`
	After    string `json:"after"`
	Limit    int    `json:"limit"`
	Model    string `json:"model"`
	UserID   string `json:"user_id"`
	APIKeyID string `json:"api_key_id"`
	Grain    string `json:"grain"`
}

type UsageView struct {
	Capture   UsageCapture  `json:"capture"`
	Summary   UsageSummary  `json:"summary"`
	Grain     string        `json:"grain"`
	Buckets   []UsageBucket `json:"buckets"`
	Records   []UsageFact   `json:"records"`
	NextAfter *string       `json:"next_after"`
}

func (s *Service) GetUsageCapture(ctx context.Context, token string, input UsageQuery) (UsageView, error) {
	if !validRevision(input.ID) || !validUsageAfter(input.After) || input.Limit < 1 || input.Limit > 100 || input.Model != "" && !safeText(input.Model, 512, "") || input.UserID != "" && !validRevision(input.UserID) || input.APIKeyID != "" && !validRevision(input.APIKeyID) || input.Grain != "hour" && input.Grain != "day" {
		return UsageView{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UsageView{}, err
	}
	defer tx.Rollback(ctx)
	// Hold current identity/grants before inspecting any capture metadata.
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "usage")
	if err != nil {
		return UsageView{}, err
	}
	if !access.AllData {
		return UsageView{}, auth.ErrNotFound
	}
	var account string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT account_id,snapshot FROM usage_captures WHERE id=$1`, input.ID).Scan(&account, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err != nil {
		return UsageView{}, err
	}
	if err = s.usageAccess(ctx, tx, token, account); err != nil {
		return UsageView{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return UsageView{}, err
	}
	capture, err := decodeUsageCapture(body, input.ID, c)
	if err != nil {
		return UsageView{}, err
	}
	rows, err := tx.Query(ctx, `SELECT fact FROM usage_capture_facts WHERE capture_id=$1
		AND ($2='' OR fact->>'model'=$2) AND ($3='' OR fact->>'user_id'=$3) AND ($4='' OR fact->>'api_key_id'=$4)
		ORDER BY upstream_id`, input.ID, input.Model, input.UserID, input.APIKeyID)
	if err != nil {
		return UsageView{}, err
	}
	defer rows.Close()
	facts := []UsageFact{}
	for rows.Next() {
		var fact UsageFact
		if err = rows.Scan(&body); err == nil {
			err = json.Unmarshal(body, &fact)
		}
		if err != nil {
			return UsageView{}, err
		}
		facts = append(facts, fact)
	}
	if err = rows.Err(); err != nil {
		return UsageView{}, err
	}
	rows.Close()
	result := UsageView{Capture: capture, Grain: input.Grain, Summary: summarizeUsage(facts), Buckets: usageBuckets(facts, capture.StartAt, capture.EndAt, input.Grain), Records: []UsageFact{}}
	after, _ := strconv.ParseInt(input.After, 10, 64)
	for _, fact := range facts {
		id, _ := strconv.ParseInt(fact.ID, 10, 64)
		if id > after {
			result.Records = append(result.Records, fact)
			if len(result.Records) > input.Limit {
				result.Records = result.Records[:input.Limit]
				result.NextAfter = &result.Records[input.Limit-1].ID
				break
			}
		}
	}
	if err = s.usageAccess(ctx, tx, token, account); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return UsageView{}, err
	}
	return result, nil
}
