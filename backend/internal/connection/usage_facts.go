package connection

import (
	"bytes"
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

var usageMeasures = []string{
	"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
	"cache_creation_5m_tokens", "cache_creation_1h_tokens", "image_input_tokens", "image_output_tokens",
	"total_cost", "actual_cost", "account_stats_cost",
}

type UsageFact struct {
	ID                    string             `json:"id"`
	UserID                *string            `json:"user_id"`
	APIKeyID              *string            `json:"api_key_id"`
	GroupID               *string            `json:"group_id"`
	SubscriptionID        *string            `json:"subscription_id"`
	RequestID             *string            `json:"request_id"`
	Model                 string             `json:"model"`
	UpstreamModel         *string            `json:"upstream_model"`
	UpstreamResponseModel *string            `json:"upstream_response_model"`
	BillingMode           *string            `json:"billing_mode"`
	BillingType           *int8              `json:"billing_type"`
	BillingCandidate      string             `json:"billing_candidate"`
	DeductionState        string             `json:"deduction_state"`
	RateMultiplier        *string            `json:"rate_multiplier"`
	AccountRateMultiplier *string            `json:"account_rate_multiplier"`
	CreatedAt             time.Time          `json:"created_at"`
	Metrics               map[string]*string `json:"metrics"`
}

// Bound exact decimals before big-number work; no binary float or current-price reconstruction.
func usageNumber(raw json.RawMessage, integer bool) (*string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	text, ok := percentText(raw)
	if !ok {
		return nil, false
	}
	n, ok := new(big.Rat).SetString(*text)
	if !ok || n.Sign() < 0 || integer && (!n.IsInt() || !n.Num().IsInt64()) {
		return nil, false
	}
	value := usageDecimal(n)
	parts := strings.Split(value, ".")
	if len(parts[0]) > 38 || len(parts) == 2 && len(parts[1]) > 36 {
		return nil, false
	}
	// FloatString must not silently round a longer input fraction.
	back, _ := new(big.Rat).SetString(value)
	if back.Cmp(n) != 0 {
		return nil, false
	}
	return &value, true
}

func usageDecimal(n *big.Rat) string {
	return strings.TrimSuffix(strings.TrimRight(n.FloatString(36), "0"), ".")
}

func usageID(raw json.RawMessage, required bool) (*string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, !required
	}
	var id int64
	if decode(raw, &id) != nil || id <= 0 {
		return nil, false
	}
	value := strconv.FormatInt(id, 10)
	return &value, true
}

func usageObject(body json.RawMessage) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || len(fields) >= 256 || fields[key] != nil {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, false
	}
	return fields, true
}

func projectUsage(body json.RawMessage, account, secret string) (UsageFact, bool) {
	fields, ok := usageObject(body)
	if !ok {
		return UsageFact{}, false
	}
	accountID, ok := usageID(fields["account_id"], true)
	if !ok || *accountID != account {
		return UsageFact{}, false
	}
	id, ok := usageID(fields["id"], true)
	if !ok {
		return UsageFact{}, false
	}
	f := UsageFact{ID: *id, Metrics: make(map[string]*string), BillingCandidate: "unknown", DeductionState: "unknown"}
	for name, dest := range map[string]**string{"user_id": &f.UserID, "api_key_id": &f.APIKeyID, "group_id": &f.GroupID, "subscription_id": &f.SubscriptionID} {
		*dest, ok = usageID(fields[name], false)
		if !ok {
			return UsageFact{}, false
		}
	}
	for name, dest := range map[string]**string{"request_id": &f.RequestID, "model": nil, "upstream_model": &f.UpstreamModel, "upstream_response_model": &f.UpstreamResponseModel, "billing_mode": &f.BillingMode} {
		raw := fields[name]
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if name == "model" {
				return UsageFact{}, false
			}
			continue
		}
		var value string
		if decode(raw, &value) != nil {
			return UsageFact{}, false
		}
		if value == "" && name != "model" {
			continue
		}
		if !safeText(value, 512, secret) {
			return UsageFact{}, false
		}
		if name == "model" {
			f.Model = value
		} else {
			*dest = &value
		}
	}
	if len(fields["billing_type"]) != 0 && string(fields["billing_type"]) != "null" {
		if decode(fields["billing_type"], &f.BillingType) != nil {
			return UsageFact{}, false
		}
		switch *f.BillingType {
		case 0:
			f.BillingCandidate = "wallet"
		case 1:
			f.BillingCandidate = "subscription"
		}
	}
	created, ok := snapshotTime(fields["created_at"])
	if !ok || created == nil {
		return UsageFact{}, false
	}
	f.CreatedAt = *created
	for _, name := range usageMeasures {
		f.Metrics[name], ok = usageNumber(fields[name], strings.HasSuffix(name, "_tokens"))
		if !ok {
			return UsageFact{}, false
		}
	}
	for name, dest := range map[string]**string{"rate_multiplier": &f.RateMultiplier, "account_rate_multiplier": &f.AccountRateMultiplier} {
		*dest, ok = usageNumber(fields[name], false)
		if !ok {
			return UsageFact{}, false
		}
	}
	return f, true
}

