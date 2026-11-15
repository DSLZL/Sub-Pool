package allocation

import (
	"math/big"
	"sort"

	"sub-pool/backend/internal/estimation"
)

type CarryMember struct {
	ID                                  string
	Baseline                            string
	RoundSpent                          string
	PeriodSpent                         string
	NextEntitlement                     string
	BaselineExact, NextEntitlementExact *estimation.Amount
}

type CarryInput struct {
	Mode, CostBasis, Quantum string
	Members                  []CarryMember
	Complete                 bool
}

type CarryResult struct {
	State       string
	Adjustments map[string]*estimation.Amount
	Residuals   map[string]*estimation.Amount
}

// Carry proposes new rights only; Complete requires independently proven frozen scope, period and next-period evidence.
func Carry(in CarryInput) CarryResult {
	out := CarryResult{State: "evidence_incomplete"}
	if !in.Complete || in.CostBasis != "wallet_deducted_usd" {
		return out
	}
	out.State = "invalid_evidence"
	quantum, ok := estimation.Decimal(in.Quantum)
	if !ok || quantum.Sign() <= 0 || in.Mode != "carry" && in.Mode != "no_carry" || len(in.Members) == 0 || len(in.Members) > 500 {
		return out
	}
	type member struct {
		id                    string
		borrow, unused, grant *big.Rat
	}
	members := make([]member, 0, len(in.Members))
	seen := map[string]bool{}
	borrowed, unused := new(big.Rat), new(big.Rat)
	postExit := false
	for _, input := range in.Members {
		baseline, bOK := estimation.Decimal(input.Baseline)
		round, rOK := estimation.Decimal(input.RoundSpent)
		period, pOK := estimation.Decimal(input.PeriodSpent)
		grant, gOK := estimation.Decimal(input.NextEntitlement)
		if input.BaselineExact != nil {
			baseline, bOK = input.BaselineExact.Rational()
			bOK = bOK && input.Baseline == ""
		}
		if input.NextEntitlementExact != nil {
			grant, gOK = input.NextEntitlementExact.Rational()
			gOK = gOK && input.NextEntitlement == ""
		}
		if input.ID == "" || seen[input.ID] || !bOK || !rOK || !pOK || !gOK || round.Cmp(period) > 0 {
			return out
		}
		seen[input.ID] = true
		m := member{id: input.ID, borrow: new(big.Rat), unused: new(big.Rat), grant: grant}
		if round.Cmp(baseline) > 0 {
			m.borrow.Sub(round, baseline)
		}
		if baseline.Cmp(period) > 0 {
			m.unused.Sub(baseline, period)
		}
		postExit = postExit || new(big.Rat).Sub(period, baseline).Cmp(m.borrow) > 0
		borrowed.Add(borrowed, m.borrow)
		unused.Add(unused, m.unused)
		members = append(members, m)
	}
	if in.Mode == "no_carry" {
		return CarryResult{State: "no_carry", Adjustments: map[string]*estimation.Amount{}, Residuals: map[string]*estimation.Amount{}}
	}
	if postExit {
		out.State = "unattributed_post_exit_spending"
		return out
	}
	if borrowed.Cmp(unused) > 0 {
		out.State = "funding_or_evidence_deficit"
		return out
	}
	if borrowed.Sign() == 0 {
		return CarryResult{State: "no_borrowing", Adjustments: map[string]*estimation.Amount{}, Residuals: map[string]*estimation.Amount{}}
	}
	adjustments, residuals := map[string]*estimation.Amount{}, map[string]*estimation.Amount{}
	units := new(big.Int)
	for _, m := range members {
		quotient := new(big.Rat).Quo(m.borrow, quantum)
		debitUnits := new(big.Int).Quo(quotient.Num(), quotient.Denom())
		debit := new(big.Rat).Mul(new(big.Rat).SetInt(debitUnits), quantum)
		if debit.Cmp(m.grant) > 0 {
			out.State = "next_entitlement_insufficient"
			return out
		}
		units.Add(units, debitUnits)
		if debit.Sign() > 0 {
			adjustments[m.id] = estimation.Exact(new(big.Rat).Neg(debit))
		}
		if residual := new(big.Rat).Sub(m.borrow, debit); residual.Sign() > 0 {
			residuals[m.id] = estimation.Exact(residual)
		}
	}
	type lender struct {
		id        string
		units     *big.Int
		remainder *big.Rat
	}
	lenders := []lender{}
	left := new(big.Int).Set(units)
	for _, m := range members {
		if m.unused.Sign() == 0 {
			continue
		}
		share := new(big.Rat).Quo(new(big.Rat).Mul(new(big.Rat).SetInt(units), m.unused), unused)
		whole := new(big.Int).Quo(share.Num(), share.Denom())
		lenders = append(lenders, lender{m.id, whole, new(big.Rat).Sub(share, new(big.Rat).SetInt(whole))})
		left.Sub(left, whole)
	}
	sort.Slice(lenders, func(i, j int) bool {
		if cmp := lenders[i].remainder.Cmp(lenders[j].remainder); cmp != 0 {
			return cmp > 0
		}
		return lenders[i].id < lenders[j].id
	})
	for _, lender := range lenders {
		if left.Sign() > 0 {
			lender.units.Add(lender.units, big.NewInt(1))
			left.Sub(left, big.NewInt(1))
		}
		if lender.units.Sign() > 0 {
			adjustments[lender.id] = estimation.Exact(new(big.Rat).Mul(new(big.Rat).SetInt(lender.units), quantum))
		}
	}
	return CarryResult{State: "scenario", Adjustments: adjustments, Residuals: residuals}
}
