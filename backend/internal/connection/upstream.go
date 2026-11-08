package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sub-pool/backend/internal/auth"
)

type Check struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	HTTPStatus *int   `json:"http_status"`
	LatencyMS  *int64 `json:"latency_ms"`
	retryAfter *time.Time
}

type Diagnostic struct {
	CheckedAt time.Time `json:"checked_at"`
	Revision  string    `json:"revision"`
	SourceID  *string   `json:"source_id"`
	Saved     bool      `json:"saved"`
	Version   *string   `json:"version"`
	RunMode   *string   `json:"run_mode"`
	Checks    []Check   `json:"checks"`
}

type upstreamClient struct{ http *http.Client }

var errRedirect = errors.New("upstream redirect refused")

func newUpstreamClient() *upstreamClient {
	return &upstreamClient{http: &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second,
			MaxConnsPerHost: 6, MaxIdleConnsPerHost: 6, IdleConnTimeout: 30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}}
}

func unknown(id, reason string) Check {
	return Check{ID: id, State: "unverified", Reason: reason}
}

func invalid(check Check) Check {
	check.State, check.Reason = "failed", "INVALID_RESPONSE"
	return check
}

func decode(body []byte, result any) error {
	if !utf8.Valid(body) || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return errors.New("invalid upstream JSON")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	if err := d.Decode(result); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("invalid upstream JSON trailing data")
	}
	return nil
}

func validNumber(body json.RawMessage) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	err := decoder.Decode(&value)
	_, number := value.(json.Number)
	return err == nil && number && json.Valid(body)
}

// Only fixed GET paths and safe projections cross this boundary; raw upstream errors never do.
func (u *upstreamClient) get(ctx context.Context, base, path, kind, secret, id string, envelope bool) (json.RawMessage, Check) {
	check := unknown(id, "NOT_CONFIGURED")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, invalid(check)
	}
	req.Header.Set("Accept", "application/json")
	if secret != "" {
		if kind == "bearer" {
			req.Header.Set("Authorization", "Bearer "+secret)
		} else {
			req.Header.Set("x-api-key", secret)
		}
	}
	start := time.Now()
	res, err := u.http.Do(req)
	latency := time.Since(start).Milliseconds()
	check.LatencyMS = &latency
	if err != nil {
		check.State, check.Reason = "failed", "NETWORK_ERROR"
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			check.Reason = "TIMEOUT"
		} else if errors.Is(err, errRedirect) {
			check.Reason = "REDIRECT_REFUSED"
		}
		return nil, check
	}
	defer res.Body.Close()
	status := res.StatusCode
	check.HTTPStatus = &status
	if status != http.StatusOK {
		check.State, check.Reason = "failed", "UPSTREAM_ERROR"
		switch status {
		case 401:
			check.State, check.Reason = "denied", "UNAUTHORIZED"
		case 403:
			check.State, check.Reason = "denied", "FORBIDDEN"
		case 423:
			check.State, check.Reason = "denied", "COMPLIANCE_REQUIRED"
		case 404, 405:
			check.State, check.Reason = "unsupported", "ROUTE_UNAVAILABLE"
		case 429:
			check.Reason = "RATE_LIMITED"
			until := time.Now().UTC().Add(5 * time.Minute)
			value := strings.TrimSpace(res.Header.Get("Retry-After"))
			if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
				if seconds <= 31536000 {
					until = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
				} else {
					until = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
				}
			} else if date, err := http.ParseTime(value); err == nil {
				until = date.UTC()
			}
			check.retryAfter = &until
		}
		return nil, check
	}
	typ, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		return nil, invalid(check)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return nil, invalid(check)
	}
	check.State, check.Reason = "available", "VERIFIED_READ"
	if !envelope {
		return body, check
	}
	var response struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if decode(body, &response) != nil || response.Code == nil || *response.Code != 0 || len(response.Data) == 0 || bytes.Equal(response.Data, []byte("null")) {
		return nil, invalid(check)
	}
	return response.Data, check
}

type pageData struct {
	Items    []json.RawMessage `json:"items"`
	Total    *int64            `json:"total"`
	Page     *int              `json:"page"`
	PageSize *int              `json:"page_size"`
}

func parsePage(body json.RawMessage, page, size int) (pageData, bool) {
	var result pageData
	err := decode(body, &result)
	valid := err == nil && result.Items != nil && result.Total != nil && *result.Total >= 0 && result.Page != nil && *result.Page == page && result.PageSize != nil && *result.PageSize == size && len(result.Items) <= size && int64(len(result.Items)) <= *result.Total
	if valid {
		remaining := *result.Total - int64(page-1)*int64(size)
		valid = int64(len(result.Items)) == max(0, min(int64(size), remaining))
	}
	return result, valid
}

type TestInput struct {
	Revision   string `json:"revision"`
	BaseURL    string `json:"base_url"`
	Credential string `json:"credential"`
}

