package connection

import (
	"encoding/json"
	"testing"
	"time"
)

func maintenanceFactAt(id, used string, at, reset time.Time) maintenanceFact {
	duration := "10800"
	snapshot := QuotaSnapshot{SourceID: "owned-source", ConnectionRevision: "2", AccountRevision: "3", SourceMode: "passive", SourceAt: &at, ReceivedAt: at.Add(time.Microsecond), Windows: []QuotaWindow{{Kind: "short", UsedPercent: &used, ResetAt: &reset, DurationSeconds: &duration}}}
	body, _ := json.Marshal(snapshot)
	return maintenanceFact{ID: id, JobID: id, ReceivedAt: snapshot.ReceivedAt, Raw: body, snapshot: snapshot}
}

func TestMaintenancePeriodIndexUsesOnlyActualRetainedNativeWindows(t *testing.T) {
	reset := time.Date(2024, 2, 29, 15, 0, 0, 123, time.UTC)
	first := maintenanceFactAt("9", "0", reset.Add(-2*time.Hour), reset)
	second := maintenanceFactAt("10", "1e1", reset.Add(-time.Hour), reset)
	duplicate := maintenanceFactAt("11", "10.000000000000000000", *second.snapshot.SourceAt, reset)
	excluded := maintenanceFactAt("12", "98", reset.Add(-30*time.Minute), reset)
	excluded.Excluded = true
	out, issues := projectMaintenance([]maintenanceFact{duplicate, excluded, second, first}, "owned-source")
	if maintenanceBlocked(maintenancePreview{Issues: issues}) || !out.ScanComplete || out.Observations != 4 || out.Excluded != 1 || len(out.Periods) != 1 {
		t.Fatal("retained historical evidence was not indexed", out, issues)
	}
	p := out.Periods[0]
	if p.Kind != "short" || p.DurationSeconds != "10800" || !p.StartAt.Equal(reset.Add(-3*time.Hour)) || !p.EndAt.Equal(reset) || p.FirstUsed != "0" || p.LastUsed != "10.000000000000000000" || len(p.ObservationIDs) != 3 || p.ObservationIDs[0] != "9" || p.ObservationIDs[1] != "10" || p.ObservationIDs[2] != "11" {
		t.Fatal("window name guessed a duration, zero was lost, or numeric source-time ordering changed", p)
	}
	changed := first
	changed.ID, changed.snapshot.AccountRevision = "13", "4"
	out, issues = projectMaintenance([]maintenanceFact{first, changed}, "owned-source")
	if len(out.Periods) != 2 || maintenanceBlocked(maintenancePreview{Issues: issues}) {
		t.Fatal("different account revisions were merged", out, issues)
	}
	zone := time.FixedZone("owned-offset", 8*60*60)
	offset := maintenanceFactAt("14", "0", first.snapshot.SourceAt.In(zone), reset.In(zone))
	out, issues = projectMaintenance([]maintenanceFact{first, offset}, "owned-source")
	if maintenanceBlocked(maintenancePreview{Issues: issues}) || len(out.Periods) != 1 || out.Periods[0].StartAt.Location() != time.UTC || out.Periods[0].FirstSourceAt.Location() != time.UTC {
		t.Fatal("equivalent native instants did not produce one canonical UTC period", out, issues)
	}
}

