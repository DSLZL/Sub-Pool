package connection

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/telemetry"
)

type Check struct {
	ID               string              `json:"id"`
	State            string              `json:"state"`
	Reason           string              `json:"reason"`
	HTTPStatus       *int                `json:"http_status"`
	LatencyMS        *int64              `json:"latency_ms"`
	Evidence         *CapabilityEvidence `json:"evidence,omitempty"`
	retryAfter       *time.Time
	retryableNetwork bool
}

type CapabilityEvidence struct {
	UpstreamID      string    `json:"upstream_id"`
	VerifiedAt      time.Time `json:"verified_at"`
	AccountRevision string    `json:"account_revision,omitempty"`
}

type Diagnostic struct {
	CheckedAt    time.Time  `json:"checked_at"`
	Revision     string     `json:"revision"`
	SourceID     *string    `json:"source_id"`
	Saved        bool       `json:"saved"`
	Version      *string    `json:"version"`
	VersionSince *time.Time `json:"version_since,omitempty"`
	RunMode      *string    `json:"run_mode"`
	Checks       []Check    `json:"checks"`
}

type upstreamClient struct {
	http        *http.Client
	readTimeout time.Duration
}

var errRedirect = errors.New("upstream redirect refused")

const (
	DefaultUpstreamReadTimeout    = 15 * time.Second
	DefaultUpstreamConnectTimeout = 5 * time.Second
	upstreamWriteTimeout          = 3 * time.Second
	keyRunModePath                = "/api/v1/admin/groups/0/model-allowlist-candidates?platform=openai"
)

