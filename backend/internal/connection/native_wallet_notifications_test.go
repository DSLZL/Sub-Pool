package connection

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
	"sub-pool/backend/internal/notifications"

	"github.com/jackc/pgx/v5"
)

func TestNativeWalletNotificationReadsConfirmedCrossPoolIssuanceAndSendsOnce(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Minute).Add(-48 * time.Hour)
	starts := map[int]time.Time{8: start, 9: start.Add(-24 * time.Hour)}
	var balance atomic.Int64
	var denied atomic.Bool
	var point atomic.Int64
	var readRequests atomic.Int64
	balance.Store(21)
	point.Store(1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readRequests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/system/version":
			respond(w, map[string]string{"version": "0.2.23"})
		case "/api/v1/admin/groups/0/model-allowlist-candidates":
			respond(w, map[string][]string{"models": {"gpt-5"}})
		case "/api/v1/admin/accounts/8", "/api/v1/admin/accounts/9":
			id, _ := strconv.Atoi(r.URL.Path[len("/api/v1/admin/accounts/"):])
			row := fullManagedAccount(id, fmt.Sprintf("notification-%d", id))
			hours := int64(1)
			if id == 8 {
				hours = point.Load()
			}
			row["extra"] = map[string]any{"codex_usage_updated_at": start.Add(time.Duration(hours) * time.Hour), "codex_5h_window_minutes": 0,
				"codex_7d_window_minutes": 10080, "codex_7d_used_percent": hours * 10, "codex_7d_reset_at": starts[id].Add(nativeWeek)}
			respond(w, row)
		case "/api/v1/admin/usage":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
			respond(w, map[string]any{"items": []any{}, "total": 0, "page": page, "page_size": size})
		case "/api/v1/admin/users/21":
			row := nativeUserRow(21)
			row["balance"], row["frozen_balance"] = balance.Load(), 0
			respond(w, row)
		case "/api/v1/admin/users/21/balance-history":
			if denied.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			respond(w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 1, "pages": 1, "total_recharged": 0})
		default:
			http.NotFound(w, r)
		}
	})
	upstream := httptest.NewServer(handler)
	defer upstream.Close()
	c := save(t, s, token, upstream.URL)
	verify := func(c Configuration) {
		t.Helper()
		d, err := s.Test(ctx, token, TestInput{Revision: c.Revision, BaseURL: c.BaseURL})
		if err != nil || d.Version == nil || *d.Version != "0.2.23" {
			t.Fatal("source verification", d.Version, err)
		}
	}
	verify(c)
	a, b := passiveManaged(t, s, token, c, 8), passiveManaged(t, s, token, c, 9)
	member := createParticipant(t, s, token, c, 21, "Native wallet participant")
	poolA := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{member}, []string{"100"})})
	poolB := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{b}, []Participant{member}, []string{"100"})})
	cycleA, cycleB := walletTestCycle(poolA.PoolID, a.ID, starts[8], "70"), walletTestCycle(poolB.PoolID, b.ID, starts[9], "50")
	cycleA.Phase, cycleA.ContractID, cycleA.OpeningDebt, cycleA.Remaining = "advance", poolA.ID, "20", "50"
	cycleB.Phase, cycleB.ContractID = "settlement", poolB.ID
	cutoff := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	exec(t, db, `INSERT INTO weekly_wallet_groups(participant_id,binding_id,pool_ids,phase,period_end,recorded_at,basis,snapshot)
	 VALUES($1,$2,$3,'advance',$4,$5,$6,$7)`, member.ID, member.Binding.ID, []string{poolA.PoolID, poolB.PoolID}, cycleB.End,
		cutoff, strings.Repeat("0", 64), weeklyWalletGroupPlan{Cutoff: cutoff, Target: "100", Contributions: []weeklyWalletContribution{cycleA, cycleB}})

	policy, err := s.GetMonitoringConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled, policy.StaleAfterSeconds = true, 3*24*60*60
	if _, err := s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	collectObservation(t, s, db, token, c, a)
	collectObservation(t, s, db, token, c, b)

	port, messages := nativeWalletNotificationMailSink(t)
	mail, err := notifications.New(db, s.auth, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	mail.Financial = s
	config, err := mail.GetConfiguration(ctx, token)
	if err != nil || config.NativeSourceID == nil || *config.NativeSourceID != *c.SourceID || config.NativeFinancialEnabled {
		t.Fatal("unexpected native readiness", config, err)
	}
	input := notifications.SaveInput{Revision: config.Revision, Enabled: true, FinancialEnabled: true,
		Channel: "smtp", From: "sender@owned.test", Recipient: "admin@owned.test", SMTP: notifications.SMTP{Host: "127.0.0.1", Port: port, TLS: "loopback_plain"},
		DedupeSeconds: 60, Preferences: notifications.Preferences{BalanceLow: true}, CredentialAction: "keep"}
	if _, err := mail.SaveConfiguration(ctx, token, input); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("historical preference could activate native finance", err)
	}
	input.ActivateNative = c.SourceID
	config, err = mail.SaveConfiguration(ctx, token, input)
	if err != nil || !config.NativeFinancialEnabled || !config.Capabilities[0].Effective {
		t.Fatal("explicit native activation", config, err)
	}
	if id, err := s.processNativeWalletNotifications(ctx, ""); err != nil || id != member.ID {
		t.Fatal("scheduled first sample", id, err)
	}
	if id, err := s.processNativeWalletNotifications(ctx, ""); err != nil || id != "" {
		t.Fatal("wallet sampled before monitoring interval", id, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.config(ctx, tx, false)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sample := func(amount int64) {
		t.Helper()
		balance.Store(amount)
		if err := s.sampleNativeWalletNotification(ctx, stored, member, config.Revision, config.Preferences); err != nil {
			t.Fatal("sample", amount, err)
		}
	}
	events := func(wanted int) []notifications.Notification {
		t.Helper()
		page, err := mail.List(ctx, token, notifications.Query{Kind: "balance_low", After: "0", Limit: 100})
		if err != nil || len(page.Notifications) != wanted {
			var samples []string
			_ = db.QueryRow(ctx, "SELECT array_agg(snapshot::text ORDER BY id) FROM native_wallet_financial_samples").Scan(&samples)
			t.Fatalf("wanted %d low balance events: %+v %v. Samples: %v", wanted, page.Notifications, err, samples)
		}
		return page.Notifications
	}

	sample(20)
	events(0)
	denied.Store(true)
	sample(19)
	denied.Store(false)
	sample(19)
	events(0)
	sample(20)
	sample(19)
	queued := events(1)[0]
	if queued.State != "queued" || queued.Details == nil || !strings.Contains(queued.Details.Body, "from 20 to 19") ||
		!strings.Contains(queued.Details.Body, "confirmed issuance 100") || queued.Details.EventReason != "NATIVE_BALANCE_BELOW_ISSUANCE" {
		t.Fatalf("incorrect threshold evidence: %+v", queued)
	}

	run := func() func() {
		worker, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); mail.Run(worker) }()
		return func() { cancel(); <-done }
	}
	stop := run()
	defer func() { stop() }()
	select {
	case msg := <-messages:
		if !strings.Contains(msg, "Subject:") || !strings.Contains(msg, "balance fell from 20 to 19") {
			t.Fatal("unexpected mail content", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("SMTP received nothing: %+v", events(1)[0])
	}
	deadline := time.Now().Add(5 * time.Second)
	for events(1)[0].State != "accepted" {
		if time.Now().After(deadline) {
			t.Fatal("transport acceptance was not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	stop = func() {}
	restarted, err := New(db, s.auth, []byte(strings.Repeat("\x07", 32)), true)
	if err != nil {
		t.Fatal(err)
	}
	s = restarted
	mail.Financial = s
	sample(21)
	sample(19)
	events(1)
	stop = run()
	select {
	case <-messages:
		t.Fatal("same-cycle event was sent again")
	case <-time.After(100 * time.Millisecond):
	}
	stop()
	stop = func() {}

	input.Revision = config.Revision
	input.ActivateNative = nil
	input.Preferences.RecommendationChanged, input.Preferences.EstimateChanged = true, true
	config, err = mail.SaveConfiguration(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []int64{2, 3} {
		point.Store(checkpoint)
		collectObservation(t, s, db, token, c, a)
	}
	publish := func(capacity, walletCapacity string) {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		insertNativeFinancialEstimate(t, ctx, tx, s, a.ID, capacity, walletCapacity)
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	publish("100", "90")
	sample(100)
	point.Store(4)
	collectObservation(t, s, db, token, c, a)
	publish("120", "100")
	sample(100)
	sample(100)
	for _, kind := range []string{"recommendation_changed", "estimate_changed"} {
		page, err := mail.List(ctx, token, notifications.Query{Kind: kind, After: "0", Limit: 100})
		if err != nil || len(page.Notifications) != 1 || page.Notifications[0].State != "queued" {
			var samples []string
			_ = db.QueryRow(ctx, "SELECT array_agg(snapshot::text ORDER BY id) FROM native_wallet_financial_samples").Scan(&samples)
			t.Fatalf("wanted one queued %s event: %+v %v. Samples: %v", kind, page.Notifications, err, samples)
		}
	}
	reads := readRequests.Load()
	stop = run()
	wanted := map[string]bool{"target changed from 120 to 130": true, "capacity changed from 100 to 120": true}
	for range 2 {
		select {
		case msg := <-messages:
			for description := range wanted {
				if strings.Contains(msg, description) {
					delete(wanted, description)
					break
				}
			}
		case <-time.After(5 * time.Second):
			page, err := mail.List(ctx, token, notifications.Query{After: "0", Limit: 100})
			t.Fatalf("SMTP received no mail for %v. Records: %+v, error: %v", wanted, page.Notifications, err)
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("unexpected mail content, missing %v", wanted)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		var accepted int
		err := db.QueryRow(ctx, "SELECT count(*) FROM notifications WHERE kind IN ('recommendation_changed','estimate_changed') AND state='accepted'").Scan(&accepted)
		if err != nil || time.Now().After(deadline) {
			t.Fatal("expected both accepted records", accepted, err)
		}
		if accepted == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	stop = func() {}
	if readRequests.Load() != reads {
		t.Fatal("dispatch revalidation called upstream")
	}

	reader, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "owned.finance.reader", Password: "ReaderFixture123", Enabled: true,
		Grants: auth.Grants{Features: []string{"notifications"}, AccountIDs: []string{a.ID, b.ID}, ParticipantIDs: []string{member.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	_, readerToken, err := s.auth.Login(ctx, reader.Username, "ReaderFixture123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	query := notifications.Query{After: "0", Limit: 100}
	scoped, err := mail.List(ctx, readerToken, query)
	if err != nil || len(scoped.Notifications) != 3 {
		t.Fatal("whole-wallet scope", scoped, err)
	}
	for _, item := range scoped.Notifications {
		if item.Details != nil {
			t.Fatal("ordinary history exposed mail details")
		}
	}
	exec(t, db, "UPDATE auth_users SET account_ids=$2,revision=revision+1 WHERE id=$1", reader.ID, []string{a.ID})
	scoped, err = mail.List(ctx, readerToken, query)
	if err != nil || len(scoped.Notifications) != 1 || scoped.Notifications[0].Kind != "estimate_changed" {
		t.Fatal("revoked account did not hide the affected wallet events", scoped, err)
	}

	replacement := httptest.NewServer(handler)
	defer replacement.Close()
	replaced := save(t, s, token, replacement.URL)
	verify(replaced)
	config, err = mail.GetConfiguration(ctx, token)
	if err != nil || config.NativeSourceID == nil || *config.NativeSourceID != *replaced.SourceID || config.NativeFinancialEnabled {
		t.Fatal("activation leaked across native sources", config, err)
	}
}

func TestNativeBalanceBaselineUsesIssuanceAndEffectiveCycles(t *testing.T) {
	now := time.Now().UTC()
	threshold, below := "20", "19"
	previous := nativeWalletFinancialSample{Source: "source", Connection: "2", Participant: "1", Policy: "3",
		SampledAt: now.Add(-time.Minute), CheckedAt: now.Add(-time.Minute),
		Snapshot: nativeWalletFinancialSnapshot{Balance: &threshold, Issuance: "100", ConfirmedID: "group:1", MonitoringRevision: "2",
			Cycles: []nativeWalletFinancialCycle{{weeklyWalletCycle: weeklyWalletCycle{
				weeklyWalletAccountKey{"p", "a"}, now.Add(-time.Hour), now.Add(nativeWeek - time.Hour)}, ContractID: "1", AccountRevision: "1"}}}}
	current := previous
	current.SampledAt, current.CheckedAt, current.Snapshot.Balance = now, now, &below
	current.Snapshot.Cycles = append([]nativeWalletFinancialCycle{}, previous.Snapshot.Cycles...)
	current.Snapshot.Cycles[0].CostVersion = 1
	if !nativeWalletFinancialChanged("balance_low", &previous, &current, time.Hour) {
		t.Fatal("an estimate cost version suppressed a qualified balance crossing")
	}
	if nativeWalletFinancialChanged("balance_low", nil, &current, time.Hour) || nativeWalletFinancialChanged("balance_low", &previous, &current, time.Second) {
		t.Fatal("a first or expired sample provided the crossing baseline")
	}
	current.Snapshot.ConfirmedID = "group:2"
	if previous.Snapshot.cycleKey() != current.Snapshot.cycleKey() || nativeWalletFinancialChanged("balance_low", &previous, &current, time.Hour) {
		t.Fatal("formal issuance must rebuild the baseline within the same cycle identity")
	}
	current.Snapshot.ConfirmedID = previous.Snapshot.ConfirmedID
	current.Snapshot.Cycles[0].End = now.Add(nativeWeek)
	if nativeWalletFinancialChanged("balance_low", &previous, &current, time.Hour) {
		t.Fatal("compared a changed cycle")
	}
	current.Snapshot.Cycles[0].End = previous.Snapshot.Cycles[0].End
	previous.Snapshot.Issuance, current.Snapshot.Issuance = "0", "0"
	if nativeWalletFinancialChanged("balance_low", &previous, &current, time.Hour) {
		t.Fatal("zero issuance provided a threshold")
	}
}

func insertNativeFinancialEstimate(t *testing.T, ctx context.Context, tx pgx.Tx, s *Service, account, capacity, walletCapacity string) {
	t.Helper()
	policy, c, a, err := s.estimateEnvironment(ctx, tx, account)
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, time.Now().UTC())
	if err != nil || len(plan.Points) == 0 {
		t.Fatalf("missing comparable plan: %+v %v", plan, err)
	}
	work := nativeWeeklyWork{Plan: plan, WalletCapacity: &estimation.Amount{Numerator: walletCapacity, Denominator: "1"}}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO native_weekly_estimates(account_id,basis_key,retry_at,work) VALUES($1,$2,clock_timestamp(),$3) RETURNING id::text`, account, plan.key(), work).Scan(&id); err != nil {
		t.Fatal(err)
	}
	record := &NativeWeeklyRecord{ID: id, AccountID: account, SourceID: a.SourceID, ConnectionRevision: c.Revision, AccountRevision: a.Revision,
		GeneratedAt: time.Now().UTC(), Observation: plan.through(), Period: *plan.Period, Cumulative: &NativeWeeklyCumulative{Confidence: "trusted"},
		Capacity: &estimation.Amount{Numerator: capacity, Denominator: "1"}}
	if _, err = tx.Exec(ctx, `UPDATE native_weekly_estimates SET result=$2 WHERE id=$1`, id, record); err != nil {
		t.Fatal(err)
	}
	if err = s.recordNativeEstimateNotification(ctx, tx, c, &nativeWeeklyJob{ID: id, Work: work, Result: record}); err != nil {
		t.Fatal(err)
	}
}

func nativeWalletNotificationMailSink(t *testing.T) (int, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = fmt.Fprint(conn, "220 owned.test\r\n")
			var body strings.Builder
			inData := false
			for input := bufio.NewScanner(conn); input.Scan(); {
				line := input.Text()
				switch {
				case inData && line == ".":
					_, _ = fmt.Fprint(conn, "250 accepted\r\n")
					messages <- body.String()
					inData = false
				case inData:
					body.WriteString(line + "\n")
				case line == "DATA":
					_, _ = fmt.Fprint(conn, "354 continue\r\n")
					inData = true
				case line == "QUIT":
					_, _ = fmt.Fprint(conn, "221 bye\r\n")
				default:
					_, _ = fmt.Fprint(conn, "250 owned.test\r\n")
				}
			}
			_ = conn.Close()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, messages
}

func TestNativeEstimateChangeUsesTrustedComparableStandardCapacity(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
	job := func(amount int64) *nativeWeeklyJob {
		return &nativeWeeklyJob{Work: nativeWeeklyWork{Plan: nativeWeeklyPlan{Binding: "same", CostVersion: 1}},
			Result: &NativeWeeklyRecord{GeneratedAt: start.Add(time.Duration(amount) * time.Minute), Period: ObservationPeriod{StartAt: start, EndAt: start.Add(nativeWeek)},
				Cumulative: &NativeWeeklyCumulative{Confidence: "trusted"}, Capacity: &estimation.Amount{Numerator: strconv.FormatInt(amount, 10), Denominator: "1"}}}
	}
	old := job(100)
	for _, val := range []struct {
		capacity int64
		change   bool
	}{{80, true}, {81, false}, {119, false}, {120, true}} {
		current := job(val.capacity)
		current.Result.GeneratedAt = old.Result.GeneratedAt.Add(time.Minute)
		if nativeEstimateChanged(old, current) != val.change {
			t.Fatal("wrong standard capacity comparison", val.capacity)
		}
		current.Work.Plan.CostVersion++
		if nativeEstimateChanged(old, current) {
			t.Fatal("compared a changed cost version")
		}
	}
	collecting := job(150)
	collecting.Result.Cumulative.Confidence = "collecting"
	if nativeEstimateChanged(old, collecting) || nativeEstimateChanged(nil, job(150)) {
		t.Fatal("used a provisional result or missing previous estimate")
	}
}
