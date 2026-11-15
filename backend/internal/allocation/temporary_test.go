package allocation

import (
	"math/big"
	"reflect"
	"testing"

	"sub-pool/backend/internal/estimation"
)

func TestTemporaryExplicitFundedCapsPreserveExactSpendingReserveAndOrder(t *testing.T) {
	input := TemporaryInput{Remaining: estimation.Exact(big.NewRat(7, 2)), Quantum: "0.25", CostBasis: "wallet_deducted_usd", Complete: true,
		Members: []TemporaryMember{{ID: "borrower", Cap: "1.25", Spent: "0.5"}, {ID: "lender", Cap: "2.75", Spent: "0.25"}}}
	result := Temporary(input)
	if result.State != "scenario" || !reflect.DeepEqual(result.Members, map[string]*estimation.Amount{"borrower": {Numerator: "3", Denominator: "4"}, "lender": {Numerator: "5", Denominator: "2"}}) || !reflect.DeepEqual(result.Reserve, &estimation.Amount{Numerator: "1", Denominator: "4"}) {
		t.Fatal("caps, exact spending or same-source reserve changed", result)
	}
	input.Members[0], input.Members[1] = input.Members[1], input.Members[0]
	if !reflect.DeepEqual(Temporary(input), result) {
		t.Fatal("member order changed funded targets")
	}
	input.Remaining = estimation.Exact(big.NewRat(0, 1))
	input.Members = []TemporaryMember{{ID: "known-zero", Cap: "0.25", Spent: "0.25"}}
	zero := Temporary(input)
	if zero.State != "scenario" || zero.Members["known-zero"].Numerator != "0" || zero.Reserve.Numerator != "0" {
		t.Fatal("confirmed exhaustion became unknown", zero)
	}
}

func TestTemporaryMissingUnfundedMalformedAndOverdrawnInputsPublishNoProposal(t *testing.T) {
	base := TemporaryInput{Remaining: estimation.Exact(big.NewRat(1, 1)), Members: []TemporaryMember{{ID: "member", Cap: "1", Spent: "0"}}, Quantum: "0.00000001", CostBasis: "wallet_deducted_usd", Complete: true}
	cases := []struct {
		name, state string
		change      func(*TemporaryInput)
	}{
		{"missing-proof", "evidence_incomplete", func(v *TemporaryInput) { v.Complete = false }},
		{"standard-is-not-wallet", "evidence_incomplete", func(v *TemporaryInput) { v.CostBasis = "standard_usd" }},
		{"missing-remaining", "invalid_evidence", func(v *TemporaryInput) { v.Remaining = nil }},
		{"malformed-remaining", "invalid_evidence", func(v *TemporaryInput) { v.Remaining = &estimation.Amount{Numerator: "1", Denominator: "0"} }},
		{"zero-quantum", "invalid_evidence", func(v *TemporaryInput) { v.Quantum = "0" }},
		{"malformed-quantum", "invalid_evidence", func(v *TemporaryInput) { v.Quantum = "0x1" }},
		{"unfunded", "funding_or_evidence_deficit", func(v *TemporaryInput) { v.Members[0].Cap = "1.00000001" }},
		{"overdrawn", "allocation_deficit", func(v *TemporaryInput) { v.Members[0].Spent = "1.00000001" }},
		{"negative-cap", "invalid_evidence", func(v *TemporaryInput) { v.Members[0].Cap = "-1" }},
		{"padded-cap", "invalid_evidence", func(v *TemporaryInput) { v.Members[0].Cap = "01" }},
		{"malformed-spent", "invalid_evidence", func(v *TemporaryInput) { v.Members[0].Spent = "1e-8" }},
		{"unrepresentable-cap", "invalid_evidence", func(v *TemporaryInput) { v.Members[0].Cap = "0.000000001" }},
		{"unrepresentable-spent", "invalid_evidence", func(v *TemporaryInput) { v.Members[0].Spent = "0.000000001" }},
		{"missing-members", "invalid_evidence", func(v *TemporaryInput) { v.Members = nil }},
		{"duplicate-member", "invalid_evidence", func(v *TemporaryInput) { v.Members = append(v.Members, v.Members[0]) }},
		{"empty-identity", "invalid_evidence", func(v *TemporaryInput) { v.Members[0].ID = "" }},
		{"member-bound", "invalid_evidence", func(v *TemporaryInput) { v.Members = make([]TemporaryMember, 501) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.Members = append([]TemporaryMember{}, base.Members...)
			tc.change(&input)
			result := Temporary(input)
			if result.State != tc.state || result.Members != nil || result.Reserve != nil {
				t.Fatal("invalid cap/evidence published money", result)
			}
		})
	}
}
