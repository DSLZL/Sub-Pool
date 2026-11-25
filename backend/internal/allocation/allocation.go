// Package allocation evaluates conditional rights in proven wallet-deduction units.
package allocation

import (
	"math/big"

	"sub-pool/backend/internal/estimation"
)

type Segment struct {
	Cost   string
	Shares map[string]string
}

type Input struct {
	Remaining   *estimation.Amount
	Segments    []Segment
	Current     map[string]string
	Spent       map[string]string
	Adjustments map[string]string
	Complete    bool
	CostBasis   string
}

type Result struct {
	State   string
	Members map[string]*estimation.Amount
	Reserve *estimation.Amount
}

const (
	MemberLimit = 10_000
	SourceLimit = 10_000
)

// Evaluate never repairs a deficit, normalizes a reserved share or substitutes standard cost.
func Evaluate(in Input) Result {
	out := Result{State: "evidence_incomplete"}
	if !in.Complete || in.CostBasis != "wallet_deducted_usd" {
		return out
	}
	out.State = "invalid_evidence"
	remaining, valid := in.Remaining.Rational()
	if !valid {
		return out
	}
	if len(in.Current) > MemberLimit || len(in.Spent) > MemberLimit || len(in.Adjustments) > MemberLimit {
		out.State = "evidence_limit"
		return out
	}
	amounts := map[string]*big.Rat{}
	add := func(total *big.Rat, shares map[string]string) bool {
		if len(shares) > MemberLimit {
			out.State = "evidence_limit"
			return false
		}
		sum := new(big.Rat)
		for id, text := range shares {
			share, valid := estimation.Decimal(text)
			if id == "" || !valid || share.Cmp(big.NewRat(1, 1)) > 0 {
				return false
			}
			sum.Add(sum, share)
			if amounts[id] == nil {
				if len(amounts) >= MemberLimit {
					out.State = "evidence_limit"
					return false
				}
				amounts[id] = new(big.Rat)
			}
			amounts[id].Add(amounts[id], new(big.Rat).Mul(total, share))
		}
		return sum.Cmp(big.NewRat(1, 1)) <= 0
	}
	consumed := new(big.Rat)
	for _, segment := range in.Segments {
		cost, valid := estimation.Decimal(segment.Cost)
		if !valid || !add(cost, segment.Shares) {
			return out
		}
		consumed.Add(consumed, cost)
	}
	if !add(remaining, in.Current) {
		return out
	}
	spent := new(big.Rat)
	for id, text := range in.Spent {
		value, valid := estimation.Decimal(text)
		if id == "" || !valid {
			return out
		}
		spent.Add(spent, value)
		if amounts[id] == nil {
			if len(amounts) >= MemberLimit {
				out.State = "evidence_limit"
				return out
			}
			amounts[id] = new(big.Rat)
		}
		amounts[id].Sub(amounts[id], value)
	}
	if spent.Cmp(consumed) != 0 {
		out.State = "evidence_incomplete"
		return out
	}
	for id, text := range in.Adjustments {
		negative := len(text) > 0 && text[0] == '-'
		if negative {
			text = text[1:]
		}
		value, valid := estimation.Decimal(text)
		if !valid || amounts[id] == nil || negative && value.Sign() == 0 {
			return out
		}
		if negative {
			value.Neg(value)
		}
		amounts[id].Add(amounts[id], value)
	}
	for _, value := range amounts {
		if value.Sign() < 0 {
			out.State = "allocation_deficit"
			return out
		}
	}
	out.State, out.Members = "scenario", map[string]*estimation.Amount{}
	reserve := new(big.Rat).Set(remaining)
	for id, value := range amounts {
		out.Members[id] = estimation.Exact(value)
		reserve.Sub(reserve, value)
	}
	if reserve.Sign() < 0 {
		return Result{State: "invalid_evidence"}
	}
	out.Reserve = estimation.Exact(reserve)
	return out
}

// Global requires every server-selected source, including sources hidden from the reader.
func Global(sources []Result, participant string) *estimation.Amount {
	if len(sources) == 0 || len(sources) > SourceLimit || participant == "" {
		return nil
	}
	total := new(big.Rat)
	for _, source := range sources {
		if source.State != "scenario" {
			return nil
		}
		value, ok := source.Members[participant].Rational()
		if !ok {
			return nil
		}
		total.Add(total, value)
	}
	return estimation.Exact(total)
}
