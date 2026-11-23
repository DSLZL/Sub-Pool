package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sub-pool/backend/internal/audit"
)

func managementContext() context.Context {
	ip, peer := "198.51.100.10", "127.0.0.1"
	return context.WithValue(context.Background(), sourceKey{}, RequestSource{IP: &ip, PeerIP: &peer, Kind: "trusted_forwarded"})
}

func auditRange() AuditRange {
	return AuditRange{From: time.Now().UTC().Add(-time.Hour), Until: time.Now().UTC().Add(time.Hour), After: "0", Limit: 100}
}

func sourceRequest(h http.Handler, path, body, token, peer, forwarded string) *httptest.ResponseRecorder {
	method := "POST"
	if body == "" {
		method = "GET"
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = net.JoinHostPort(peer, "12345")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:8081")
	}
	if token != "" {
		r.Header.Set("Cookie", "sub_pool_dev_session="+token)
	}
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSourcePolicyRequiresAnExplicitTrustedPeer(t *testing.T) {
	h, _ := NewHandler(New(nil), "http://127.0.0.1:8081", true)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.5")
	if source := h.requestSource(r); source.Kind != "peer" || *source.IP != "127.0.0.1" {
		t.Fatal("untrusted header selected the source")
	}
	if err := h.ConfigureSources("127.0.0.1/32,10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Forwarded-For", "192.0.2.99, 203.0.113.5, 10.0.0.8")
	if source := h.requestSource(r); source.Kind != "trusted_forwarded" || *source.IP != "203.0.113.5" || *source.PeerIP != "127.0.0.1" {
		t.Fatal("trusted chain did not stop at the first untrusted hop")
	}
	for _, bad := range []string{"", "203.0.113.5, invalid", "[2001:db8::1]", "fe80::1%eth0", strings.Repeat("1,", 21), strings.Repeat("x", 2049)} {
		r.Header.Set("X-Forwarded-For", bad)
		if source := h.requestSource(r); source.Kind != "peer" || *source.IP != "127.0.0.1" {
			t.Fatal("malformed chain changed source")
		}
	}
	r.Header["X-Forwarded-For"] = []string{"203.0.113.5", "203.0.113.6"}
	if source := h.requestSource(r); source.Kind != "peer" {
		t.Fatal("ambiguous headers accepted")
	}
	for _, bad := range []string{"invalid", "127.0.0.1", "::ffff:127.0.0.1/128", "127.0.0.1/33"} {
		if err := h.ConfigureSources(bad); err == nil {
			t.Fatal("invalid trust configuration accepted")
		}
	}
	r.RemoteAddr = "[::ffff:127.0.0.1]:1234"
	r.Header.Del("X-Forwarded-For")
	if source := h.requestSource(r); *source.IP != "127.0.0.1" {
		t.Fatal("mapped IP bypassed canonical identity")
	}
}

func TestSecuritySourceRulesProtectTheAdministratorAndEverySharedBoundary(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	for _, address := range []string{"198.51.100.10", "127.0.0.1"} {
		if _, err := s.UpdateSource(ctx, token, SourceUpdate{IP: address, Revision: "0", Blocked: true}); !errors.Is(err, ErrSelfSource) {
			t.Fatal("current management source/peer could be blocked")
		}
	}
	rule, err := s.UpdateSource(ctx, token, SourceUpdate{IP: "203.0.113.20", Revision: "0", Blocked: true})
	if err != nil || !rule.Blocked || rule.Revision != "1" || rule.SavedAt.Location() != time.UTC {
		t.Fatal("rule was not persisted")
	}
	h, _ := NewHandler(New(pool), "http://127.0.0.1:8081", true)
	if w := sourceRequest(h, "/api/v1/auth/session", "", token, "127.0.0.1", rule.IP); w.Code != 200 {
		t.Fatal("untrusted forwarding header selected a blocked source")
	}
	if err := h.ConfigureSources("127.0.0.1/32"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/auth/session", "/api/v1/security/config"} {
		w := sourceRequest(h, path, "", token, "127.0.0.1", rule.IP)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "SOURCE_BLOCKED") {
			t.Fatal("blocked source reached a protected route after restart")
		}
	}
	var dispatched bool
	domain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleJSON(w, r, "POST", nil, func(context.Context, string, map[string]json.RawMessage) (any, error) {
			dispatched = true
			return struct{}{}, nil
		})
	})
	if w := sourceRequest(domain, "/api/v1/accounts/list", `{}`, token, "127.0.0.1", rule.IP); w.Code != 403 || dispatched {
		t.Fatal("blocked source reached a domain handler")
	}
	if w := sourceRequest(h, "/api/v1/auth/login", `{"username":"admin","password":"OriginalPassword123"}`, "", "127.0.0.1", rule.IP); w.Code != 403 {
		t.Fatal("blocked login was allowed")
	}
	if _, _, err := s.Login(context.Background(), "admin", testPassword, netip.MustParseAddr(rule.IP)); !errors.Is(err, ErrSourceBlocked) {
		t.Fatal("direct login bypassed durable source rules")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_events WHERE action='login' AND outcome='blocked' AND source_ip=$1`, rule.IP).Scan(&count); err != nil || count != 2 {
		t.Fatal("blocked login attempts were not recorded")
	}
	if _, err := s.UpdateSource(ctx, token, SourceUpdate{IP: rule.IP, Revision: "0", Blocked: false}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale unblock succeeded")
	}
	unblocked, err := s.UpdateSource(ctx, token, SourceUpdate{IP: rule.IP, Revision: rule.Revision, Blocked: false})
	if err != nil || unblocked.Blocked || unblocked.Revision != "2" {
		t.Fatal("explicit unblock failed")
	}
	if w := sourceRequest(h, "/api/v1/auth/session", "", token, "127.0.0.1", rule.IP); w.Code != 200 {
		t.Fatal("unblock did not restore allowed access")
	}
	page, err := s.ListSources(ctx, token, "", "unblocked", 1)
	if err != nil || len(page.Rules) != 1 || page.NextAfter != nil || page.Rules[0].IP != rule.IP {
		t.Fatal("unblocked history was deleted or pagination changed")
	}
	for _, invalid := range []string{"203.0.113.20/32", "::ffff:203.0.113.20", "fe80::1%eth0", "0.0.0.0"} {
		if _, err := s.UpdateSource(ctx, token, SourceUpdate{IP: invalid, Revision: "0", Blocked: true}); !errors.Is(err, ErrInput) {
			t.Fatal("noncanonical address/range accepted")
		}
	}
	v6, err := s.UpdateSource(ctx, token, SourceUpdate{IP: "::c000:201", Revision: "0", Blocked: true})
	if err != nil || v6.IP != "::c000:201" {
		t.Fatal("PostgreSQL IPv6 formatting escaped the canonical API")
	}
	first, err := s.ListSources(ctx, token, "", "all", 1)
	if err != nil || first.NextAfter == nil {
		t.Fatal("mixed-family rule cursor was lost")
	}
	second, err := s.ListSources(ctx, token, *first.NextAfter, "all", 1)
	if err != nil || len(second.Rules) != 1 || second.NextAfter != nil || second.Rules[0].IP != v6.IP {
		t.Fatal("canonical IPv6 cursor omitted or repeated an address")
	}
	if _, err := s.UpdateSource(ctx, token, SourceUpdate{IP: second.Rules[0].IP, Revision: v6.Revision, Blocked: false}); err != nil {
		t.Fatal("returned IPv6 address could not be unblocked")
	}
}

func TestSecurityAuditUsesExplicitCurrentReadGrantsAndSafeCounts(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	ctx := managementContext()
	member, err := s.CreateUser(ctx, admin, UserCreate{Username: "audit.reader", Password: testPassword, Enabled: true, Grants: Grants{Features: []string{}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := s.Login(ctx, member.Username, testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginAudit(ctx, memberToken, auditRange(), "all"); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user received global source data by default")
	}
	exec(t, pool, `UPDATE auth_users SET read_features=ARRAY['security'],revision=revision+1 WHERE id=$1`, member.ID)
	for i := 0; i < 6; i++ {
		_, _, _ = s.Login(context.Background(), "unknown.user", "NeverLoggedSecret", netip.MustParseAddr("203.0.113.30"))
	}
	page, err := s.LoginAudit(ctx, memberToken, auditRange(), "all")
	if err != nil || page.Counts.Success != "2" || page.Counts.Failure != "5" || page.Counts.Limited != "1" || page.Counts.Blocked != "0" {
		t.Fatalf("login outcomes/counts lost: %+v %v", page.Counts, err)
	}
	var full []LoginEvent
	after := "0"
	for {
		input := auditRange()
		input.After, input.Limit = after, 2
		part, err := s.LoginAudit(ctx, memberToken, input, "all")
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, part.Events...)
		if part.NextAfter == nil {
			break
		}
		after = *part.NextAfter
	}
	if len(full) != 8 {
		t.Fatal("audit pagination omitted or repeated events")
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "NeverLoggedSecret") || strings.Contains(string(encoded), testPassword) || strings.Contains(string(encoded), memberToken) {
		t.Fatal("audit exposed authentication secrets")
	}
	if _, err := s.ManagementAudit(ctx, memberToken, auditRange()); !errors.Is(err, ErrForbidden) {
		t.Fatal("read grant authorized management audit")
	}
	if _, err := s.UpdateSource(ctx, memberToken, SourceUpdate{IP: "203.0.113.31", Revision: "0", Blocked: true}); !errors.Is(err, ErrForbidden) {
		t.Fatal("read grant authorized source management")
	}
	if _, err := s.ListSources(ctx, memberToken, "", "all", 100); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `UPDATE auth_users SET read_features='{}',revision=revision+1 WHERE id=$1`, member.ID)
	if _, err := s.ListSources(ctx, memberToken, "", "all", 100); !errors.Is(err, ErrForbidden) {
		t.Fatal("removed grant retained source access")
	}
	if err := s.Logout(ctx, memberToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginAudit(ctx, memberToken, auditRange(), "all"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("logged out session retained audit access")
	}
}

func TestOptionalNetworkComparisonNeverControlsLoginOrRetainsBrowserHints(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	c, err := s.GetSecurityConfig(ctx, token)
	if err != nil || c.NetworkDiagnosticsEnabled {
		t.Fatal("diagnostics enabled by default")
	}
	if _, err := s.CompareNetwork(ctx, token, "203.0.113.40", true); !errors.Is(err, ErrDiagnosticsDisabled) {
		t.Fatal("disabled diagnostic accepted a report")
	}
	login(t, s)
	c, err = s.UpdateSecurityConfig(ctx, token, c.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareNetwork(ctx, token, "203.0.113.40", false); !errors.Is(err, ErrInput) {
		t.Fatal("missing consent accepted")
	}
	var before, after int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM auth_events`).Scan(&before)
	result, err := s.CompareNetwork(ctx, token, "203.0.113.40", true)
	if err != nil || result.Trusted || result.Source.IP == nil || *result.Source.IP != "198.51.100.10" || result.ReportedIP != "203.0.113.40" {
		t.Fatal("self-report became trusted server identity")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM auth_events`).Scan(&after)
	if before != after {
		t.Fatal("browser hint was retained as an authentication event")
	}
	if _, err := s.UpdateSecurityConfig(ctx, token, "1", false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale diagnostic settings overwrote current state")
	}
	if _, err := s.UpdateSecurityConfig(ctx, token, c.Revision, false); err != nil {
		t.Fatal(err)
	}
	login(t, s)
}

func TestSecurityMutationsAndExistingSensitiveChangesRollBackWithJournalFailure(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	exec(t, pool, `CREATE FUNCTION reject_management() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject'; END $$; CREATE TRIGGER reject_management BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_management()`)
	if _, err := s.UpdateSource(ctx, token, SourceUpdate{IP: "203.0.113.50", Revision: "0", Blocked: true}); err == nil {
		t.Fatal("source mutation ignored journal failure")
	}
	if _, err := s.UpdateSecurityConfig(ctx, token, "1", true); err == nil {
		t.Fatal("configuration mutation ignored journal failure")
	}
	if _, err := s.CreateUser(ctx, token, UserCreate{Username: "audit.rollback", Password: testPassword, Enabled: true, Grants: Grants{Features: []string{}, AccountIDs: []string{}, ParticipantIDs: []string{}}}); err == nil {
		t.Fatal("user creation ignored journal failure")
	}
	if err := s.ChangePassword(ctx, token, testPassword, "ChangedPassword123"); err == nil {
		t.Fatal("credential change ignored journal failure")
	}
	var rules, users int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM source_blocks`).Scan(&rules)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM auth_users WHERE username='audit.rollback'`).Scan(&users)
	c, err := s.GetSecurityConfig(ctx, token)
	if err != nil || c.Revision != "1" || c.NetworkDiagnosticsEnabled || rules != 0 || users != 0 {
		t.Fatal("failed audit left committed configuration or identity changes")
	}
	login(t, s)
}

