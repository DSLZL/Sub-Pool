package notifications

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"sub-pool/backend/internal/auth"
)

func TestFinancialDatabaseRejectsMissingCurrentIdentity(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	exec(t, s.pool, `INSERT INTO connection_sources(id,base_url,created_at) VALUES ($1,'https://owned.invalid',clock_timestamp())`, id(101))
	exec(t, s.pool, `UPDATE notification_config SET financial_enabled=true`)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := FinancialEvidence{Kind: "balance_low", SubjectID: id(102), SourceID: id(101), ConnectionRevision: "1", SubjectRevision: "1", RecordID: "1", PreviousID: "0", AccountIDs: []string{id(103)}, AsOf: time.Now().UTC().Truncate(time.Microsecond)}
	if err = RecordFinancial(ctx, tx, e, "Conditional simulated evidence."); err == nil {
		err = tx.Commit(ctx)
	}
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"connection_revision", "subject_revision", "participant_id", "financial_evidence"} {
		t.Run(column, func(t *testing.T) {
			tx, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			_, err = tx.Exec(ctx, `UPDATE notifications SET `+column+`=NULL`)
			var check *pgconn.PgError
			if !errors.As(err, &check) || check.Code != "23514" {
				t.Fatal("missing financial identity passed native constraint", column, err)
			}
		})
	}
}

type financialFixtureSource struct {
	checks    int
	invalidAt int
}

func (*financialFixtureSource) FinancialNotificationSupport(context.Context, pgx.Tx) (FinancialSupport, error) {
	return FinancialSupport{Simulation: true}, nil
}
func (f *financialFixtureSource) ValidateFinancialNotification(context.Context, pgx.Tx, FinancialEvidence) (bool, error) {
	f.checks++
	return f.invalidAt == 0 || f.checks < f.invalidAt, nil
}

func TestFinancialActivationDedupeCurrentEvidenceAndScope(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	sink := newSink(t)
	source := &financialFixtureSource{}
	s.Financial = source
	c := sinkConfig(t, s, token, sink)
	if c.FinancialEnabled || !c.Capabilities[0].Available || c.Capabilities[0].Effective {
		t.Fatal("upgrade activated notifications", c)
	}
	exec(t, s.pool, `INSERT INTO connection_sources(id,base_url,created_at) VALUES ($1,'https://owned.invalid',clock_timestamp())`, id(101))
	evidence := FinancialEvidence{Kind: "balance_low", SubjectID: id(102), SourceID: id(101), ConnectionRevision: "2", SubjectRevision: "3", RecordID: "1", PreviousID: "0", AccountIDs: []string{id(103), id(104)}, AsOf: time.Now().UTC().Truncate(time.Microsecond)}
	record := func(e FinancialEvidence) {
		t.Helper()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err = RecordFinancial(ctx, tx, e, "Complete simulated whole-wallet evidence; no financial operation."); err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	record(evidence)
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count); err != nil || count != 0 {
		t.Fatal("default-off produced outbox", count, err)
	}
	in := input(c)
	in.FinancialEnabled = true
	in.Preferences.BalanceLow = true
	c = save(t, s, token, in)
	if !c.Capabilities[0].Effective {
		t.Fatal("explicit activation not effective")
	}
	record(evidence)
	record(evidence)
	duplicate := evidence
	duplicate.RecordID = "2"
	record(duplicate)
	page, err := s.List(ctx, token, Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 2 || page.Notifications[0].State != "queued" || page.Notifications[1].Reason != "DUPLICATE_WINDOW" {
		t.Fatal("financial event dedupe", page, err)
	}
	if !process(t, s) || process(t, s) || sink.count.Load() != 1 {
		t.Fatal("financial mail duplicated")
	}
	user, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "financial.reader", Password: "OwnedTestPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"notifications"}, AccountIDs: []string{id(103)}, ParticipantIDs: []string{id(102)}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, user.Username, "OwnedTestPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.List(ctx, reader, Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 0 {
		t.Fatal("hidden whole-wallet source leaked", page, err)
	}
	exec(t, s.pool, `UPDATE auth_users SET account_ids=$2,revision=revision+1 WHERE id=$1`, user.ID, []string{id(103), id(104)})
	page, err = s.List(ctx, reader, Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 2 || page.Notifications[0].Details != nil || page.Notifications[0].Reason != "ACCEPTED" {
		t.Fatal("safe financial summary missing", page, err)
	}
	exec(t, s.pool, `UPDATE auth_users SET participant_ids='{}',revision=revision+1 WHERE id=$1`, user.ID)
	page, err = s.List(ctx, reader, Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 0 {
		t.Fatal("participant scope contraction leaked", page, err)
	}
	// A separate configuration revision creates a fresh event, but current evidence must still survive the final wait.
	c = save(t, s, token, input(c))
	evidence.RecordID = "3"
	record(evidence)
	source.checks = 0
	source.invalidAt = 2
	process(t, s)
	page, err = s.List(ctx, token, Query{After: "0", Limit: 100})
	if err != nil || page.Notifications[2].Reason != "FINANCIAL_EVENT_UNAVAILABLE" || sink.count.Load() != 1 {
		t.Fatal("stale final evidence sent", page, err)
	}
}

func TestFinancialKindsKeepFactualSMTPUnknownWithoutReplay(t *testing.T) {
	s, token := setup(t)
	ctx := context.Background()
	sink := newSink(t)
	s.Financial = &financialFixtureSource{}
	c := sinkConfig(t, s, token, sink)
	in := input(c)
	in.FinancialEnabled = true
	in.Preferences = Preferences{true, true, true, true}
	save(t, s, token, in)
	exec(t, s.pool, `INSERT INTO connection_sources(id,base_url,created_at) VALUES ($1,'https://owned.invalid',clock_timestamp())`, id(101))
	for i, kind := range []string{"balance_low", "recommendation_changed", "estimate_changed"} {
		sink.mode.Store(int32(i))
		previous := "1"
		if i == 0 {
			previous = "0"
		}
		e := FinancialEvidence{Kind: kind, SubjectID: id(103 + i), SourceID: id(101), ConnectionRevision: "2", SubjectRevision: "1", RecordID: "2", PreviousID: previous, AccountIDs: []string{id(103 + i)}, AsOf: time.Now().UTC().Truncate(time.Microsecond)}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = RecordFinancial(ctx, tx, e, "Conditional simulated evidence."); err == nil {
			err = tx.Commit(ctx)
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !process(t, s) {
			t.Fatal("event not processed")
		}
	}
	page, err := s.List(ctx, token, Query{After: "0", Limit: 100})
	if err != nil || len(page.Notifications) != 3 {
		t.Fatal(page, err)
	}
	for i, state := range []string{"accepted", "failed", "unknown"} {
		if page.Notifications[i].State != state {
			t.Fatal("fabricated financial delivery outcome", page.Notifications[i])
		}
	}
	if process(t, s) || sink.count.Load() != 3 {
		t.Fatal("financial transport was replayed")
	}
}
