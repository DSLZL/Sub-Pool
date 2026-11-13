package connection

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type WalletUsageSource struct {
	UpstreamAccountID string  `json:"upstream_account_id"`
	ManagedAccountID  *string `json:"managed_account_id"`
}

type WalletUsageSources struct {
	StartedAt     time.Time           `json:"started_at"`
	CapturedAt    time.Time           `json:"captured_at"`
	State         string              `json:"state"`
	Read          Check               `json:"read"`
	PagesRead     int                 `json:"pages_read"`
	UpstreamTotal *string             `json:"upstream_total"`
	ScannedCount  string              `json:"scanned_count"`
	Accounts      []WalletUsageSource `json:"accounts"`
	Complete      bool                `json:"complete"`
	Limitations   []string            `json:"limitations"`
}

func walletUsagePage(body json.RawMessage, page int, user string, lastID int64) (pageData, []string, int64, bool) {
	fields, valid := usageObject(body)
	for _, name := range []string{"items", "total", "page", "page_size"} {
		if fields[name] == nil {
			valid = false
		}
		for key := range fields {
			if key != name && strings.EqualFold(key, name) {
				valid = false
			}
		}
	}
	data, ok := parsePage(body, page, usagePageSize)
	if !valid || !ok {
		return pageData{}, nil, lastID, false
	}
	accounts := make([]string, 0, len(data.Items))
	for _, item := range data.Items {
		row, ok := usageObject(item)
		id, idOK := usageID(row["id"], true)
		userID, userOK := usageID(row["user_id"], true)
		account, accountOK := usageID(row["account_id"], true)
		if !ok || !idOK || !userOK || !accountOK || *userID != user {
			return pageData{}, nil, lastID, false
		}
		next, _ := strconv.ParseInt(*id, 10, 64)
		if next <= lastID {
			return pageData{}, nil, lastID, false
		}
		lastID = next
		accounts = append(accounts, *account)
	}
	return data, accounts, lastID, true
}

func (s *Service) walletUsageSources(ctx, network context.Context, tx pgx.Tx, token string, c storedConfig, p Participant, kind, secret string) (*WalletUsageSources, error) {
	out := &WalletUsageSources{StartedAt: time.Now().UTC(), State: "failed", ScannedCount: "0", Accounts: []WalletUsageSource{}, Limitations: []string{
		"RETENTION_UNVERIFIED", "OFFSET_SNAPSHOT_UNVERIFIED", "WALLET_DEDUCTION_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED",
	}}
	query := url.Values{"user_id": {p.Binding.UpstreamUserID}, "page_size": {strconv.Itoa(usagePageSize)}, "exact_total": {"true"}, "sort_by": {"id"}, "sort_order": {"asc"}}
	seen, scanned, lastID := map[string]bool{}, 0, int64(0)
	for page := 1; page <= usageMaxPages; page++ {
		if _, err := s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
			return nil, err
		}
		query.Set("page", strconv.Itoa(page))
		body, check := s.upstream.get(network, c.BaseURL, "/api/v1/admin/usage?"+query.Encode(), kind, secret, "usage.read", true)
		out.Read = check
		if check.State != "available" {
			break
		}
		data, accounts, nextID, valid := walletUsagePage(body, page, p.Binding.UpstreamUserID, lastID)
		if !valid {
			out.Read = invalid(check)
			break
		}
		total := strconv.FormatInt(*data.Total, 10)
		if out.UpstreamTotal != nil && *out.UpstreamTotal != total {
			out.Read.State, out.Read.Reason = "failed", "PAGINATION_CHANGED"
			break
		}
		for _, account := range accounts {
			seen[account] = true
		}
		out.UpstreamTotal = &total
		out.PagesRead++
		scanned += len(accounts)
		lastID = nextID
		if int64(scanned) == *data.Total {
			out.State = "exhausted"
			break
		}
		out.State = "partial"
		if page == usageMaxPages {
			out.Limitations = append(out.Limitations, "SCAN_LIMIT")
		}
	}
	out.CapturedAt, out.ScannedCount = time.Now().UTC(), strconv.Itoa(scanned)
	if out.Read.State != "available" {
		out.Limitations = append(out.Limitations, out.Read.Reason)
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		n, _ := strconv.ParseInt(id, 10, 64)
		ids = append(ids, n)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	managed := map[string]string{}
	if len(ids) != 0 {
		// Record membership at this observation; never acquire account locks after the participant lock.
		rows, err := tx.Query(ctx, `SELECT id,upstream_id::text FROM managed_accounts WHERE source_id=$1 AND upstream_id=ANY($2::bigint[])`, p.Binding.SourceID, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, upstream string
			if err = rows.Scan(&id, &upstream); err != nil {
				rows.Close()
				return nil, err
			}
			managed[upstream] = id
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		source := WalletUsageSource{UpstreamAccountID: strconv.FormatInt(id, 10)}
		if managedID, ok := managed[source.UpstreamAccountID]; ok {
			source.ManagedAccountID = &managedID
		}
		out.Accounts = append(out.Accounts, source)
	}
	return out, nil
}
