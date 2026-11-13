package allocation

import (
	"fmt"
	"math/big"
	"reflect"
	"testing"
)

func carryExample() CarryInput {
	return CarryInput{Mode: "carry", Complete: true, CostBasis: "wallet_deducted_usd", Quantum: "1", Members: []CarryMember{
		{ID: "borrower", Baseline: "12", RoundSpent: "18", PeriodSpent: "18", NextEntitlement: "8"},
		{ID: "lender-a", Baseline: "12", RoundSpent: "6", PeriodSpent: "6", NextEntitlement: "8"},
		{ID: "lender-b", Baseline: "6", RoundSpent: "3", PeriodSpent: "3", NextEntitlement: "8"},
	}}
}

func TestCarryUsesFullOriginalPeriodAndPreservesInput(t *testing.T) {
	in := carryExample()
	before := carryExample()
	r := Carry(in)
	if r.State != "scenario" || !equal(r.Adjustments["borrower"], "-6") || !equal(r.Adjustments["lender-a"], "4") || !equal(r.Adjustments["lender-b"], "2") || len(r.Residuals) != 0 {
		t.Fatal("conditional original-period transfers differ", r)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatal("proposal mutated frozen input")
	}
	in.Members[1].PeriodSpent = "9"
	r = Carry(in)
	if r.State != "scenario" || !equal(r.Adjustments["lender-a"], "3") || !equal(r.Adjustments["lender-b"], "3") {
		t.Fatal("post-exit normal consumption was compensated twice", r)
	}
}

