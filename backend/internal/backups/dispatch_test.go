package backups

import (
	"context"
	"slices"
	"testing"
)

func TestActiveSchedulingPausesBlockBothCurrentAndCandidateRestore(t *testing.T) {
	f := setup(t)
	seedEveryBusinessTable(t, f)
	data := currentSnapshot(t, f)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	shape, err := readSchema(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateRows(ctx, tx, shape, data); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pausing", "paused", "restoring", "needs_review"} {
		if _, err = tx.Exec(ctx, `UPDATE `+shape.table("dispatch_pauses")+` SET state=$1`, state); err != nil {
			t.Fatal(err)
		}
		reasons, err := restoreBlockers(ctx, tx, shape, true)
		if err != nil || !slices.Contains(reasons, "REMOTE_WRITE_UNCONFIRMED") || slices.Contains(reasons, "BACKUP_REMOTE_WRITE_UNCONFIRMED") {
			t.Fatal(state, reasons, err)
		}
		if _, err = tx.Exec(ctx, `UPDATE `+shape.table("dispatch_pauses")+` SET state='reviewed'`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE `+tempTable("dispatch_pauses")+` SET state=$1`, state); err != nil {
			t.Fatal(err)
		}
		reasons, err = restoreBlockers(ctx, tx, shape, true)
		if err != nil || slices.Contains(reasons, "REMOTE_WRITE_UNCONFIRMED") || !slices.Contains(reasons, "BACKUP_REMOTE_WRITE_UNCONFIRMED") {
			t.Fatal(state, reasons, err)
		}
		if _, err = tx.Exec(ctx, `UPDATE `+tempTable("dispatch_pauses")+` SET state='reviewed'`); err != nil {
			t.Fatal(err)
		}
	}
}
