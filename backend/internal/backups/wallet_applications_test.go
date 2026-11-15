package backups

import (
	"context"
	"slices"
	"testing"
)

func TestWalletApplicationUnknownAndMismatchedResolutionBlockBothRestoreSides(t *testing.T) {
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
	for _, candidate := range []bool{false, true} {
		table := shape.table("wallet_application_results")
		want := "REMOTE_WRITE_UNCONFIRMED"
		if candidate {
			table = tempTable("wallet_application_results")
			want = "BACKUP_REMOTE_WRITE_UNCONFIRMED"
		}
		for _, mismatch := range []bool{false, true} {
			if mismatch {
				_, err = tx.Exec(ctx, `UPDATE `+table+` SET snapshot=jsonb_set(snapshot,'{simulation,epoch}','"AgICAgICAgICAgICAgICAg"')`)
			} else {
				_, err = tx.Exec(ctx, `UPDATE `+table+` SET resolved=false,snapshot=snapshot||'{"state":"unknown","dispatch_state":"unknown"}'::jsonb`)
			}
			if err != nil {
				t.Fatal(err)
			}
			reasons, err := restoreBlockers(ctx, tx, shape, true)
			if err != nil || !slices.Contains(reasons, want) || len(reasons) != 1 {
				t.Fatal("unresolved or mismatched proof bypassed two-sided restore guard", candidate, mismatch, reasons, err)
			}
			if _, err = tx.Exec(ctx, `UPDATE `+table+` SET resolved=true,snapshot=jsonb_set(snapshot||'{"state":"applied","dispatch_state":"confirmed"}'::jsonb,'{simulation,epoch}','"AAAAAAAAAAAAAAAAAAAAAA"')`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if reasons, err := restoreBlockers(ctx, tx, shape, true); err != nil || len(reasons) != 0 {
		t.Fatal("resolved application kept a false obligation", reasons, err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM `+shape.table("wallet_application_results")); err != nil {
		t.Fatal(err)
	}
	if reasons, err := restoreBlockers(ctx, tx, shape, true); err != nil || !slices.Contains(reasons, "REMOTE_WRITE_UNCONFIRMED") {
		t.Fatal("initial pending intent was not protected", reasons, err)
	}
}
