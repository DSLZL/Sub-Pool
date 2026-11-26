package connection

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func listWeeklyWalletGroups(ctx context.Context, tx pgx.Tx, pool string) ([]weeklyWalletEntry, error) {
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.participant_id,p.name,g.phase,g.period_end,g.recorded_at,g.snapshot,
	 g.application_id::text,`+walletApplicationSnapshotSQL+`->>'state' FROM weekly_wallet_groups g
	 JOIN participants p ON p.id=g.participant_id LEFT JOIN wallet_application_requests r ON r.id=g.application_id
	 WHERE g.pool_ids @> ARRAY[$1]::text[] ORDER BY g.id DESC LIMIT 100`, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []weeklyWalletEntry
	for rows.Next() {
		var entry weeklyWalletEntry
		var state *string
		if err = rows.Scan(&entry.ID, &entry.ParticipantID, &entry.Name, &entry.Phase, &entry.PeriodEnd, &entry.RecordedAt, &entry.Group, &entry.ApplicationID, &state); err != nil {
			return nil, err
		}
		entry.PeriodEnd, entry.RecordedAt = entry.PeriodEnd.UTC(), entry.RecordedAt.UTC()
		entry.State = "applied"
		if state != nil {
			entry.State = *state
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
