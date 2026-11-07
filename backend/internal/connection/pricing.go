package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type PricingContext struct {
	Revision    string    `json:"revision"`
	SourceID    string    `json:"source_id"`
	CheckedAt   time.Time `json:"checked_at"`
	RunMode     *string   `json:"run_mode"`
	ModeRead    Check     `json:"mode_read"`
	CanApply    bool      `json:"can_apply"`
	WriteReason string    `json:"write_reason"`
}

type PricingGroupsInput struct {
	Revision string `json:"revision"`
	SourceID string `json:"source_id"`
	Page     int    `json:"page"`
	Search   string `json:"search"`
}

type PricingGroups struct {
	PricingContext
	Page     int            `json:"page"`
	Total    *string        `json:"total"`
	NextPage *int           `json:"next_page"`
	Groups   []PricingGroup `json:"groups"`
	Read     Check          `json:"read"`
}

type PricingGroupInput struct {
	Revision string `json:"revision"`
	SourceID string `json:"source_id"`
	GroupID  string `json:"group_id"`
	Page     int    `json:"page"`
}

type PricingGroupRead struct {
	PricingContext
	GroupID       string                `json:"group_id"`
	Group         *PricingGroup         `json:"group"`
	Configuration *PricingConfiguration `json:"configuration"`
	Read          Check                 `json:"read"`
	Page          int                   `json:"page"`
	Total         *string               `json:"total"`
	NextPage      *int                  `json:"next_page"`
	Accounts      []Account             `json:"accounts"`
	AccountsRead  Check                 `json:"accounts_read"`
}

type PricingDefaultInput struct {
	Revision string `json:"revision"`
	SourceID string `json:"source_id"`
	Model    string `json:"model"`
}

type PricingDefault struct {
	PricingContext
	Model       string             `json:"model"`
	Found       *bool              `json:"found"`
	Values      map[string]*string `json:"values"`
	Unreported  []string           `json:"unreported"`
	Unit        string             `json:"unit"`
	Projection  string             `json:"projection"`
	Unprojected []string           `json:"unprojected"`
	Read        Check              `json:"read"`
}

func (s *Service) pricingConfig(ctx context.Context, tx pgx.Tx, token, revision, source string) (storedConfig, string, string, error) {
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return c, "", "", err
	}
	if c.Revision != revision || c.SourceID == nil || *c.SourceID != source {
		return c, "", "", auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return c, "", "", err
	}
	kind, secret, err := s.decrypt(c)
	return c, kind, secret, err
}

func (s *Service) pricingContext(ctx context.Context, c storedConfig, kind, secret string) PricingContext {
	mode, check := s.upstream.pricingMode(ctx, c.BaseURL, kind, secret)
	return PricingContext{Revision: c.Revision, SourceID: *c.SourceID, CheckedAt: time.Now().UTC().Truncate(time.Microsecond), RunMode: mode, ModeRead: check, WriteReason: "PRICING_WRITE_UNQUALIFIED"}
}

func pricingSafe(value any, secret string) bool {
	body, err := json.Marshal(value)
	encoded, encodeErr := json.Marshal(secret)
	return err == nil && encodeErr == nil && (secret == "" || !bytes.Contains(body, encoded[1:len(encoded)-1]))
}

func pricingPage(body json.RawMessage, page int) (pageData, *string, *int, bool) {
	data, ok := parsePage(body, page, 100)
	if !ok || *data.Total > 214748364700 {
		return data, nil, nil, false
	}
	total := strconv.FormatInt(*data.Total, 10)
	var next *int
	if int64(page)*100 < *data.Total {
		value := page + 1
		next = &value
	}
	return data, &total, next, true
}

