package estimation

import (
	"math/big"
	"testing"
)

func amount(value *Amount) string {
	if value == nil {
		return "null"
	}
	if value.Denominator == "1" {
		return value.Numerator
	}
	return value.Numerator + "/" + value.Denominator
}

func TestConditionalScenarioExactKnownZeroAndNonTerminatingFractions(t *testing.T) {
	for _, c := range []struct {
		name, keep                     string
		quota, cost                    []string
		capacity, remaining, low, high string
	}{
		{"spread and explicit keep", "0.8", []string{"10", "20", "40"}, []string{"4", "6"}, "24", "72/5", "30", "40"},
		{"native exponent remains exact", "0.8", []string{"1e1", "2E+1", "4e1"}, []string{"4", "6"}, "24", "72/5", "30", "40"},
		{"confirmed exhausted", "1", []string{"50", "80", "100"}, []string{"9", "6"}, "30", "0", "30", "30"},
		{"exact recurring fraction", "1", []string{"0", "3", "6"}, []string{"1", "1"}, "100/3", "94/3", "100/3", "100/3"},
		{"no float precision loss", "0.1", []string{"0", "10", "20"}, []string{"9007199254740993.1", "9007199254740993.2"}, "90071992547409931/10", "180143985094819862/25", "90071992547409931", "90071992547409932"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Evaluate(Input{Keep: c.keep, Quota: c.quota, Costs: c.cost, CurrentQuota: c.quota[len(c.quota)-1], Complete: true})
			if r.State != "scenario" || amount(r.Capacity) != c.capacity || amount(r.Remaining) != c.remaining || amount(r.RangeMin) != c.low || amount(r.RangeMax) != c.high {
				t.Fatalf("unexpected exact result: %+v %s %s %s %s", r, amount(r.Capacity), amount(r.Remaining), amount(r.RangeMin), amount(r.RangeMax))
			}
		})
	}
}

func TestScenarioRejectsMissingMixedAndNonIdentifyingEvidence(t *testing.T) {
	for _, c := range []struct {
		name, keep  string
		quota, cost []string
		complete    bool
		state       string
	}{
		{"no assumption", "0", []string{"10", "20", "40"}, []string{"4", "6"}, true, "not_configured"},
		{"invalid keep", "1.000000000000000001", nil, nil, true, "not_configured"},
		{"two endpoints", "1", []string{"10", "20"}, []string{"4"}, true, "insufficient_samples"},
		{"bounded capture never complete", "1", []string{"10", "20", "40"}, []string{"4", "6"}, false, "evidence_incomplete"},
		{"missing cost", "1", []string{"10", "20", "40"}, []string{"4"}, true, "evidence_incomplete"},
		{"negative quota delta", "1", []string{"10", "20", "19"}, []string{"4", "6"}, true, "condition_changed"},
		{"zero quota delta", "1", []string{"10", "10", "40"}, []string{"4", "6"}, true, "non_identifying_delta"},
		{"zero cost", "1", []string{"10", "20", "40"}, []string{"4", "0"}, true, "non_identifying_delta"},
		{"quota above bound", "1", []string{"10", "20", "100.000000000000000001"}, []string{"4", "6"}, true, "invalid_evidence"},
		{"negative cost", "1", []string{"10", "20", "40"}, []string{"4", "-6"}, true, "invalid_evidence"},
		{"nondecimal quota", "1", []string{"10", "20/1", "40"}, []string{"4", "6"}, true, "invalid_evidence"},
		{"excess exponent", "1", []string{"10", "2e-19", "40"}, []string{"4", "6"}, true, "invalid_evidence"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Evaluate(Input{Keep: c.keep, Quota: c.quota, Costs: c.cost, CurrentQuota: "40", Complete: c.complete})
			if r.State != c.state || r.Capacity != nil || r.Remaining != nil || r.RangeMin != nil || r.RangeMax != nil {
				t.Fatalf("invalid evidence became money: %+v", r)
			}
		})
	}
	for _, v := range []string{"NaN", "1/2", "+1", "-0", "01", " 1", "1.", "8e-1", "0.1234567890123456789"} {
		if _, ok := Decimal(v); ok {
			t.Fatalf("unsafe decimal %q", v)
		}
	}
}

func TestManualScenarioKeepsCurrentOfficialConsumption(t *testing.T) {
	r := Evaluate(Input{Keep: "0.8", Quota: []string{"10", "20", "40"}, Costs: []string{"4", "6"}, CurrentQuota: "60", Complete: true})
	if r.State != "scenario" || amount(r.Remaining) != "48/5" {
		t.Fatalf("manual endpoint erased later official usage: %+v", r)
	}
	for _, q := range []string{"", "39", "101"} {
		r = Evaluate(Input{Keep: "0.8", Quota: []string{"10", "20", "40"}, Costs: []string{"4", "6"}, CurrentQuota: q, Complete: true})
		if r.Remaining != nil {
			t.Fatal("unverified current quota produced money", q)
		}
	}
}

func TestRequiredOverlappingWindowsUseOnlyTheBottleneck(t *testing.T) {
	first := Result{State: "scenario", Remaining: &Amount{"72", "5"}}
	second := Result{State: "scenario", Remaining: &Amount{"9", "1"}}
	if amount(MinimumRemaining([]Result{first, second})) != "9" {
		t.Fatal("overlapping windows added capacity")
	}
	if MinimumRemaining([]Result{first, {State: "evidence_incomplete"}}) != nil || MinimumRemaining(nil) != nil {
		t.Fatal("missing required window became complete")
	}
	if amount(MinimumRemaining([]Result{first, {State: "scenario", Remaining: &Amount{"0", "1"}}})) != "0" {
		t.Fatal("known zero was lost")
	}
}

func TestRequiredWindowsRejectMalformedDecimalAmounts(t *testing.T) {
	first := Result{State: "scenario", Remaining: &Amount{"9", "1"}}
	for _, bad := range []*Amount{nil, {}, {"0x10", "1"}, {"0b10", "1"}, {"0o10", "1"}, {"1", "0x10"}, {"1_0", "1"}, {"1", "0"}, {"-1", "1"}, {"-1", "-1"}, {"1.5", "1"}, {"1e2", "1"}} {
		if MinimumRemaining([]Result{first, {State: "scenario", Remaining: bad}}) != nil {
			t.Fatal("malformed required window became money", bad)
		}
	}
}

func TestAmountRationalKeepsIndependentExactValues(t *testing.T) {
	a := &Amount{"002", "004"}
	before := *a
	value, ok := a.Rational()
	if !ok || value.Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatal("decimal amount was not exact", value, ok)
	}
	value.SetInt64(99)
	again, ok := a.Rational()
	if !ok || again.Cmp(big.NewRat(1, 2)) != 0 || *a != before {
		t.Fatal("parsed value mutated caller evidence", a, again)
	}
}
