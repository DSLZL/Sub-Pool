package connection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

func dailyFixtureReceipt(t *testing.T, s *Service, db *pgxpool.Pool, token string, c Configuration, a ManagedAccount, received string, known bool) ObservationRecord {
	t.Helper()
	item := collectObservation(t, s, db, token, c, a)
	at, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		t.Fatal(err)
	}
	item.Snapshot.ReceivedAt = at
	source, reset, duration := at.Add(-time.Second), at.Add(time.Hour), "604800"
	item.Snapshot.SourceAt = nil
	if known {
		item.Snapshot.SourceAt = &source
	}
	item.Snapshot.Windows[1].ResetAt, item.Snapshot.Windows[1].DurationSeconds = &reset, &duration
	body, _ := json.Marshal(item.Snapshot)
	exec(t, db, `UPDATE quota_observations SET received_at=$2,snapshot=$3 WHERE id=$1`, item.ID, at, body)
	return item
}

func dailyFixtureQuery(t *testing.T, db *pgxpool.Pool, account, start, end string) DailyObservationQuery {
	t.Helper()
	input := DailyObservationQuery{AccountID: account, StartAt: start, EndAt: end}
	if err := db.QueryRow(context.Background(), `SELECT revision::text FROM system_settings`).Scan(&input.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	return input
}

func TestDailyObservationHistorySelectsNumericLatestAndScopedNativeCosts(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	p := createParticipant(t, s, token, c, 21, "Visible member")
	q := createParticipant(t, s, token, c, 22, "Hidden member")
	exec(t, db, `UPDATE system_settings SET timezone='UTC'`)
	if empty, e := s.GetDailyObservationHistory(ctx, token, dailyFixtureQuery(t, db, a.ID, "2026-11-11T00:00:00Z", "2026-11-14T00:00:00Z")); e != nil || len(empty.Records) != 0 || empty.Current != nil {
		t.Fatal("no retained observation became a current result", empty, e)
	}
	exec(t, db, `ALTER TABLE quota_observations ALTER COLUMN id RESTART WITH 9`)
	first := dailyFixtureReceipt(t, s, db, token, c, a, "2026-11-11T23:59:59.123456Z", true)
	last := dailyFixtureReceipt(t, s, db, token, c, a, "2026-11-11T23:59:59.123456Z", true)
	unknown := dailyFixtureReceipt(t, s, db, token, c, a, "2026-11-13T00:00:00Z", false)
	if first.ID != "9" || last.ID != "10" {
		t.Fatal("numeric tie fixture not established", first.ID, last.ID)
	}
	if _, err := s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: unknown.ID, Excluded: true, Reason: "Synthetic excluded receipt"}); err != nil {
		t.Fatal(err)
	}
	start, end, valid := observationUsageBounds(last.Snapshot, last.Snapshot.Windows[1])
	if !valid {
		t.Fatal("synthetic actual-window metadata is invalid")
	}
	exec(t, db, `ALTER TABLE usage_captures ALTER COLUMN id RESTART WITH 9`)
	for _, expected := range []string{"9", "10"} {
		capture := UsageCapture{AccountID: a.ID, SourceID: a.SourceID, ConnectionRevision: c.Revision, AccountRevision: a.Revision, StartAt: start, EndAt: end, CapturedAt: time.Now().UTC(), State: "partial"}
		body, _ := json.Marshal(capture)
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) SELECT $1,id,now(),$2 FROM auth_users WHERE role='admin' RETURNING usage_captures.id::text`, a.ID, body).Scan(&id); err != nil || id != expected {
			t.Fatal("capture fixture failed", id, err)
		}
		for index, row := range []struct {
			cost   string
			member *Participant
		}{{"0.123456789123456789", &p}, {"7", &q}, {"3", nil}} {
			fact, _ := json.Marshal(UsageFact{ID: fmt.Sprint(index + 1), CreatedAt: end.Add(-time.Nanosecond), Metrics: map[string]*string{"total_cost": &row.cost}})
			exec(t, db, `INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) VALUES ($1,$2,$3)`, id, index+1, fact)
			if row.member == nil {
				exec(t, db, `INSERT INTO usage_attributions(capture_id,upstream_id,basis) VALUES ($1,$2,'missing_user_id')`, id, index+1)
			} else {
				exec(t, db, `INSERT INTO usage_attributions(capture_id,upstream_id,participant_id,binding_id,participant_name,basis) VALUES ($1,$2,$3,$4,$5,'local_binding_at_log_time')`, id, index+1, row.member.ID, row.member.Binding.ID, row.member.Name)
			}
		}
	}
	input := dailyFixtureQuery(t, db, a.ID, "2026-11-11T00:00:00Z", "2026-11-14T00:00:00Z")
	before := calls.Load()
	v, err := s.GetDailyObservationHistory(ctx, token, input)
	if err != nil || len(v.Records) != 2 || v.Records[0].Day != "2026-11-11" || v.Records[0].ReceiptCount != "2" || v.Records[0].Observation.ID != "10" || v.Records[1].Day != "2026-11-13" || !v.Records[1].Observation.Excluded {
		t.Fatal("daily selection, missing dates or review state lost", v, err)
	}
	if v.RulesVersion != "recorded-day-last-v1" || v.TimeBasis != "received_at" || v.Timezone != "UTC" || v.Complete || v.SettlementEligible || v.Records[1].Usage.Windows[1].Reason != "NATIVE_WINDOW_UNVERIFIED" {
		t.Fatal("receipt became a financial close or invented a source", v)
	}
	if v.Current == nil || v.Current.Observation.ID != unknown.ID || !v.Current.Observation.Excluded || v.Current.Usage.Windows[1].Reason != "NATIVE_WINDOW_UNVERIFIED" {
		t.Fatal("unknown excluded latest observation fell back to earlier known evidence", v.Current)
	}
	usage := v.Records[0].Usage.Windows[1].Capture
	if usage == nil || usage.ID != "10" || usage.Summary.RequestCount != "3" || usage.Summary.Metrics["total_cost"] == nil || *usage.Summary.Metrics["total_cost"] != "10.123456789123456789" || usage.Summary.Metrics["actual_cost"] != nil {
		t.Fatal("overlap, decimal or independent unknown cost lost", v)
	}
	knownLatest := last.Snapshot
	knownLatest.ReceivedAt = unknown.Snapshot.ReceivedAt
	body, _ := json.Marshal(knownLatest)
	exec(t, db, `UPDATE quota_observations SET snapshot=$2 WHERE id=$1`, unknown.ID, body)
	for index, test := range []struct {
		features []string
		accounts []string
		members  []string
		cost     string
		want     error
	}{
		{[]string{"usage"}, []string{a.ID}, []string{p.ID}, "", auth.ErrForbidden},
		{[]string{"observations"}, []string{a.ID}, []string{p.ID}, "", auth.ErrForbidden},
		{[]string{"usage", "observations"}, []string{}, []string{p.ID}, "", auth.ErrNotFound},
		{[]string{"usage", "observations"}, []string{a.ID}, []string{p.ID}, "0.123456789123456789", nil},
		{[]string{"usage", "observations"}, []string{a.ID}, []string{q.ID}, "7", nil},
		{[]string{"usage", "observations"}, []string{a.ID}, []string{}, "", nil},
	} {
		name := fmt.Sprintf("daily-reader-%d", index)
		if _, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: name, Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: test.features, AccountIDs: test.accounts, ParticipantIDs: test.members}}); err != nil {
			t.Fatal(err)
		}
		_, reader, err := s.auth.Login(ctx, name, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDailyObservationHistory(ctx, reader, input)
		if !errors.Is(err, test.want) {
			t.Fatal("feature/account scope escaped", got, err)
		}
		if err != nil {
			if got.AccountID != "" {
				t.Fatal("denied daily read exposed protected result")
			}
			continue
		}
		if got.Current == nil || got.Current.Observation.ID != unknown.ID {
			t.Fatal("current observation reference lost", got.Current)
		}
		for _, u := range []ObservationUsageEvidence{got.Records[0].Usage, got.Current.Usage} {
			if u.Scope != "authorized_participants" || u.Complete || u.SettlementEligible {
				t.Fatal("ordinary result claimed whole-account coverage", u)
			}
			capture := u.Windows[1].Capture
			if test.cost == "" {
				if capture != nil {
					t.Fatal("no participant scope exposed a capture", capture)
				}
			} else if capture == nil || capture.Summary.Metrics["total_cost"] == nil || *capture.Summary.Metrics["total_cost"] != test.cost {
				t.Fatal("hidden participant or global remainder leaked", capture)
			}
		}
	}
	precise := input
	precise.StartAt, precise.EndAt = "2026-11-11T23:59:59.123456001Z", "2026-11-11T23:59:59.123456999Z"
	if got, e := s.GetDailyObservationHistory(ctx, token, precise); e != nil || len(got.Records) != 0 || got.Current == nil || got.Current.Observation.ID != unknown.ID {
		t.Fatal("half-open nanosecond receipt bounds changed", got, e)
	}
	stale := input
	stale.SettingsRevision = "999"
	if got, e := s.GetDailyObservationHistory(ctx, token, stale); !errors.Is(e, auth.ErrConflict) || got.AccountID != "" {
		t.Fatal("stale calendar accepted", got, e)
	}
	replacement, err := New(db, auth.New(db), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, e := replacement.GetDailyObservationHistory(ctx, token, input); e != nil || len(got.Records) != 2 || got.Records[0].Observation.ID != last.ID || got.Current == nil || got.Current.Observation.ID != unknown.ID || calls.Load() != before {
		t.Fatal("reconstruction changed retained projection or read contacted native", got, e)
	}
}

func TestDailyObservationHistoryCurrentIsNumericLatestOutsideReceiptRange(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	exec(t, db, `UPDATE system_settings SET timezone='UTC'`)
	first := dailyFixtureReceipt(t, s, db, token, c, a, "2026-11-13T12:00:00Z", true)
	latest := dailyFixtureReceipt(t, s, db, token, c, a, "2026-11-11T12:00:00Z", false)
	if _, err := s.ReviewObservation(ctx, token, ObservationReviewInput{AccountID: a.ID, Revision: "1", ObservationID: latest.ID, Excluded: true, Reason: "Synthetic latest ID has an older receipt"}); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	v, err := s.GetDailyObservationHistory(ctx, token, dailyFixtureQuery(t, db, a.ID, "2026-11-13T00:00:00Z", "2026-11-14T00:00:00Z"))
	if err != nil || len(v.Records) != 1 || v.Records[0].Observation.ID != first.ID || v.Current == nil || v.Current.Observation.ID != latest.ID || !v.Current.Observation.Excluded || v.Current.Usage.Windows[1].Capture != nil || v.Current.Usage.Windows[1].Reason != "NATIVE_WINDOW_UNVERIFIED" || calls.Load() != before {
		t.Fatal("current used the receipt range, latest date or a known older observation", v, err)
	}
}

func TestDailyObservationHistoryUsesSavedCalendarAcrossRepeatedAndSkippedDates(t *testing.T) {
	for _, test := range []struct {
		zone, start, end string
		receipts         []string
		days, counts     []string
	}{
		{"America/Goose_Bay", "1988-10-30T01:00:00Z", "1988-10-30T05:00:00Z", []string{"1988-10-30T01:59:59Z", "1988-10-30T02:00:30Z", "1988-10-30T02:30:00Z", "1988-10-30T04:30:00Z"}, []string{"1988-10-29", "1988-10-30"}, []string{"2", "2"}},
		{"Pacific/Apia", "2011-12-30T09:00:00Z", "2011-12-30T11:00:00Z", []string{"2011-12-30T09:59:59Z", "2011-12-30T10:00:00Z"}, []string{"2011-12-29", "2011-12-31"}, []string{"1", "1"}},
	} {
		t.Run(test.zone, func(t *testing.T) {
			s, db, token, c, _ := observationFixture(t)
			a := passiveManaged(t, s, token, c, 1)
			exec(t, db, `UPDATE system_settings SET timezone=$1,revision=revision+1`, test.zone)
			for _, at := range test.receipts {
				dailyFixtureReceipt(t, s, db, token, c, a, at, false)
			}
			got, err := s.GetDailyObservationHistory(context.Background(), token, dailyFixtureQuery(t, db, a.ID, test.start, test.end))
			if err != nil || len(got.Records) != len(test.days) {
				t.Fatal("calendar projection failed", got, err)
			}
			for i, row := range got.Records {
				if row.Day != test.days[i] || row.ReceiptCount != test.counts[i] {
					t.Fatal("repeated date lost or skipped date invented", got)
				}
			}
		})
	}
}

func TestDailyObservationHistoryRejectsExpiryAfterActualQueryWait(t *testing.T) {
	for _, table := range []string{"quota_observations", "usage_captures"} {
		t.Run(table, func(t *testing.T) {
			s, db, token, c, _ := observationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a := passiveManaged(t, s, token, c, 1)
			item := collectObservation(t, s, db, token, c, a)
			input := dailyFixtureQuery(t, db, a.ID, item.Snapshot.ReceivedAt.Add(-time.Second).Format(time.RFC3339Nano), item.Snapshot.ReceivedAt.Add(time.Second).Format(time.RFC3339Nano))
			if table == "usage_captures" {
				input.StartAt, input.EndAt = item.Snapshot.ReceivedAt.Add(time.Hour).Format(time.RFC3339Nano), item.Snapshot.ReceivedAt.Add(2*time.Hour).Format(time.RFC3339Nano)
			}
			barrier, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			if _, err = barrier.Exec(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			digest := sha256.Sum256([]byte(token))
			exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
			done := make(chan error, 1)
			go func() {
				value, e := s.GetDailyObservationHistory(ctx, token, input)
				if value.AccountID != "" {
					e = errors.New("expired daily read exposed protected result")
				}
				done <- e
			}()
			awaitMaintenanceLock(t, db, table)
			if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
				time.Sleep(delay)
			}
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatal("daily query retained expired authority", err)
			}
		})
	}
}
