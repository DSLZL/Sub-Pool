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

	"sub-pool/backend/internal/auth"
)

func TestObservationUsageBoundsRequireActualNativeMetadata(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 0, 0, 123456789, time.UTC)
	reset, duration := at.Add(time.Hour), "10800"
	snapshot := QuotaSnapshot{SourceAt: &at, ReceivedAt: at.Add(time.Nanosecond)}
	window := QuotaWindow{ResetAt: &reset, DurationSeconds: &duration}
	from, to, valid := observationUsageBounds(snapshot, window)
	if !valid || !to.Equal(at) || !from.Equal(reset.Add(-3*time.Hour)) {
		t.Fatal("native time precision/duration lost", from, to, valid)
	}
	for _, value := range []string{"0", "-1", "not-seconds", "9223372036854775807", "2678401"} {
		changed := window
		changed.DurationSeconds = &value
		if _, _, valid := observationUsageBounds(snapshot, changed); valid {
			t.Fatal("invalid or unsupported window accepted", value)
		}
	}
	for _, changed := range []QuotaSnapshot{{ReceivedAt: at}, {SourceAt: &at, ReceivedAt: at.Add(-time.Nanosecond)}, {SourceAt: &reset, ReceivedAt: reset}} {
		if _, _, valid := observationUsageBounds(changed, window); valid {
			t.Fatal("missing, future or reset-boundary source accepted")
		}
	}
}

