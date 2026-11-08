package connection

import (
	"encoding/json"
	"testing"
)

func TestNativeNumberRejectsTrailingTokensBeforeQuotaProjection(t *testing.T) {
	for _, value := range []string{"0", "100.000000000000000001", "1e-18", " 1.5 "} {
		if !validNumber(json.RawMessage(value)) {
			t.Fatalf("valid exact JSON number rejected: %q", value)
		}
	}
	for _, value := range []string{"1/2", "1 2", "0 trailing", "1true", "01", "+1", "NaN", "null", `"1"`} {
		if validNumber(json.RawMessage(value)) {
			t.Fatalf("numeric prefix accepted as a JSON number: %q", value)
		}
		if _, ok := percentText(json.RawMessage(value)); ok {
			t.Fatalf("invalid native quota accepted: %q", value)
		}
	}
}