func TestMaintenancePeriodIndexBlocksMissingConflictingAndTruncatedEvidence(t *testing.T) {
	reset := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		change func(*maintenanceFact)
	}{
		{"missing source", func(f *maintenanceFact) { f.snapshot.SourceAt = nil }},
		{"future source by nanosecond", func(f *maintenanceFact) { f.snapshot.ReceivedAt = f.snapshot.SourceAt.Add(-time.Nanosecond) }},
		{"at half-open end", func(f *maintenanceFact) { f.snapshot.SourceAt = &reset; f.snapshot.ReceivedAt = reset }},
		{"before actual start", func(f *maintenanceFact) { at := reset.Add(-3*time.Hour - time.Nanosecond); f.snapshot.SourceAt = &at }},
		{"missing duration", func(f *maintenanceFact) { f.snapshot.Windows[0].DurationSeconds = nil }},
		{"noncanonical duration", func(f *maintenanceFact) { duration := "+10800"; f.snapshot.Windows[0].DurationSeconds = &duration }},
		{"missing reset", func(f *maintenanceFact) { f.snapshot.Windows[0].ResetAt = nil }},
		{"missing percentage", func(f *maintenanceFact) { f.snapshot.Windows[0].UsedPercent = nil }},
		{"above exact 100", func(f *maintenanceFact) {
			value := "100.000000000000000001"
			f.snapshot.Windows[0].UsedPercent = &value
		}},
		{"non-decimal rational", func(f *maintenanceFact) { value := "1/2"; f.snapshot.Windows[0].UsedPercent = &value }},
		{"wrong source", func(f *maintenanceFact) { f.snapshot.SourceID = "other-source" }},
		{"receipt mismatch", func(f *maintenanceFact) { f.ReceivedAt = f.ReceivedAt.Add(time.Microsecond) }},
		{"unknown kind", func(f *maintenanceFact) { f.snapshot.Windows[0].Kind = "assumed_week" }},
		{"duplicate kind with missing value", func(f *maintenanceFact) {
			valid := f.snapshot.Windows[0]
			f.snapshot.Windows[0].UsedPercent = nil
			f.snapshot.Windows = append(f.snapshot.Windows, valid)
		}},
		{"too many native windows", func(f *maintenanceFact) {
			f.snapshot.Windows = append(f.snapshot.Windows, f.snapshot.Windows[0], f.snapshot.Windows[0])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := maintenanceFactAt("1", "25", reset.Add(-time.Hour), reset)
			test.change(&f)
			_, issues := projectMaintenance([]maintenanceFact{f}, "owned-source")
			if !maintenanceBlocked(maintenancePreview{Issues: issues}) {
				t.Fatal("unverified historical evidence became applicable", issues)
			}
		})
	}
	f := maintenanceFactAt("1", "25", reset.Add(-time.Hour), reset)
	other := maintenanceFactAt("2", "25.000000000000000001", *f.snapshot.SourceAt, reset)
	_, issues := projectMaintenance([]maintenanceFact{f, other}, "owned-source")
	if !maintenanceBlocked(maintenancePreview{Issues: issues}) || issues[0].Code != "SOURCE_TIME_CONFLICT" {
		t.Fatal("exact contradictory source-time evidence was hidden", issues)
	}
	facts := make([]maintenanceFact, maintenanceObservationLimit+1)
	facts[0].Excluded = true
	out, issues := projectMaintenance(facts, "owned-source")
	if out.ScanComplete || out.Excluded != 1 || len(out.Periods) != 0 || len(issues) != 1 || issues[0].Code != "OBSERVATION_LIMIT" {
		t.Fatal("truncated evidence looked like complete empty history", out, issues)
	}
	_, issues = projectMaintenance(nil, "owned-source")
	if len(issues) != 1 || issues[0].Code != "NO_LOCAL_OBSERVATIONS" {
		t.Fatal("missing history became zero history", issues)
	}
}

func TestMaintenanceDifferencePreservesOldProjectionAndExposesIncorrectBounds(t *testing.T) {
	reset := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	out, _ := projectMaintenance([]maintenanceFact{maintenanceFactAt("1", "0", reset.Add(-time.Hour), reset)}, "owned-source")
	old := MaintenanceProjection{Periods: append([]MaintenancePeriod(nil), out.Periods...)}
	old.Periods[0].StartAt = old.Periods[0].StartAt.Add(-time.Hour)
	diff := maintenanceDifference(old, out)
	if diff.Added != 1 || diff.Removed != 1 || diff.Changed != 0 || old.Periods[0].StartAt.Equal(out.Periods[0].StartAt) {
		t.Fatal("incorrect cached bounds were not a retained replacement", diff)
	}
	old.Periods[0] = out.Periods[0]
	old.Periods[0].LastUsed = "1"
	diff = maintenanceDifference(old, out)
	if diff.Added != 0 || diff.Removed != 0 || diff.Changed != 1 {
		t.Fatal("changed sample statistics were not visible", diff)
	}
}
