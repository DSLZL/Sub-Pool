package connection

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) weeklyWalletUses(ctx context.Context, tx pgx.Tx, c storedConfig, a storedAccount, kind, secret, user string,
	key weeklyWalletAccountKey, start, end time.Time) ([]weeklyWalletUse, []string, bool, error) {
	var usage []weeklyWalletUse
	var proofs []string
	if !start.Before(end) {
		return nil, nil, true, nil
	}
	if end.Sub(start) > 31*24*time.Hour {
		return nil, nil, false, nil
	}
	for day := start.UTC().Truncate(24 * time.Hour); day.Before(end); day = day.AddDate(0, 0, 1) {
		date := day.Format(weeklyDate)
		published, err := readWeeklyWalletDay(ctx, tx, c, a, date, user)
		if err != nil {
			return nil, nil, false, err
		}
		facts, proof := published.facts, published.proof
		if proof == "" {
			var ok bool
			facts, proof, ok = s.readWeeklyWalletDay(ctx, c, kind, secret, a.UpstreamID, date, user)
			if !ok {
				return nil, nil, false, nil
			}
		}
		proofs = append(proofs, date+":"+proof)
		for _, fact := range facts {
			if fact.CreatedAt.Before(start) || !fact.CreatedAt.Before(end) {
				continue
			}
			if fact.UserID == nil || *fact.UserID != user || fact.BillingCandidate != "wallet" || fact.Metrics["actual_cost"] == nil {
				return nil, nil, false, nil
			}
			text := *fact.Metrics["actual_cost"]
			cost := weeklyWalletAmount(text)
			if cost == nil || cost.Sign() < 0 || usageDecimal(cost) != text {
				return nil, nil, false, nil
			}
			usage = append(usage, weeklyWalletUse{fact.ID, key, fact.CreatedAt, text})
		}
	}
	return usage, proofs, true, nil
}

type weeklyWalletDay struct {
	facts []UsageFact
	proof string
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

func readWeeklyWalletDay(ctx context.Context, tx pgx.Tx, c storedConfig, a storedAccount, day, user string) (weeklyWalletDay, error) {
	var out weeklyWalletDay
	var id string
	err := tx.QueryRow(ctx, `SELECT j.published_capture_id::text
		FROM usage_day_jobs j JOIN usage_captures u ON u.id=j.published_capture_id
		WHERE j.source_id=$1 AND j.account_id=$2 AND j.day=$3::date AND j.fingerprint=$4
		AND j.connection_revision=$5 AND j.state='complete' AND NOT u.staging`,
		a.SourceID, a.ID, day, a.fingerprint, c.Revision).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT fact FROM usage_capture_facts WHERE capture_id=$1
		AND (fact->>'user_id'=$2 OR fact->>'user_id' IS NULL)
		AND fact->>'billing_type' IS DISTINCT FROM '1'
		ORDER BY upstream_id LIMIT $3`, id, user, nativeWeeklyDayLimit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var fact UsageFact
		if err = rows.Scan(&fact); err != nil {
			return out, err
		}
		out.facts = append(out.facts, fact)
	}
	if len(out.facts) <= nativeWeeklyDayLimit {
		out.proof = "capture:" + id
	} else {
		out.facts = nil
	}
	return out, rows.Err()
}

func (s *Service) readWeeklyWalletDay(ctx context.Context, c storedConfig, kind, secret, account, day, user string) ([]UsageFact, string, bool) {
	var all []UsageFact
	var total, lastID int64
	var firstDigest, lastDigest string
	for page := 1; ; page++ {
		batch, check := s.readUsageDayPage(ctx, c, kind, secret, account, day, page, usagePageSize, "asc", user)
		if check.State != "available" || batch.total > nativeWeeklyDayLimit {
			return nil, "", false
		}
		digest, err := usageDayDigest("", batch.facts)
		if err != nil {
			return nil, "", false
		}
		if page == 1 {
			total, firstDigest = batch.total, digest
		}
		if batch.total != total || len(batch.ids) != 0 && batch.ids[0] <= lastID {
			return nil, "", false
		}
		all = append(all, batch.facts...)
		if int64(len(all)) == total {
			lastDigest = digest
			break
		}
		lastID = batch.ids[len(batch.ids)-1]
	}
	lastPage := max(1, (len(all)+usagePageSize-1)/usagePageSize)
	for _, anchor := range []struct {
		page   int
		digest string
	}{{1, firstDigest}, {lastPage, lastDigest}} {
		checkPage, check := s.readUsageDayPage(ctx, c, kind, secret, account, day, anchor.page, usagePageSize, "asc", user)
		digest, err := usageDayDigest("", checkPage.facts)
		if check.State != "available" || err != nil || checkPage.total != total || digest != anchor.digest {
			return nil, "", false
		}
		if lastPage == 1 {
			break
		}
	}
	digest, err := usageDayDigest("", all)
	return all, strconv.FormatInt(total, 10) + ":" + digest, err == nil
}
