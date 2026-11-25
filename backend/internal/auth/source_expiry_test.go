package auth

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSourceExpiryReleasesBeforeSweepAndAuditsOnce(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	h, _ := NewHandler(s, "http://127.0.0.1:8081", true)
	if err := h.ConfigureSources("127.0.0.1/32"); err != nil {
		t.Fatal(err)
	}
	const address = "2001:db8::25"
	reason := "  限时阻断  "
	expiry := time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute)
	body, err := json.Marshal(SourceUpdate{IP: address, Revision: "0", Blocked: true, Reason: &reason, ExpiresAt: &expiry})
	if err != nil {
		t.Fatal(err)
	}
	for _, timestamp := range []string{"2099-11-24T10:00:00.1234560001Z", "2099-11-24T10:00:00,123Z"} {
		invalid := strings.Replace(string(body), expiry.Format(time.RFC3339Nano), timestamp, 1)
		if w := sourceRequest(h, "/api/v1/security/sources/update", invalid, token, "127.0.0.1", "198.51.100.10"); w.Code != 400 {
			t.Fatal("non-canonical expiry accepted", w.Code)
		}
	}
	w := sourceRequest(h, "/api/v1/security/sources/update", string(body), token, "127.0.0.1", "198.51.100.10")
	var saved SourceRule
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Revision != "1" ||
		saved.Reason == nil || *saved.Reason != "限时阻断" || saved.ExpiresAt == nil || !saved.ExpiresAt.Equal(expiry) {
		t.Fatalf("timed rule receipt: %d %s", w.Code, w.Body)
	}
	requestStatus := func() int {
		return sourceRequest(h, "/api/v1/auth/session", "", token, "127.0.0.1", address).Code
	}
	if requestStatus() != 403 {
		t.Fatal("an active rule allowed an existing session")
	}
	ip := netip.MustParseAddr(address)
	if _, _, err = s.Login(ctx, "admin", testPassword, ip); !errors.Is(err, ErrSourceBlocked) {
		t.Fatal("an active rule allowed login", err)
	}

	exec(t, pool, `UPDATE source_blocks SET expires_at=statement_timestamp()-interval '1 second' WHERE ip=$1::inet`, address)
	if requestStatus() != 200 {
		t.Fatal("session waited for the background sweep")
	}
	if _, _, err = s.Login(ctx, "admin", testPassword, ip); err != nil {
		t.Fatal("login waited for the background sweep", err)
	}
	page, err := s.ListSources(ctx, token, "", "unblocked", 100)
	if err != nil || len(page.Rules) != 1 || page.Rules[0].Blocked || page.Rules[0].Revision != "1" || page.Rules[0].ExpiresAt == nil {
		t.Fatal("list did not project effective status", page, err)
	}

	exec(t, pool, `CREATE FUNCTION reject_expiry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action='security.source.expire' THEN RAISE EXCEPTION 'reject'; END IF;
		RETURN NEW; END $$; CREATE TRIGGER reject_expiry BEFORE INSERT ON management_events
		FOR EACH ROW EXECUTE FUNCTION reject_expiry()`)
	if err = s.expireSourceRules(ctx); err == nil {
		t.Fatal("sweep ignored journal failure")
	}
	var blocked bool
	var revision string
	if err = pool.QueryRow(ctx, `SELECT blocked,revision::text FROM source_blocks WHERE ip=$1::inet`, address).Scan(&blocked, &revision); err != nil || !blocked || revision != "1" || requestStatus() != 200 {
		t.Fatal("failed audit changed the stored rule or effective gate", blocked, revision, err)
	}
	exec(t, pool, `DROP TRIGGER reject_expiry ON management_events`)
	restarted := New(pool)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- restarted.expireSourceRules(ctx) }()
	}
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	var events int
	err = pool.QueryRow(ctx, `SELECT blocked,revision::text,
		(SELECT count(*) FROM management_events WHERE action='security.source.expire' AND task_id='source-expiry'
			AND actor_id IS NULL AND target_id=$1 AND target_revision=1)
		FROM source_blocks WHERE ip=$1::inet`, address).Scan(&blocked, &revision, &events)
	if err != nil || blocked || revision != "2" || events != 1 {
		t.Fatal("expiry state or journal was lost or duplicated", blocked, revision, events, err)
	}
}

func TestManualSourceChangesKeepExpiryHistory(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, token := login(t, s)
	ctx := managementContext()
	address, reason := "203.0.113.70", "维护检查"
	expiry := time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute)
	timed := SourceUpdate{IP: address, Revision: "0", Blocked: true, Reason: &reason, ExpiresAt: &expiry}
	for _, invalid := range []time.Time{expiry.Add(-2 * time.Minute), expiry.Add(time.Nanosecond), expiry.In(time.FixedZone("offset", 3600))} {
		bad := timed
		bad.ExpiresAt = &invalid
		if _, err := s.UpdateSource(ctx, token, bad); !errors.Is(err, ErrInput) {
			t.Fatal("invalid expiry accepted", err)
		}
	}
	long := strings.Repeat("因", 257)
	bad := timed
	bad.Reason = &long
	if _, err := s.UpdateSource(ctx, token, bad); !errors.Is(err, ErrInput) {
		t.Fatal("oversized reason accepted", err)
	}
	rule, err := s.UpdateSource(ctx, token, timed)
	if err != nil {
		t.Fatal(err)
	}
	unblocked, err := s.UpdateSource(ctx, token, SourceUpdate{IP: address, Revision: rule.Revision})
	if err != nil || unblocked.Reason == nil || *unblocked.Reason != reason || unblocked.ExpiresAt == nil || !unblocked.ExpiresAt.Equal(expiry) {
		t.Fatal("manual release changed the prior explanation and deadline", unblocked, err)
	}
	timed.Revision = unblocked.Revision
	rule, err = s.UpdateSource(ctx, token, timed)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `UPDATE source_blocks SET expires_at=statement_timestamp()-interval '1 second' WHERE ip=$1::inet`, address)
	permanent, err := s.UpdateSource(ctx, token, SourceUpdate{IP: address, Revision: rule.Revision, Blocked: true})
	if err != nil || permanent.Revision != "4" || !permanent.Blocked || permanent.ExpiresAt != nil || permanent.Reason != nil {
		t.Fatal("reblock did not use the next revision and new metadata", permanent, err)
	}
	if err = s.expireSourceRules(ctx); err != nil {
		t.Fatal(err)
	}
	var events string
	if err = pool.QueryRow(ctx, `SELECT COALESCE(string_agg(target_revision::text,',' ORDER BY id),'') FROM management_events
		WHERE action='security.source.expire' AND target_id=$1`, address).Scan(&events); err != nil || events != "3" {
		t.Fatal("reblock did not settle the previous expiry exactly once", events, err)
	}
}
