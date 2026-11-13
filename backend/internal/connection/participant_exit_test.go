package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

func TestParticipantExitReleasesAllSharesAndPreservesHistoricalIdentityAndCosts(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a, b := adoptManaged(t, s, token, c, 1, "Root one"), adoptManaged(t, s, token, c, 2, "Root two")
	p, q := createParticipant(t, s, token, c, 21, "Leaving member"), createParticipant(t, s, token, c, 22, "Staying member")
	first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p, q}, []string{"33.333333", "20"})})
	second := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{b}, []Participant{p}, []string{"100"})})
	second, err := s.DisablePool(ctx, token, second.PoolID, second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	row := usageRow(1, first.EffectiveAt.Add(time.Microsecond).Format(time.RFC3339Nano))
	row["user_id"] = 21
	f.logs[1] = []map[string]any{row}
	input := UsageCaptureInput{AccountReadInput: readInput(a, c), StartAt: first.EffectiveAt.Add(-time.Minute).Format(time.RFC3339Nano), EndAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	capture, err := s.CaptureUsage(ctx, token, input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetPoolUsageEvidence(ctx, token, capture.ID, "0", 100)
	if err != nil || len(before.Records) != 1 || before.Records[0].ContractID == nil {
		t.Fatal("missing captured agreement", err)
	}
	calls := f.calls.Load()
	out, err := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name)
	if err != nil || out.Revision != "2" || out.BindingID != p.Binding.ID || len(out.PoolContractIDs) != 2 {
		t.Fatal("exit did not retain all receipts", out, err)
	}
	for _, old := range []PoolContract{first, second} {
		now, e := s.GetPool(ctx, token, old.PoolID)
		if e != nil || !now.EffectiveAt.Equal(out.EffectiveAt) || now.Enabled != old.Enabled || len(now.Accounts) != len(old.Accounts) {
			t.Fatal("exit changed pool ownership/state", e)
		}
		for _, member := range now.Participants {
			if member.ID == p.ID {
				t.Fatal("current share not released")
			}
		}
		want := "100"
		if old.PoolID == first.PoolID {
			want = "80"
			if len(now.Participants) != 1 || now.Participants[0].SharePercent != "20" {
				t.Fatal("another share was normalized")
			}
		}
		if *now.UnallocatedPercent != want {
			t.Fatal("released fraction was lost", *now.UnallocatedPercent)
		}
		retained, e := s.GetPoolContract(ctx, token, old.ID)
		if e != nil || retained.EndedAt == nil || !retained.EndedAt.Equal(out.EffectiveAt) || len(retained.Participants) != len(old.Participants) {
			t.Fatal("old contract was rewritten", e)
		}
		for i, member := range retained.Participants {
			if member.SharePercent != old.Participants[i].SharePercent || member.BindingID != old.Participants[i].BindingID {
				t.Fatal("old share/binding changed")
			}
		}
	}
	current, err := s.GetParticipant(ctx, token, p.ID)
	if err != nil || current.Enabled || !reflect.DeepEqual(current.Binding, p.Binding) {
		t.Fatal("exit erased or released native identity", err)
	}
	loaded, err := s.GetParticipantExit(ctx, token, p.ID, p.Revision)
	if err != nil || !reflect.DeepEqual(loaded, out) {
		t.Fatal("exit receipt not durable", err)
	}
	after, err := s.GetPoolUsageEvidence(ctx, token, capture.ID, "0", 100)
	if err != nil || !reflect.DeepEqual(before, after) || f.calls.Load() != calls {
		t.Fatal("exit changed costs or contacted native", err)
	}
	if _, err = s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale exit replayed", err)
	}
	if err = s.DeleteParticipant(ctx, token, p.ID, current.Revision, p.Name); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("historical identity deleted", err)
	}
	var events int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE action IN ('participant.exit','pool.participant.exit')`).Scan(&events); err != nil || events != 3 {
		t.Fatal("atomic audit incomplete", events, err)
	}
	exec(t, db, `UPDATE auth_users SET role='user' WHERE role='admin'`)
	if _, err = s.GetParticipantExit(ctx, token, p.ID, p.Revision); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary user read global exit receipt", err)
	}
	if _, err = s.ExitParticipant(ctx, token, q.ID, q.Revision, q.Name); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ordinary user dispatched exit", err)
	}
}

func TestParticipantExitDisabledMemberCanReleaseOfflineHistoricalSource(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Root")
	p := createParticipant(t, s, token, c, 21, "Member")
	first := savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"99.999999"})})
	edit := participantEdit(p, c)
	edit.Enabled = false
	updated, err := s.SaveParticipant(ctx, token, edit)
	if err != nil || updated.Participant == nil {
		t.Fatal(err)
	}
	p = *updated.Participant
	save(t, s, token, "http://127.0.0.1:1")
	calls := f.calls.Load()
	out, err := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name)
	if err != nil || len(out.PoolContractIDs) != 1 || f.calls.Load() != calls {
		t.Fatal("offline exit required native capabilities", err)
	}
	now, err := s.GetPool(ctx, token, first.PoolID)
	if err != nil || now.SourceCurrent || len(now.Participants) != 0 || *now.UnallocatedPercent != "100" {
		t.Fatal("historical source share not released", err)
	}
}

func TestParticipantExitRejectsUnknownMoneyButAllowsKnownUndispatchedRejections(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, f := poolUpstream(t)
	c := save(t, s, token, server.URL)
	p := createParticipant(t, s, token, c, 21, "Member")
	exec(t, db, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot)
		SELECT id,'AAAAAAAAAAAAAAAAAAAAAA',$3,$1,$2,now(),'{}'::jsonb FROM auth_users WHERE role='admin'`, p.ID, p.Binding.ID, strings.Repeat("a", 64))
	calls := f.calls.Load()
	for _, body := range []string{`{}`, `{"state":"unknown","dispatch_state":"dispatched"}`, `{"state":"rejected","dispatch_state":"dispatched"}`, `{"state":"rejected"}`} {
		exec(t, db, `UPDATE wallet_application_requests SET snapshot=$1`, body)
		if _, err := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name); !errors.Is(err, errParticipantExitBlocked) {
			t.Fatal("unsettled request allowed exit", err)
		}
		rebind := participantEdit(p, c)
		rebind.UpstreamID = "22"
		if _, err := s.SaveParticipant(ctx, token, rebind); !errors.Is(err, errParticipantExitBlocked) {
			t.Fatal("obligation allowed rebind", err)
		}
		if err := s.DeleteParticipant(ctx, token, p.ID, p.Revision, p.Name); !errors.Is(err, errParticipantExitBlocked) {
			t.Fatal("obligation allowed delete", err)
		}
	}
	exec(t, db, `UPDATE wallet_application_requests SET snapshot='{"state":"rejected","dispatch_state":"not_dispatched"}'`)
	if _, err := s.ExitParticipant(ctx, token, p.ID, p.Revision, "wrong name"); !errors.Is(err, auth.ErrInput) {
		t.Fatal("wrong confirmation accepted", err)
	}
	if _, err := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name); err != nil || f.calls.Load() != calls {
		t.Fatal("undispatched rejection blocked or native changed", err)
	}
}

