package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type PricingGroup struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       *string `json:"status"`
	AccountCount *string `json:"account_count"`
}

var pricingAmounts = []string{"input_price", "output_price", "cache_write_price", "cache_write_1h_price", "cache_read_price", "image_input_price", "image_output_price", "per_request_price", "fast_multiplier", "flex_multiplier"}

type PricingRule struct {
	Models        []string           `json:"models"`
	BillingMode   string             `json:"billing_mode"`
	Values        map[string]*string `json:"values"`
	Unreported    []string           `json:"unreported"`
	Unprojected   []string           `json:"unprojected"`
	NativeDetails map[string]string  `json:"native_details"`
}

type PricingConfiguration struct {
	RateMultiplier            *string        `json:"rate_multiplier"`
	ForceOpenAIFast           *bool          `json:"force_openai_fast"`
	FreeOpenAIFast            *bool          `json:"free_openai_fast"`
	LongContextPricingEnabled *bool          `json:"long_context_pricing_enabled"`
	ModelRules                *[]PricingRule `json:"model_rules"`
	Unreported                []string       `json:"unreported"`
	Projection                string         `json:"projection"`
	Unit                      string         `json:"unit"`
}

func pricingText(value string, limit int, secret string) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0 && (secret == "" || !strings.Contains(value, secret))
}

func projectPricingGroup(body json.RawMessage, secret string) (PricingGroup, bool) {
	fields, ok := usageObject(body)
	id, valid := usageID(fields["id"], true)
	var name, platform string
	if !ok || !valid || decode(fields["name"], &name) != nil || !pricingText(name, 512, secret) || decode(fields["platform"], &platform) != nil || platform != "openai" {
		return PricingGroup{}, false
	}
	out := PricingGroup{ID: *id, Name: name}
	if raw := fields["status"]; raw != nil {
		var status string
		if decode(raw, &status) != nil || !pricingText(status, 64, secret) {
			return PricingGroup{}, false
		}
		out.Status = &status
	}
	if raw := fields["account_count"]; raw != nil {
		var count *int64
		if decode(raw, &count) != nil || count == nil || *count < 0 {
			return PricingGroup{}, false
		}
		value := strconv.FormatInt(*count, 10)
		out.AccountCount = &value
	}
	return out, true
}

func projectPricingValues(fields map[string]json.RawMessage, names []string) (map[string]*string, []string, bool) {
	values, unreported := make(map[string]*string), []string{}
	for _, name := range names {
		raw := fields[name]
		values[name] = nil
		if raw == nil {
			unreported = append(unreported, name)
		} else if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			value, ok := walletNumber(raw, true)
			if !ok {
				return nil, nil, false
			}
			values[name] = value
		}
	}
	return values, unreported, true
}

func projectPricingRule(body json.RawMessage, secret string) (PricingRule, bool) {
	fields, ok := usageObject(body)
	out := PricingRule{Unprojected: []string{}, NativeDetails: map[string]string{}}
	var platform string
	if !ok || decode(fields["platform"], &platform) != nil || platform != "openai" || decode(fields["models"], &out.Models) != nil || len(out.Models) == 0 || len(out.Models) > 100 || decode(fields["billing_mode"], &out.BillingMode) != nil {
		return PricingRule{}, false
	}
	if out.BillingMode != "" && out.BillingMode != "token" && out.BillingMode != "per_request" && out.BillingMode != "image" && out.BillingMode != "video" {
		return PricingRule{}, false
	}
	for _, model := range out.Models {
		if !pricingText(model, 256, secret) {
			return PricingRule{}, false
		}
	}
	out.Values, out.Unreported, ok = projectPricingValues(fields, pricingAmounts)
	if !ok {
		return PricingRule{}, false
	}
	for _, name := range []string{"intervals", "time_pricing", "reasoning_effort_multipliers"} {
		raw := bytes.TrimSpace(fields[name])
		if len(raw) == 0 {
			out.Unprojected = append(out.Unprojected, name)
			continue
		}
		value, valid := projectPricingDetail(raw, secret)
		if !valid {
			out.Unprojected = append(out.Unprojected, name)
			continue
		}
		out.NativeDetails[name] = value
	}
	return out, true
}