func TestObservationUsageEvidenceScopesExactNativeIntervalWithoutAddingCaptures(t *testing.T) {
	s, db, token, c, calls := observationFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	p := createParticipant(t, s, token, c, 21, "Visible member")
	q := createParticipant(t, s, token, c, 22, "Hidden member")
	fact := collectObservation(t, s, db, token, c, a)
	start, end, ok := observationUsageBounds(fact.Snapshot, fact.Snapshot.Windows[0])
	if !ok {
		t.Fatal("actual native fixture has no period")
	}
	if v, e := s.GetObservationUsageEvidence(ctx, token, fact.ID); e != nil || v.Windows[0].Capture != nil || v.Windows[0].Reason != "NO_RECORDED_WINDOW_CAPTURE" || v.Windows[1].Reason != "NATIVE_WINDOW_UNVERIFIED" {
		t.Fatal("missing evidence became an amount", v, e)
	}
	insertCapture := func(from, to time.Time, connection string) string {
		t.Helper()
		capture := UsageCapture{AccountID: a.ID, SourceID: a.SourceID, ConnectionRevision: connection, AccountRevision: a.Revision, StartAt: from, EndAt: to, CapturedAt: time.Now().UTC(), State: "partial"}
		body, _ := json.Marshal(capture)
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO usage_captures(account_id,actor_id,captured_at,snapshot) SELECT $1,id,now(),$2 FROM auth_users WHERE role='admin' RETURNING usage_captures.id::text`, a.ID, body).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec(t, db, `ALTER TABLE usage_captures ALTER COLUMN id RESTART WITH 9`)
	for _, want := range []string{"9", "10"} {
		id := insertCapture(start.Add(-time.Nanosecond), end.Add(time.Nanosecond), c.Revision)
		if id != want {
			t.Fatal("capture fixture lost numeric ordering", id)
		}
		for i, row := range []struct {
			at     time.Time
			cost   string
			member *Participant
		}{{start.Add(-time.Nanosecond), "100", nil}, {start, "1", nil}, {end.Add(-time.Nanosecond), "2", &p}, {end, "100", &p}, {end.Add(-2 * time.Nanosecond), "3", &q}, {end.Add(-3 * time.Nanosecond), "0", &p}} {
			metrics := map[string]*string{}
			for _, name := range usageMeasures {
				metrics[name] = nil
			}
			metrics["total_cost"] = &row.cost
			body, _ := json.Marshal(UsageFact{ID: fmt.Sprint(i + 1), CreatedAt: row.at, Metrics: metrics})
			exec(t, db, `INSERT INTO usage_capture_facts(capture_id,upstream_id,fact) VALUES ($1,$2,$3)`, id, i+1, body)
			if row.member == nil {
				exec(t, db, `INSERT INTO usage_attributions(capture_id,upstream_id,basis) VALUES ($1,$2,'missing_user_id')`, id, i+1)
			} else {
				exec(t, db, `INSERT INTO usage_attributions(capture_id,upstream_id,participant_id,binding_id,participant_name,basis) VALUES ($1,$2,$3,$4,$5,'local_binding_at_log_time')`, id, i+1, row.member.ID, row.member.Binding.ID, row.member.Name)
			}
		}
	}
	insertCapture(start, end, "999")
	insertCapture(start, end.Add(-time.Nanosecond), c.Revision)
	insertCapture(start.Add(time.Nanosecond), end, c.Revision)
	before := calls.Load()
	var original string
	if err := db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM quota_observations q),(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM usage_captures c))::text`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for index, test := range []struct {
		features []string
		accounts []string
		members  []string
		cost     string
		count    string
		want     error
	}{
		{[]string{"observations"}, []string{a.ID}, []string{p.ID}, "", "", auth.ErrForbidden},
		{[]string{"usage"}, []string{a.ID}, []string{p.ID}, "", "", auth.ErrForbidden},
		{[]string{"observations", "usage"}, []string{}, []string{p.ID}, "", "", auth.ErrNotFound},
		{[]string{"observations", "usage"}, []string{a.ID}, []string{p.ID}, "2", "2", nil},
		{[]string{"observations", "usage"}, []string{a.ID}, []string{q.ID}, "3", "1", nil},
		{[]string{"observations", "usage"}, []string{a.ID}, []string{}, "", "", nil},
	} {
		name := fmt.Sprintf("usage-observer-%d", index)
		if _, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: name, Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: test.features, AccountIDs: test.accounts, ParticipantIDs: test.members}}); err != nil {
			t.Fatal(err)
		}
		_, reader, err := s.auth.Login(ctx, name, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
		if err != nil {
			t.Fatal(err)
		}
		v, err := s.GetObservationUsageEvidence(ctx, reader, fact.ID)
		if !errors.Is(err, test.want) {
			t.Fatal("features/account scope escaped", v, err, test.want)
		}
		if err != nil {
			if v.ObservationID != "" {
				t.Fatal("denial exposed protected data", v)
			}
			continue
		}
		if v.Scope != "authorized_participants" || v.Complete || v.SettlementEligible {
			t.Fatal("ordinary evidence claimed whole-account coverage", v)
		}
		if test.cost == "" {
			if v.Windows[0].Capture != nil {
				t.Fatal("no member scope exposed a capture", v)
			}
		} else if u := v.Windows[0].Capture; u == nil || u.ID != "10" || u.Summary.RequestCount != test.count || u.Summary.Metrics["total_cost"] == nil || *u.Summary.Metrics["total_cost"] != test.cost || u.Summary.Metrics["actual_cost"] != nil {
			t.Fatal("hidden/unassigned facts, nanosecond boundary or overlapping capture affected cost", v)
		}
	}
	v, err := s.GetObservationUsageEvidence(ctx, token, fact.ID)
	if err != nil || v.Scope != "account" || v.Windows[0].Capture == nil || v.Windows[0].Capture.ID != "10" || *v.Windows[0].Capture.Summary.Metrics["total_cost"] != "6" || v.Windows[0].Capture.Summary.RequestCount != "4" {
		t.Fatal("account evidence did not retain one exact capture", v, err)
	}
	replacement, err := New(db, auth.New(db), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.GetObservationUsageEvidence(ctx, token, fact.ID); err != nil {
		t.Fatal(err)
	}
	var after string
	if err = db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM quota_observations q),(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM usage_captures c))::text`).Scan(&after); err != nil || original != after || calls.Load() != before {
		t.Fatal("evidence read changed facts/audit or contacted upstream", err)
	}
}

func TestObservationUsageEvidenceRejectsExpiryAfterActualQueryWait(t *testing.T) {
	for _, table := range []string{"quota_observations", "usage_captures"} {
		t.Run(table, func(t *testing.T) {
			s, db, token, c, _ := observationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a := passiveManaged(t, s, token, c, 1)
			fact := collectObservation(t, s, db, token, c, a)
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
				v, e := s.GetObservationUsageEvidence(ctx, token, fact.ID)
				if v.ObservationID != "" {
					e = errors.New("expired evidence read exposed protected data")
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
				t.Fatal("query wait retained expired authority", err)
			}
		})
	}
}
