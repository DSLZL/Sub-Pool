package connection

import (
	"context"
	"os"
	"testing"
)

func TestTemporarySettlementJournalAtomicAndIdempotent(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	round, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.StopTemporary(ctx, f.token, round.ID); err != nil {
		t.Fatal(err)
	}
	exec(t, f.db, `CREATE FUNCTION reject_settlement_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='temporary.settlement' THEN RAISE EXCEPTION 'owned settlement audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_settlement_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_settlement_audit()`)
	if err = f.s.processTemporary(ctx); err == nil {
		t.Fatal("settlement committed without its management audit")
	}
	var count int
	if err = f.db.QueryRow(ctx, "SELECT count(*) FROM temporary_events WHERE round_id=$1 AND kind='settlement'", round.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed audit left settlement effects", count, err)
	}
	exec(t, f.db, "DROP TRIGGER reject_settlement_audit ON management_events")
	for i := 0; i < 2; i++ {
		if err = f.s.processTemporary(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var events, traced int
	err = f.db.QueryRow(ctx, `SELECT count(*),count(m.id) FROM temporary_events e LEFT JOIN management_events m
 ON m.operation_id='temporary-settlement:'||e.id AND m.action='temporary.settlement'
 AND m.task_id='temporary:'||e.round_id AND m.actor_id IS NULL
 AND m.target_type='account' AND m.target_id=e.account_id AND m.outcome='succeeded'
 WHERE e.round_id=$1 AND e.kind='settlement'`, round.ID).Scan(&events, &traced)
	if err != nil || events != len(round.Scope.Accounts) || traced != events || f.writes.Load() != 0 {
		t.Fatal("missing,duplicate or uncorrelated settlement journal", events, traced, err)
	}
}

func TestTemporarySettlementJournalPendingCompletionAndLegacyBackfill(t *testing.T) {
	f := temporaryFixture(t)
	ctx := context.Background()
	round, err := f.s.StartTemporary(ctx, f.token, temporaryStartInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ state, outcome string }{
		{"pending_evidence", "pending"}, {"settled", "succeeded"}, {"no_carry", "succeeded"},
		{"no_borrowing", "succeeded"}, {"cancelled", "succeeded"}, {"unrecognized", "unknown"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			tx, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			value := TemporarySettlement{RoundID: round.ID, AccountID: round.Scope.Accounts[0].AccountID, State: tc.state}
			if err = temporarySaveSettlement(ctx, tx, value); err != nil {
				t.Fatal(err)
			}
			if err = temporarySaveSettlement(ctx, tx, value); err != nil {
				t.Fatal(err)
			}
			var count int
			err = tx.QueryRow(ctx, `SELECT count(*) FROM temporary_events e JOIN management_events m
    ON m.operation_id='temporary-settlement:'||e.id AND m.action='temporary.settlement'
    AND m.task_id='temporary:'||e.round_id AND m.actor_id IS NULL
    AND m.target_type='account' AND m.target_id=e.account_id AND m.outcome=$2
    WHERE e.round_id=$1 AND e.snapshot->>'state'=$3`, round.ID, tc.outcome, tc.state).Scan(&count)
			if err != nil || count != 1 {
				t.Fatal("wrong settlement outcome or repeated journal", count, err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	var retained string
	if err = f.db.QueryRow(ctx, `SELECT row_to_json(m)::text FROM management_events m WHERE action='temporary.settlement' ORDER BY id LIMIT 1`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	exec(t, f.db, `DELETE FROM management_events WHERE action='temporary.settlement' AND id<>(SELECT min(id) FROM management_events WHERE action='temporary.settlement')`)
	migration, err := os.ReadFile("../database/migrations/027_temporary_settlement_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		exec(t, f.db, string(migration))
	}
	var preserved string
	if err = f.db.QueryRow(ctx, `SELECT row_to_json(m)::text FROM management_events m WHERE action='temporary.settlement' ORDER BY id LIMIT 1`).Scan(&preserved); err != nil || preserved != retained {
		t.Fatal("backfill replaced existing journal", err)
	}
	var events, traced, originalTimes int
	err = f.db.QueryRow(ctx, `SELECT count(*),count(m.id),count(*) FILTER(WHERE m.occurred_at=e.recorded_at)
  FROM temporary_events e LEFT JOIN management_events m ON m.operation_id='temporary-settlement:'||e.id
  AND m.action='temporary.settlement' AND m.task_id='temporary:'||e.round_id AND m.actor_id IS NULL
  AND m.target_type='account' AND m.target_id=e.account_id
  AND m.outcome=CASE e.snapshot->>'state' WHEN 'pending_evidence' THEN 'pending' WHEN 'unrecognized' THEN 'unknown' ELSE 'succeeded' END
  WHERE e.round_id=$1 AND e.kind='settlement'`, round.ID).Scan(&events, &traced, &originalTimes)
	if err != nil || events != 6 || traced != 6 || originalTimes != 5 || f.writes.Load() != 0 {
		t.Fatal("legacy settlement traces lost,duplicated or retimed", events, traced, originalTimes, err)
	}
}
