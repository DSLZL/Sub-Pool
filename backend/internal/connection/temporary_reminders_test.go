package connection

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/notifications"
)

func temporarySMTP(t *testing.T) (int, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Int32)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.WriteString(conn, "220 owned temporary SMTP\r\n")
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						_, _ = io.WriteString(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "DATA"):
						_, _ = io.WriteString(conn, "354 continue\r\n")
						for {
							line, err = reader.ReadString('\n')
							if err != nil {
								return
							}
							if line == ".\r\n" {
								break
							}
						}
						count.Add(1)
						_, _ = io.WriteString(conn, "250 accepted\r\n")
					default:
						return
					}
				}
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	value, _ := strconv.Atoi(port)
	return value, count
}

func TestTemporaryReminderDefaultDedupeAndDispatchStopFence(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint("stopped-", stop), func(t *testing.T) {
			f := temporaryFixture(t)
			ctx := context.Background()
			r, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
			if err != nil {
				t.Fatal(err)
			}
			port, count := temporarySMTP(t)
			mail, err := notifications.New(f.db, f.s.auth, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			c, err := mail.GetConfiguration(ctx, f.token)
			if err != nil {
				t.Fatal(err)
			}
			_, err = mail.SaveConfiguration(ctx, f.token, notifications.SaveInput{Revision: c.Revision, Enabled: true, Channel: "smtp", From: "sender@owned.test", Recipient: "admin@owned.test", SMTP: notifications.SMTP{Host: "127.0.0.1", Port: port, TLS: "loopback_plain"}, Preferences: c.Preferences, DedupeSeconds: c.DedupeSeconds, CredentialAction: "keep"})
			if err != nil {
				t.Fatal(err)
			}
			// Crossing decimal digit widths must not make ORDER BY use an id::text output alias.
			exec(t, f.db, "ALTER SEQUENCE quota_observations_id_seq RESTART WITH 100")
			for _, account := range r.Scope.Accounts {
				var raw []byte
				if err = f.db.QueryRow(ctx, "SELECT snapshot FROM quota_observations WHERE account_id=$1 ORDER BY id DESC LIMIT 1", account.AccountID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var snapshot QuotaSnapshot
				if err = json.Unmarshal(raw, &snapshot); err != nil {
					t.Fatal(err)
				}
				at := time.Now().UTC()
				snapshot.SourceAt = &at
				snapshot.ReceivedAt = at
				for i := range snapshot.Windows {
					used := "30"
					snapshot.Windows[i].UsedPercent = &used
				}
				raw, _ = json.Marshal(snapshot)
				exec(t, f.db, `UPDATE quota_observations SET snapshot=jsonb_set(jsonb_set(snapshot,'{windows,0,used_percent}','"0"'),'{windows,1,used_percent}','"0"') WHERE account_id=$1`, account.AccountID)
				var run, job string
				if err = f.db.QueryRow(ctx, "INSERT INTO collection_runs(reason,selected,created_at) VALUES('scheduled',false,clock_timestamp()) RETURNING id::text").Scan(&run); err != nil {
					t.Fatal(err)
				}
				if err = f.db.QueryRow(ctx, `INSERT INTO collection_jobs(run_id,account_id,source_id,connection_revision,account_revision,policy_revision,source_mode,state,reason,queued_at,finished_at) VALUES($1,$2,$3,$4,$5,$6,'active','succeeded','OWNED_CURRENT_QUOTA',clock_timestamp(),clock_timestamp()) RETURNING id::text`, run, account.AccountID, r.Scope.SourceID, r.Scope.ConnectionRevision, account.AccountRevision, r.Scope.PolicyRevision).Scan(&job); err != nil {
					t.Fatal(err)
				}
				exec(t, f.db, "INSERT INTO quota_observations(job_id,account_id,received_at,snapshot) VALUES($1,$2,$3,$4)", job, account.AccountID, at, raw)
			}
			if err = f.s.processTemporary(ctx); err != nil {
				t.Fatal(err)
			}
			var rows int
			if err = f.db.QueryRow(ctx, "SELECT count(*) FROM notifications").Scan(&rows); err != nil || rows != 0 {
				t.Fatal("reminders enabled by default", rows, err)
			}
			control, err := f.s.ConfigureTemporaryReminders(ctx, f.token, TemporaryRemindersInput{RoundID: r.ID, Revision: "0", Enabled: true, Threshold: "25"})
			if err != nil || control.Revision == "0" {
				t.Fatal(control, err)
			}
			for i := 0; i < 2; i++ {
				if err = f.s.processTemporary(ctx); err != nil {
					t.Fatal(err)
				}
			}
			want := len(r.Scope.Accounts) * len(r.Scope.Wallets)
			if err = f.db.QueryRow(ctx, "SELECT count(*) FROM notifications WHERE state='queued'").Scan(&rows); err != nil || rows != want {
				t.Fatal("fresh event missing or duplicated", rows, want, err)
			}
			if stop {
				if _, err = f.s.StopTemporary(ctx, f.token, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { mail.Run(runCtx); close(done) }()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(4 * time.Second)
			for {
				if err = f.db.QueryRow(ctx, "SELECT count(*) FROM notifications WHERE state IN ('queued','dispatching')").Scan(&rows); err != nil {
					t.Fatal(err)
				}
				if rows == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("temporary notices did not reach an honest terminal state")
				}
				time.Sleep(10 * time.Millisecond)
			}
			state, reason := "accepted", "TRANSPORT_ACCEPTED"
			if stop {
				state, reason = "skipped", "TEMPORARY_EVENT_UNAVAILABLE"
			}
			if err = f.db.QueryRow(ctx, "SELECT count(*) FROM notifications WHERE state=$1 AND reason=$2", state, reason).Scan(&rows); err != nil || rows != want {
				t.Fatal("dispatch fence or receipt failed", rows, want, err)
			}
			expected := int32(want)
			if stop {
				expected = 0
			}
			if count.Load() != expected {
				t.Fatal("transport acceptance count", count.Load(), expected)
			}
			if f.writes.Load() != 0 {
				t.Fatal("reminders changed balances")
			}
		})
	}
}
