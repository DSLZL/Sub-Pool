package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"time"
)

const usageDayPagesPerBatch = 8

type usageDayCheckpoint struct {
	Version    int              `json:"version,omitempty"`
	Anchored   bool             `json:"anchored"`
	Anchor     int64            `json:"anchor"`
	Total      int64            `json:"total"`
	Seen       int64            `json:"seen"`
	Last       int64            `json:"last"`
	Stored     int64            `json:"stored"`
	FirstPage  string           `json:"first_page,omitempty"`
	LastPage   string           `json:"last_page"`
	PinnedAt   time.Time        `json:"pinned_at,omitempty"`
	Scope      []usageDayRange  `json:"scope,omitempty"`
	Coverage   []usageDayRange  `json:"coverage,omitempty"`
	Omitted    usageDayRange    `json:"omitted,omitempty"`
	Weekly     *usageDayDemand  `json:"weekly,omitempty"`
	WeeklyOnly bool             `json:"weekly_only,omitempty"`
	Repair     []usageDayRepair `json:"repair,omitempty"`
	NextRepair int              `json:"next_repair,omitempty"`
}

type usageDayRepair struct {
	Page   int    `json:"page"`
	Count  int    `json:"count"`
	Digest string `json:"digest"`
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

func (b *usageDayBatch) changed(now time.Time) {
	b.checkpoint, b.facts, b.reset = b.checkpoint.restart(), nil, true
	b.read.State, b.read.Reason = "failed", "PAGINATION_CHANGED"
	b.state, b.reason, b.retryAt = "waiting", "PAGINATION_CHANGED", now.Add(2*time.Second)
}

func (p usageDayCheckpoint) restart() usageDayCheckpoint {
	return usageDayCheckpoint{Version: 1, Weekly: p.Weekly, WeeklyOnly: p.WeeklyOnly}
}

type usageDayPage struct {
	facts []UsageFact
	ids   []int64
	total int64
}

func (s *Service) usageDayPage(ctx context.Context, c storedConfig, kind, secret, account, day string, page, size int, order string) (usageDayPage, Check) {
	var result usageDayPage
	values := url.Values{
		"account_id": {account}, "start_date": {day}, "end_date": {day}, "timezone": {"UTC"},
		"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}, "exact_total": {"true"},
		"sort_by": {"id"}, "sort_order": {order},
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
		fact, valid := projectUsageFact(item, account, secret, true)
		id, err := strconv.ParseInt(fact.ID, 10, 64)
		if !valid || err != nil || id <= 0 || fact.CreatedAt.Before(start) || !fact.CreatedAt.Before(end) {
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

func usageDayDigest(facts []UsageFact) (string, error) {
	h := sha256.New()
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
	dayRange := usageCalendarDay(j.day)
	closed := !now.Before(dayRange.End)
	ranges := j.ranges
	b := usageDayBatch{checkpoint: j.checkpoint, state: "collecting", reason: "READING", retryAt: now, read: unknown("usage.read", "NOT_CONFIGURED")}
	p := &b.checkpoint
	revisit := slices.ContainsFunc(ranges, func(r usageDayRange) bool {
		return !usageDayRangesCover(p.Scope, r.intersect(p.Omitted))
	})
	if rebind || p.Anchored && (p.Version != 1 || revisit) {
		*p, b.reset = p.restart(), true
	}
	p.Version = 1
	p.Scope = ranges
	day := j.day.Format(weeklyDate)
	fetch := func(page, size int, order string) (usageDayPage, bool) {
		rows, check := s.usageDayPage(ctx, c, kind, secret, a.UpstreamID, day, page, size, order)
		b.read = check
		if check.State == "available" {
			return rows, true
		}
		if check.Reason == "PAGINATION_CHANGED" {
			b.changed(now)
		} else {
			b.wait(check, now)
		}
		return rows, false
	}
	if !p.Anchored || p.Seen == p.Total {
		head, ok := fetch(1, 1, "desc")
		if !ok {
			return b, nil
		}
		if int64(len(head.ids)) != min(1, head.total) {
			b.changed(now)
			return b, nil
		}
		anchor := int64(0)
		if len(head.ids) == 1 {
			anchor = head.ids[0]
		}
		if p.Anchored && (head.total < p.Total || head.total == p.Total && anchor != p.Anchor || head.total > p.Total && anchor <= p.Anchor) {
			b.changed(now)
			return b, nil
		}
		p.Anchored, p.Anchor, p.Total, p.PinnedAt = true, anchor, head.total, now
	}
	if len(p.Repair) > 0 && j.checkpoint.Seen == j.checkpoint.Total {
		target := p.Repair[p.NextRepair%len(p.Repair)]
		checked, ok := fetch(target.Page, usagePageSize, "asc")
		if !ok {
			return b, nil
		}
		if len(checked.facts) < target.Count || checked.total < p.Total {
			b.changed(now)
			return b, nil
		}
		digest, err := usageDayDigest(checked.facts[:target.Count])
		if err != nil {
			return b, err
		}
		if target.Digest != digest {
			b.changed(now)
			return b, nil
		}
		p.NextRepair = (p.NextRepair + 1) % len(p.Repair)
	}
	if p.Seen > 0 {
		page := int((p.Seen-1)/usagePageSize) + 1
		count := int((p.Seen-1)%usagePageSize) + 1
		previous, ok := fetch(page, usagePageSize, "asc")
		if !ok {
			return b, nil
		}
		if len(previous.facts) < count || previous.ids[count-1] != p.Last || previous.total < p.Total {
			b.changed(now)
			return b, nil
		}
		digest, err := usageDayDigest(previous.facts[:count])
		if err != nil {
			return b, err
		}
		if digest != p.LastPage {
			b.changed(now)
			return b, nil
		}
	}
	for pages := 0; pages < usageDayPagesPerBatch && ctx.Err() == nil; pages++ {
		page, skip := int(p.Seen/usagePageSize)+1, int(p.Seen%usagePageSize)
		if p.Seen == p.Total && p.FirstPage != "" {
			break
		}
		out, ok := fetch(page, usagePageSize, "asc")
		if !ok {
			return b, nil
		}
		take := int(min(p.Total-p.Seen, int64(usagePageSize-skip)))
		if out.total < p.Total || len(out.ids) < skip+take {
			b.changed(now)
			return b, nil
		}
		if skip != 0 {
			prefix, err := usageDayDigest(out.facts[:skip])
			if err != nil {
				return b, err
			}
			if out.ids[skip-1] != p.Last || prefix != p.LastPage {
				b.changed(now)
				return b, nil
			}
		}
		if take != 0 {
			last := out.ids[skip+take-1]
			if out.ids[skip] <= p.Last || last > p.Anchor || (last == p.Anchor) != (p.Seen+int64(take) == p.Total) {
				b.changed(now)
				return b, nil
			}
			p.Last = last
			pageDigest, err := usageDayDigest(out.facts[:skip+take])
			if err != nil {
				return b, err
			}
			p.LastPage = pageDigest
		}
		if page == 1 {
			var err error
			p.FirstPage, err = usageDayDigest(out.facts[:skip+take])
			if err != nil {
				return b, err
			}
		}
		needsRepair := false
		for _, fact := range out.facts[skip : skip+take] {
			if usageDayRangesContain(p.Coverage, fact.CreatedAt) {
				b.changed(now)
				return b, nil
			}
			if usageDayRangesContain(ranges, fact.CreatedAt) {
				b.facts = append(b.facts, fact)
				p.Stored++
				if fact.Metrics["total_cost"] == nil || fact.Metrics["actual_cost"] == nil ||
					fact.BillingCandidate == "unknown" || fact.UserID == nil && fact.BillingCandidate == "wallet" {
					needsRepair = true
				}
			} else {
				if p.Omitted.Start.IsZero() || fact.CreatedAt.Before(p.Omitted.Start) {
					p.Omitted.Start = fact.CreatedAt
				}
				p.Omitted.End = maxTime(p.Omitted.End, fact.CreatedAt.Add(time.Nanosecond))
			}
		}
		if needsRepair {
			target := usageDayRepair{Page: page, Count: skip + take, Digest: p.LastPage}
			if last := len(p.Repair) - 1; last >= 0 && p.Repair[last].Page == page {
				p.Repair[last] = target
			} else {
				p.Repair = append(p.Repair, target)
			}
		}
		p.Seen += int64(take)
	}
	if p.Seen != p.Total || p.FirstPage == "" || ctx.Err() != nil {
		return b, nil
	}
	first, ok := fetch(1, usagePageSize, "asc")
	if !ok {
		return b, nil
	}
	count := int(min(p.Total, usagePageSize))
	if len(first.facts) < count || first.total < p.Total {
		b.changed(now)
		return b, nil
	}
	digest, err := usageDayDigest(first.facts[:count])
	if err != nil {
		return b, err
	}
	if digest != p.FirstPage {
		b.changed(now)
		return b, nil
	}
	if p.Total > usagePageSize {
		page := int((p.Total-1)/usagePageSize) + 1
		count := int((p.Total-1)%usagePageSize) + 1
		last, ok := fetch(page, usagePageSize, "asc")
		if !ok {
			return b, nil
		}
		if len(last.facts) < count || last.total < p.Total {
			b.changed(now)
			return b, nil
		}
		digest, err = usageDayDigest(last.facts[:count])
		if err != nil {
			return b, err
		}
		if digest != p.LastPage {
			b.changed(now)
			return b, nil
		}
		first.total = max(first.total, last.total)
	}
	if first.total > p.Total {
		return b, nil
	}
	for _, r := range ranges {
		p.Coverage = append(p.Coverage, r.intersect(usageDayRange{Start: j.day, End: p.PinnedAt}))
	}
	p.Coverage = mergeUsageDayRanges(p.Coverage...)
	b.state, b.reason, b.retryAt = "partial", "DAY_OPEN", now.Add(interval)
	if closed {
		switch {
		case len(p.Repair) > 0:
			b.reason = "RECORD_FIELDS_UNAVAILABLE"
		case usageDayRangesCover(p.Coverage, dayRange):
			b.state, b.reason = "complete", "VERIFIED_READ"
		default:
			b.reason = "SCOPED_READ"
		}
	}
	return b, nil
}
