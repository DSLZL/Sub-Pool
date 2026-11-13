package connection

import (
	"bytes"
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

type WalletField struct {
	Value  *string `json:"value"`
	State  string  `json:"state"`
	Reason string  `json:"reason"`
}

var walletNumeric = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE]([+-]?[0-9]+))?$`)

func walletNumber(raw json.RawMessage, frozen bool) (*string, bool) {
	text := string(bytes.TrimSpace(raw))
	if len(text) > 64 {
		return nil, false
	}
	parts := walletNumeric.FindStringSubmatch(text)
	if parts == nil {
		return nil, false
	}
	if parts[1] != "" {
		exponent, err := strconv.Atoi(parts[1])
		if err != nil || exponent < -18 || exponent > 18 {
			return nil, false
		}
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok || frozen && n.Sign() < 0 {
		return nil, false
	}
	value := usageDecimal(n)
	digits := strings.Split(strings.TrimPrefix(value, "-"), ".")
	back, valid := new(big.Rat).SetString(value)
	if len(digits[0]) > 38 || len(digits) == 2 && len(digits[1]) > 36 || !valid || back.Cmp(n) != 0 {
		return nil, false
	}
	return &value, true
}

func walletField(raw json.RawMessage, frozen, authority bool) WalletField {
	out := WalletField{State: "unverified", Reason: "FIELD_NOT_RETURNED"}
	if !authority {
		out.Reason = "FIELD_PERMISSION_UNVERIFIED"
		return out
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return out
	}
	value, ok := walletNumber(raw, frozen)
	if !ok {
		out.State, out.Reason = "invalid", "INVALID_RESPONSE"
		return out
	}
	out.Value, out.State, out.Reason = value, "reported", "NATIVE_FIELD_REPORTED"
	return out
}

func walletAdmin(body json.RawMessage) bool {
	fields, ok := usageObject(body)
	var role string
	return ok && decode(fields["role"], &role) == nil && role == "admin"
}

func walletBalanceAuthority(body json.RawMessage) bool {
	fields, ok := usageObject(body)
	page, valid := parsePage(body, 1, 1)
	var pages int64
	if !ok || !valid || decode(fields["pages"], &pages) != nil || pages != max(1, *page.Total) {
		return false
	}
	for _, item := range page.Items {
		if _, ok := usageObject(item); !ok {
			return false
		}
	}
	total, ok := walletNumber(fields["total_recharged"], false)
	return ok && total != nil
}
