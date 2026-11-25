package allocation

import (
	"math/big"

	"sub-pool/backend/internal/estimation"
)

type TemporaryMember struct {
	ID, Cap, Spent string
}

type TemporaryInput struct {
	Remaining          *estimation.Amount
	Members            []TemporaryMember
	Quantum, CostBasis string
	Complete           bool
}

// Temporary uses explicit funded caps and independently attributed spending; it never changes shares or supplies missing evidence.
func Temporary(in TemporaryInput) Result {
	out := Result{State: "evidence_incomplete"}
	if !in.Complete || in.CostBasis != "wallet_deducted_usd" {
		return out
	}
	out.State = "invalid_evidence"
	remaining, valid := in.Remaining.Rational()
	quantum, qOK := estimation.Decimal(in.Quantum)
	if len(in.Members) > MemberLimit {
		return Result{State: "evidence_limit"}
	}
	if !valid || !qOK || quantum.Sign() <= 0 || len(in.Members) == 0 {
		return out
	}
	members := map[string]*estimation.Amount{}
	total := new(big.Rat)
	for _, member := range in.Members {
		cap, cOK := estimation.Decimal(member.Cap)
		spent, sOK := estimation.Decimal(member.Spent)
		if member.ID == "" || members[member.ID] != nil || !cOK || !sOK {
			return out
		}
		if !new(big.Rat).Quo(cap, quantum).IsInt() || !new(big.Rat).Quo(spent, quantum).IsInt() {
			return out
		}
		if spent.Cmp(cap) > 0 {
			out.State = "allocation_deficit"
			return out
		}
		value := new(big.Rat).Sub(cap, spent)
		members[member.ID] = estimation.Exact(value)
		total.Add(total, value)
	}
	if total.Cmp(remaining) > 0 {
		out.State = "funding_or_evidence_deficit"
		return out
	}
	return Result{State: "scenario", Members: members, Reserve: estimation.Exact(new(big.Rat).Sub(remaining, total))}
}
