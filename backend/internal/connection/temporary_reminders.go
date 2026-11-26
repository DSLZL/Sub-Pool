package connection

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type TemporaryReminders struct {
	Revision  string  `json:"revision"`
	Enabled   bool    `json:"enabled"`
	Threshold *string `json:"threshold"`
}

func temporaryReminders(ctx context.Context, tx pgx.Tx, round string) (TemporaryReminders, error) {
	out := TemporaryReminders{Revision: "0"}
	var raw []byte
	var revision string
	err := tx.QueryRow(ctx, `SELECT id::text,snapshot FROM temporary_events WHERE round_id=$1 AND kind='reminders' ORDER BY temporary_events.id DESC LIMIT 1`, round).Scan(&revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	out.Revision = revision
	return out, err
}