func (s *Service) ListPricingGroups(ctx context.Context, token string, input PricingGroupsInput) (PricingGroups, error) {
	if !validRevision(input.Revision) || input.SourceID == "" || input.Page < 1 || input.Page > 2147483647 || input.Search != "" && !pricingText(input.Search, 100, "") {
		return PricingGroups{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingGroups{}, err
	}
	defer tx.Rollback(ctx)
	c, kind, secret, err := s.pricingConfig(ctx, tx, token, input.Revision, input.SourceID)
	if err != nil {
		return PricingGroups{}, err
	}
	out := PricingGroups{PricingContext: s.pricingContext(ctx, c, kind, secret), Page: input.Page, Groups: []PricingGroup{}}
	query := url.Values{"platform": {"openai"}, "page": {strconv.Itoa(input.Page)}, "page_size": {"100"}, "search": {input.Search}}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/groups?"+query.Encode(), kind, secret, "pricing.groups.read", true)
	out.Read = check
	if check.State == "available" {
		page, total, next, ok := pricingPage(body, input.Page)
		seen := make(map[string]bool)
		for _, item := range page.Items {
			group, valid := projectPricingGroup(item, secret)
			if !valid || seen[group.ID] {
				ok = false
				break
			}
			seen[group.ID] = true
			out.Groups = append(out.Groups, group)
		}
		if ok && pricingSafe(out.Groups, secret) {
			out.Total, out.NextPage = total, next
		} else {
			out.Groups, out.Read = []PricingGroup{}, invalid(check)
		}
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return PricingGroups{}, err
	}
	return out, nil
}

func (s *Service) ReadPricingGroup(ctx context.Context, token string, input PricingGroupInput) (PricingGroupRead, error) {
	if !validRevision(input.Revision) || input.SourceID == "" || !validRevision(input.GroupID) || input.Page < 1 || input.Page > 2147483647 {
		return PricingGroupRead{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingGroupRead{}, err
	}
	defer tx.Rollback(ctx)
	c, kind, secret, err := s.pricingConfig(ctx, tx, token, input.Revision, input.SourceID)
	if err != nil {
		return PricingGroupRead{}, err
	}
	out := PricingGroupRead{PricingContext: s.pricingContext(ctx, c, kind, secret), GroupID: input.GroupID, Page: input.Page, Accounts: []Account{}, AccountsRead: unknown("pricing.accounts.read", "GROUP_NOT_VERIFIED")}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/groups/"+input.GroupID, kind, secret, "pricing.group.read", true)
	out.Read = check
	if check.State == "available" {
		group, ok := projectPricingGroup(body, secret)
		configuration, valid := projectPricingConfiguration(body, secret)
		if !ok || !valid || group.ID != input.GroupID || !pricingSafe(configuration, secret) {
			out.Read = invalid(check)
		} else {
			out.Group, out.Configuration = &group, &configuration
			query := url.Values{"group": {input.GroupID}, "platform": {"openai"}, "page": {strconv.Itoa(input.Page)}, "page_size": {"100"}}
			body, check = s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts?"+query.Encode(), kind, secret, "pricing.accounts.read", true)
			out.AccountsRead = check
			if check.State == "available" {
				page, total, next, ok := pricingPage(body, input.Page)
				seen := make(map[string]bool)
				for _, item := range page.Items {
					account, valid := projectAccount(item)
					var associations struct {
						GroupIDs []int64 `json:"group_ids"`
					}
					member := false
					if decode(item, &associations) == nil {
						for _, id := range associations.GroupIDs {
							member = member || strconv.FormatInt(id, 10) == input.GroupID
						}
					}
					if !valid || account.Platform != "openai" || !member || seen[account.UpstreamID] {
						ok = false
						break
					}
					seen[account.UpstreamID] = true
					out.Accounts = append(out.Accounts, account)
				}
				if ok && pricingSafe(out.Accounts, secret) {
					out.Total, out.NextPage = total, next
				} else {
					out.Accounts, out.AccountsRead = []Account{}, invalid(check)
				}
			}
		}
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return PricingGroupRead{}, err
	}
	return out, nil
}

func (s *Service) ReadPricingDefault(ctx context.Context, token string, input PricingDefaultInput) (PricingDefault, error) {
	if !validRevision(input.Revision) || input.SourceID == "" || !pricingText(input.Model, 256, "") {
		return PricingDefault{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return PricingDefault{}, err
	}
	defer tx.Rollback(ctx)
	c, kind, secret, err := s.pricingConfig(ctx, tx, token, input.Revision, input.SourceID)
	if err != nil {
		return PricingDefault{}, err
	}
	out := PricingDefault{PricingContext: s.pricingContext(ctx, c, kind, secret), Model: input.Model, Unit: "USD_per_token", Projection: "base_fields_only", Unreported: []string{}, Unprojected: []string{"reasoning_effort_multipliers"}}
	query := url.Values{"model": {input.Model}}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/channels/model-pricing?"+query.Encode(), kind, secret, "pricing.default.read", true)
	out.Read = check
	if check.State == "available" {
		fields, ok := usageObject(body)
		var found *bool
		if !ok || decode(fields["found"], &found) != nil || found == nil {
			out.Read = invalid(check)
		} else if !*found {
			out.Found = found
		} else {
			values, unreported, ok := projectPricingValues(fields, pricingAmounts[:7])
			if !ok || !pricingSafe(values, secret) {
				out.Read = invalid(check)
			} else {
				out.Found, out.Values, out.Unreported = found, values, unreported
			}
		}
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return PricingDefault{}, err
	}
	return out, nil
}
