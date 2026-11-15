package notifications

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/testdb"
)

func setup(t *testing.T) (*Service, string) {
	t.Helper()
	pool := testdb.Open(t)
	ctx := context.Background()
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	identity := auth.New(pool)
	if _, e := identity.Bootstrap(ctx, "administrator", "OwnedTestPassword123"); e != nil {
		t.Fatal(e)
	}
	_, token, e := identity.Login(ctx, "administrator", "OwnedTestPassword123", netip.MustParseAddr("127.0.0.1"))
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(pool, identity, bytes.Repeat([]byte{31}, 32), true)
	if e != nil {
		t.Fatal(e)
	}
	return s, token
}
func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, e := pool.Exec(context.Background(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func id(n int) string {
	b := make([]byte, 16)
	b[14] = byte(n >> 8)
	b[15] = byte(n)
	return base64.RawURLEncoding.EncodeToString(b)
}
func input(c Configuration) SaveInput {
	return SaveInput{Revision: c.Revision, Enabled: c.Enabled, FinancialEnabled: c.FinancialEnabled, Channel: c.Channel, From: c.From, Recipient: c.Recipient, SMTP: c.SMTP, Preferences: c.Preferences, DedupeSeconds: c.DedupeSeconds, CredentialAction: "keep"}
}
func config(t *testing.T, s *Service, token string) Configuration {
	t.Helper()
	c, e := s.GetConfiguration(context.Background(), token)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func save(t *testing.T, s *Service, token string, in SaveInput) Configuration {
	t.Helper()
	c, e := s.SaveConfiguration(context.Background(), token, in)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func request(t *testing.T, s *Service, token, revision string, n int) Notification {
	t.Helper()
	v, e := s.RequestTest(context.Background(), token, TestInput{id(n), revision})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func process(t *testing.T, s *Service) bool {
	t.Helper()
	conn, e := s.pool.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	p, e := s.process(context.Background(), conn)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestConfigurationSecretsAreTargetBoundAndFinancialCapabilitiesStayUnavailable(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	c := config(t, s, token)
	if c.Enabled || c.SecretSet || len(c.Capabilities) != 4 {
		t.Fatal("unsafe defaults")
	}
	in := input(c)
	in.Channel = "resend"
	in.From = "sender@owned.test"
	in.Recipient = "admin@owned.test"
	in.CredentialAction = "replace"
	in.Credential = "owned-private-mail-key"
	in.Enabled = true
	in.Preferences = Preferences{true, true, true, true}
	c = save(t, s, token, in)
	encoded, _ := json.Marshal(c)
	var encrypted []byte
	if e := s.pool.QueryRow(ctx, `SELECT secret FROM notification_config`).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(encoded, []byte(in.Credential)) || bytes.Contains(encrypted, []byte(in.Credential)) || !c.SecretSet || !c.SecretStorageAvailable {
		t.Fatal("mail secret escaped encryption")
	}
	for _, v := range c.Capabilities[:3] {
		if v.Available || v.Effective || v.Reason != "FINANCIAL_BASIS_UNAVAILABLE" {
			t.Fatal("financial preference enabled a producer")
		}
	}
	in = input(c)
	in.Channel = "smtp"
	in.SMTP.Host = "smtp.owned.test"
	if _, e := s.SaveConfiguration(ctx, token, in); !errors.Is(e, auth.ErrInput) {
		t.Fatal("secret moved to another channel")
	}
	copyService, e := New(s.pool, s.auth, bytes.Repeat([]byte{31}, 32), true)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	stored, e := readConfig(ctx, tx, false)
	if e != nil {
		t.Fatal(e)
	}
	secret, e := copyService.decrypt(stored)
	if e != nil || secret != "owned-private-mail-key" {
		t.Fatal("restart lost encrypted config")
	}
	stored.Channel = "smtp"
	if _, e = copyService.decrypt(stored); !errors.Is(e, ErrSecretStorage) {
		t.Fatal("cross-domain ciphertext accepted")
	}
	missing, e := New(s.pool, s.auth, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	stored.Channel = "resend"
	if _, e = missing.decrypt(stored); !errors.Is(e, ErrSecretStorage) {
		t.Fatal("missing key accepted")
	}
	if _, e = New(s.pool, s.auth, []byte{1}, true); e == nil {
		t.Fatal("bad key accepted")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*SaveInput){func(v *SaveInput) { v.From = "evil@owned.test\r\nBcc: victim@owned.test" }, func(v *SaveInput) { v.SMTP.Host = "smtp.owned.test/path" }, func(v *SaveInput) { v.DedupeSeconds = 0 }, func(v *SaveInput) { v.CredentialAction = "replace"; v.Credential = "bad\nkey" }, func(v *SaveInput) { v.SMTP.TLS = "loopback_plain"; v.SMTP.Host = "smtp.owned.test" }} {
		bad := input(c)
		change(&bad)
		if _, e = s.SaveConfiguration(ctx, token, bad); !errors.Is(e, auth.ErrInput) {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestExplicitTestIntentIsActorAndRevisionBoundAndAuditAtomic(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	c := config(t, s, token)
	n := request(t, s, token, c.Revision, 1)
	if n.State != "skipped" || n.Reason != "CHANNEL_INCOMPLETE" {
		t.Fatal(n.State, n.Reason)
	}
	if replay := request(t, s, token, c.Revision, 1); replay.ID != n.ID {
		t.Fatal("replay created intent")
	}
	c = save(t, s, token, input(c))
	if replay := request(t, s, token, "1", 1); replay.ID != n.ID {
		t.Fatal("old request did not reconcile")
	}
	if _, e := s.RequestTest(ctx, token, TestInput{id(1), c.Revision}); !errors.Is(e, auth.ErrConflict) {
		t.Fatal("key rebound to newer configuration")
	}
	exec(t, s.pool, `CREATE FUNCTION reject_notification_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'notification%' THEN RAISE EXCEPTION 'owned audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_notification_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_notification_audit()`)
	if _, e := s.RequestTest(ctx, token, TestInput{id(2), c.Revision}); e == nil {
		t.Fatal("missing audit accepted")
	}
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count); e != nil || count != 1 {
		t.Fatal("failed audit left an intent", count, e)
	}
	if _, e := s.SaveConfiguration(ctx, token, input(c)); e == nil {
		t.Fatal("configuration journal failure ignored")
	}
	if config(t, s, token).Revision != c.Revision {
		t.Fatal("config changed without audit")
	}
}

type smtpSink struct {
	listener net.Listener
	mode     atomic.Int32
	count    atomic.Int32
	messages chan string
	release  chan struct{}
}

func newSink(t *testing.T) *smtpSink {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	s := &smtpSink{listener: l, messages: make(chan string, 20), release: make(chan struct{})}
	go func() {
		for {
			conn, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(conn)
				_, _ = io.WriteString(conn, "220 owned local SMTP\r\n")
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						_, _ = io.WriteString(conn, "250 owned-local\r\n")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						_, _ = io.WriteString(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "DATA"):
						_, _ = io.WriteString(conn, "354 continue\r\n")
						var body strings.Builder
						for {
							v, e := r.ReadString('\n')
							if e != nil {
								return
							}
							if v == ".\r\n" {
								break
							}
							body.WriteString(v)
						}
						s.count.Add(1)
						s.messages <- body.String()
						if s.mode.Load() == 3 {
							select {
							case <-s.release:
							case <-time.After(5 * time.Second):
								return
							}
						}
						if s.mode.Load() == 2 {
							return
						}
						if s.mode.Load() == 1 {
							_, _ = io.WriteString(conn, "550 rejected private diagnostics\r\n")
						} else {
							_, _ = io.WriteString(conn, "250 queued\r\n")
						}
					default:
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return s
}
func sinkConfig(t *testing.T, s *Service, token string, sink *smtpSink) Configuration {
	t.Helper()
	_, port, e := net.SplitHostPort(sink.listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	p, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	in := input(config(t, s, token))
	in.From = "sender@owned.test"
	in.Recipient = "admin@owned.test"
	in.SMTP = SMTP{"127.0.0.1", p, "loopback_plain", ""}
	in.Enabled = true
	in.Preferences.CollectionFailed = true
	return save(t, s, token, in)
}
func TestSMTPRecordsAcceptanceRejectionAndUnknownWithoutReplay(t *testing.T) {
	s, token := setup(t)
	sink := newSink(t)
	c := sinkConfig(t, s, token, sink)
	for i, want := range []string{"accepted", "failed", "unknown"} {
		sink.mode.Store(int32(i))
		n := request(t, s, token, c.Revision, i+1)
		if n.State != "queued" || !process(t, s) {
			t.Fatal("intent not processed")
		}
		got, e := s.Get(context.Background(), token, n.ID)
		if e != nil || got.State != want || got.FinishedAt == nil {
			t.Fatal("wrong transport receipt", got.State, e)
		}
		if request(t, s, token, c.Revision, i+1).State != want {
			t.Fatal("replay lost original outcome")
		}
	}
	if process(t, s) || sink.count.Load() != 3 {
		t.Fatal("mail was automatically replayed")
	}
	message := <-sink.messages
	if !strings.Contains(message, "To: admin@owned.test") || strings.Contains(message, "owned-private-mail-key") {
		t.Fatal("message destination or secret invalid")
	}
	// A server without STARTTLS must never receive DATA through a plaintext fallback.
	in := input(c)
	in.SMTP.TLS = "starttls"
	c = save(t, s, token, in)
	n := request(t, s, token, c.Revision, 4)
	process(t, s)
	got, e := s.Get(context.Background(), token, n.ID)
	if e != nil || got.State != "failed" || sink.count.Load() != 3 {
		t.Fatal("TLS downgrade", got.State, e)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestResendUsesOnlyFixedEndpointAndRequiresAnUnambiguousReceipt(t *testing.T) {
	s, token := setup(t)
	in := input(config(t, s, token))
	in.Channel = "resend"
	in.From = "sender@owned.test"
	in.Recipient = "admin@owned.test"
	in.CredentialAction = "replace"
	in.Credential = "owned-private-mail-key"
	c := save(t, s, token, in)
	for i, tc := range []struct {
		status      int
		body, state string
	}{{200, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`, "accepted"}, {400, `{"message":"private rejection"}`, "failed"}, {302, `{}`, "unknown"}, {500, `{}`, "unknown"}, {200, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794","id":"bad"}`, "unknown"}, {200, `{"id":"bad"}`, "unknown"}, {200, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"} {}`, "unknown"}} {
		calls := 0
		s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() != "https://api.resend.com/emails" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer owned-private-mail-key" || r.Header.Get("Cookie") != "" {
				t.Fatal("fixed Resend boundary escaped")
			}
			body, _ := io.ReadAll(r.Body)
			if !bytes.Contains(body, []byte(`"text":`)) || bytes.Contains(body, []byte("owned-private-mail-key")) {
				t.Fatal("private message body")
			}
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://attacker.invalid/"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		n := request(t, s, token, c.Revision, i+1)
		process(t, s)
		got, e := s.Get(context.Background(), token, n.ID)
		if e != nil || got.State != tc.state || calls != 1 {
			t.Fatal("invalid Resend outcome", got.State, calls, e)
		}
	}
	if e := s.client.CheckRedirect(httptest.NewRequest("GET", "https://attacker.invalid", nil), nil); !errors.Is(e, http.ErrUseLastResponse) {
		t.Fatal("redirects enabled")
	}
}

func TestQueuedTestsRecheckActorAndConfigurationAfterObservedWait(t *testing.T) {
	s, token := setup(t)
	sink := newSink(t)
	c := sinkConfig(t, s, token, sink)
	n := request(t, s, token, c.Revision, 1)
	c = save(t, s, token, input(c))
	process(t, s)
	got, e := s.Get(context.Background(), token, n.ID)
	if e != nil || got.State != "skipped" || got.Reason != "CONFIGURATION_CHANGED" || sink.count.Load() != 0 {
		t.Fatal("retargeted queued test")
	}
	n = request(t, s, token, c.Revision, 2)
	expires := time.Now().Add(time.Second)
	exec(t, s.pool, `UPDATE auth_sessions SET idle_expires_at=$1`, expires)
	block, e := s.pool.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback(context.Background())
	if _, e = block.Exec(context.Background(), `SELECT 1 FROM notification_config FOR UPDATE`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		conn, e := s.pool.Acquire(context.Background())
		if e != nil {
			done <- e
			return
		}
		defer conn.Release()
		_, e = s.process(context.Background(), conn)
		done <- e
	}()
	deadline := time.Now().Add(4 * time.Second)
	observed := false
	for time.Now().Before(deadline) {
		var wait bool
		e = s.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM notification_config%')`).Scan(&wait)
		if e != nil {
			t.Fatal(e)
		}
		if wait {
			observed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observed {
		t.Fatal("configuration wait was not observed")
	}
	time.Sleep(time.Until(expires) + 25*time.Millisecond)
	if e = block.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	var state, reason string
	e = s.pool.QueryRow(context.Background(), `SELECT state,reason FROM notifications WHERE id=$1`, n.ID).Scan(&state, &reason)
	if e != nil || state != "skipped" || reason != "ACTOR_UNAVAILABLE" || sink.count.Load() != 0 {
		t.Fatal("expired actor dispatched", state, reason, e)
	}
}

func TestWorkerRecoveryKeepsUnknownAndDoesNotReplay(t *testing.T) {
	s, token := setup(t)
	sink := newSink(t)
	c := sinkConfig(t, s, token, sink)
	n := request(t, s, token, c.Revision, 1)
	exec(t, s.pool, `UPDATE notifications SET state='dispatching',reason='DISPATCHING',started_at=clock_timestamp() WHERE id=$1`, n.ID)
	conn, e := s.pool.Acquire(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = recoverDispatches(context.Background(), conn); e != nil {
		t.Fatal(e)
	}
	conn.Release()
	if process(t, s) || sink.count.Load() != 0 {
		t.Fatal("interrupted mail was replayed")
	}
	got, e := s.Get(context.Background(), token, n.ID)
	if e != nil || got.State != "unknown" || got.Reason != "WORKER_INTERRUPTED" {
		t.Fatal("interrupted receipt changed")
	}
	request(t, s, token, c.Revision, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(4 * time.Second)
	for sink.count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sink.count.Load() != 1 {
		t.Fatal("background delivery did not run")
	}
}

func TestHistoryScopesBeforeNumericPagingAndHidesSystemDetails(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	c := config(t, s, token)
	for i := 1; i <= 12; i++ {
		request(t, s, token, c.Revision, i)
	}
	account, other, source := id(101), id(102), id(103)
	exec(t, s.pool, `INSERT INTO connection_sources(id,base_url,created_at) VALUES ($1,'https://owned.invalid',clock_timestamp())`, source)
	for i, subject := range []string{other, account, account} {
		exec(t, s.pool, `INSERT INTO notifications(event_key,kind,subject_type,subject_id,source_id,connection_revision,subject_revision,config_revision,channel,from_address,recipient_address,subject,body,event_reason,created_at,finished_at,state,reason) VALUES ($1,'collection_failed','account',$2,$3,1,1,1,'smtp','sender@owned.test','admin-private@owned.test','private subject','private body','UPSTREAM_UNAUTHORIZED',$4,$4,'skipped','EVENT_DISABLED')`, fmt.Sprintf("collection:%d", i), subject, source, time.Date(2026, 11, 7, 12, 0, i, 0, time.UTC))
	}
	u, e := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "ordinary", Password: "OwnedTestPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"notifications"}, AccountIDs: []string{account}, ParticipantIDs: []string{}}})
	if e != nil {
		t.Fatal(e)
	}
	_, ordinary, e := s.auth.Login(ctx, u.Username, "OwnedTestPassword123", netip.MustParseAddr("127.0.0.2"))
	if e != nil {
		t.Fatal(e)
	}
	page, e := s.List(ctx, ordinary, Query{After: "0", Limit: 1})
	if e != nil || len(page.Notifications) != 1 || page.Notifications[0].ID != "14" || page.NextAfter == nil {
		t.Fatal("scope was applied after paging", page, e)
	}
	encoded, _ := json.Marshal(page)
	for _, private := range []string{"admin-private", "private body", "UPSTREAM_UNAUTHORIZED", "EVENT_DISABLED", "config_revision"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("ordinary history leaked administrator detail")
		}
	}
	if page.Notifications[0].Details != nil || page.Notifications[0].Reason != "SKIPPED" {
		t.Fatal("private outcome projection")
	}
	if _, e = s.Get(ctx, ordinary, "1"); !errors.Is(e, auth.ErrNotFound) {
		t.Fatal("system test leaked")
	}
	if _, e = s.Get(ctx, ordinary, "13"); !errors.Is(e, auth.ErrNotFound) {
		t.Fatal("other account leaked")
	}
	p, e := s.List(ctx, token, Query{After: "8", Limit: 2})
	if e != nil || len(p.Notifications) != 2 || p.Notifications[0].ID != "9" || p.Notifications[1].ID != "10" || p.NextAfter == nil || *p.NextAfter != "10" {
		t.Fatal("text numeric cursor", p, e)
	}
	p, e = s.List(ctx, ordinary, Query{After: "0", Limit: 10, StartAt: "2026-11-07T12:00:01.000000001Z", EndAt: "2026-11-07T12:00:03Z", Kind: "collection_failed", SubjectType: "account", SubjectID: account, State: "skipped"})
	if e != nil || len(p.Notifications) != 1 || p.Notifications[0].ID != "15" {
		t.Fatal("range/filter mismatch", p, e)
	}
	exec(t, s.pool, `UPDATE auth_users SET account_ids='{}',revision=revision+1 WHERE id=$1`, u.ID)
	p, e = s.List(ctx, ordinary, Query{After: "0", Limit: 100})
	if e != nil || len(p.Notifications) != 0 {
		t.Fatal("scope contraction retained history", e)
	}
	exec(t, s.pool, `UPDATE auth_users SET read_features='{}',revision=revision+1 WHERE id=$1`, u.ID)
	if _, e = s.List(ctx, ordinary, Query{After: "0", Limit: 100}); !errors.Is(e, auth.ErrForbidden) {
		t.Fatal("removed feature still readable")
	}
}

func TestExpiredSessionCannotEraseAnAlreadyAcceptedMailReceipt(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	in := input(config(t, s, token))
	in.Channel = "resend"
	in.From = "sender@owned.test"
	in.Recipient = "admin@owned.test"
	in.CredentialAction = "replace"
	in.Credential = "owned-private-mail-key"
	c := save(t, s, token, in)
	n := request(t, s, token, c.Revision, 1)
	expires := time.Now().Add(time.Second)
	exec(t, s.pool, `UPDATE auth_sessions SET idle_expires_at=$1`, expires)
	calls := 0
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		time.Sleep(time.Until(expires) + 25*time.Millisecond)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`))}, nil
	})
	process(t, s)
	var state string
	if e := s.pool.QueryRow(ctx, `SELECT state FROM notifications WHERE id=$1`, n.ID).Scan(&state); e != nil || state != "accepted" || calls != 1 {
		t.Fatal("expired session erased dispatched result", state, e)
	}
	if _, e := s.Get(ctx, token, n.ID); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("expired browser received protected receipt")
	}
	if process(t, s) || calls != 1 {
		t.Fatal("accepted mail was replayed")
	}
}

func TestDeliveryReceiptAuditFailureRecoversUnknownWithoutASecondSend(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	sink := newSink(t)
	c := sinkConfig(t, s, token, sink)
	n := request(t, s, token, c.Revision, 1)
	exec(t, s.pool, `CREATE FUNCTION reject_mail_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='notification.result' THEN RAISE EXCEPTION 'owned result failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_mail_result BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_mail_result()`)
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	if _, e = s.process(ctx, conn); e == nil {
		t.Fatal("receipt committed without audit")
	}
	var state string
	if e = s.pool.QueryRow(ctx, `SELECT state FROM notifications WHERE id=$1`, n.ID).Scan(&state); e != nil || state != "dispatching" || sink.count.Load() != 1 {
		t.Fatal("missing durable uncertain intent", state, e)
	}
	exec(t, s.pool, `DROP TRIGGER reject_mail_result ON management_events`)
	if e = recoverDispatches(ctx, conn); e != nil {
		t.Fatal(e)
	}
	if p, e := s.process(ctx, conn); e != nil || p || sink.count.Load() != 1 {
		t.Fatal("failed receipt audit replayed mail", e)
	}
	got, e := s.Get(ctx, token, n.ID)
	if e != nil || got.State != "unknown" {
		t.Fatal("unpersisted receipt was inferred successful", e)
	}
}

func TestWorkerLeaderFencesACompetingServiceDuringActualDATA(t *testing.T) {
	s, token := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	sink := newSink(t)
	c := sinkConfig(t, s, token, sink)
	n := request(t, s, token, c.Revision, 1)
	sink.mode.Store(3)
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); s.Run(ctx) }()
	second, e := New(s.pool, s.auth, bytes.Repeat([]byte{31}, 32), true)
	if e != nil {
		t.Fatal(e)
	}
	secondDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
		cancel()
		<-firstDone
		select {
		case <-secondDone:
		default:
		}
	})
	select {
	case <-sink.messages:
	case <-time.After(4 * time.Second):
		t.Fatal("actual DATA barrier not reached")
	}
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var leader bool
	e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, workerLock).Scan(&leader)
	conn.Release()
	if e != nil || leader {
		t.Fatal("live owner was not fenced", e)
	}
	go func() { defer close(secondDone); second.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	got, e := s.Get(ctx, token, n.ID)
	if e != nil || got.State != "dispatching" || sink.count.Load() != 1 {
		t.Fatal("competing owner recovered live DATA", got.State, e)
	}
	close(sink.release)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		got, e = s.Get(ctx, token, n.ID)
		if e != nil {
			t.Fatal(e)
		}
		if got.State == "accepted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.State != "accepted" || sink.count.Load() != 1 {
		t.Fatal("DATA receipt lost", got.State)
	}
	cancel()
	<-secondDone
}
