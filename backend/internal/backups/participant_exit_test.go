package backups

import (
	"context"
	"reflect"
	"testing"
)

func TestParticipantExitReceiptSurvivesActualBackupRestore(t *testing.T) {
	f := setup(t)
	seedEveryBusinessTable(t, f)
	// The generic archive fixture keeps opaque facts; this operation reads the typed binding projection.
	exec(t, f.pool, `UPDATE participant_bindings SET snapshot=jsonb_set(snapshot,'{upstream_id}',to_jsonb(upstream_user_id::text))`)
	exec(t, f.pool, `UPDATE managed_accounts SET read_check='{"state":"available"}'`)
	ctx := context.Background()
	var id string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM participants`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	p, err := f.connection.GetParticipant(ctx, f.token, id)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.connection.ExitParticipant(ctx, f.token, p.ID, p.Revision, p.Name)
	if err != nil || len(want.PoolContractIDs) != 1 {
		t.Fatal("native exit not persisted", err)
	}
	file := export(t, f, true)
	plan := preflight(t, f, file)
	result, err := f.s.Apply(ctx, f.token, ApplyInput{plan.ID, plan.TargetBasis, true})
	if err != nil || result.State != "applied" {
		t.Fatal("exit history restore failed", result.State, err)
	}
	token := login(t, f, f.admin.Username)
	got, err := f.connection.GetParticipantExit(ctx, token, p.ID, p.Revision)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("restored exit receipt changed", got, err)
	}
}