func (s *Service) Test(ctx context.Context, token string, input TestInput) (Diagnostic, error) {
	base, err := s.normalizeURL(input.BaseURL)
	if err != nil || !validRevision(input.Revision) {
		return Diagnostic{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return Diagnostic{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize saved diagnostics with configuration changes; there is no lock upgrade.
	c, err := s.config(ctx, tx, true)
	if err != nil {
		return Diagnostic{}, err
	}
	if c.Revision != input.Revision {
		return Diagnostic{}, auth.ErrConflict
	}
	kind, secret := "", ""
	if input.Credential != "" {
		kind, secret, err = credential(input.Credential)
	} else if base != c.BaseURL && c.CredentialSet {
		err = auth.ErrInput
	} else if base == c.BaseURL && c.CredentialSet {
		// Only this explicit diagnostic may use a restored credential before verification.
		c.reverificationRequired = false
		kind, secret, err = s.decrypt(c)
	}
	if err != nil {
		return Diagnostic{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return Diagnostic{}, err
	}
	result := s.upstream.diagnose(ctx, base, kind, secret)
	result.Revision, result.Saved = c.Revision, base == c.BaseURL && input.Credential == ""
	if base == c.BaseURL {
		result.SourceID = c.SourceID
	}
	if result.Saved {
		body, err := json.Marshal(result)
		if err != nil {
			return Diagnostic{}, err
		}
		verified := false
		for _, check := range result.Checks {
			if check.ID == "management_auth" && check.State == "available" {
				verified = true
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE connection_config SET diagnostic=$1,reverification_required=CASE WHEN $2 THEN false ELSE reverification_required END WHERE singleton`, body, verified); err != nil {
			return Diagnostic{}, err
		}
	}
	if err = record(ctx, tx, actor.ID, result.SourceID, c.Revision, "connection.test"); err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return Diagnostic{}, err
	}
	return result, nil
}

func (u *upstreamClient) diagnose(ctx context.Context, base, kind, secret string) Diagnostic {
	result := Diagnostic{CheckedAt: time.Now().UTC().Truncate(time.Microsecond)}
	paths := []string{"/health", "/api/v1/admin/system/version", "/api/v1/admin/accounts?page=1&page_size=1", "/api/v1/admin/usage?page=1&page_size=1", "/api/v1/admin/users?page=1&page_size=1", "/api/v1/auth/me"}
	ids := []string{"health", "version.read", "accounts.read", "usage.read", "wallet.read", "run_mode"}
	data := make([]json.RawMessage, len(paths))
	result.Checks = make([]Check, len(paths))
	var wg sync.WaitGroup
	for i := range paths {
		result.Checks[i] = unknown(ids[i], "NOT_CONFIGURED")
		if i > 0 && secret == "" {
			continue
		}
		if i == 5 && kind != "bearer" {
			result.Checks[i] = unknown(ids[i], "MODE_REQUIRES_JWT")
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			probeKind, probeSecret := kind, secret
			if i == 0 {
				probeKind, probeSecret = "", ""
			}
			data[i], result.Checks[i] = u.get(ctx, base, paths[i], probeKind, probeSecret, ids[i], i != 0)
		}(i)
	}
	wg.Wait()
	for i := range paths {
		if result.Checks[i].State != "available" {
			continue
		}
		switch i {
		case 0:
			var health struct {
				Status string `json:"status"`
			}
			if decode(data[i], &health) != nil || health.Status != "ok" {
				result.Checks[i] = invalid(result.Checks[i])
			}
		case 1:
			var version struct {
				Version string `json:"version"`
			}
			if decode(data[i], &version) != nil || version.Version == "" || len(version.Version) > 128 || secret != "" && strings.Contains(version.Version, secret) {
				result.Checks[i] = invalid(result.Checks[i])
			} else {
				result.Version = &version.Version
			}
		case 5:
			var mode struct {
				RunMode string `json:"run_mode"`
				Role    string `json:"role"`
			}
			if decode(data[i], &mode) != nil || mode.Role != "admin" || mode.RunMode != "standard" && mode.RunMode != "simple" {
				result.Checks[i] = invalid(result.Checks[i])
			} else {
				result.RunMode = &mode.RunMode
			}
		default:
			page, valid := parsePage(data[i], 1, 1)
			if !valid {
				result.Checks[i] = invalid(result.Checks[i])
			} else if len(page.Items) == 0 {
				result.Checks[i].State, result.Checks[i].Reason = "unverified", "NO_VISIBLE_RESOURCE"
			} else if i == 2 {
				if _, valid := projectAccount(page.Items[0]); !valid {
					result.Checks[i] = invalid(result.Checks[i])
				}
			} else if i == 3 {
				var usage struct {
					ID         int64           `json:"id"`
					CreatedAt  time.Time       `json:"created_at"`
					TotalCost  json.RawMessage `json:"total_cost"`
					ActualCost json.RawMessage `json:"actual_cost"`
				}
				if decode(page.Items[0], &usage) != nil || usage.ID <= 0 || usage.CreatedAt.IsZero() || !validNumber(usage.TotalCost) || !validNumber(usage.ActualCost) {
					result.Checks[i] = invalid(result.Checks[i])
				}
			} else if i == 4 {
				var user struct {
					ID      int64           `json:"id"`
					Balance json.RawMessage `json:"balance"`
				}
				if decode(page.Items[0], &user) != nil || user.ID <= 0 || !validNumber(user.Balance) {
					result.Checks[i] = invalid(result.Checks[i])
				}
			}
		}
	}
	authenticated := unknown("management_auth", "NO_VERIFIED_ADMIN_READ")
	for _, check := range result.Checks[1:5] {
		if check.State == "available" || check.Reason == "NO_VISIBLE_RESOURCE" {
			authenticated.State, authenticated.Reason = "available", "VERIFIED_READ"
			break
		}
		if check.Reason == "UNAUTHORIZED" {
			authenticated.State, authenticated.Reason = "denied", "UNAUTHORIZED"
		}
	}
	result.Checks = append(result.Checks, authenticated,
		unknown("quota.read", "ACCOUNT_CHECK_REQUIRED"), unknown("wallet.write", "WRITE_NOT_PROBED"),
		unknown("scheduling.write", "WRITE_NOT_PROBED"), unknown("pricing.write", "WRITE_NOT_PROBED"), unknown("pricing.read", "PRICING_CHECK_REQUIRED"))
	return result
}

type Account struct {
	UpstreamID  string `json:"upstream_id"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Schedulable bool   `json:"schedulable"`
	Eligibility string `json:"eligibility"`
	Reason      string `json:"reason"`
}

func projectAccount(body json.RawMessage) (Account, bool) {
	var raw struct {
		ID          *int64 `json:"id"`
		Name        string `json:"name"`
		Platform    string `json:"platform"`
		Type        string `json:"type"`
		Status      string `json:"status"`
		Schedulable *bool  `json:"schedulable"`
	}
	if decode(body, &raw) != nil || raw.ID == nil || *raw.ID <= 0 || raw.Name == "" || len(raw.Name) > 512 || raw.Platform == "" || len(raw.Platform) > 64 || raw.Type == "" || len(raw.Type) > 64 || raw.Status == "" || len(raw.Status) > 64 || raw.Schedulable == nil {
		return Account{}, false
	}
	result := Account{UpstreamID: strconv.FormatInt(*raw.ID, 10), Name: raw.Name, Platform: raw.Platform, Type: raw.Type, Status: raw.Status, Schedulable: *raw.Schedulable, Eligibility: "unsupported", Reason: "NOT_OPENAI_OAUTH"}
	if raw.Platform == "openai" && raw.Type == "oauth" {
		result.Eligibility, result.Reason = "unverified", "RESOURCE_IDENTITY_UNVERIFIED"
	}
	return result, true
}

type DiscoverInput struct {
	Revision string `json:"revision"`
	SourceID string `json:"source_id"`
	Page     int    `json:"page"`
	Search   string `json:"search"`
}

type Discovery struct {
	Revision string    `json:"revision"`
	SourceID string    `json:"source_id"`
	Page     int       `json:"page"`
	Total    *string   `json:"total"`
	NextPage *int      `json:"next_page"`
	Accounts []Account `json:"accounts"`
	Read     Check     `json:"read"`
}

func (s *Service) Discover(ctx context.Context, token string, input DiscoverInput) (Discovery, error) {
	if !validRevision(input.Revision) || input.SourceID == "" || input.Page < 1 || input.Page > 2147483647 || len(input.Search) > 256 || !utf8.ValidString(input.Search) || strings.ContainsAny(input.Search, "\r\n\x00") {
		return Discovery{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return Discovery{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return Discovery{}, err
	}
	if c.Revision != input.Revision || c.SourceID == nil || *c.SourceID != input.SourceID {
		return Discovery{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return Discovery{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return Discovery{}, err
	}
	query := url.Values{"page": {strconv.Itoa(input.Page)}, "page_size": {"100"}, "search": {input.Search}}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts?"+query.Encode(), kind, secret, "accounts.read", true)
	result := Discovery{Revision: c.Revision, SourceID: *c.SourceID, Page: input.Page, Accounts: []Account{}, Read: check}
	if check.State == "available" {
		page, valid := parsePage(body, input.Page, 100)
		seen := make(map[string]bool)
		if valid {
			for _, item := range page.Items {
				account, ok := projectAccount(item)
				if !ok || seen[account.UpstreamID] || strings.Contains(account.Name+"\n"+account.Platform+"\n"+account.Type+"\n"+account.Status, secret) {
					valid = false
					break
				}
				seen[account.UpstreamID] = true
				result.Accounts = append(result.Accounts, account)
			}
		}
		if valid && *page.Total <= 214748364700 {
			total := strconv.FormatInt(*page.Total, 10)
			result.Total = &total
			if int64(input.Page)*100 < *page.Total {
				next := input.Page + 1
				result.NextPage = &next
			}
		} else {
			result.Accounts, result.Read = []Account{}, invalid(check)
		}
	}
	if err = record(ctx, tx, actor.ID, c.SourceID, c.Revision, "connection.discover"); err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return Discovery{}, err
	}
	return result, nil
}
