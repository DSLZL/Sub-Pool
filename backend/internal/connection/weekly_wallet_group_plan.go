package connection

import (
	"math/big"
	"sort"
	"time"
)

type weeklyWalletAccountKey struct {
	PoolID    string `json:"pool_id"`
	AccountID string `json:"account_id"`
}

type weeklyWalletCycle struct {
	weeklyWalletAccountKey
	Start time.Time `json:"period_start"`
	End   time.Time `json:"period_end"`
}

type weeklyWalletContribution struct {
	weeklyWalletCycle
	Name               string  `json:"name"`
	ContractID         string  `json:"contract_id"`
	Phase              string  `json:"phase"`
	EstimateID         *string `json:"estimate_id"`
	SharePercent       string  `json:"share_percent"`
	UnallocatedPercent string  `json:"unallocated_percent,omitempty"`
	Gross              string  `json:"gross"`
	Spent              string  `json:"spent"`
	OpeningDebt        string  `json:"opening_debt"`
	Covered            string  `json:"covered"`
	UsedToCover        string  `json:"used_to_cover"`
	Debt               string  `json:"debt"`
	Remaining          string  `json:"remaining"`
	LastFormalGross    string  `json:"last_formal_gross"`
}

type weeklyWalletOffset struct {
	From   weeklyWalletCycle `json:"from"`
	To     weeklyWalletCycle `json:"to"`
	Amount string            `json:"amount"`
	At     time.Time         `json:"at"`
}

type weeklyWalletGroupPlan struct {
	Cutoff        time.Time                  `json:"cutoff"`
	BalanceBefore string                     `json:"balance_before"`
	Target        string                     `json:"target"`
	Delta         string                     `json:"delta"`
	Contributions []weeklyWalletContribution `json:"contributions"`
	Closed        []weeklyWalletContribution `json:"closed"`
	Offsets       []weeklyWalletOffset       `json:"offsets"`
}

type weeklyWalletUse struct {
	ID      string
	Account weeklyWalletAccountKey
	At      time.Time
	Cost    string
}

type weeklyWalletCycleBook struct {
	weeklyWalletContribution
	gross, spent, debt, covered, used, net *big.Rat
}

func newWeeklyWalletCycleBook(c weeklyWalletContribution) (*weeklyWalletCycleBook, bool) {
	book := &weeklyWalletCycleBook{weeklyWalletContribution: c, net: new(big.Rat)}
	fields := []struct {
		value string
		dst   **big.Rat
	}{
		{c.Gross, &book.gross}, {c.Spent, &book.spent}, {c.OpeningDebt, &book.debt},
		{c.Covered, &book.covered}, {c.UsedToCover, &book.used},
	}
	for _, field := range fields {
		*field.dst = weeklyWalletAmount(field.value)
		if *field.dst == nil || (*field.dst).Sign() < 0 {
			return nil, false
		}
	}
	return book, c.Start.Before(c.End)
}

func weeklyWalletCycleSnapshot(book *weeklyWalletCycleBook) weeklyWalletContribution {
	out := book.weeklyWalletContribution
	out.Gross = usageDecimal(book.gross)
	out.Spent = usageDecimal(book.spent)
	out.OpeningDebt = usageDecimal(book.debt)
	out.Covered = usageDecimal(book.covered)
	out.UsedToCover = usageDecimal(book.used)
	out.Debt, out.Remaining = "0", "0"
	if book.net.Sign() < 0 {
		out.Debt = usageDecimal(new(big.Rat).Neg(book.net))
	} else {
		out.Remaining = usageDecimal(book.net)
	}
	return out
}

func weeklyWalletCycleOrder(left, right weeklyWalletCycle) bool {
	if !left.End.Equal(right.End) {
		return left.End.Before(right.End)
	}
	if left.PoolID != right.PoolID {
		return left.PoolID < right.PoolID
	}
	return left.AccountID < right.AccountID
}

func offsetWeeklyWalletCycles(books []*weeklyWalletCycleBook, at time.Time, expiry bool, plan *weeklyWalletGroupPlan) {
	var funds, debts []*weeklyWalletCycleBook
	for _, book := range books {
		if at.Before(book.Start) || expiry && at.Equal(book.Start) || at.After(book.End) {
			continue
		}
		book.net.Sub(book.gross, book.spent)
		book.net.Sub(book.net, book.debt)
		book.net.Sub(book.net, book.used)
		book.net.Add(book.net, book.covered)
		switch book.net.Sign() {
		case -1:
			debts = append(debts, book)
		case 1:
			funds = append(funds, book)
		}
	}
	for _, debtor := range debts {
		for len(funds) > 0 && debtor.net.Sign() < 0 {
			donor := funds[0]
			amount := new(big.Rat).Neg(debtor.net)
			if amount.Cmp(donor.net) > 0 {
				amount.Set(donor.net)
			}
			debtor.covered.Add(debtor.covered, amount)
			debtor.net.Add(debtor.net, amount)
			donor.used.Add(donor.used, amount)
			donor.net.Sub(donor.net, amount)
			plan.Offsets = append(plan.Offsets, weeklyWalletOffset{donor.weeklyWalletCycle, debtor.weeklyWalletCycle, usageDecimal(amount), at})
			if donor.net.Sign() == 0 {
				funds = funds[1:]
			}
		}
	}
}

