package connection

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Runtime struct {
	LastUsedAt             *time.Time `json:"last_used_at"`
	RateLimitedAt          *time.Time `json:"rate_limited_at"`
	RateLimitResetAt       *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil          *time.Time `json:"overload_until"`
	TempUnschedulableUntil *time.Time `json:"temp_unschedulable_until"`
	ErrorPresent           *bool      `json:"error_present"`
}

type AccountSnapshot struct {
	Account Account `json:"account"`
	Runtime Runtime `json:"runtime"`
}

type accountObservation struct {
	AccountSnapshot
	identity    string
	fingerprint []byte
}

func safeText(value string, limit int, secret string) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl) && (secret == "" || !strings.Contains(value, secret))
}

func utc(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func inspectAccount(body json.RawMessage, expectedID, secret string) (accountObservation, bool) {
	base, ok := projectAccount(body)
	if !ok || base.UpstreamID != expectedID || !safeText(base.Name, 512, secret) || !safeText(base.Platform, 64, secret) || !safeText(base.Type, 64, secret) || !safeText(base.Status, 64, secret) {
		return accountObservation{}, false
	}
	var raw struct {
		Runtime
		ErrorMessage    *string                    `json:"error_message"`
		ParentAccountID *int64                     `json:"parent_account_id"`
		Credentials     map[string]json.RawMessage `json:"credentials"`
		Extra           json.RawMessage            `json:"extra"`
		CreatedAt       *time.Time                 `json:"created_at"`
		UpdatedAt       *time.Time                 `json:"updated_at"`
	}
	if decode(body, &raw) != nil || raw.ParentAccountID != nil && (*raw.ParentAccountID <= 0 || strconv.FormatInt(*raw.ParentAccountID, 10) == expectedID) {
		return accountObservation{}, false
	}
	// Only derive error presence; upstream error text may contain credentials.
	raw.Runtime.ErrorPresent = nil
	if raw.ErrorMessage != nil {
		present := *raw.ErrorMessage != ""
		raw.Runtime.ErrorPresent = &present
	}
	raw.Runtime.LastUsedAt = utc(raw.Runtime.LastUsedAt)
	raw.Runtime.RateLimitedAt = utc(raw.Runtime.RateLimitedAt)
	raw.Runtime.RateLimitResetAt = utc(raw.Runtime.RateLimitResetAt)
	raw.Runtime.OverloadUntil = utc(raw.Runtime.OverloadUntil)
	raw.Runtime.TempUnschedulableUntil = utc(raw.Runtime.TempUnschedulableUntil)
	result := accountObservation{AccountSnapshot: AccountSnapshot{Account: base, Runtime: raw.Runtime}, identity: "unverified"}
	if base.Platform != "openai" || base.Type != "oauth" {
		result.identity = "unsupported"
		return result, true
	}
	if raw.ParentAccountID != nil {
		result.identity = "shadow"
		return result, true
	}
	// Restricted projections omit these full-detail fields; absence cannot prove a root identity.
	if len(raw.Extra) == 0 || raw.CreatedAt == nil || raw.UpdatedAt == nil || raw.CreatedAt.IsZero() || raw.UpdatedAt.IsZero() {
		return result, true
	}
	if !bytes.Equal(bytes.TrimSpace(raw.Extra), []byte("null")) {
		var extra map[string]json.RawMessage
		if decode(raw.Extra, &extra) != nil || extra == nil {
			return accountObservation{}, false
		}
	}
	var resource string
	for _, key := range []string{"chatgpt_account_id", "organization_id"} {
		if value, exists := raw.Credentials[key]; exists {
			if decode(value, &resource) != nil {
				return accountObservation{}, false
			}
			resource = strings.TrimSpace(resource)
			if resource != "" {
				break
			}
		}
	}
	if resource != "" {
		if !safeText(resource, 1024, secret) {
			return accountObservation{}, false
		}
		digest := sha256.Sum256([]byte("sub-pool:openai-resource:" + resource))
		result.identity, result.fingerprint = "verified", digest[:]
	}
	return result, true
}

type ModelMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type CatalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AccountModels struct {
	AccountID          string         `json:"account_id"`
	SourceID           string         `json:"source_id"`
	ConnectionRevision string         `json:"connection_revision"`
	CheckedAt          time.Time      `json:"checked_at"`
	Models             []CatalogModel `json:"models"`
	Mapping            []ModelMapping `json:"mapping"`
	Passthrough        *bool          `json:"passthrough"`
	MappingMode        string         `json:"mapping_mode"`
	AccessProven       bool           `json:"access_proven"`
	AccountRead        Check          `json:"account_read"`
	CatalogRead        Check          `json:"catalog_read"`
}

func modelConfiguration(body json.RawMessage, secret string) ([]ModelMapping, *bool, bool) {
	var raw struct {
		Credentials map[string]json.RawMessage `json:"credentials"`
		Extra       json.RawMessage            `json:"extra"`
	}
	if decode(body, &raw) != nil {
		return nil, nil, false
	}
	var mappings []ModelMapping
	if value, exists := raw.Credentials["model_mapping"]; exists {
		var mapping map[string]string
		if decode(value, &mapping) != nil || mapping == nil || len(mapping) > 1000 {
			return nil, nil, false
		}
		mappings = []ModelMapping{}
		for from, to := range mapping {
			if !safeText(from, 256, secret) || !safeText(to, 256, secret) {
				return nil, nil, false
			}
			mappings = append(mappings, ModelMapping{From: from, To: to})
		}
		sort.Slice(mappings, func(i, j int) bool { return mappings[i].From < mappings[j].From })
	}
	var passthrough *bool
	if len(raw.Extra) != 0 {
		var extra map[string]json.RawMessage
		if !bytes.Equal(bytes.TrimSpace(raw.Extra), []byte("null")) && (decode(raw.Extra, &extra) != nil || extra == nil) {
			return nil, nil, false
		}
		value := false
		for _, key := range []string{"openai_passthrough", "openai_oauth_passthrough"} {
			if encoded, exists := extra[key]; exists {
				if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) || decode(encoded, &value) != nil {
					return nil, nil, false
				}
				break
			}
		}
		passthrough = &value
	}
	return mappings, passthrough, true
}

func projectModels(body json.RawMessage, secret string) ([]CatalogModel, bool) {
	var raw []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if decode(body, &raw) != nil || raw == nil || len(raw) > 1000 {
		return nil, false
	}
	models := []CatalogModel{}
	seen := make(map[string]bool)
	for _, model := range raw {
		if model.DisplayName == "" {
			model.DisplayName = model.ID
		}
		if !safeText(model.ID, 256, secret) || !safeText(model.DisplayName, 512, secret) || seen[model.ID] {
			return nil, false
		}
		seen[model.ID] = true
		models = append(models, CatalogModel{ID: model.ID, Name: model.DisplayName})
	}
	return models, true
}
