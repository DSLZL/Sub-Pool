package connection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type PricingChange struct {
	Kind       string  `json:"kind"`
	ForceFast  *bool   `json:"force_fast,omitempty"`
	FreeFast   *bool   `json:"free_fast,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
	RuleIndex  *int    `json:"rule_index,omitempty"`
	PriceField string  `json:"price_field,omitempty"`
	Value      *string `json:"value"`
}

func validPricingChanges(changes []PricingChange) bool {
	if len(changes) == 0 || len(changes) > 30 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range changes {
		key := c.Kind
		switch c.Kind {
		case "fast":
			if c.ForceFast == nil || c.FreeFast == nil || c.Enabled != nil || c.RuleIndex != nil || c.PriceField != "" || c.Value != nil {
				return false
			}
		case "long_context":
			if c.Enabled == nil || c.ForceFast != nil || c.FreeFast != nil || c.RuleIndex != nil || c.PriceField != "" || c.Value != nil {
				return false
			}
		case "model_price":
			if c.RuleIndex == nil || *c.RuleIndex < 0 || *c.RuleIndex >= 100 || c.Enabled != nil || c.ForceFast != nil || c.FreeFast != nil {
				return false
			}
			found := false
			for _, name := range pricingAmounts {
				found = found || c.PriceField == name
			}
			if !found || c.Value != nil && !nativePricingNumber(*c.Value) {
				return false
			}
			key += ":" + strconv.Itoa(*c.RuleIndex) + ":" + c.PriceField
		default:
			return false
		}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

// Native price fields are float64; reject values that would change on its JSON round trip.
func nativePricingNumber(value string) bool {
	normal, ok := walletNumber(json.RawMessage(value), true)
	f, err := strconv.ParseFloat(value, 64)
	encoded, marshalErr := json.Marshal(f)
	back, valid := walletNumber(encoded, true)
	return ok && err == nil && marshalErr == nil && valid && *normal == value && *back == value
}

type pricingNative struct {
	GroupID     string                     `json:"group_id"`
	Name        string                     `json:"name"`
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
	Values      map[string]json.RawMessage `json:"values"`
	Fingerprint string                     `json:"fingerprint"`
}

// Preserve complete existing model rules, including intervals and time/reasoning multipliers.
func canonicalPricing(raw json.RawMessage, secret string) (json.RawMessage, bool) {
	allowed := "|rate_multiplier|force_openai_fast|free_openai_fast|long_context_pricing_enabled|model_pricing|id|channel_id|platform|models|billing_mode|input_price|output_price|cache_write_price|cache_write_1h_price|cache_read_price|image_input_price|image_output_price|per_request_price|fast_multiplier|flex_multiplier|reasoning_effort_multipliers|minimal|low|medium|high|xhigh|none|auto|intervals|pricing_id|min_tokens|max_tokens|tier_label|input_multiplier|output_multiplier|cache_write_multiplier|cache_read_multiplier|sort_order|created_at|updated_at|time_pricing|timezone|weekdays_only|periods|start_time|end_time|multiplier|"
	var normalize func(json.RawMessage, int) (any, bool)
	normalize = func(body json.RawMessage, depth int) (any, bool) {
		if depth > 8 || len(body) == 0 {
			return nil, false
		}
		body = bytes.TrimSpace(body)
		switch body[0] {
		case '{':
			fields, ok := usageObject(body)
			if !ok || len(fields) > 40 {
				return nil, false
			}
			out := map[string]any{}
			for name, value := range fields {
				if strings.Contains(name, "|") || !strings.Contains(allowed, "|"+name+"|") {
					return nil, false
				}
				out[name], ok = normalize(value, depth+1)
				if !ok {
					return nil, false
				}
			}
			return out, true
		case '[':
			var items []json.RawMessage
			if decode(body, &items) != nil || len(items) > 100 {
				return nil, false
			}
			out := make([]any, len(items))
			for i, item := range items {
				var ok bool
				out[i], ok = normalize(item, depth+1)
				if !ok {
					return nil, false
				}
			}
			return out, true
		case '"':
			var value string
			if decode(body, &value) != nil || len(value) > 512 || value != "" && !pricingText(value, 512, secret) {
				return nil, false
			}
			return value, true
		case 't', 'f':
			var value bool
			ok := decode(body, &value) == nil
			return value, ok
		case 'n':
			return nil, bytes.Equal(body, []byte("null"))
		default:
			value, ok := walletNumber(body, true)
			if !ok {
				return nil, false
			}
			return json.Number(*value), true
		}
	}
	value, ok := normalize(raw, 0)
	if !ok {
		return nil, false
	}
	body, err := json.Marshal(value)
	return body, err == nil
}

func inspectPricingNative(body json.RawMessage, groupID, secret string) (pricingNative, bool) {
	group, ok := projectPricingGroup(body, secret)
	config, valid := projectPricingConfiguration(body, secret)
	fields, object := usageObject(body)
	created, validCreated := snapshotTime(fields["created_at"])
	updated, validUpdated := snapshotTime(fields["updated_at"])
	if !ok || !valid || !object || group.ID != groupID || len(config.Unreported) != 0 || !validCreated || !validUpdated || created == nil || updated == nil || updated.Before(*created) {
		return pricingNative{}, false
	}
	n := pricingNative{GroupID: groupID, Name: group.Name, CreatedAt: created.UTC(), UpdatedAt: updated.UTC(), Values: map[string]json.RawMessage{}}
	for _, name := range []string{"rate_multiplier", "force_openai_fast", "free_openai_fast", "long_context_pricing_enabled", "model_pricing"} {
		raw := fields[name]
		if name == "model_pricing" && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			raw = json.RawMessage(`[]`)
		}
		n.Values[name], ok = canonicalPricing(raw, secret)
		if !ok {
			return pricingNative{}, false
		}
	}
	n.Fingerprint = pricingFingerprint(n)
	return n, true
}

func pricingFingerprint(n pricingNative) string {
	n.Fingerprint = ""
	body, _ := json.Marshal(n)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func pricingTarget(before pricingNative, changes []PricingChange) (pricingNative, []byte, error) {
	body, _ := json.Marshal(before)
	var target pricingNative
	_ = json.Unmarshal(body, &target)
	patch := map[string]json.RawMessage{}
	for _, change := range changes {
		switch change.Kind {
		case "fast":
			patch["force_openai_fast"], _ = json.Marshal(*change.ForceFast)
			patch["free_openai_fast"], _ = json.Marshal(*change.FreeFast)
		case "long_context":
			patch["long_context_pricing_enabled"], _ = json.Marshal(*change.Enabled)
		case "model_price":
			var rules []map[string]json.RawMessage
			if json.Unmarshal(target.Values["model_pricing"], &rules) != nil || *change.RuleIndex >= len(rules) {
				return target, nil, errors.New("pricing rule unavailable")
			}
			value := json.RawMessage(`null`)
			if change.Value != nil {
				value = json.RawMessage(*change.Value)
			}
			rules[*change.RuleIndex][change.PriceField] = value
			patch["model_pricing"], _ = json.Marshal(rules)
		}
		for name, value := range patch {
			target.Values[name] = value
		}
	}
	if pricingValuesEqual(before, target) {
		return target, nil, errors.New("pricing change has no effect")
	}
	body, _ = json.Marshal(patch)
	return target, body, nil
}

func pricingValuesEqual(a, b pricingNative) bool {
	x, _ := json.Marshal(a.Values)
	y, _ := json.Marshal(b.Values)
	x, validX := canonicalPricing(x, "")
	y, validY := canonicalPricing(y, "")
	return validX && validY && a.GroupID == b.GroupID && a.Name == b.Name && a.CreatedAt.Equal(b.CreatedAt) && bytes.Equal(x, y)
}