// Keep donor debits in their own periods even when the covered cycle expires.
func calculateWeeklyWalletGroup(previous, current []weeklyWalletContribution, usage []weeklyWalletUse, cutoff time.Time, balance string) (weeklyWalletGroupPlan, bool) {
	plan := weeklyWalletGroupPlan{
		Cutoff: cutoff, BalanceBefore: balance, Contributions: []weeklyWalletContribution{},
		Offsets: []weeklyWalletOffset{}, Closed: []weeklyWalletContribution{},
	}
	before := weeklyWalletAmount(balance)
	if before == nil || len(current) == 0 {
		return plan, false
	}

	prior := make(map[weeklyWalletAccountKey]weeklyWalletContribution, len(previous))
	latest := make(map[weeklyWalletAccountKey]*weeklyWalletCycleBook, len(current))
	var books []*weeklyWalletCycleBook
	var ends []time.Time
	for _, contribution := range previous {
		if _, duplicate := prior[contribution.weeklyWalletAccountKey]; duplicate {
			return plan, false
		}
		prior[contribution.weeklyWalletAccountKey] = contribution
	}
	for _, desired := range current {
		key := desired.weeklyWalletAccountKey
		if latest[key] != nil || desired.Start.After(cutoff) || !desired.End.After(cutoff) {
			return plan, false
		}
		active := desired
		old, exists := prior[key]
		delete(prior, key)
		active.Gross, active.Spent, active.OpeningDebt = "0", "0", "0"
		active.Covered, active.UsedToCover = "0", "0"
		if exists && old.End.Equal(active.End) && old.Start.Equal(active.Start) {
			active = old
		} else if exists {
			closed, valid := newWeeklyWalletCycleBook(old)
			if !valid || active.Start.Before(old.End) || old.End.After(cutoff) {
				return plan, false
			}
			books = append(books, closed)
			ends = append(ends, old.End)
		}
		book, valid := newWeeklyWalletCycleBook(active)
		if !valid {
			return plan, false
		}
		latest[key] = book
		books = append(books, book)
	}
	for _, expired := range prior {
		closed, valid := newWeeklyWalletCycleBook(expired)
		if !valid || expired.End.After(cutoff) {
			return plan, false
		}
		books = append(books, closed)
		ends = append(ends, expired.End)
	}
	sort.Slice(books, func(i, j int) bool {
		return weeklyWalletCycleOrder(books[i].weeklyWalletCycle, books[j].weeklyWalletCycle)
	})
	sort.Slice(ends, func(i, j int) bool { return ends[i].Before(ends[j]) })
	sort.Slice(usage, func(i, j int) bool {
		if usage[i].At.Equal(usage[j].At) {
			return usage[i].ID < usage[j].ID
		}
		return usage[i].At.Before(usage[j].At)
	})
	chargeUntil := func(at time.Time) bool {
		for len(usage) > 0 && usage[0].At.Before(at) {
			item := usage[0]
			usage = usage[1:]
			cost := weeklyWalletAmount(item.Cost)
			if cost == nil || cost.Sign() < 0 {
				return false
			}
			var owner *weeklyWalletCycleBook
			for _, book := range books {
				if book.weeklyWalletAccountKey == item.Account && !item.At.Before(book.Start) && item.At.Before(book.End) {
					owner = book
					break
				}
			}
			if owner == nil {
				return false
			}
			owner.spent.Add(owner.spent, cost)
		}
		return true
	}

	for i, end := range ends {
		if i > 0 && end.Equal(ends[i-1]) {
			continue
		}
		if !chargeUntil(end) {
			return plan, false
		}
		offsetWeeklyWalletCycles(books, end, true, &plan)
		for _, book := range books {
			if book.End.Equal(end) {
				snapshot := weeklyWalletCycleSnapshot(book)
				plan.Closed = append(plan.Closed, snapshot)
				if next := latest[book.weeklyWalletAccountKey]; next != nil {
					next.debt.Set(weeklyWalletAmount(snapshot.Debt))
				} else if snapshot.Debt != "0" {
					return plan, false
				}
			}
		}
	}
	if !chargeUntil(cutoff) || len(usage) != 0 {
		return plan, false
	}
	for _, desired := range current {
		book := latest[desired.weeklyWalletAccountKey]
		gross := weeklyWalletAmount(desired.Gross)
		if gross == nil || gross.Sign() < 0 || weeklyWalletFloor(gross).Cmp(gross) != 0 {
			return plan, false
		}
		book.weeklyWalletContribution = desired
		book.gross.Set(gross)
	}

	active := make([]*weeklyWalletCycleBook, 0, len(current))
	for _, book := range books {
		if book.End.After(cutoff) {
			active = append(active, book)
		}
	}
	offsetWeeklyWalletCycles(active, cutoff, false, &plan)
	target := new(big.Rat)
	for _, book := range active {
		if book.net.Sign() > 0 {
			target.Add(target, book.net)
		}
		plan.Contributions = append(plan.Contributions, weeklyWalletCycleSnapshot(book))
	}
	plan.Target = usageDecimal(target)
	plan.Delta = usageDecimal(target.Sub(target, before))
	return plan, true
}