func TestSecurityJournalCorrelatesIntentAndUnknownWithoutInventingSuccess(t *testing.T) {
	s, pool := setup(t)
	actor := bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	tx, _ := pool.Begin(ctx)
	operation, err := audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "fixture.apply", Outcome: "pending", TargetType: "account", TargetID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx, _ = pool.Begin(ctx)
	job := "fixture-task"
	if _, err := audit.Record(ctx, tx, audit.Entry{OperationID: operation, TaskID: &job, Action: "fixture.apply", Outcome: "unknown", TargetType: "account", TargetID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.ManagementAudit(ctx, token, auditRange())
	if err != nil || len(page.Events) != 3 || page.Events[1].OperationID != operation || page.Events[2].OperationID != operation || page.Events[2].Outcome != "unknown" {
		t.Fatal("intent/result correlation or explicit uncertainty was lost")
	}
	for _, entry := range []audit.Entry{
		{ActorID: &actor.ID, TaskID: &job, Action: "fixture", Outcome: "succeeded", TargetType: "account", TargetID: "fixture"},
		{ActorID: &actor.ID, Action: "fixture", Outcome: "raw-error-with-secret", TargetType: "account", TargetID: "fixture"},
		{ActorID: &actor.ID, Action: "fixture", Outcome: "failed", TargetType: "account", TargetID: "unsafe\nbody"},
	} {
		if _, err := audit.Record(ctx, nil, entry); err == nil {
			t.Fatal("invalid audit metadata accepted")
		}
	}
}

func TestSecurityManagementAuditPaginatesNumericIDs(t *testing.T) {
	s, _ := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	config, err := s.GetSecurityConfig(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		config, err = s.UpdateSecurityConfig(ctx, token, config.Revision, !config.NetworkDiagnosticsEnabled)
		if err != nil {
			t.Fatal(err)
		}
	}
	input := auditRange()
	input.Limit = 3
	seen := 0
	for {
		page, err := s.ManagementAudit(ctx, token, input)
		if err != nil || len(page.Events) == 0 || len(page.Events) > input.Limit {
			t.Fatalf("invalid numeric audit page: %v", err)
		}
		for _, event := range page.Events {
			seen++
			if seen > 13 || event.ID != strconv.Itoa(seen) {
				t.Fatalf("audit pagination lost numeric order at record %d: %s", seen, event.ID)
			}
		}
		if page.NextAfter == nil {
			break
		}
		if *page.NextAfter != page.Events[len(page.Events)-1].ID {
			t.Fatal("audit cursor does not match the last returned numeric ID")
		}
		input.After = *page.NextAfter
	}
	if seen != 13 {
		t.Fatalf("audit pagination omitted records: %d", seen)
	}
}

func TestSecurityConcurrentRulesAndExpiryAfterActualConfigurationWait(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateSource(ctx, token, SourceUpdate{IP: "203.0.113.60", Revision: "0", Blocked: true})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var pass, conflict int
	for err := range results {
		if err == nil {
			pass++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if pass != 1 || conflict != 1 {
		t.Fatal("stale source-rule writers both committed")
	}
	blocker, _ := pool.Begin(ctx)
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT singleton FROM security_config WHERE singleton FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	digest, _ := tokenDigest(token)
	exec(t, pool, `UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '1 second' WHERE digest=$1`, digest)
	done := make(chan error, 1)
	go func() {
		_, err := s.UpdateSource(ctx, token, SourceUpdate{IP: "203.0.113.61", Revision: "0", Blocked: true})
		done <- err
	}()
	deadline, waiting := time.Now().Add(5*time.Second), false
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%network_diagnostics_enabled FROM security_config%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("operation did not reach the real configuration lock")
	}
	time.Sleep(1100 * time.Millisecond)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired session committed after the lock wait")
	}
	var exists bool
	_ = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_blocks WHERE ip='203.0.113.61')`).Scan(&exists)
	if exists {
		t.Fatal("expired mutation persisted a source rule")
	}
}
