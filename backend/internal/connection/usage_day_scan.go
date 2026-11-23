package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

const usageDayPagesPerBatch = 8

type usageDayCheckpoint struct {
	Anchored  bool   `json:"anchored"`
	Closed    bool   `json:"closed"`
	Anchor    int64  `json:"anchor"`
	Total     int64  `json:"total"`
	Seen      int64  `json:"seen"`
	Last      int64  `json:"last"`
	Stored    int64  `json:"stored"`
	Digest    string `json:"digest"`
	LastPage  string `json:"last_page"`
	FirstPass string `json:"first_pass"`
}

type usageDayBatch struct {
	checkpoint usageDayCheckpoint
	facts      []UsageFact
	read       Check
	state      string
	reason     string
	retryAt    time.Time
	reset      bool
}

func (b *usageDayBatch) wait(check Check, now time.Time) {
	b.read, b.state, b.reason = check, "waiting", check.Reason
	b.retryAt = now.Add(time.Minute)
	if check.retryAfter != nil {
		b.retryAt = maxTime(now, *check.retryAfter)
	}
}

func (b *usageDayBatch) changed(closed bool, now time.Time) {
	b.checkpoint, b.facts, b.reset = usageDayCheckpoint{Closed: closed}, nil, true
	b.read.State, b.read.Reason = "failed", "PAGINATION_CHANGED"
	b.state, b.reason, b.retryAt = "waiting", "PAGINATION_CHANGED", now.Add(2*time.Second)
}

type usageDayPage struct {
	facts []UsageFact
	ids   []int64
	total int64
}

func (s *Service) usageDayPage(ctx context.Context, c storedConfig, kind, secret, account, day string, page, size int, order string) (usageDayPage, Check) {
	return s.readUsageDayPage(ctx, c, kind, secret, account, day, page, size, order, "")
}

