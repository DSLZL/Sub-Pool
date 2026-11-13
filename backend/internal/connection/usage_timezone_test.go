package connection

import (
	"testing"
	"time"
)

func TestUsageCalendarBucketsHonorCivilDaysAndMonths(t *testing.T) {
	for _, test := range []struct{ name, zone, grain, fact, start, end string }{
		{"spring", "America/Los_Angeles", "day", "2026-03-08T12:00:00Z", "2026-03-08T08:00:00Z", "2026-03-09T07:00:00Z"},
		{"fall", "America/Los_Angeles", "day", "2026-11-01T12:00:00Z", "2026-11-01T07:00:00Z", "2026-11-02T08:00:00Z"},
		{"half-hour", "Australia/Lord_Howe", "day", "2026-10-04T00:00:00Z", "2026-10-03T13:30:00Z", "2026-10-04T13:00:00Z"},
		{"midnight-gap", "America/Santiago", "day", "2026-09-06T12:00:00Z", "2026-09-06T04:00:00Z", "2026-09-07T03:00:00Z"},
		{"quarter-hour", "Asia/Kathmandu", "day", "2026-11-08T12:00:00Z", "2026-11-07T18:15:00Z", "2026-11-08T18:15:00Z"},
		{"calendar-month", "America/Los_Angeles", "month", "2026-03-15T12:00:00Z", "2026-03-01T08:00:00Z", "2026-04-01T07:00:00Z"},
		{"first-midnight", "America/Goose_Bay", "day", "1988-10-30T02:00:30Z", "1988-10-30T02:00:00Z", "1988-10-30T02:01:00Z"},
		{"previous-date-before", "America/Goose_Bay", "day", "1988-10-30T01:59:59Z", "1988-10-29T02:00:00Z", "1988-10-30T02:00:00Z"},
		{"previous-date-repeated", "America/Goose_Bay", "day", "1988-10-30T02:01:00Z", "1988-10-30T02:01:00Z", "1988-10-30T04:00:00Z"},
		{"second-midnight", "America/Goose_Bay", "day", "1988-10-30T04:30:00Z", "1988-10-30T04:00:00Z", "1988-10-31T04:00:00Z"},
		{"first-month-midnight", "America/St_Johns", "month", "2009-11-01T02:30:30Z", "2009-11-01T02:30:00Z", "2009-11-01T02:31:00Z"},
		{"previous-month-repeated", "America/St_Johns", "month", "2009-11-01T03:00:00Z", "2009-11-01T02:31:00Z", "2009-11-01T03:30:00Z"},
		{"second-month-midnight", "America/St_Johns", "month", "2009-11-01T04:00:00Z", "2009-11-01T03:30:00Z", "2009-12-01T03:30:00Z"},
		{"second-offset", "Africa/Monrovia", "day", "1971-01-05T12:00:00Z", "1971-01-05T00:44:30Z", "1971-01-06T00:44:30Z"},
		{"future-month", "America/Los_Angeles", "month", "9998-12-15T12:00:00Z", "9998-12-01T08:00:00Z", "9999-01-01T08:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			instant, _ := time.Parse(time.RFC3339, test.fact)
			buckets := usageBuckets([]UsageFact{{CreatedAt: instant}}, instant.Add(-32*24*time.Hour), instant.Add(32*24*time.Hour), test.grain, location)
			if len(buckets) != 1 || buckets[0].StartAt.Format(time.RFC3339) != test.start || buckets[0].EndAt.Format(time.RFC3339) != test.end || buckets[0].RequestCount != "1" || buckets[0].Metrics["actual_cost"] != nil {
				t.Fatalf("wrong native calendar bounds or invented money: %+v", buckets)
			}
		})
	}
	location, _ := time.LoadLocation("Asia/Taipei")
	before, _ := time.Parse(time.RFC3339Nano, "2028-02-29T15:59:59.999999999Z")
	after := before.Add(time.Nanosecond)
	buckets := usageBuckets([]UsageFact{{CreatedAt: before}, {CreatedAt: after}}, before, after.Add(time.Nanosecond), "day", location)
	if len(buckets) != 2 || !buckets[0].StartAt.Equal(before) || !buckets[0].EndAt.Equal(after) || !buckets[1].StartAt.Equal(after) || !buckets[1].EndAt.Equal(after.Add(time.Nanosecond)) {
		t.Fatalf("calendar clipping lost native nanoseconds: %+v", buckets)
	}
	location, _ = time.LoadLocation("Pacific/Apia")
	if got := usageCalendarPeriods(time.Date(2011, time.December, 30, 0, 0, 0, 0, time.UTC), "day", location); len(got) != 0 {
		t.Fatal("nonexistent civil day invented an interval", got)
	}
}
