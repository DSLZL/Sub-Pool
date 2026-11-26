package connection

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type CollectInput struct {
	SourceID           string   `json:"source_id"`
	ConnectionRevision string   `json:"connection_revision"`
	All                bool     `json:"all"`
	AccountIDs         []string `json:"account_ids"`
}

func collectionAudit(ctx context.Context, tx pgx.Tx, job CollectionJob, actor *string, outcome, action string) error {
	entry := audit.Entry{OperationID: "collection:job:" + job.ID, ActorID: actor, Action: action, Outcome: outcome, TargetType: "account", TargetID: job.AccountID, Revision: &job.AccountRevision, SourceID: &job.SourceID}
	if actor == nil {
		task := "collector"
		entry.TaskID = &task
	}
	_, err := audit.Record(ctx, tx, entry)
	return err
}

func (s *Service) queueRun(ctx context.Context, tx pgx.Tx, reason string, actor *auth.Identity, digest []byte, selected bool, policy MonitoringConfig, c storedConfig, accounts []storedAccount) (CollectionRun, error) {
	var actorID, actorRevision *string
	if actor != nil {
		actorID = &actor.ID
		actorRevision = &actor.Revision
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var runID string
	err := tx.QueryRow(ctx, `INSERT INTO collection_runs(reason,actor_id,actor_revision,actor_digest,selected,created_at) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`, reason, actorID, actorRevision, digest, selected, now).Scan(&runID)
	if err != nil {
		return CollectionRun{}, err
	}
	for _, a := range accounts {
		h, err := readCollectionHistory(ctx, tx, a.ID)
		if err != nil {
			return CollectionRun{}, err
		}
		state, why := "queued", "QUEUED"
		var retryAfter *time.Time
		switch {
		case h.Job != nil && (h.Job.State == "queued" || h.Job.State == "running"):
			state, why = "conflict", "ALREADY_RUNNING"
		case !selected && !a.Monitoring:
			state, why = "skipped", "PAUSED"
		case !a.SourceCurrent:
			state, why = "skipped", "SOURCE_CHANGED"
		case a.IdentityState != "verified":
			state, why = "skipped", "IDENTITY_UNVERIFIED"
		case a.SourceMode != "passive":
			state, why = "skipped", "ACTIVE_QUERY_SIDE_EFFECT"
		default:
			if h.Cooldown != nil && h.Cooldown.After(now) {
				retryAfter = h.Cooldown
			}
			if h.Job != nil && h.Job.StartedAt != nil {
				minimum := h.Job.StartedAt.Add(60 * time.Second)
				if minimum.After(now) && (retryAfter == nil || minimum.After(*retryAfter)) {
					retryAfter = &minimum
				}
			}
			if retryAfter != nil {
				state, why = "skipped", "COOLDOWN"
			}
		}
		var finished *time.Time
		if state != "queued" {
			finished = &now
		}
		insert := `INSERT INTO collection_jobs(run_id,account_id,source_id,connection_revision,account_revision,policy_revision,source_mode,state,reason,queued_at,finished_at,retry_after) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (account_id) WHERE state IN ('queued','running') DO NOTHING RETURNING ` + jobColumns
		j, err := scanJob(tx.QueryRow(ctx, insert, runID, a.ID, a.SourceID, c.Revision, a.Revision, policy.Revision, a.SourceMode, state, why, now, finished, retryAfter))
		if errors.Is(err, pgx.ErrNoRows) {
			j, err = scanJob(tx.QueryRow(ctx, insert, runID, a.ID, a.SourceID, c.Revision, a.Revision, policy.Revision, a.SourceMode, "conflict", "ALREADY_RUNNING", now, now, nil))
		}
		if err != nil {
			return CollectionRun{}, err
		}
		outcome := "pending"
		if j.State == "conflict" {
			outcome = "conflict"
		} else if j.State == "skipped" {
			outcome = "denied"
		}
		if err = collectionAudit(ctx, tx, j, actorID, outcome, "collection.request"); err != nil {
			return CollectionRun{}, err
		}
	}
	return readRun(ctx, tx, runID)
}

func loadCollectionAccounts(ctx context.Context, tx pgx.Tx, c storedConfig, all bool, ids []string) ([]storedAccount, error) {
	rows, err := tx.Query(ctx, `SELECT `+managedColumns+` FROM managed_accounts WHERE ($1 OR id=ANY($2::text[])) ORDER BY id FOR SHARE`, all, ids)
	if err != nil {
		return nil, err
	}
	accounts := []storedAccount{}
	for rows.Next() {
		a, scanErr := scanManaged(rows, c)
		if scanErr != nil {
			err = scanErr
			break
		}
		accounts = append(accounts, a)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err == nil && !all && len(accounts) != len(ids) {
		err = auth.ErrNotFound
	}
	return accounts, err
}

func (s *Service) QueueCollection(ctx context.Context, token string, input CollectInput) (CollectionRun, error) {
	if !managedID(input.SourceID) || !validRevision(input.ConnectionRevision) || input.AccountIDs == nil || len(input.AccountIDs) > 500 || input.All && len(input.AccountIDs) != 0 || !input.All && len(input.AccountIDs) == 0 {
		return CollectionRun{}, auth.ErrInput
	}
	seen := map[string]bool{}
	for _, id := range input.AccountIDs {
		if !managedID(id) || seen[id] {
			return CollectionRun{}, auth.ErrInput
		}
		seen[id] = true
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return CollectionRun{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return CollectionRun{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return CollectionRun{}, err
	}
	if c.SourceID == nil || *c.SourceID != input.SourceID || c.Revision != input.ConnectionRevision {
		return CollectionRun{}, auth.ErrConflict
	}
	accounts, err := loadCollectionAccounts(ctx, tx, c, input.All, input.AccountIDs)
	if err != nil {
		return CollectionRun{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return CollectionRun{}, err
	}
	digest := sha256.Sum256([]byte(token))
	r, err := s.queueRun(ctx, tx, "manual", &actor, digest[:], !input.All, policy, c, accounts)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return r, err
}
