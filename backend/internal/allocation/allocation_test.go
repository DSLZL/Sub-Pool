package allocation

import (
	"math/big"
	"testing"

	"sub-pool/backend/internal/estimation"
)

func equal(a *estimation.Amount, want string) bool {
	if a == nil {
		return false
	}
	v, ok := new(big.Rat).SetString(a.Numerator + "/" + a.Denominator)
	w, valid := new(big.Rat).SetString(want)
	return ok && valid && v.Cmp(w) == 0
}

func TestHistoricalSharesCurrentRemainderAndGlobalCoverage(t *testing.T) {
	in := Input{Remaining: estimation.Exact(big.NewRat(120, 1)), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "0.5", "q": "0.5"}, Spent: map[string]string{"p": "70", "q": "10"}, Segments: []Segment{{Cost: "50", Shares: map[string]string{"p": "0.2", "q": "0.8"}}, {Cost: "30", Shares: map[string]string{"p": "0.5", "q": "0.5"}}}}
	r := Evaluate(in)
	if r.State != "scenario" || !equal(r.Members["p"], "15") || !equal(r.Members["q"], "105") || !equal(r.Reserve, "0") {
		t.Fatal("historical rights changed", r)
	}
	if !equal(Global([]Result{r, r}, "p"), "30") || Global([]Result{r, {State: "evidence_incomplete"}}, "p") != nil {
		t.Fatal("partial sources became a global target")
	}
	in.Current = map[string]string{"p": "0.25", "q": "0.5"}
	r = Evaluate(in)
	if r.State != "allocation_deficit" || r.Members != nil {
		t.Fatal("negative member silently repaired")
	}
	in.Spent = map[string]string{"p": "40", "q": "40"}
	r = Evaluate(in)
	if !equal(r.Members["p"], "15") || !equal(r.Members["q"], "75") || !equal(r.Reserve, "30") {
		t.Fatal("reserve was normalized", r)
	}
	in.Complete = false
	if r = Evaluate(in); r.Members != nil || r.State != "evidence_incomplete" {
		t.Fatal("partial provider qualified")
	}
	in.Complete, in.CostBasis = true, "standard_usd"
	if Evaluate(in).Members != nil {
		t.Fatal("standard cost became wallet units")
	}
}

func TestAllocationZeroExactAndInvalidInputs(t *testing.T) {
	for _, bad := range []Input{
		{Remaining: estimation.Exact(big.NewRat(-1, 1)), Complete: true, CostBasis: "wallet_deducted_usd"},
		{Remaining: estimation.Exact(big.NewRat(1, 1)), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "0.6", "q": "0.6"}},
		{Remaining: estimation.Exact(big.NewRat(1, 1)), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"": "1"}},
		{Remaining: estimation.Exact(big.NewRat(1, 1)), Complete: true, CostBasis: "wallet_deducted_usd", Spent: map[string]string{"p": "1"}},
	} {
		if r := Evaluate(bad); r.Members != nil {
			t.Fatal("invalid/omitted spending qualified", bad)
		}
	}
	r := Evaluate(Input{Remaining: estimation.Exact(big.NewRat(0, 1)), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "1"}})
	if !equal(r.Members["p"], "0") || !equal(Global([]Result{r}, "p"), "0") {
		t.Fatal("known zero lost")
	}
	for i := 0; i <= 100; i++ {
		in := Input{Remaining: estimation.Exact(big.NewRat(1, 1000000000000000000)), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": new(big.Rat).SetFrac64(int64(i), 100).FloatString(2)}}
		r = Evaluate(in)
		if r.State != "scenario" || r.Reserve == nil {
			t.Fatal("exact tiny amount failed", i)
		}
		a, _ := new(big.Rat).SetString(r.Members["p"].Numerator + "/" + r.Members["p"].Denominator)
		b, _ := new(big.Rat).SetString(r.Reserve.Numerator + "/" + r.Reserve.Denominator)
		if a.Add(a, b).Cmp(big.NewRat(1, 1000000000000000000)) != 0 {
			t.Fatal("exact conservation lost", i)
		}
	}
}

func TestAllocationConsumesExactEstimateWithoutRounding(t *testing.T) {
	in := estimation.Input{Keep: "1", Quota: []string{"0", "3", "6"}, Costs: []string{"1", "1"}, CurrentQuota: "6", Complete: true}
	first := estimation.Evaluate(in)
	in.Costs = []string{"2", "2"}
	remaining := estimation.MinimumRemaining([]estimation.Result{first, estimation.Evaluate(in)})
	if !equal(remaining, "94/3") {
		t.Fatal("non-terminating estimate fixture changed", remaining)
	}
	before := *remaining
	allocation := Input{Remaining: remaining, Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "0.2", "q": "0.6"}, Spent: map[string]string{"p": "1", "q": "1"}, Segments: []Segment{{Cost: "2", Shares: map[string]string{"p": "0.25", "q": "0.5"}}}}
	r := Evaluate(allocation)
	if r.State != "scenario" || !equal(r.Members["p"], "173/30") || !equal(r.Members["q"], "94/5") || !equal(r.Reserve, "203/30") || !equal(Global([]Result{r, r}, "p"), "173/15") {
		t.Fatal("exact estimate, historical rights or reserve were rounded", r)
	}
	if *remaining != before || allocation.Current["p"] != "0.2" || allocation.Spent["p"] != "1" || allocation.Segments[0].Shares["p"] != "0.25" {
		t.Fatal("allocation mutated its evidence")
	}
	allocation.Remaining = estimation.MinimumRemaining([]estimation.Result{first, {State: "evidence_incomplete"}})
	if r := Evaluate(allocation); r.State == "scenario" || r.Members != nil || r.Reserve != nil {
		t.Fatal("missing overlap became money", r)
	}
}

