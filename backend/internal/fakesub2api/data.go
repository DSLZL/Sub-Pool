package fakesub2api

import (
	"encoding/json"
	"fmt"
	"time"
)

const model = "requested-model"

// AccountData returns the account row shared with the connection tests.
func AccountData(id int) map[string]any {
	return map[string]any{
		"id": id, "name": fmt.Sprintf("Account %d", id), "platform": "openai", "type": "oauth",
		"status": "active", "schedulable": true,
		"credentials": map[string]string{"access_token": "must-never-leave-upstream"},
		"extra":       map[string]any{"is_shadow": true},
	}
}

// UsageRow returns the usage row shared with the connection tests.
func UsageRow(id int64, stamp string) map[string]any {
	return map[string]any{
		"id": id, "account_id": 1, "user_id": 21, "api_key_id": 31, "model": model, "upstream_model": "upstream-model",
		"created_at": stamp, "input_tokens": 10, "output_tokens": 0, "cache_read_tokens": 2,
		"total_cost": json.Number("0.1"), "actual_cost": json.Number("0.2"), "billing_type": 0,
		"api_key": map[string]any{"key": "private-model-key"}, "user": map[string]any{"password": "private-password"}, "ip_address": "private-ip",
	}
}

func managedAccount(at time.Time) map[string]any {
	account := AccountData(1)
	account["credentials"] = map[string]any{
		"access_token": "private-account-token", "chatgpt_account_id": "fixture-resource-1", "model_mapping": map[string]string{},
	}
	account["extra"] = map[string]any{}
	account["created_at"], account["updated_at"], account["last_used_at"] = at, at, at
	account["error_message"], account["group_ids"] = "", []int{1}
	return account
}

func pricingGroup(at time.Time) map[string]any {
	return map[string]any{
		"id": 1, "name": "Fixture group", "platform": "openai", "status": "active", "account_count": 1,
		"created_at": at, "updated_at": at, "rate_multiplier": json.Number("1.25"),
		"force_openai_fast": false, "free_openai_fast": false, "long_context_pricing_enabled": true,
		"model_pricing": []map[string]any{{
			"platform": "openai", "models": []string{model}, "billing_mode": "token",
			"input_price": json.Number("0.1"), "output_price": json.Number("2"),
		}},
	}
}