func (s *Service) readUsageDayPage(ctx context.Context, c storedConfig, kind, secret, account, day string, page, size int, order, walletUser string) (usageDayPage, Check) {
	var result usageDayPage
	values := url.Values{
		"account_id": {account}, "start_date": {day}, "end_date": {day}, "timezone": {"UTC"},
		"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}, "exact_total": {"true"},
		"sort_by": {"id"}, "sort_order": {order},
	}
	if walletUser != "" {
		values.Set("user_id", walletUser)
		values.Set("billing_type", "0")
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/usage?"+values.Encode(), kind, secret, "usage.read", true)
	if check.State != "available" {
		return result, check
	}
	data, ok := parsePage(body, page, size)
	if !ok {
		return result, invalid(check)
	}
	start, _ := time.Parse(weeklyDate, day)
	end := start.AddDate(0, 0, 1)
	result.facts, result.ids = make([]UsageFact, 0, len(data.Items)), make([]int64, 0, len(data.Items))
	for _, item := range data.Items {
		fact, valid := projectUsage(item, account, secret)
		id, err := strconv.ParseInt(fact.ID, 10, 64)
		if !valid || err != nil || id <= 0 || fact.CreatedAt.Before(start) || !fact.CreatedAt.Before(end) {
			return usageDayPage{}, invalid(check)
		}
		if walletUser != "" && (fact.UserID == nil || *fact.UserID != walletUser || fact.BillingCandidate != "wallet") {
			return usageDayPage{}, invalid(check)
		}
		if len(result.ids) != 0 && id <= result.ids[len(result.ids)-1] {
			check.State, check.Reason = "failed", "PAGINATION_CHANGED"
			return usageDayPage{}, check
		}
		result.facts, result.ids = append(result.facts, fact), append(result.ids, id)
	}
	result.total = *data.Total
	return result, check
}

func usageDayDigest(previous string, facts []UsageFact) (string, error) {
	h := sha256.New()
	h.Write([]byte(previous))
	enc := json.NewEncoder(h)
	for _, fact := range facts {
		if err := enc.Encode(fact); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Service) scanUsageDayBatch(ctx context.Context, j usageDayJob, c storedConfig, a storedAccount,
	kind, secret string, interval time.Duration, now time.Time, rebind bool) (usageDayBatch, error) {
	closed := !now.Before(j.day.AddDate(0, 0, 1))
	b := usageDayBatch{checkpoint: j.checkpoint, state: "collecting", reason: "READING", retryAt: now, read: unknown("usage.read", "NOT_CONFIGURED")}
	p := &b.checkpoint
	if rebind || p.Anchored && p.Closed != closed {
		*p, b.reset = usageDayCheckpoint{Closed: closed}, true
	}
	p.Closed = closed
	day := j.day.Format(weeklyDate)
	fetch := func(page, size int, order string) (usageDayPage, bool) {
		rows, check := s.usageDayPage(ctx, c, kind, secret, a.UpstreamID, day, page, size, order)
		b.read = check
		if check.State == "available" {
			return rows, true
		}
		if check.Reason == "PAGINATION_CHANGED" {
			b.changed(closed, now)
		} else {
			b.wait(check, now)
		}
		return rows, false
	}
	if !p.Anchored || !closed && p.Seen == p.Total {
		head, ok := fetch(1, 1, "desc")
		if !ok {
			return b, nil
		}
		anchor := int64(0)
		if len(head.ids) == 1 {
			anchor = head.ids[0]
		}
		if p.Anchored && (head.total < p.Total || head.total == p.Total && anchor != p.Anchor || head.total > p.Total && anchor <= p.Anchor) {
			b.changed(closed, now)
			return b, nil
		}
		p.Anchored, p.Anchor, p.Total = true, anchor, head.total
	}
	if p.Seen > 0 {
		page := int((p.Seen-1)/usagePageSize) + 1
		count := int((p.Seen-1)%usagePageSize) + 1
		previous, ok := fetch(page, usagePageSize, "asc")
		if !ok {
			return b, nil
		}
		if len(previous.facts) < count || previous.ids[count-1] != p.Last || previous.total < p.Total || closed && previous.total != p.Total {
			b.changed(closed, now)
			return b, nil
		}
		digest, err := usageDayDigest("", previous.facts[:count])
		if err != nil {
			return b, err
		}
		if digest != p.LastPage {
			b.changed(closed, now)
			return b, nil
		}
	}
	for pages := 0; pages < usageDayPagesPerBatch && ctx.Err() == nil; pages++ {
		page, skip := int(p.Seen/usagePageSize)+1, int(p.Seen%usagePageSize)
		if !closed && p.Seen == p.Total {
			b.state, b.reason, b.retryAt = "partial", "DAY_OPEN", now.Add(interval)
			return b, nil
		}
		out, ok := fetch(page, usagePageSize, "asc")
		if !ok {
			return b, nil
		}
		take := int(min(p.Total-p.Seen, int64(usagePageSize-skip)))
		if out.total < p.Total || closed && out.total != p.Total || len(out.ids) < skip+take {
			b.changed(closed, now)
			return b, nil
		}
		if skip != 0 {
			prefix, err := usageDayDigest("", out.facts[:skip])
			if err != nil {
				return b, err
			}
			if out.ids[skip-1] != p.Last || prefix != p.LastPage {
				b.changed(closed, now)
				return b, nil
			}
		}
		if take != 0 {
			last := out.ids[skip+take-1]
			if out.ids[skip] <= p.Last || last > p.Anchor || (last == p.Anchor) != (p.Seen+int64(take) == p.Total) {
				b.changed(closed, now)
				return b, nil
			}
			p.Last = last
			pageDigest, err := usageDayDigest("", out.facts[:skip+take])
			if err != nil {
				return b, err
			}
			p.LastPage = pageDigest
		}
		facts := out.facts[skip : skip+take]
		digest, err := usageDayDigest(p.Digest, facts)
		if err != nil {
			return b, err
		}
		p.Seen, p.Digest = p.Seen+int64(take), digest
		if p.FirstPass == "" {
			b.facts = append(b.facts, facts...)
			p.Stored = p.Seen
		}
		if p.Seen != p.Total {
			continue
		}
		if !closed {
			b.state, b.reason, b.retryAt = "partial", "DAY_OPEN", now.Add(interval)
			return b, nil
		}
		if p.FirstPass == "" {
			p.FirstPass, p.Seen, p.Last, p.Digest, p.LastPage = p.Digest, 0, 0, "", ""
			b.reason = "VERIFYING"
			return b, nil
		}
		if p.Digest != p.FirstPass {
			b.changed(closed, now)
			return b, nil
		}
		b.state, b.reason = "complete", "VERIFIED_READ"
		return b, nil
	}
	if p.FirstPass != "" {
		b.reason = "VERIFYING"
	}
	return b, nil
}
