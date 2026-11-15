package connection

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Frozen identities stay in place until tails and current-period rights are resolved.
func temporaryObligations(ctx context.Context, tx pgx.Tx, source *string, participants []string) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `WITH rounds AS (
 SELECT r.* FROM temporary_rounds r
 WHERE ($1::text IS NULL OR r.snapshot->'scope'->>'source_id'=$1)
 AND ($2::text[] IS NULL OR EXISTS(
  SELECT 1 FROM jsonb_array_elements(r.snapshot->'scope'->'wallets') w WHERE w->>'participant_id'=ANY($2)))
 ), settlements AS (
 SELECT DISTINCT ON(e.round_id,e.account_id) e.* FROM temporary_events e JOIN rounds r ON r.id=e.round_id
 WHERE e.kind='settlement' ORDER BY e.round_id,e.account_id,e.id DESC
 ), rights AS (
 SELECT s.account_id, a.key AS participant_id, s.snapshot->'next_period' AS period, a.value::numeric AS delta
 FROM settlements s CROSS JOIN LATERAL jsonb_each_text(s.snapshot->'adjustments') a
 WHERE s.snapshot->>'state'='settled'
 UNION ALL
 SELECT e.account_id,e.snapshot->>'participant_id',e.snapshot->'period',(e.snapshot->>'delta')::numeric
 FROM temporary_events e JOIN rounds r ON r.id=e.round_id WHERE e.kind='rights'
 )
 SELECT EXISTS(
 SELECT 1 FROM rounds r LEFT JOIN temporary_exits x ON x.round_id=r.id
 WHERE x.round_id IS NULL OR (
  x.reason<>'terminated' AND r.snapshot->>'mode'='carry' AND EXISTS(
   SELECT 1 FROM jsonb_array_elements(r.snapshot->'scope'->'accounts') a
   WHERE NOT EXISTS(SELECT 1 FROM settlements s WHERE s.round_id=r.id AND s.account_id=a->>'account_id'
    AND s.snapshot->>'state' IN ('settled','no_carry','no_borrowing','cancelled'))))
 ) OR EXISTS(
 SELECT 1 FROM rights WHERE (period->>'end_at')::timestamptz>clock_timestamp()
 AND ($2::text[] IS NULL OR participant_id=ANY($2))
 GROUP BY account_id,participant_id,period HAVING sum(delta)<>0
 )`, source, participants).Scan(&blocked)
	return blocked, err
}