func TestAllocationRejectsMalformedExactRemainders(t *testing.T) {
	for _, amount := range []*estimation.Amount{nil, {}, {Numerator: "1"}, {Denominator: "1"}, {Numerator: "-1", Denominator: "1"}, {Numerator: "1", Denominator: "0"}, {Numerator: "-1", Denominator: "-1"}, {Numerator: "1", Denominator: "-1"}, {Numerator: "1.5", Denominator: "1"}, {Numerator: "1", Denominator: "1.5"}, {Numerator: "1e2", Denominator: "1"}, {Numerator: "0x2", Denominator: "1"}, {Numerator: "1/2", Denominator: "1"}, {Numerator: "NaN", Denominator: "1"}, {Numerator: " 1", Denominator: "1"}, {Numerator: "0b10", Denominator: "1"}, {Numerator: "0o10", Denominator: "1"}, {Numerator: "1", Denominator: "0x10"}, {Numerator: "1_0", Denominator: "1"}} {
		r := Evaluate(Input{Remaining: amount, Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "1"}})
		if r.State != "invalid_evidence" || r.Members != nil || r.Reserve != nil {
			t.Fatal("invalid exact remainder became money", amount, r)
		}
		if Global([]Result{{State: "scenario", Members: map[string]*estimation.Amount{"p": amount}}}, "p") != nil {
			t.Fatal("malformed source became a global target", amount)
		}
	}
}

func TestExactAmountParsingAgreesAcrossWindowAllocationAndGlobal(t *testing.T) {
	for _, c := range []struct{ numerator, denominator, want string }{
		{"010", "1", "10"}, {"31", "010", "31/10"}, {"009", "1", "9"}, {"000", "001", "0"},
		{"+10", "+2", "5"}, {"94", "3", "94/3"}, {"1", "1000000000000000003", "1/1000000000000000003"},
		{"100000000000000000000000000000000000001", "3", "100000000000000000000000000000000000001/3"},
	} {
		t.Run(c.numerator+"/"+c.denominator, func(t *testing.T) {
			a := &estimation.Amount{Numerator: c.numerator, Denominator: c.denominator}
			before := *a
			minimum := estimation.MinimumRemaining([]estimation.Result{{State: "scenario", Remaining: a}})
			r := Evaluate(Input{Remaining: a, Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "1"}})
			global := Global([]Result{{State: "scenario", Members: map[string]*estimation.Amount{"p": a}}}, "p")
			if r.State != "scenario" || !equal(minimum, c.want) || !equal(r.Members["p"], c.want) || !equal(r.Reserve, "0") || !equal(global, c.want) || !equal(Global([]Result{r}, "p"), c.want) || *a != before {
				t.Fatal("exact amount changed between consumers or mutated evidence", a, minimum, r, global)
			}
		})
	}
}

func TestExactAllocationConservationAndGlobalOrder(t *testing.T) {
	for _, text := range []string{"0", "94/3", "1/1000000000000000003", "100000000000000000000000000000000000001/3"} {
		remaining, ok := new(big.Rat).SetString(text)
		if !ok {
			t.Fatal("invalid matrix fixture", text)
		}
		for p := int64(0); p <= 10; p++ {
			for q := int64(0); p+q <= 10; q++ {
				in := Input{Remaining: estimation.Exact(remaining), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": big.NewRat(p, 10).FloatString(1), "q": big.NewRat(q, 10).FloatString(1)}, Spent: map[string]string{"p": "0.5", "q": "1.5"}, Segments: []Segment{{Cost: "2", Shares: map[string]string{"p": "0.25", "q": "0.75"}}}}
				r := Evaluate(in)
				wantP := new(big.Rat).Mul(remaining, big.NewRat(p, 10))
				wantQ := new(big.Rat).Mul(remaining, big.NewRat(q, 10))
				wantReserve := new(big.Rat).Mul(remaining, big.NewRat(10-p-q, 10))
				if r.State != "scenario" || !equal(r.Members["p"], wantP.RatString()) || !equal(r.Members["q"], wantQ.RatString()) || !equal(r.Reserve, wantReserve.RatString()) || new(big.Rat).Add(new(big.Rat).Add(wantP, wantQ), wantReserve).Cmp(remaining) != 0 {
					t.Fatal("exact conservation failed", text, p, q, r)
				}
				other := Evaluate(Input{Remaining: estimation.Exact(big.NewRat(2, 7)), Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "0.3", "q": "0.7"}})
				wantGlobal := new(big.Rat).Add(wantP, big.NewRat(3, 35))
				if !equal(Global([]Result{r, other}, "p"), wantGlobal.RatString()) || !equal(Global([]Result{other, r}, "p"), wantGlobal.RatString()) || Global([]Result{r, {State: "evidence_incomplete"}}, "p") != nil {
					t.Fatal("global order changed money or incomplete sources qualified", text, p, q)
				}
			}
		}
	}
}