type UsageSummary struct {
	RequestCount string             `json:"request_count"`
	Metrics      map[string]*string `json:"metrics"`
}

func summarizeUsage(facts []UsageFact) UsageSummary {
	result := UsageSummary{RequestCount: strconv.Itoa(len(facts)), Metrics: make(map[string]*string)}
	for _, name := range usageMeasures {
		sum, known := new(big.Rat), len(facts) > 0
		for _, f := range facts {
			if f.Metrics[name] == nil {
				known = false
				break
			}
			n, _ := new(big.Rat).SetString(*f.Metrics[name])
			sum.Add(sum, n)
		}
		result.Metrics[name] = nil
		if known {
			value := usageDecimal(sum)
			result.Metrics[name] = &value
		}
	}
	return result
}

type UsageBucket struct {
	StartAt time.Time `json:"start_at"`
	EndAt   time.Time `json:"end_at"`
	UsageSummary
}

type usageCivilPeriod struct{ start, end time.Time }

// A midnight rollback can put the same civil date in disjoint UTC intervals.
func usageCalendarPeriods(date time.Time, grain string, location *time.Location) []usageCivilPeriod {
	end := date.AddDate(0, 0, 1)
	if grain == "month" {
		end = date.AddDate(0, 1, 0)
	}
	periods := []usageCivilPeriod{}
	until := end.Add(48 * time.Hour)
	for at := date.Add(-48 * time.Hour); at.Before(until); {
		local := at.In(location)
		_, offset := local.Zone()
		_, zoneEnd := local.ZoneBounds()
		to := until
		if !zoneEnd.IsZero() && zoneEnd.Before(to) {
			to = zoneEnd.UTC()
		}
		from, through := date.Add(-time.Duration(offset)*time.Second), end.Add(-time.Duration(offset)*time.Second)
		if from.Before(at) {
			from = at
		}
		if through.After(to) {
			through = to
		}
		if from.Before(through) {
			if n := len(periods); n > 0 && periods[n-1].end.Equal(from) {
				periods[n-1].end = through
			} else {
				periods = append(periods, usageCivilPeriod{from, through})
			}
		}
		at = to
	}
	return periods
}

func usageBuckets(facts []UsageFact, start, end time.Time, grain string, location *time.Location) []UsageBucket {
	groups := make(map[usageCivilPeriod][]UsageFact)
	calendar := make(map[string][]usageCivilPeriod)
	for _, f := range facts {
		from := f.CreatedAt.UTC().Truncate(time.Hour)
		key := usageCivilPeriod{from, from.Add(time.Hour)}
		if grain != "hour" {
			local := f.CreatedAt.In(location)
			day := local.Day()
			label := local.Format("2006-01-02")
			if grain == "month" {
				day = 1
				label = local.Format("2006-01")
			}
			periods, ok := calendar[label]
			if !ok {
				periods = usageCalendarPeriods(time.Date(local.Year(), local.Month(), day, 0, 0, 0, 0, time.UTC), grain, location)
				calendar[label] = periods
			}
			for _, period := range periods {
				if !f.CreatedAt.Before(period.start) && f.CreatedAt.Before(period.end) {
					key = period
					break
				}
			}
		}
		groups[key] = append(groups[key], f)
	}
	keys := make([]usageCivilPeriod, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].start.Before(keys[j].start) })
	result := []UsageBucket{}
	for _, key := range keys {
		from, to := key.start, key.end
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		result = append(result, UsageBucket{StartAt: from, EndAt: to, UsageSummary: summarizeUsage(groups[key])})
	}
	return result
}