func projectPricingDetail(raw json.RawMessage, secret string) (string, bool) {
	// JSON text preserves exact native decimals; it is configuration, not a locally resolved price.
	value, valid := canonicalPricing(raw, secret)
	if !valid || len(value) > 65536 {
		return "", false
	}
	var readable bytes.Buffer
	if json.Indent(&readable, value, "", " ") != nil || readable.Len() > 65536 {
		return "", false
	}
	return readable.String(), true
}

func projectPricingConfiguration(body json.RawMessage, secret string) (PricingConfiguration, bool) {
	fields, ok := usageObject(body)
	out := PricingConfiguration{Unreported: []string{}, Projection: "base_fields_only", Unit: "native_config_value"}
	if !ok {
		return out, false
	}
	if raw := fields["rate_multiplier"]; raw == nil {
		out.Unreported = append(out.Unreported, "rate_multiplier")
	} else if out.RateMultiplier, ok = walletNumber(raw, true); !ok {
		return out, false
	}
	for _, field := range []struct {
		name string
		dest **bool
	}{{"force_openai_fast", &out.ForceOpenAIFast}, {"free_openai_fast", &out.FreeOpenAIFast}, {"long_context_pricing_enabled", &out.LongContextPricingEnabled}} {
		raw := fields[field.name]
		if raw == nil {
			out.Unreported = append(out.Unreported, field.name)
		} else {
			var value bool
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || decode(raw, &value) != nil {
				return out, false
			}
			*field.dest = &value
		}
	}
	if raw := fields["model_pricing"]; raw == nil {
		out.Unreported = append(out.Unreported, "model_pricing")
	} else {
		var rules []json.RawMessage
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && decode(raw, &rules) != nil || len(rules) > 100 {
			return out, false
		}
		projected := []PricingRule{}
		for _, rule := range rules {
			value, valid := projectPricingRule(rule, secret)
			if !valid {
				return out, false
			}
			projected = append(projected, value)
		}
		out.ModelRules = &projected
	}
	return out, true
}

func (u *upstreamClient) readPricingGroup(ctx context.Context, base, kind, secret, groupID, checkID string) (PricingGroup, PricingConfiguration, Check) {
	body, check := u.get(ctx, base, "/api/v1/admin/groups/"+groupID, kind, secret, checkID, true)
	if check.State != "available" {
		return PricingGroup{}, PricingConfiguration{}, check
	}
	group, ok := projectPricingGroup(body, secret)
	configuration, valid := projectPricingConfiguration(body, secret)
	if !ok || !valid || group.ID != groupID || !pricingSafe(configuration, secret) {
		return PricingGroup{}, PricingConfiguration{}, invalid(check)
	}
	return group, configuration, check
}

func (u *upstreamClient) diagnosePricing(ctx context.Context, base, kind, secret string) Check {
	const id = "pricing.read"
	if secret == "" {
		return unknown(id, "NOT_CONFIGURED")
	}
	body, check := u.get(ctx, base, "/api/v1/admin/groups?page=1&page_size=1&platform=openai", kind, secret, id, true)
	if check.State != "available" {
		return check
	}
	page, valid := parsePage(body, 1, 1)
	if !valid {
		return invalid(check)
	}
	if len(page.Items) == 0 {
		check.State, check.Reason = "unverified", "NO_VISIBLE_RESOURCE"
		return check
	}
	group, valid := projectPricingGroup(page.Items[0], secret)
	if !valid || !pricingSafe(group, secret) {
		return invalid(check)
	}
	_, config, check := u.readPricingGroup(ctx, base, kind, secret, group.ID, id)
	if check.State == "available" {
		if len(config.Unreported) != 0 {
			check.State, check.Reason = "unverified", "PRICING_FIELDS_UNREPORTED"
		} else {
			check.Evidence = &CapabilityEvidence{UpstreamID: group.ID, VerifiedAt: time.Now().UTC().Truncate(time.Microsecond)}
		}
	}
	return check
}
