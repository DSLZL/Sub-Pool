package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
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
	Models      []string           `json:"models"`
	BillingMode string             `json:"billing_mode"`
	Values      map[string]*string `json:"values"`
	Unreported  []string           `json:"unreported"`
	Unprojected []string           `json:"unprojected"`
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
	out := PricingRule{Unprojected: []string{}}
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
	// shortcut: complex native rules are not resolved here; project them before effective-price or write support.
	for _, name := range []string{"intervals", "time_pricing", "reasoning_effort_multipliers"} {
		raw := bytes.TrimSpace(fields[name])
		if len(raw) == 0 || !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("[]")) && !bytes.Equal(raw, []byte("{}")) {
			out.Unprojected = append(out.Unprojected, name)
		}
	}
	return out, true
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

func (u *upstreamClient) pricingMode(ctx context.Context, base, kind, secret string) (*string, Check) {
	check := unknown("run_mode", "MODE_REQUIRES_JWT")
	if kind != "bearer" {
		return nil, check
	}
	body, check := u.get(ctx, base, "/api/v1/auth/me", kind, secret, "run_mode", true)
	if check.State != "available" {
		return nil, check
	}
	var profile struct{ Role, RunMode string }
	fields, ok := usageObject(body)
	if !ok || decode(fields["role"], &profile.Role) != nil || profile.Role != "admin" || decode(fields["run_mode"], &profile.RunMode) != nil || profile.RunMode != "standard" && profile.RunMode != "simple" {
		return nil, invalid(check)
	}
	return &profile.RunMode, check
}
