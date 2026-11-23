package connection

import (
	"testing"
	"time"
)

func TestWeeklyWalletGroupOffsetsAndRetainsBothPeriods(t *testing.T) {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a := walletTestCycle("pool-a", "a", start, "50")
	b := walletTestCycle("pool-b", "b", start.Add(48*time.Hour), "30")
	for _, tt := range []struct {
		name, available, before, target, delta, covered, debt, nextTarget string
	}{
		{"full", "30", "45", "25", "-20", "5", "0", "35"},
		{"partial", "3", "18", "0", "-18", "3", "2", "8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b.Gross = tt.available
			formal := a
			formal.Gross, formal.Phase = "30", "settlement"
			at := start.Add(72 * time.Hour)
			prior := []weeklyWalletContribution{a, b}
			use := []weeklyWalletUse{{"1", a.weeklyWalletAccountKey, at.Add(-time.Hour), "35"}}
			plan, ok := calculateWeeklyWalletGroup(prior, []weeklyWalletContribution{formal, b}, use, at, tt.before)
			if !ok || plan.Target != tt.target || plan.Delta != tt.delta || len(plan.Offsets) != 1 {
				t.Fatalf("settlement: %+v %v", plan, ok)
			}
			edge := plan.Offsets[0]
			if edge.From != b.weeklyWalletCycle || edge.To != a.weeklyWalletCycle || edge.Amount != tt.covered ||
				plan.Contributions[0].Debt != tt.debt || plan.Contributions[1].Remaining != tt.target {
				t.Fatalf("offset: %+v", plan)
			}
			repeat, ok := calculateWeeklyWalletGroup(plan.Contributions, []weeklyWalletContribution{formal, b}, nil, at, tt.target)
			if !ok || repeat.Delta != "0" || len(repeat.Offsets) != 0 {
				t.Fatalf("duplicate offset: %+v %v", repeat, ok)
			}
			newA := walletTestCycle(a.PoolID, a.AccountID, a.End, "10")
			next, ok := calculateWeeklyWalletGroup(plan.Contributions, []weeklyWalletContribution{newA, b}, nil, a.End, tt.target)
			if !ok || next.Target != tt.nextTarget || len(next.Closed) != 1 || next.Contributions[0].UsedToCover != tt.covered ||
				next.Contributions[1].OpeningDebt != tt.debt || len(next.Offsets) != 0 {
				t.Fatalf("next week: %+v %v", next, ok)
			}
		})
	}
}

func TestWeeklyWalletGroupOffsetsBeforeExpiry(t *testing.T) {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	b := walletTestCycle("pool-b", "b", start, "30")
	a := walletTestCycle("pool-a", "a", start.Add(time.Hour), "30")
	newB := walletTestCycle(b.PoolID, b.AccountID, b.End, "0")
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{b.End.Add(-time.Nanosecond), "0"},
		{b.End, "5"},
	} {
		use := []weeklyWalletUse{{"1", a.weeklyWalletAccountKey, tc.at, "35"}}
		plan, ok := calculateWeeklyWalletGroup([]weeklyWalletContribution{a, b}, []weeklyWalletContribution{a, newB}, use, b.End.Add(time.Minute), "25")
		if !ok || len(plan.Closed) != 1 || plan.Contributions[0].Debt != tc.want {
			t.Fatalf("usage at %s: %+v %v", tc.at, plan, ok)
		}
	}
}

func TestWeeklyWalletGroupStockDebitPrecision(t *testing.T) {
	for _, tc := range []struct{ log, debit string }{
		{"35.0000000001", "35"},
		{"0.0000000049", "0"},
		{"0.0000000051", "0.00000001"},
		{"1.000000005", ""},
	} {
		got, ok := stockWeeklyWalletDebit(weeklyWalletAmount(tc.log))
		if ok != (tc.debit != "") || ok && usageDecimal(got) != tc.debit {
			t.Fatalf("log %s debit %v proven=%v", tc.log, got, ok)
		}
	}
}

func walletTestCycle(pool, account string, at time.Time, gross string) weeklyWalletContribution {
	return weeklyWalletContribution{
		weeklyWalletCycle: weeklyWalletCycle{weeklyWalletAccountKey{pool, account}, at, at.Add(7 * 24 * time.Hour)},
		Gross:             gross, Spent: "0", OpeningDebt: "0", Covered: "0", UsedToCover: "0",
	}
}
