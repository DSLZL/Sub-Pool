package connection

import (
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"time"
)

const maintenanceRules = "native-period-index-v1"
const maintenanceObservationLimit = 2000

type MaintenanceScope struct {
	AccountID string `json:"account_id"`
	From      string `json:"from"`
	Until     string `json:"until"`
}

type MaintenanceInput struct {
	MaintenanceScope
	Kind         string `json:"kind"`
	RulesVersion string `json:"rules_version"`
}

type MaintenanceIssue struct {
	Code          string  `json:"code"`
	Blocking      bool    `json:"blocking"`
	ObservationID *string `json:"observation_id"`
	WindowKind    *string `json:"window_kind"`
	EstimateID    *string `json:"estimate_id,omitempty"`
}

type MaintenancePeriod struct {
	SourceID           string `json:"source_id"`
	ConnectionRevision string `json:"connection_revision"`
	AccountRevision    string `json:"account_revision"`
	SourceMode         string `json:"source_mode"`
	ObservationPeriod
	ObservationIDs []string  `json:"observation_ids"`
	FirstSourceAt  time.Time `json:"first_source_at"`
	LastSourceAt   time.Time `json:"last_source_at"`
	FirstUsed      string    `json:"first_used_percent"`
	LastUsed       string    `json:"last_used_percent"`
}

type MaintenanceProjection struct {
	ScanComplete bool                  `json:"scan_complete"`
	Observations int                   `json:"observations"`
	Excluded     int                   `json:"excluded"`
	Periods      []MaintenancePeriod   `json:"periods"`
	Financial    *MaintenanceFinancial `json:"financial,omitempty"`
}

type MaintenanceDifference struct {
	Added     int                             `json:"added"`
	Removed   int                             `json:"removed"`
	Changed   int                             `json:"changed"`
	Financial *MaintenanceFinancialDifference `json:"financial,omitempty"`
}

type maintenanceFact struct {
	ID         string          `json:"id"`
	JobID      string          `json:"job_id"`
	ReceivedAt time.Time       `json:"received_at"`
	Raw        json.RawMessage `json:"snapshot"`
	Excluded   bool            `json:"excluded"`
	snapshot   QuotaSnapshot
}

func maintenancePeriodKey(p MaintenancePeriod) string {
	b, _ := json.Marshal([]string{p.SourceID, p.ConnectionRevision, p.AccountRevision, p.SourceMode, p.Kind, p.StartAt.Format(time.RFC3339Nano), p.EndAt.Format(time.RFC3339Nano), p.DurationSeconds})
	return string(b)
}