func newUpstreamClient(readTimeout, connectTimeout time.Duration) *upstreamClient {
	return &upstreamClient{readTimeout: readTimeout, http: &http.Client{
		Transport: &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: connectTimeout}).DialContext,
			TLSHandshakeTimeout: connectTimeout,
			MaxConnsPerHost:     6, MaxIdleConnsPerHost: 6, IdleConnTimeout: 30 * time.Second,
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

// Only fixed paths and safe projections cross this boundary; raw upstream errors never do.
func (u *upstreamClient) get(ctx context.Context, base, path, kind, secret, id string, envelope bool) (json.RawMessage, Check) {
	ctx, cancel := context.WithTimeout(ctx, u.readTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	backoff := 250 * time.Millisecond
	for retries := 0; ; retries++ {
		body, check := u.request(ctx, http.MethodGet, base, path, kind, secret, id, nil, envelope)
		if retries == 2 || ctx.Err() != nil || !check.canRetryRead() {
			return body, check
		}
		wait := backoff
		if check.retryAfter != nil {
			wait = max(wait, time.Until(*check.retryAfter))
		}
		if wait >= time.Until(deadline) {
			return body, check
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return body, check
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return body, check
		}
		backoff *= 2
	}
}

func (c Check) canRetryRead() bool {
	return c.retryableNetwork || c.retryAfter != nil ||
		c.HTTPStatus != nil && *c.HTTPStatus >= http.StatusInternalServerError && *c.HTTPStatus < 600
}

func temporaryUpstreamError(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return !dns.IsNotFound && (dns.IsTimeout || dns.IsTemporary)
	}
	var address *net.AddrError
	var certificate *tls.CertificateVerificationError
	if errors.Is(err, context.Canceled) || errors.Is(err, os.ErrPermission) ||
		errors.As(err, &address) || errors.As(err, &certificate) {
		return false
	}
	var operation *net.OpError
	var timeout net.Error
	return errors.As(err, &operation) || errors.As(err, &timeout) && timeout.Timeout() ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (u *upstreamClient) request(ctx context.Context, method, base, path, kind, secret, id string, payload []byte, envelope bool) (json.RawMessage, Check) {
	return u.requestWithKey(ctx, method, base, path, kind, secret, id, payload, envelope, "")
}

func (u *upstreamClient) requestWithKey(ctx context.Context, method, base, path, kind, secret, id string, payload []byte, envelope bool, key string) (body json.RawMessage, check Check) {
	check = unknown(id, "NOT_CONFIGURED")
	var cause error
	defer func() {
		if check.State == "available" {
			return
		}
		attrs := []slog.Attr{slog.String("reason", check.Reason)}
		if check.LatencyMS != nil {
			attrs = append(attrs, slog.Int64("duration_ms", *check.LatencyMS))
		}
		if cause != nil {
			telemetry.Error(ctx, id, cause, attrs...)
			return
		}
		attrs = append(attrs, slog.String("operation", id))
		level := slog.LevelDebug
		if check.State == "failed" {
			level = slog.LevelWarn
		}
		if check.HTTPStatus != nil {
			attrs = append(attrs, slog.Int("upstream_status", *check.HTTPStatus))
			if *check.HTTPStatus >= 500 {
				level = slog.LevelError
			}
		}
		telemetry.Log(ctx, level, "upstream request unavailable", attrs...)
	}()
	if method != http.MethodGet {
		if len(payload) == 0 {
			return nil, invalid(check)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, upstreamWriteTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
	if err != nil {
		cause = err
		return nil, invalid(check)
	}
	if method != http.MethodGet {
		// A replayable body plus Idempotency-Key allows net/http to resend a write.
		req.GetBody = nil
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
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
		cause = err
		check.State, check.Reason = "failed", "NETWORK_ERROR"
		check.retryableNetwork = method == http.MethodGet && temporaryUpstreamError(err)
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
			if id == "run_mode" && kind == "api_key" && method == http.MethodGet && path == keyRunModePath && nativeSimpleModeResponse(res) {
				check.State, check.Reason = "available", "VERIFIED_READ"
			}
		case 423:
			check.State, check.Reason = "denied", "COMPLIANCE_REQUIRED"
		case 404, 405:
			check.State, check.Reason = "unsupported", "ROUTE_UNAVAILABLE"
		case 429:
			check.Reason = "RATE_LIMITED"
			until := time.Now().UTC().Add(5 * time.Minute)
			value := strings.TrimSpace(res.Header.Get("Retry-After"))
			seconds, err := strconv.ParseUint(value, 10, 64)
			switch {
			case err == nil && seconds <= 31536000:
				until = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
			case err == nil || errors.Is(err, strconv.ErrRange) && strings.Trim(value, "0123456789") == "":
				until = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			default:
				if date, err := http.ParseTime(value); err == nil {
					until = date.UTC()
				}
			}
			check.retryAfter = &until
		}
		return nil, check
	}
	body, ok := readUpstreamJSON(res)
	if !ok {
		return nil, invalid(check)
	}
	check.State, check.Reason = "available", "VERIFIED_READ"
	if method != http.MethodGet {
		check.Reason = "RESPONSE_RECEIVED"
	}
	if !envelope {
		return body, check
	}
	response, valid := usageObject(body)
	var code int
	if !valid || !utf8.Valid(body) || !json.Valid(body) || decode(response["code"], &code) != nil || code != 0 || len(response["data"]) == 0 || bytes.Equal(bytes.TrimSpace(response["data"]), []byte("null")) {
		return nil, invalid(check)
	}
	// Struct decoding accepts duplicate keys and case-insensitive control-field aliases.
	for name := range response {
		if name != "code" && strings.EqualFold(name, "code") || name != "data" && strings.EqualFold(name, "data") {
			return nil, invalid(check)
		}
	}
	return response["data"], check
}

func readUpstreamJSON(res *http.Response) ([]byte, bool) {
	typ, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	return body, err == nil && len(body) <= 2<<20
}

func nativeSimpleModeResponse(res *http.Response) bool {
	body, ok := readUpstreamJSON(res)
	if !ok || !utf8.Valid(body) || !json.Valid(body) {
		return false
	}
	fields, ok := usageObject(body)
	var code int
	var reason string
	return ok && decode(fields["code"], &code) == nil && code == http.StatusForbidden &&
		decode(fields["reason"], &reason) == nil && reason == "SIMPLE_MODE_OPERATION_UNSUPPORTED"
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
		if secret != "" {
			staleAfter, err := diagnosticQuotaStaleAfter(ctx, tx)
			if err != nil {
				return Diagnostic{}, err
			}
			capability := result.capability("quota.read")
			*capability, err = s.diagnoseQuota(ctx, tx, c, kind, secret, staleAfter)
			if err != nil {
				return Diagnostic{}, err
			}
		}
		if result.Version != nil {
			result.VersionSince = &result.CheckedAt
			if previous := c.Diagnostic; previous != nil && previous.Version != nil && *previous.Version == *result.Version && previous.VersionSince != nil {
				result.VersionSince = previous.VersionSince
			}
		}
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
		if err = s.projectWriteCapabilities(ctx, tx, c, &result); err != nil {
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
	paths := []string{"/health", "/api/v1/admin/system/version", "/api/v1/admin/accounts?page=1&page_size=1", "/api/v1/admin/usage?page=1&page_size=1", "/api/v1/admin/users?page=1&page_size=1"}
	ids := []string{"health", "version.read", "accounts.read", "usage.read", "wallet.read"}
	data := make([]json.RawMessage, len(paths))
	result.Checks = make([]Check, len(paths))
	var wg sync.WaitGroup
	modeCheck := unknown("run_mode", "NOT_CONFIGURED")
	var pricingCheck Check
	if kind != "api_key" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result.RunMode, modeCheck = u.readRunMode(ctx, base, kind, secret)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		pricingCheck = u.diagnosePricing(ctx, base, kind, secret)
	}()
	for i := range paths {
		result.Checks[i] = unknown(ids[i], "NOT_CONFIGURED")
		if i > 0 && secret == "" {
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
			if i == 1 && kind == "api_key" {
				result.RunMode, modeCheck = u.readKeyRunMode(ctx, base, secret, data[i], result.Checks[i])
			}
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
	result.Checks = append(result.Checks, modeCheck, authenticated,
		unknown("quota.read", "ACCOUNT_CHECK_REQUIRED"), unknown("wallet.write", "WRITE_NOT_PROBED"),
		unknown("scheduling.write", "WRITE_NOT_PROBED"), unknown("pricing.write", "WRITE_NOT_PROBED"), pricingCheck)
	result.projectVersionCompatibility()
	return result
}

func (u *upstreamClient) readRunMode(ctx context.Context, base, kind, secret string) (*string, Check) {
	if secret == "" {
		return nil, unknown("run_mode", "NOT_CONFIGURED")
	}
	if kind == "api_key" {
		body, check := u.get(ctx, base, "/api/v1/admin/system/version", kind, secret, "version.read", true)
		return u.readKeyRunMode(ctx, base, secret, body, check)
	}
	if kind != "bearer" {
		return nil, unknown("run_mode", "MODE_REQUIRES_JWT")
	}
	body, check := u.get(ctx, base, "/api/v1/auth/me", kind, secret, "run_mode", true)
	if check.State != "available" {
		return nil, check
	}
	var role, mode string
	fields, ok := usageObject(body)
	if !ok || decode(fields["role"], &role) != nil || role != "admin" || decode(fields["run_mode"], &mode) != nil || mode != "standard" && mode != "simple" {
		return nil, invalid(check)
	}
	return &mode, check
}

type sub2APIVersion [3]uint64

var verifiedSub2APIProbes = [...]struct {
	min, limit  sub2APIVersion
	keyModePath string
}{
	{min: sub2APIVersion{0, 2, 23}, limit: sub2APIVersion{0, 2, 24}, keyModePath: keyRunModePath},
}

func keyModeProbeForVersion(raw string) string {
	parts := strings.Split(raw, ".")
	var version sub2APIVersion
	if len(parts) != len(version) {
		return ""
	}
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != part {
			return ""
		}
		version[i] = value
	}
	for _, probe := range verifiedSub2APIProbes {
		if slices.Compare(version[:], probe.min[:]) >= 0 && slices.Compare(version[:], probe.limit[:]) < 0 {
			return probe.keyModePath
		}
	}
	return ""
}

func (d *Diagnostic) projectVersionCompatibility() {
	check := d.capability("version.read")
	if d.Version == nil || check == nil || check.State != "available" {
		return
	}
	check.Reason = "VERIFIED_READ"
	if keyModeProbeForVersion(*d.Version) == "" {
		check.Reason = "VERSION_UNVERIFIED"
	}
}

func (u *upstreamClient) readKeyRunMode(ctx context.Context, base, secret string, body json.RawMessage, check Check) (*string, Check) {
	const id, kind = "run_mode", "api_key"
	check.ID = id
	if check.State != "available" {
		return nil, check
	}
	fields, ok := usageObject(body)
	var version string
	if !ok || decode(fields["version"], &version) != nil || version == "" {
		return nil, invalid(check)
	}
	path := keyModeProbeForVersion(version)
	if path == "" {
		return nil, unknown(id, "MODE_REQUIRES_JWT")
	}
	body, check = u.get(ctx, base, path, kind, secret, id, true)
	if check.State != "available" {
		return nil, check
	}
	if check.HTTPStatus != nil && *check.HTTPStatus == http.StatusForbidden {
		mode := "simple"
		return &mode, check
	}
	fields, ok = usageObject(body)
	var models []string
	if !ok || decode(fields["models"], &models) != nil || len(models) == 0 || slices.Contains(models, "") {
		return nil, invalid(check)
	}
	mode := "standard"
	return &mode, check
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
