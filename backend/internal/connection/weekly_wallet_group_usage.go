package connection

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type weeklyWalletUsageKey struct {
	Account weeklyWalletAccountKey
	Range   usageDayRange
}

type weeklyWalletUsageRequest struct {
	Key     weeklyWalletUsageKey
	Account storedAccount
}

type weeklyWalletUsageProof struct {
	Day     time.Time
	Range   usageDayRange
	Capture string
}

type weeklyWalletUsageResult struct {
	Uses   []weeklyWalletUse
	Proofs []weeklyWalletUsageProof
	Ready  bool
}

type weeklyWalletUsagePlan struct {
	Requests           *[]weeklyWalletUsageRequest
	Results            map[weeklyWalletUsageKey]weeklyWalletUsageResult
	Binding            string
	User               string
	ConnectionRevision string
	lockProofs         bool
}

func (p *weeklyWalletUsagePlan) discover() bool {
	return p != nil && p.Requests != nil
}

func (p *weeklyWalletUsagePlan) read(ctx context.Context, tx pgx.Tx, c storedConfig, request weeklyWalletUsageRequest, user string) (weeklyWalletUsageResult, error) {
	if !request.Key.Range.Start.Before(request.Key.Range.End) {
		return weeklyWalletUsageResult{Ready: true}, nil
	}
	if p == nil {
		return readWeeklyWalletUsage(ctx, tx, c, request, user)
	}
	if p.ConnectionRevision != c.Revision || p.User != user {
		return weeklyWalletUsageResult{}, nil
	}
	result := p.Results[request.Key]
	if !result.Ready {
		return result, nil
	}
	for _, expected := range result.Proofs {
		proof, err := usageDayCurrentProof(ctx, tx, c, request.Account, expected.Day, p.lockProofs)
		if err != nil || proof.Capture != expected.Capture || !usageDayRangesCover(proof.Ranges, expected.Range) {
			return weeklyWalletUsageResult{}, err
		}
	}
	return result, nil
}

func (r weeklyWalletUsageResult) references() []string {
	var refs []string
	for _, proof := range r.Proofs {
		refs = append(refs, proof.Day.Format(weeklyDate)+":capture:"+proof.Capture)
	}
	return refs
}

func readWeeklyWalletUsage(ctx context.Context, tx pgx.Tx, c storedConfig, request weeklyWalletUsageRequest, user string) (weeklyWalletUsageResult, error) {
	window := request.Key.Range
	if window.End.Sub(window.Start) > 31*24*time.Hour {
		return weeklyWalletUsageResult{}, nil
	}
	var result weeklyWalletUsageResult
	for day := window.Start.UTC().Truncate(24 * time.Hour); day.Before(window.End); day = day.AddDate(0, 0, 1) {
		part := window.intersect(usageCalendarDay(day))
		proof, err := readUsageDayProof(ctx, tx, c, request.Account, day)
		if err != nil || proof.Capture == "" || !usageDayRangesCover(proof.Ranges, part) {
			return weeklyWalletUsageResult{}, err
		}
		result.Proofs = append(result.Proofs, weeklyWalletUsageProof{Day: day, Range: part, Capture: proof.Capture})
		var after int64
		for {
			facts, next, err := readUsageDayFacts(ctx, tx, proof.Capture, user, after)
			if err != nil {
				return weeklyWalletUsageResult{}, err
			}
			for _, fact := range facts {
				if !part.contains(fact.CreatedAt) || fact.UserID != nil && *fact.UserID != user || fact.BillingCandidate == "subscription" {
					continue
				}
				text := fact.Metrics["actual_cost"]
				if fact.UserID == nil || fact.BillingCandidate != "wallet" || text == nil {
					return weeklyWalletUsageResult{}, nil
				}
				cost := weeklyWalletAmount(*text)
				if cost == nil || cost.Sign() < 0 {
					return weeklyWalletUsageResult{}, nil
				}
				result.Uses = append(result.Uses, weeklyWalletUse{fact.ID, request.Key.Account, fact.CreatedAt, usageDecimal(cost)})
			}
			if len(facts) < usageLocalPageSize {
				break
			}
			after = next
		}
	}
	result.Ready = true
	return result, nil
}

