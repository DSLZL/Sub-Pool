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
	in := Input{Remaining: "120", Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "0.5", "q": "0.5"}, Spent: map[string]string{"p": "70", "q": "10"}, Segments: []Segment{{Cost: "50", Shares: map[string]string{"p": "0.2", "q": "0.8"}}, {Cost: "30", Shares: map[string]string{"p": "0.5", "q": "0.5"}}}}
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
		{Remaining: "-1", Complete: true, CostBasis: "wallet_deducted_usd"},
		{Remaining: "1", Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "0.6", "q": "0.6"}},
		{Remaining: "1", Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"": "1"}},
		{Remaining: "1", Complete: true, CostBasis: "wallet_deducted_usd", Spent: map[string]string{"p": "1"}},
	} {
		if r := Evaluate(bad); r.Members != nil {
			t.Fatal("invalid/omitted spending qualified", bad)
		}
	}
	r := Evaluate(Input{Remaining: "0", Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": "1"}})
	if !equal(r.Members["p"], "0") || !equal(Global([]Result{r}, "p"), "0") {
		t.Fatal("known zero lost")
	}
	for i := 0; i <= 100; i++ {
		in := Input{Remaining: "0.000000000000000001", Complete: true, CostBasis: "wallet_deducted_usd", Current: map[string]string{"p": new(big.Rat).SetFrac64(int64(i), 100).FloatString(2)}}
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
