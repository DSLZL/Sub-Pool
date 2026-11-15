package allocation

import (
	"math/big"
	"sub-pool/backend/internal/estimation"
	"testing"
)

func TestExplicitRightsUseOnlySameSourceReserveAndCanFundClearingNegative(t *testing.T) {
	input := Input{Remaining: estimation.Exact(big.NewRat(10, 1)), Current: map[string]string{"a": "0.25", "b": "0.5"}, Complete: true, CostBasis: "wallet_deducted_usd"}
	for _, tc := range []struct{ adjust, state, a, reserve string }{{"2.5", "scenario", "5", "0"}, {"2.50000001", "invalid_evidence", "", ""}, {"-2.5", "scenario", "0", "5"}, {"-2.50000001", "allocation_deficit", "", ""}, {"-0", "invalid_evidence", "", ""}, {"0", "scenario", "2.5", "2.5"}} {
		input.Adjustments = map[string]string{"a": tc.adjust}
		out := Evaluate(input)
		if out.State != tc.state {
			t.Fatal(tc, out)
		}
		if tc.state == "scenario" {
			a, _ := out.Members["a"].Rational()
			r, _ := out.Reserve.Rational()
			if a.FloatString(8) != mustRat(t, tc.a).FloatString(8) || r.Cmp(mustRat(t, tc.reserve)) != 0 {
				t.Fatal("funding changed", tc, out)
			}
		}
	}
	input.Adjustments = map[string]string{"foreign": "1"}
	if out := Evaluate(input); out.State != "invalid_evidence" || out.Members != nil {
		t.Fatal("foreign rights", out)
	}
}
func mustRat(t *testing.T, text string) *big.Rat {
	t.Helper()
	v, ok := new(big.Rat).SetString(text)
	if !ok {
		t.Fatal(text)
	}
	return v
}
func TestCarryPreservesExactNonTerminatingFrozenBaselines(t *testing.T) {
	input := CarryInput{Mode: "carry", CostBasis: "wallet_deducted_usd", Quantum: "0.01", Complete: true, Members: []CarryMember{
		{ID: "borrower", BaselineExact: estimation.Exact(big.NewRat(1, 3)), RoundSpent: "0.5", PeriodSpent: "0.5", NextEntitlementExact: estimation.Exact(big.NewRat(1, 3))},
		{ID: "lender", BaselineExact: estimation.Exact(big.NewRat(2, 3)), RoundSpent: "0.1", PeriodSpent: "0.1", NextEntitlement: "1"},
	}}
	out := Carry(input)
	if out.State != "scenario" || out.Adjustments["borrower"].Numerator != "-4" || out.Adjustments["borrower"].Denominator != "25" {
		t.Fatal("frozen rational baseline rounded", out)
	}
	residual, _ := out.Residuals["borrower"].Rational()
	if residual.Cmp(big.NewRat(1, 150)) != 0 {
		t.Fatal("sub-quantum residual lost", out)
	}
	input.Members[0].Baseline = "0.33"
	if out := Carry(input); out.State != "invalid_evidence" {
		t.Fatal("contradictory frozen inputs", out)
	}
}