func weeklyWalletCoveredThrough(ctx context.Context, tx pgx.Tx, c storedConfig, request weeklyWalletUsageRequest) (time.Time, error) {
	window := request.Key.Range
	if window.End.Sub(window.Start) > 31*24*time.Hour {
		return window.Start, nil
	}
	for day := window.Start.UTC().Truncate(24 * time.Hour); day.Before(window.End); day = day.AddDate(0, 0, 1) {
		part := window.intersect(usageCalendarDay(day))
		proof, err := readUsageDayProof(ctx, tx, c, request.Account, day)
		if err != nil {
			return time.Time{}, err
		}
		if proof.Capture != "" && usageDayRangesCover(proof.Ranges, part) {
			continue
		}
		for _, r := range proof.Ranges {
			if r.contains(part.Start) {
				return r.End, nil
			}
		}
		return part.Start, nil
	}
	return window.End, nil
}

func (s *Service) discoverWeeklyWalletUsage(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, anchor string, cutoff time.Time) ([]weeklyWalletUsageRequest, error) {
	var requests []weeklyWalletUsageRequest
	_, _, _, _, err := s.weeklyWalletGroupCandidate(ctx, tx, env, anchor, nil, cutoff, nil, &weeklyWalletUsagePlan{Requests: &requests})
	return requests, err
}

func (s *Service) prepareWeeklyWalletUsage(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, anchor string, cutoff time.Time, chooseCutoff bool) (*weeklyWalletUsagePlan, time.Time, error) {
	requests, err := s.discoverWeeklyWalletUsage(ctx, tx, env, anchor, cutoff)
	if err != nil {
		return nil, cutoff, err
	}
	if chooseCutoff {
		common := cutoff
		var latestStart time.Time
		for _, request := range requests {
			if !request.Key.Range.Start.Before(request.Key.Range.End) {
				continue
			}
			latestStart = maxTime(latestStart, request.Key.Range.Start)
			end, err := weeklyWalletCoveredThrough(ctx, tx, env.config, request)
			if err != nil {
				return nil, cutoff, err
			}
			if request.Key.Range.Start.Before(end) && end.Before(common) {
				common = end.Truncate(time.Microsecond)
			}
		}
		if common.After(latestStart) && common.Before(cutoff) && time.Since(common) <= time.Duration(env.policy.StaleAfterSeconds)*time.Second {
			cutoff = common
			requests, err = s.discoverWeeklyWalletUsage(ctx, tx, env, anchor, cutoff)
			if err != nil {
				return nil, cutoff, err
			}
		}
	}
	plan := &weeklyWalletUsagePlan{
		Results: make(map[weeklyWalletUsageKey]weeklyWalletUsageResult, len(requests)),
		Binding: env.member.Binding.ID, User: env.member.Binding.UpstreamUserID, ConnectionRevision: env.config.Revision,
	}
	for _, request := range requests {
		result, err := readWeeklyWalletUsage(ctx, tx, env.config, request, env.member.Binding.UpstreamUserID)
		if err != nil {
			return nil, cutoff, err
		}
		plan.Results[request.Key] = result
	}
	return plan, cutoff, nil
}

// The stock log keeps ten decimal places, while its wallet update stores eight.
// A logged half-quantum cannot establish the original rounding direction.
func stockWeeklyWalletDebit(cost *big.Rat) (*big.Rat, bool) {
	floor := weeklyWalletFloor(cost)
	remainder := new(big.Rat).Sub(cost, floor)
	switch remainder.Cmp(big.NewRat(1, 200000000)) {
	case 0:
		return nil, false
	case 1:
		floor.Add(floor, big.NewRat(1, 100000000))
	}
	return floor, true
}

func (s *Service) weeklyWalletStockPrecision(ctx context.Context, c storedConfig, kind, secret string) bool {
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/system/version", kind, secret, "system.version", true)
	var reply struct {
		Version string `json:"version"`
	}
	return check.State == "available" && decode(body, &reply) == nil && strings.TrimPrefix(reply.Version, "v") == "0.2.23"
}