func TestParticipantExitAuditFailureRollsBackEveryPoolAndParticipant(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, _ := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a, b := adoptManaged(t, s, token, c, 1, "One"), adoptManaged(t, s, token, c, 2, "Two")
	p := createParticipant(t, s, token, c, 21, "Member")
	for _, a := range []ManagedAccount{a, b} {
		savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"25"})})
	}
	for _, action := range []string{"participant.exit", "pool.participant.exit"} {
		exec(t, db, `ALTER TABLE management_events ADD CONSTRAINT deny_exit_audit CHECK(action <> '`+action+`')`)
		if _, err := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name); err == nil {
			t.Fatal("audit failure accepted")
		}
		current, err := s.GetParticipant(ctx, token, p.ID)
		if err != nil || current.Revision != p.Revision || !current.Enabled {
			t.Fatal("partial participant change", err)
		}
		var count int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM pool_contracts`).Scan(&count); err != nil || count != 2 {
			t.Fatal("partial pool changes", count, err)
		}
		if _, err = s.GetParticipantExit(ctx, token, p.ID, p.Revision); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("failed exit left receipt", err)
		}
		exec(t, db, `ALTER TABLE management_events DROP CONSTRAINT deny_exit_audit`)
	}
}

func TestParticipantExitSerializesNewPoolMembershipInBothOrders(t *testing.T) {
	for _, first := range []string{"exit", "pool"} {
		t.Run(first, func(t *testing.T) {
			s, db, token := setup(t)
			ctx := context.Background()
			server, _ := poolUpstream(t)
			c := save(t, s, token, server.URL)
			a := adoptManaged(t, s, token, c, 1, "Root")
			p := createParticipant(t, s, token, c, 21, "Member")
			barrier, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(ctx)
			table, pattern := "participant_events", "INSERT INTO participant_events%"
			if first == "pool" {
				table, pattern = "pool_events", "INSERT INTO pool_events%"
			}
			if _, err = barrier.Exec(ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			exited, pooled := make(chan error, 1), make(chan error, 1)
			exit := func() { _, e := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name); exited <- e }
			pool := func() {
				_, e := s.SavePool(ctx, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"75"})})
				pooled <- e
			}
			if first == "exit" {
				go exit()
			} else {
				go pool()
			}
			observePoolWait(t, db, pattern)
			if first == "exit" {
				go pool()
			} else {
				go exit()
			}
			observePoolWait(t, db, "SELECT c.revision,c.source_id%FOR % OF c")
			if err = barrier.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-exited; err != nil {
				t.Fatal("exit failed", err)
			}
			err = <-pooled
			if first == "exit" && !errors.Is(err, auth.ErrConflict) || first == "pool" && err != nil {
				t.Fatal("pool race result incorrect", err)
			}
			var members int
			if err = db.QueryRow(ctx, `SELECT count(*) FROM pool_contract_participants m JOIN pool_contracts c ON c.id=m.contract_id WHERE c.ended_at IS NULL AND m.participant_id=$1`, p.ID).Scan(&members); err != nil || members != 0 {
				t.Fatal("late membership escaped exit", members, err)
			}
		})
	}
}

func TestParticipantExitFinalAuditWaitRechecksActualSessionExpiry(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	server, _ := poolUpstream(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Root")
	p := createParticipant(t, s, token, c, 21, "Member")
	savePool(t, s, token, PoolUpdate{PoolInput: poolInput(c, []ManagedAccount{a}, []Participant{p}, []string{"75"})})
	barrier, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(ctx)
	if _, err = barrier.Exec(ctx, "LOCK TABLE participant_events IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	digest := sha256.Sum256([]byte(token))
	exec(t, db, `UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1`, digest[:], deadline)
	done := make(chan error, 1)
	go func() {
		out, e := s.ExitParticipant(ctx, token, p.ID, p.Revision, p.Name)
		if out.ParticipantID != "" {
			done <- errors.New("expired result disclosed participant")
			return
		}
		done <- e
	}()
	observePoolWait(t, db, "INSERT INTO participant_events%")
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired exit accepted", err)
	}
	var enabled bool
	var revision string
	var count int
	if err = db.QueryRow(ctx, `SELECT enabled,revision::text FROM participants WHERE id=$1`, p.ID).Scan(&enabled, &revision); err != nil || !enabled || revision != p.Revision {
		t.Fatal("expired exit changed participant", err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM pool_contracts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("expired exit changed contracts", err)
	}
}
