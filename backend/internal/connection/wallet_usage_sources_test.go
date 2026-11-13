package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestWalletUserSourcesObserveUnmanaged(t *testing.T) {
	f := newWalletFixture(t, true)
	a := adoptManaged(t, f.s, f.token, f.c, 1, "Known source")
	f.usageRows.Store(map[string][]map[string]any{"": {
		{"id": 1, "user_id": 21, "account_id": 1},
		{"id": 2, "user_id": 21, "account_id": 2},
	}})
	r, err := f.s.RefreshWallet(context.Background(), f.token, walletExpected(f.p, f.c, "0"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(r)
	var receipt map[string]json.RawMessage
	_ = json.Unmarshal(body, &receipt)
	if len(receipt["usage_sources"]) == 0 {
		t.Fatal("wallet refresh omitted native-user source presence outside adopted account", a.ID)
	}
	sources := r.UsageSources
	if sources == nil || sources.State != "exhausted" || sources.ScannedCount != "2" || sources.Complete || len(sources.Accounts) != 2 || sources.Accounts[0].ManagedAccountID == nil || *sources.Accounts[0].ManagedAccountID != a.ID || sources.Accounts[1].ManagedAccountID != nil {
		t.Fatal("source presence lost the unmanaged account or implied coverage", sources)
	}
	ctx := context.Background()
	admin, err := f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || len(admin.Contributions) != 1 || admin.Contributions[0].AccountID != a.ID || admin.LatestRead.UsageSources == nil || admin.CurrentTarget != nil || admin.Applicable {
		t.Fatal("managed source observation was omitted or financially qualified", admin, err)
	}
	member, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: "source-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"balances"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{f.p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := f.s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	view, err := f.s.GetWallet(ctx, reader, f.p.ID)
	if err != nil || view.LatestRead.UsageSources != nil || view.LastConfirmedRead.UsageSources != nil || len(view.Contributions) != 1 {
		t.Fatal("native-user inventory reached a balances reader", view, err)
	}
	public, _ := json.Marshal(view)
	if bytes.Contains(public, []byte("usage_sources")) || bytes.Contains(public, []byte("upstream_account_id")) || f.calls.Load() != before {
		t.Fatal("ordinary read exposed global source metadata or contacted native")
	}
	if _, err = f.s.ListWalletHistory(ctx, reader, f.p.ID, "wallet", "0", 100); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("source history reached ordinary reader", err)
	}
	b := adoptManaged(t, f.s, f.token, f.c, 2, "Later adopted source")
	admin, err = f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || len(admin.Contributions) != 2 || !*admin.NeedsRecalculation {
		t.Fatal("later adoption did not invalidate the shared whole-wallet basis", admin, err)
	}
	view, err = f.s.GetWallet(ctx, reader, f.p.ID)
	public, _ = json.Marshal(view)
	if err != nil || len(view.Contributions) != 1 || bytes.Contains(public, []byte(b.ID)) || bytes.Contains(public, []byte("usage_sources")) {
		t.Fatal("hidden contribution or source metadata leaked after adoption", err)
	}
	restarted, err := New(f.db, auth.New(f.db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := restarted.ListWalletHistory(ctx, f.token, f.p.ID, "wallet", "0", 100)
	retained, _ := json.Marshal(history.WalletReads[0])
	if err != nil || len(history.WalletReads) != 1 || !bytes.Equal(body, retained) {
		t.Fatal("later adoption or service replacement rewrote source membership", err)
	}
	var captures int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM usage_captures`).Scan(&captures); err != nil || captures != 0 {
		t.Fatal("source observation invented usage ownership or capture", captures, err)
	}
	edit := participantEdit(f.p, f.c)
	edit.UpstreamID = "22"
	changed, err := f.s.SaveParticipant(ctx, f.token, edit)
	if err != nil || changed.Participant == nil {
		t.Fatal("source observation invented an ownership obligation", err)
	}
	view, err = f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || len(view.Contributions) != 0 || view.LatestRead != nil {
		t.Fatal("old wallet source observation reached another native user", view, err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"))
		respond(w, nativeUserRow(id))
	}))
	t.Cleanup(other.Close)
	c := save(t, f.s, f.token, other.URL)
	edit = participantEdit(*changed.Participant, c)
	edit.SourceID = *c.SourceID
	edit.UpstreamID = "21"
	changed, err = f.s.SaveParticipant(ctx, f.token, edit)
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.s.GetWallet(ctx, f.token, f.p.ID)
	if err != nil || len(view.Contributions) != 0 || view.LatestRead != nil {
		t.Fatal("same numeric user in another native source inherited observations", view, err)
	}
}

func TestWalletUsagePageRejectsAmbiguousIdentityAndOrdering(t *testing.T) {
	valid := `{"items":[{"id":9007199254740993,"user_id":21,"account_id":9223372036854775807,"user":{"email":"private"}}],"total":1,"page":1,"page_size":100}`
	data, accounts, last, ok := walletUsagePage(json.RawMessage(valid), 1, "21", 0)
	if !ok || *data.Total != 1 || len(accounts) != 1 || accounts[0] != "9223372036854775807" || last != 9007199254740993 {
		t.Fatal("precise minimal native identities lost", accounts, last, ok)
	}
	for name, body := range map[string]string{
		"duplicate control":  strings.Replace(valid, `"total":1`, `"total":1,"total":1`, 1),
		"aliased control":    strings.Replace(valid, `"total":1`, `"total":1,"Total":0`, 1),
		"missing canonical":  strings.Replace(valid, `"total":1`, `"Total":1`, 1),
		"null page":          strings.Replace(valid, `"page":1`, `"page":null`, 1),
		"wrong page":         strings.Replace(valid, `"page":1`, `"page":2`, 1),
		"wrong cardinality":  strings.Replace(valid, `"total":1`, `"total":2`, 1),
		"duplicate identity": strings.Replace(valid, `"user_id":21`, `"user_id":22,"user_id":21`, 1),
		"wrong user":         strings.Replace(valid, `"user_id":21`, `"user_id":22`, 1),
		"null user":          strings.Replace(valid, `"user_id":21`, `"user_id":null`, 1),
		"null account":       strings.Replace(valid, `"account_id":9223372036854775807`, `"account_id":null`, 1),
		"quoted account":     strings.Replace(valid, `"account_id":9223372036854775807`, `"account_id":"1"`, 1),
		"overflow ID":        strings.Replace(valid, `9007199254740993`, `9223372036854775808`, 1),
		"zero ID":            strings.Replace(valid, `9007199254740993`, `0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, ok := walletUsagePage(json.RawMessage(body), 1, "21", 0); ok {
				t.Fatal("invalid page accepted")
			}
		})
	}
	if _, _, _, ok = walletUsagePage(json.RawMessage(valid), 1, "21", last); ok {
		t.Fatal("duplicate ID at a page boundary accepted")
	}
}

