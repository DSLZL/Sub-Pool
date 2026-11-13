// Package estimation evaluates conditional cost/quota scenarios, never wallet targets.
package estimation

import (
	"math/big"
	"regexp"
	"strconv"
)

const Plan = "empirical_min_v1"
const Rules = "gate-003-rules-v1"

var decimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,37})(\.[0-9]{1,18})?$`)
var quotaNumber = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE]([+-]?[0-9]+))?$`)

func quotaRatio(value string) (*big.Rat, bool) {
	if len(value) > 64 {
		return nil, false
	}
	parts := quotaNumber.FindStringSubmatch(value)
	if parts == nil {
		return nil, false
	}
	if parts[1] != "" {
		exponent, err := strconv.Atoi(parts[1])
		if err != nil || exponent < -18 || exponent > 18 {
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(value)
	return r, ok && r.Cmp(big.NewRat(100, 1)) <= 0
}

func Decimal(value string) (*big.Rat, bool) {
	if !decimal.MatchString(value) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(value)
	return r, ok
}

func ValidKeep(value string) bool {
	r, ok := Decimal(value)
	return ok && r.Sign() > 0 && r.Cmp(big.NewRat(1, 1)) <= 0
}

type Amount struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}

// Rational parses decimal integers without letting leading zeros change their base.
func (a *Amount) Rational() (*big.Rat, bool) {
	if a == nil {
		return nil, false
	}
	numerator, nOK := new(big.Int).SetString(a.Numerator, 10)
	denominator, dOK := new(big.Int).SetString(a.Denominator, 10)
	if !nOK || !dOK || numerator.Sign() < 0 || denominator.Sign() <= 0 {
		return nil, false
	}
	return new(big.Rat).SetFrac(numerator, denominator), true
}

func Exact(r *big.Rat) *Amount {
	return &Amount{Numerator: r.Num().String(), Denominator: r.Denom().String()}
}

type Result struct {
	State     string  `json:"state"`
	Capacity  *Amount `json:"capacity"`
	Remaining *Amount `json:"remaining"`
	RangeMin  *Amount `json:"range_min"`
	RangeMax  *Amount `json:"range_max"`
}

type Input struct {
	Keep         string
	Quota        []string
	Costs        []string
	CurrentQuota string
	Complete     bool
}

// Evaluate requires independently verified adjacent interval costs; pagination cannot supply Complete.
func Evaluate(in Input) Result {
	out := Result{State: "not_configured"}
	if !ValidKeep(in.Keep) {
		return out
	}
	out.State = "invalid_evidence"
	if len(in.Quota) > 2000 {
		return out
	}
	quota := make([]*big.Rat, len(in.Quota))
	for i, v := range in.Quota {
		q, ok := quotaRatio(v)
		if !ok {
			return out
		}
		quota[i] = q
	}
	if len(quota) < 3 {
		out.State = "insufficient_samples"
		return out
	}
	current, ok := quotaRatio(in.CurrentQuota)
	if !ok {
		return out
	}
	if current.Cmp(quota[len(quota)-1]) < 0 {
		out.State = "condition_changed"
		return out
	}
	delta := make([]*big.Rat, len(quota)-1)
	for i := range delta {
		delta[i] = new(big.Rat).Sub(quota[i+1], quota[i])
		if delta[i].Sign() < 0 {
			out.State = "condition_changed"
			return out
		}
	}
	if !in.Complete || len(in.Costs) != len(delta) {
		out.State = "evidence_incomplete"
		return out
	}
	var low, high *big.Rat
	for i, v := range in.Costs {
		cost, ok := Decimal(v)
		if !ok {
			return out
		}
		if cost.Sign() == 0 || delta[i].Sign() == 0 {
			out.State = "non_identifying_delta"
			return out
		}
		empirical := new(big.Rat).Quo(new(big.Rat).Mul(big.NewRat(100, 1), cost), delta[i])
		if low == nil || empirical.Cmp(low) < 0 {
			low = new(big.Rat).Set(empirical)
		}
		if high == nil || empirical.Cmp(high) > 0 {
			high = new(big.Rat).Set(empirical)
		}
	}
	keep, _ := Decimal(in.Keep)
	capacity := new(big.Rat).Mul(low, keep)
	remaining := new(big.Rat).Quo(new(big.Rat).Mul(capacity, new(big.Rat).Sub(big.NewRat(100, 1), current)), big.NewRat(100, 1))
	return Result{State: "scenario", Capacity: Exact(capacity), Remaining: Exact(remaining), RangeMin: Exact(low), RangeMax: Exact(high)}
}

// MinimumRemaining keeps overlapping required windows from creating additive capacity.
func MinimumRemaining(results []Result) *Amount {
	if len(results) == 0 {
		return nil
	}
	var smallest *big.Rat
	for _, r := range results {
		if r.State != "scenario" {
			return nil
		}
		amount, ok := r.Remaining.Rational()
		if !ok {
			return nil
		}
		if smallest == nil || amount.Cmp(smallest) < 0 {
			smallest = amount
		}
	}
	return Exact(smallest)
}
