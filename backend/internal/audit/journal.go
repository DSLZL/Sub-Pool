package audit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type Entry struct {
	OperationID string
	ActorID     *string
	TaskID      *string
	Action      string
	Outcome     string
	TargetType  string
	TargetID    string
	Revision    *string
	SourceID    *string
}

var metadata = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,128}$`)
var localID = regexp.MustCompile(`^[A-Za-z0-9_-]{21}[AQgw]$`)

// Record appends in the caller's transaction; intent and later results share an operation ID.
func Record(ctx context.Context, tx pgx.Tx, e Entry) (string, error) {
	valid := (e.ActorID == nil) != (e.TaskID == nil) && metadata.MatchString(e.Action) && metadata.MatchString(e.TargetType) && metadata.MatchString(e.TargetID)
	for _, id := range []*string{e.ActorID, e.SourceID} {
		valid = valid && (id == nil || localID.MatchString(*id))
	}
	valid = valid && (e.TaskID == nil || metadata.MatchString(*e.TaskID))
	if e.Revision != nil {
		n, err := strconv.ParseInt(*e.Revision, 10, 64)
		valid = valid && err == nil && n > 0 && strconv.FormatInt(n, 10) == *e.Revision
	}
	switch e.Outcome {
	case "pending", "succeeded", "failed", "unknown", "denied", "conflict":
	default:
		valid = false
	}
	if !valid || e.OperationID != "" && !metadata.MatchString(e.OperationID) {
		return "", errors.New("invalid management audit metadata")
	}
	if e.OperationID == "" {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return "", err
		}
		e.OperationID = base64.RawURLEncoding.EncodeToString(id)
	}
	_, err := tx.Exec(ctx, `INSERT INTO management_events(operation_id,occurred_at,actor_id,task_id,action,outcome,target_type,target_id,target_revision,source_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, e.OperationID, time.Now().UTC(), e.ActorID, e.TaskID, e.Action, e.Outcome, e.TargetType, e.TargetID, e.Revision, e.SourceID)
	return e.OperationID, err
}