func TestCarryQuantizesExactlyAndRetainsUnsettledBorrowing(t *testing.T) {
	in := CarryInput{Mode: "carry", Complete: true, CostBasis: "wallet_deducted_usd", Quantum: "0.1", Members: []CarryMember{
		{ID: "p", Baseline: "0", RoundSpent: "0.25", PeriodSpent: "0.25", NextEntitlement: "0.2"},
		{ID: "q", Baseline: "0.3", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
	}}
	r := Carry(in)
	if r.State != "scenario" || !equal(r.Adjustments["p"], "-0.2") || !equal(r.Adjustments["q"], "0.2") || !equal(r.Residuals["p"], "0.05") {
		t.Fatal("quantum rounding lost or invented borrowing", r)
	}
	in.Members[0].RoundSpent, in.Members[0].PeriodSpent = "0.05", "0.05"
	r = Carry(in)
	if r.State != "scenario" || len(r.Adjustments) != 0 || !equal(r.Residuals["p"], "0.05") {
		t.Fatal("below-quantum borrowing disappeared or became a transfer", r)
	}
	large := "9007199254740993.000000000000000003"
	in.Quantum = "0.000000000000000001"
	in.Members[0].RoundSpent, in.Members[0].PeriodSpent, in.Members[0].NextEntitlement = large, large, large
	in.Members[1].Baseline = large
	r = Carry(in)
	if r.State != "scenario" || !equal(r.Adjustments["p"], "-"+large) || !equal(r.Adjustments["q"], large) || len(r.Residuals) != 0 {
		t.Fatal("large exact units crossed a floating point or int64 boundary", r)
	}
}

func TestCarryLargestRemainderUsesStableIdentityAndNoZeroLender(t *testing.T) {
	in := CarryInput{Mode: "carry", Complete: true, CostBasis: "wallet_deducted_usd", Quantum: "1", Members: []CarryMember{
		{ID: "z", Baseline: "1", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
		{ID: "borrower", Baseline: "0", RoundSpent: "1", PeriodSpent: "1", NextEntitlement: "1"},
		{ID: "a", Baseline: "1", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
		{ID: "0", Baseline: "0", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
	}}
	want := Carry(in)
	if want.State != "scenario" || len(want.Adjustments) != 2 || !equal(want.Adjustments["a"], "1") || !equal(want.Adjustments["borrower"], "-1") {
		t.Fatal("tied remainder did not use stable participant identity", want)
	}
	for i, j := 0, len(in.Members)-1; i < j; i, j = i+1, j-1 {
		in.Members[i], in.Members[j] = in.Members[j], in.Members[i]
	}
	if got := Carry(in); !reflect.DeepEqual(got, want) {
		t.Fatal("input order changed carry proposal", got, want)
	}
	in.Members = []CarryMember{
		{ID: "p", Baseline: "0", RoundSpent: "5", PeriodSpent: "5", NextEntitlement: "5"},
		{ID: "a", Baseline: "4", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
		{ID: "z", Baseline: "2", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
	}
	if got := Carry(in); !equal(got.Adjustments["a"], "3") || !equal(got.Adjustments["z"], "2") {
		t.Fatal("identity order overrode a larger exact remainder", got)
	}
}

func TestCarryRoundsEachBorrowerBeforePooling(t *testing.T) {
	in := CarryInput{Mode: "carry", Complete: true, CostBasis: "wallet_deducted_usd", Quantum: "0.1", Members: []CarryMember{
		{ID: "p", Baseline: "0", RoundSpent: "0.15", PeriodSpent: "0.15", NextEntitlement: "0.1"},
		{ID: "q", Baseline: "0", RoundSpent: "0.15", PeriodSpent: "0.15", NextEntitlement: "0.1"},
		{ID: "lender", Baseline: "0.3", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
	}}
	got := Carry(in)
	if got.State != "scenario" || !equal(got.Adjustments["p"], "-0.1") || !equal(got.Adjustments["q"], "-0.1") || !equal(got.Adjustments["lender"], "0.2") || !equal(got.Residuals["p"], "0.05") || !equal(got.Residuals["q"], "0.05") {
		t.Fatal("rounding pooled borrowers into an extra debit", got)
	}
}

func TestCarryRejectsWholeProposalWithoutPartialRights(t *testing.T) {
	tests := []struct {
		name, state string
		edit        func(*CarryInput)
	}{
		{"incomplete", "evidence_incomplete", func(in *CarryInput) { in.Complete = false }},
		{"standard is not deduction", "evidence_incomplete", func(in *CarryInput) { in.CostBasis = "standard_usd" }},
		{"unrecognized mode", "invalid_evidence", func(in *CarryInput) { in.Mode = "other" }},
		{"empty", "invalid_evidence", func(in *CarryInput) { in.Members = nil }},
		{"member bound", "invalid_evidence", func(in *CarryInput) { in.Members = make([]CarryMember, 501) }},
		{"duplicate identity", "invalid_evidence", func(in *CarryInput) { in.Members[1].ID = in.Members[0].ID }},
		{"empty identity", "invalid_evidence", func(in *CarryInput) { in.Members[1].ID = "" }},
		{"negative baseline", "invalid_evidence", func(in *CarryInput) { in.Members[0].Baseline = "-1" }},
		{"invalid spend", "invalid_evidence", func(in *CarryInput) { in.Members[0].RoundSpent = "NaN" }},
		{"round exceeds period", "invalid_evidence", func(in *CarryInput) { in.Members[0].PeriodSpent = "17" }},
		{"missing next grant", "invalid_evidence", func(in *CarryInput) { in.Members[0].NextEntitlement = "" }},
		{"zero quantum", "invalid_evidence", func(in *CarryInput) { in.Quantum = "0" }},
		{"negative quantum", "invalid_evidence", func(in *CarryInput) { in.Quantum = "-1" }},
		{"scientific quantum", "invalid_evidence", func(in *CarryInput) { in.Quantum = "1e-3" }},
		{"unbounded precision", "invalid_evidence", func(in *CarryInput) { in.Quantum = "0.0000000000000000001" }},
		{"post-exit overspend", "unattributed_post_exit_spending", func(in *CarryInput) { in.Members[0].PeriodSpent = "19" }},
		{"tail removes funding", "funding_or_evidence_deficit", func(in *CarryInput) { in.Members[1].PeriodSpent = "10" }},
		{"next grant insufficient", "next_entitlement_insufficient", func(in *CarryInput) { in.Members[0].NextEntitlement = "5.999999999999999999" }},
		{"rounding must not hide deficit", "funding_or_evidence_deficit", func(in *CarryInput) {
			in.Quantum = "0.1"
			in.Members = []CarryMember{{ID: "p", Baseline: "0", RoundSpent: "0.09", PeriodSpent: "0.09", NextEntitlement: "1"}, {ID: "q", Baseline: "0.08", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := carryExample()
			tc.edit(&in)
			if got := Carry(in); got.State != tc.state || got.Adjustments != nil || got.Residuals != nil {
				t.Fatal("invalid input produced a partial proposal", got, tc.state)
			}
		})
	}
}

func TestNoCarryAndNoBorrowingCreateNoNewEntries(t *testing.T) {
	in := carryExample()
	in.Mode = "no_carry"
	if got := Carry(in); got.State != "no_carry" || len(got.Adjustments) != 0 || len(got.Residuals) != 0 {
		t.Fatal("no_carry created or cleared entitlements", got)
	}
	in.Mode = "carry"
	for i := range in.Members {
		in.Members[i].RoundSpent, in.Members[i].PeriodSpent = "0", "0"
	}
	if got := Carry(in); got.State != "no_borrowing" || len(got.Adjustments) != 0 || len(got.Residuals) != 0 {
		t.Fatal("unused rights were carried without borrowing", got)
	}
	in.Members = make([]CarryMember, 500)
	for i := range in.Members {
		in.Members[i] = CarryMember{ID: fmt.Sprint(i), Baseline: "0", RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"}
	}
	if got := Carry(in); got.State != "no_borrowing" {
		t.Fatal("valid member bound rejected", got)
	}
}

func TestCarryConservationDebitBoundsAndPermutation(t *testing.T) {
	decimal := func(cents int) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
	cases := 0
	for borrowed := 1; borrowed <= 25; borrowed++ {
		for a := 0; a <= 12; a++ {
			for b := 0; b <= 12; b++ {
				in := CarryInput{Mode: "carry", Complete: true, CostBasis: "wallet_deducted_usd", Quantum: "0.1", Members: []CarryMember{
					{ID: "p", Baseline: "0", RoundSpent: decimal(borrowed), PeriodSpent: decimal(borrowed), NextEntitlement: "1"},
					{ID: "a", Baseline: decimal(a), RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
					{ID: "b", Baseline: decimal(b), RoundSpent: "0", PeriodSpent: "0", NextEntitlement: "0"},
				}}
				got := Carry(in)
				if borrowed > a+b {
					if got.State != "funding_or_evidence_deficit" || got.Adjustments != nil {
						t.Fatal("raw funding deficit was rounded away", borrowed, a, b, got)
					}
					continue
				}
				if got.State != "scenario" {
					t.Fatal("funded exact proposal rejected", borrowed, a, b, got)
				}
				total := new(big.Rat)
				for id, amount := range got.Adjustments {
					value, valid := new(big.Rat).SetString(amount.Numerator + "/" + amount.Denominator)
					if !valid || new(big.Rat).Mul(new(big.Rat).Set(value), big.NewRat(10, 1)).Denom().Cmp(big.NewInt(1)) != 0 || id == "p" && value.Sign() >= 0 || id != "p" && value.Sign() <= 0 || id == "a" && a == 0 || id == "b" && b == 0 {
						t.Fatal("invalid quantum or borrower/lender entry", borrowed, a, b, id, amount)
					}
					total.Add(total, value)
				}
				if total.Sign() != 0 || borrowed >= 10 && !equal(got.Adjustments["p"], fmt.Sprintf("-%d/10", borrowed/10)) || borrowed%10 != 0 && !equal(got.Residuals["p"], fmt.Sprintf("%d/100", borrowed%10)) {
					t.Fatal("conservation, debit bound or residual failed", borrowed, a, b, got)
				}
				in.Members[0], in.Members[2] = in.Members[2], in.Members[0]
				if !reflect.DeepEqual(Carry(in), got) {
					t.Fatal("permutation changed exact proposal", borrowed, a, b)
				}
				cases++
			}
		}
	}
	t.Logf("verified %d funded synthetic proposals plus every raw-funding rejection in 4225 input combinations", cases)
}
