package connection

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type QuotaWindow struct {
	Kind            string     `json:"kind"`
	DurationSeconds *string    `json:"duration_seconds"`
	UsedPercent     *string    `json:"used_percent"`
	ResetAt         *time.Time `json:"reset_at"`
	ResetExpired    bool       `json:"reset_expired"`
}

type QuotaSnapshot struct {
	SourceID           string                `json:"source_id"`
	ConnectionRevision string                `json:"connection_revision"`
	AccountRevision    string                `json:"account_revision"`
	SourceMode         string                `json:"source_mode"`
	Simulation         *SimulationProvenance `json:"simulation,omitempty"`
	SourceAt           *time.Time            `json:"source_at"`
	ReceivedAt         time.Time             `json:"received_at"`
	Windows            []QuotaWindow         `json:"windows"`
}

// PostgreSQL receipt precision must not move an already received source time into the future.
func ceilMicrosecond(at time.Time) time.Time {
	rounded := at.Truncate(time.Microsecond)
	if rounded.Before(at) {
		rounded = rounded.Add(time.Microsecond)
	}
	return rounded
}

func percentText(raw json.RawMessage) (*string, bool) {
	value := strings.TrimSpace(string(raw))
	if !validNumber(raw) || len(value) > 64 || strings.HasPrefix(value, "-") {
		return nil, false
	}
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(value[i+1:])
		if err != nil || exponent < -18 || exponent > 18 {
			return nil, false
		}
	}
	return &value, true
}

func snapshotTime(raw json.RawMessage) (*time.Time, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var value string
	if decode(raw, &value) != nil {
		return nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.IsZero() {
		return nil, false
	}
	t = t.UTC()
	return &t, true
}

func passiveQuota(body json.RawMessage) (QuotaSnapshot, bool) {
	result := QuotaSnapshot{SourceMode: "passive", Windows: []QuotaWindow{}}
	var raw struct {
		Extra map[string]json.RawMessage `json:"extra"`
	}
	if decode(body, &raw) != nil {
		return result, false
	}
	var valid bool
	result.SourceAt, valid = snapshotTime(raw.Extra["codex_usage_updated_at"])
	if !valid {
		return result, false
	}
	for _, item := range []struct{ prefix, kind string }{{"codex_5h_", "short"}, {"codex_7d_", "seven_day"}} {
		window := QuotaWindow{Kind: item.kind}
		present := false
		for _, suffix := range []string{"used_percent", "window_minutes", "reset_at", "reset_after_seconds"} {
			encoded, exists := raw.Extra[item.prefix+suffix]
			if !exists || string(encoded) == "null" {
				continue
			}
			present = true
			switch suffix {
			case "used_percent":
				window.UsedPercent, valid = percentText(encoded)
			case "window_minutes":
				var minutes int64
				valid = decode(encoded, &minutes) == nil && minutes >= 0 && minutes <= 5256000
				// Native snapshots can report zero when a window duration is unavailable.
				if valid && minutes > 0 {
					seconds := strconv.FormatInt(minutes*60, 10)
					window.DurationSeconds = &seconds
				}
			case "reset_at":
				window.ResetAt, valid = snapshotTime(encoded)
			case "reset_after_seconds":
				var seconds int64
				valid = decode(encoded, &seconds) == nil && seconds >= 0 && seconds <= 31536000
				if valid && window.ResetAt == nil && result.SourceAt != nil {
					t := result.SourceAt.Add(time.Duration(seconds) * time.Second)
					window.ResetAt = &t
				}
			}
			if !valid {
				return result, false
			}
		}
		if present {
			result.Windows = append(result.Windows, window)
		}
	}
	return result, true
}