// These are retained sample statistics, never complete periods or monetary estimates.
func projectMaintenance(facts []maintenanceFact, source string) (MaintenanceProjection, []MaintenanceIssue) {
	out := MaintenanceProjection{ScanComplete: len(facts) <= maintenanceObservationLimit, Observations: len(facts), Periods: []MaintenancePeriod{}}
	issues := []MaintenanceIssue{}
	for _, fact := range facts {
		if fact.Excluded {
			out.Excluded++
		}
	}
	if !out.ScanComplete {
		return out, []MaintenanceIssue{{Code: "OBSERVATION_LIMIT", Blocking: true}}
	}
	if len(facts) == 0 {
		return out, []MaintenanceIssue{{Code: "NO_LOCAL_OBSERVATIONS", Blocking: true}}
	}
	type sample struct {
		id   string
		at   time.Time
		used string
	}
	groups := map[string]*MaintenancePeriod{}
	points := map[string][]sample{}
	for _, fact := range facts {
		if fact.Excluded {
			continue
		}
		snapshot := fact.snapshot
		if !snapshot.ReceivedAt.Equal(fact.ReceivedAt) {
			issues = append(issues, MaintenanceIssue{Code: "RECEIPT_MISMATCH", Blocking: true, ObservationID: &fact.ID})
			continue
		}
		if snapshot.SourceID != source || !validRevision(snapshot.ConnectionRevision) || !validRevision(snapshot.AccountRevision) || (snapshot.SourceMode != "passive" && snapshot.SourceMode != "active") {
			issues = append(issues, MaintenanceIssue{Code: "SOURCE_PROVENANCE_INVALID", Blocking: true, ObservationID: &fact.ID})
			continue
		}
		if len(snapshot.Windows) == 0 || len(snapshot.Windows) > 2 {
			issues = append(issues, MaintenanceIssue{Code: "PERIOD_EVIDENCE_MISSING", Blocking: true, ObservationID: &fact.ID})
			continue
		}
		seen := map[string]bool{}
		for _, window := range snapshot.Windows {
			period := nativeObservationPeriod(snapshot, window.Kind)
			valid := period != nil && window.UsedPercent != nil && window.ResetAt != nil && window.DurationSeconds != nil && validRevision(*window.DurationSeconds) && (window.Kind == "short" || window.Kind == "seven_day") && !seen[window.Kind]
			seen[window.Kind] = true
			if valid {
				_, decimal := percentText(json.RawMessage(*window.UsedPercent))
				used, ok := new(big.Rat).SetString(*window.UsedPercent)
				valid = decimal && ok && used.Sign() >= 0 && used.Cmp(big.NewRat(100, 1)) <= 0
			}
			if !valid {
				issue := MaintenanceIssue{Code: "PERIOD_EVIDENCE_MISSING", Blocking: true, ObservationID: &fact.ID}
				if window.Kind == "short" || window.Kind == "seven_day" {
					issue.WindowKind = &window.Kind
				}
				issues = append(issues, issue)
				continue
			}
			p := MaintenancePeriod{SourceID: snapshot.SourceID, ConnectionRevision: snapshot.ConnectionRevision, AccountRevision: snapshot.AccountRevision, SourceMode: snapshot.SourceMode, ObservationPeriod: *period}
			p.StartAt, p.EndAt = p.StartAt.UTC(), p.EndAt.UTC()
			key := maintenancePeriodKey(p)
			if groups[key] == nil {
				groups[key] = &p
			}
			points[key] = append(points[key], sample{fact.ID, snapshot.SourceAt.UTC(), *window.UsedPercent})
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p, samples := groups[key], points[key]
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].at.Equal(samples[j].at) {
				left, _ := strconv.ParseInt(samples[i].id, 10, 64)
				right, _ := strconv.ParseInt(samples[j].id, 10, 64)
				return left < right
			}
			return samples[i].at.Before(samples[j].at)
		})
		for i, point := range samples {
			p.ObservationIDs = append(p.ObservationIDs, point.id)
			if i > 0 && point.at.Equal(samples[i-1].at) {
				left, _ := new(big.Rat).SetString(point.used)
				right, _ := new(big.Rat).SetString(samples[i-1].used)
				if left.Cmp(right) != 0 {
					issues = append(issues, MaintenanceIssue{Code: "SOURCE_TIME_CONFLICT", Blocking: true, ObservationID: &point.id, WindowKind: &p.Kind})
				}
			}
		}
		p.FirstSourceAt, p.LastSourceAt = samples[0].at, samples[len(samples)-1].at
		p.FirstUsed, p.LastUsed = samples[0].used, samples[len(samples)-1].used
		out.Periods = append(out.Periods, *p)
	}
	if len(out.Periods) == 0 {
		issues = append(issues, MaintenanceIssue{Code: "NO_NATIVE_PERIODS", Blocking: true})
	}
	return out, issues
}

func maintenanceDifference(before, after MaintenanceProjection) MaintenanceDifference {
	old := map[string]string{}
	for _, p := range before.Periods {
		body, _ := json.Marshal(p)
		old[maintenancePeriodKey(p)] = string(body)
	}
	diff := MaintenanceDifference{}
	for _, p := range after.Periods {
		key := maintenancePeriodKey(p)
		body, _ := json.Marshal(p)
		previous, exists := old[key]
		if !exists {
			diff.Added++
		} else if previous != string(body) {
			diff.Changed++
		}
		delete(old, key)
	}
	diff.Removed = len(old)
	return diff
}