func TestWalletUserSourceScanRetainsOnlyValidatedPages(t *testing.T) {
	for _, mode := range []string{"empty", "denied", "wrong user", "valid", "changed total", "late denied", "late invalid", "duplicate ID", "limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newWalletFixture(t, true)
			f.usageHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				page, _ := strconv.Atoi(q.Get("page"))
				if r.Method != "GET" || page < 1 || page > 10 || q.Get("user_id") != "21" || q.Get("account_id") != "" || q.Get("exact_total") != "true" || q.Get("page_size") != "100" || q.Get("sort_by") != "id" || q.Get("sort_order") != "asc" || len(q) != 6 {
					t.Error("user inventory changed its fixed filtered read", q)
				}
				if mode == "denied" || mode == "late denied" && page == 2 {
					http.Error(w, "private-native-denial", 403)
					return
				}
				total := 101
				if mode == "empty" {
					total = 0
				}
				if mode == "limit" {
					total = 1001
				}
				if mode == "changed total" && page == 2 {
					total = 102
				}
				items := []map[string]any{}
				for n := (page-1)*100 + 1; n <= min(page*100, total); n++ {
					user, id, account := 21, n, 10
					if page == 2 {
						account = 20
					}
					if mode == "wrong user" || mode == "late invalid" && n == 101 {
						user = 22
					}
					if mode == "duplicate ID" && n == 101 {
						id = 100
					}
					items = append(items, map[string]any{"id": id, "user_id": user, "account_id": account, "actual_cost": 123, "user": map[string]any{"email": "private-native-email"}})
				}
				respond(w, map[string]any{"items": items, "total": total, "page": page, "page_size": 100})
			}))
			r, err := f.s.RefreshWallet(context.Background(), f.token, walletExpected(f.p, f.c, "0"))
			if err != nil || r.Balance.Value == nil || r.UsageSources == nil || r.UsageSources.Complete {
				t.Fatal("source scan changed independent balance evidence", r, err)
			}
			scan := r.UsageSources
			state, count, pages, sources := "partial", "100", 1, 1
			switch mode {
			case "empty":
				state, count, pages, sources = "exhausted", "0", 1, 0
			case "denied", "wrong user":
				state, count, pages, sources = "failed", "0", 0, 0
			case "valid":
				state, count, pages, sources = "exhausted", "101", 2, 2
			case "limit":
				state, count, pages, sources = "partial", "1000", 10, 2
			}
			if scan.State != state || scan.ScannedCount != count || scan.PagesRead != pages || len(scan.Accounts) != sources || scan.CapturedAt.Before(scan.StartedAt) {
				t.Fatal("invalid page prefix, scan bound or time", scan)
			}
			if mode == "limit" && scan.Limitations[len(scan.Limitations)-1] != "SCAN_LIMIT" || mode == "changed total" && scan.Read.Reason != "PAGINATION_CHANGED" {
				t.Fatal("bounded or changing pagination reason lost", scan)
			}
			body, _ := json.Marshal(scan)
			for _, hidden := range []string{"private-native", "actual_cost", "user_id", "request_id"} {
				if bytes.Contains(body, []byte(hidden)) {
					t.Fatal("unneeded native row data persisted", hidden)
				}
			}
		})
	}
}

func TestWalletUserSourceNetworkExpiryRollsBack(t *testing.T) {
	f := newWalletFixture(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	f.usageHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		respond(w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 100})
	}))
	digest := sha256.Sum256([]byte(f.token))
	deadline := time.Now().Add(2 * time.Second)
	exec(t, f.db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() {
		_, err := f.s.RefreshWallet(context.Background(), f.token, walletExpected(f.p, f.c, "0"))
		done <- err
	}()
	<-entered
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	close(release)
	if err := <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired source scan returned protected receipt", err)
	}
	var reads, recommendations int
	if err := f.db.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM wallet_reads),(SELECT count(*) FROM wallet_recommendations)`).Scan(&reads, &recommendations); err != nil || reads != 0 || recommendations != 0 {
		t.Fatal("expired observation survived rollback", reads, recommendations, err)
	}
}
