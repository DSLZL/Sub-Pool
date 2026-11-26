package connection

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type MonitoringConfig struct {
	Revision                 string     `json:"revision"`
	Enabled                  bool       `json:"enabled"`
	IntervalSeconds          int        `json:"interval_seconds"`
	StaleAfterSeconds        int        `json:"stale_after_seconds"`
	RefreshOnChange          bool       `json:"refresh_on_change"`
	NearResetEnabled         bool       `json:"near_reset_enabled"`
	NearResetLeadSeconds     int        `json:"near_reset_lead_seconds"`
	NearResetIntervalSeconds int        `json:"near_reset_interval_seconds"`
	StaleWarningsEnabled     bool       `json:"stale_warnings_enabled"`
	UsageEnabled             bool       `json:"usage_enabled"`
	UsageIntervalSeconds     int        `json:"usage_interval_seconds"`
	UsageEnabledAt           *time.Time `json:"-"`
	SavedAt                  *time.Time `json:"saved_at"`
}

func readMonitoringConfig(ctx context.Context, tx pgx.Tx, write bool) (MonitoringConfig, error) {
	var c MonitoringConfig
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT revision::text,enabled,interval_seconds,stale_after_seconds,refresh_on_change,near_reset_enabled,near_reset_lead_seconds,near_reset_interval_seconds,stale_warnings_enabled,usage_enabled,usage_interval_seconds,usage_enabled_at,saved_at FROM monitoring_config WHERE singleton`+lock).Scan(&c.Revision, &c.Enabled, &c.IntervalSeconds, &c.StaleAfterSeconds, &c.RefreshOnChange, &c.NearResetEnabled, &c.NearResetLeadSeconds, &c.NearResetIntervalSeconds, &c.StaleWarningsEnabled, &c.UsageEnabled, &c.UsageIntervalSeconds, &c.UsageEnabledAt, &c.SavedAt)
	c.SavedAt = utc(c.SavedAt)
	return c, err
}

func validMonitoringConfig(c MonitoringConfig) bool {
	usageInterval := c.UsageIntervalSeconds == 0 && !c.UsageEnabled || c.UsageIntervalSeconds >= 60 && c.UsageIntervalSeconds <= 86400
	return usageInterval && validRevision(c.Revision) && c.IntervalSeconds >= 60 && c.IntervalSeconds <= 86400 && c.StaleAfterSeconds >= c.IntervalSeconds && c.StaleAfterSeconds <= 604800 && c.NearResetLeadSeconds >= 60 && c.NearResetLeadSeconds <= 86400 && c.NearResetIntervalSeconds >= 60 && c.NearResetIntervalSeconds <= c.IntervalSeconds
}

func (s *Service) GetMonitoringConfig(ctx context.Context, token string) (MonitoringConfig, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return MonitoringConfig{}, err
	}
	defer tx.Rollback(ctx)
	c, err := readMonitoringConfig(ctx, tx, false)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return c, err
}

func (s *Service) UpdateMonitoringConfig(ctx context.Context, token string, input MonitoringConfig) (MonitoringConfig, error) {
	if !validMonitoringConfig(input) {
		return MonitoringConfig{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return MonitoringConfig{}, err
	}
	defer tx.Rollback(ctx)
	c, err := readMonitoringConfig(ctx, tx, true)
	if err != nil {
		return MonitoringConfig{}, err
	}
	if c.Revision != input.Revision {
		return MonitoringConfig{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return MonitoringConfig{}, err
	}
	if input.UsageIntervalSeconds == 0 {
		input.UsageIntervalSeconds, input.UsageEnabled = c.UsageIntervalSeconds, c.UsageEnabled
	}
	_, err = tx.Exec(ctx, `UPDATE monitoring_config SET revision=revision+1,enabled=$1,interval_seconds=$2,
		stale_after_seconds=$3,refresh_on_change=$4,near_reset_enabled=$5,near_reset_lead_seconds=$6,
		near_reset_interval_seconds=$7,stale_warnings_enabled=$8,saved_at=$9,
		usage_enabled=$10,usage_interval_seconds=$11,
		usage_enabled_at=CASE WHEN $1 AND $10 THEN COALESCE(usage_enabled_at,$9) ELSE usage_enabled_at END
		WHERE singleton`, input.Enabled, input.IntervalSeconds, input.StaleAfterSeconds, input.RefreshOnChange, input.NearResetEnabled,
		input.NearResetLeadSeconds, input.NearResetIntervalSeconds, input.StaleWarningsEnabled, time.Now().UTC(), input.UsageEnabled, input.UsageIntervalSeconds)
	if err == nil {
		c, err = readMonitoringConfig(ctx, tx, true)
	}
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "monitoring.config.update", Outcome: "succeeded", TargetType: "monitoring", TargetID: "global", Revision: &c.Revision})
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return c, err
}

type CollectionJob struct {
	ID                 string     `json:"id"`
	RunID              string     `json:"run_id"`
	AccountID          string     `json:"account_id"`
	SourceID           string     `json:"source_id"`
	ConnectionRevision string     `json:"connection_revision"`
	AccountRevision    string     `json:"account_revision"`
	PolicyRevision     string     `json:"policy_revision"`
	SourceMode         string     `json:"source_mode"`
	State              string     `json:"state"`
	Reason             string     `json:"reason"`
	QueuedAt           time.Time  `json:"queued_at"`
	StartedAt          *time.Time `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	RetryAfter         *time.Time `json:"retry_after"`
	Read               *Check     `json:"read"`
}

const jobColumns = `id::text,run_id::text,account_id,source_id,connection_revision::text,account_revision::text,policy_revision::text,source_mode,state,reason,queued_at,started_at,finished_at,retry_after,read_check`

func scanJob(row pgx.Row) (CollectionJob, error) {
	var j CollectionJob
	var body []byte
	err := row.Scan(&j.ID, &j.RunID, &j.AccountID, &j.SourceID, &j.ConnectionRevision, &j.AccountRevision, &j.PolicyRevision, &j.SourceMode, &j.State, &j.Reason, &j.QueuedAt, &j.StartedAt, &j.FinishedAt, &j.RetryAfter, &body)
	if err == nil && body != nil {
		err = json.Unmarshal(body, &j.Read)
	}
	j.QueuedAt, j.StartedAt, j.FinishedAt, j.RetryAfter = j.QueuedAt.UTC(), utc(j.StartedAt), utc(j.FinishedAt), utc(j.RetryAfter)
	return j, err
}

type CollectionCounts struct {
	Queued      string `json:"queued"`
	Running     string `json:"running"`
	Succeeded   string `json:"succeeded"`
	Failed      string `json:"failed"`
	Skipped     string `json:"skipped"`
	Conflict    string `json:"conflict"`
	Interrupted string `json:"interrupted"`
}

type CollectionRun struct {
	ID        string           `json:"id"`
	Reason    string           `json:"reason"`
	ActorID   *string          `json:"actor_id"`
	CreatedAt time.Time        `json:"created_at"`
	State     string           `json:"state"`
	Counts    CollectionCounts `json:"counts"`
}

func readRun(ctx context.Context, tx pgx.Tx, id string) (CollectionRun, error) {
	var r CollectionRun
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.reason,r.actor_id,r.created_at,count(j.id) FILTER (WHERE j.state='queued')::text,count(j.id) FILTER (WHERE j.state='running')::text,count(j.id) FILTER (WHERE j.state='succeeded')::text,count(j.id) FILTER (WHERE j.state='failed')::text,count(j.id) FILTER (WHERE j.state='skipped')::text,count(j.id) FILTER (WHERE j.state='conflict')::text,count(j.id) FILTER (WHERE j.state='interrupted')::text FROM collection_runs r LEFT JOIN collection_jobs j ON j.run_id=r.id WHERE r.id=$1 GROUP BY r.id`, id).Scan(&r.ID, &r.Reason, &r.ActorID, &r.CreatedAt, &r.Counts.Queued, &r.Counts.Running, &r.Counts.Succeeded, &r.Counts.Failed, &r.Counts.Skipped, &r.Counts.Conflict, &r.Counts.Interrupted)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, auth.ErrNotFound
	}
	r.CreatedAt, r.State = r.CreatedAt.UTC(), "completed"
	if r.Counts.Running != "0" {
		r.State = "running"
	} else if r.Counts.Queued != "0" {
		r.State = "queued"
	}
	return r, err
}

type CollectionRunPage struct {
	Runs      []CollectionRun `json:"runs"`
	NextAfter *string         `json:"next_after"`
}

func (s *Service) ListCollectionRuns(ctx context.Context, token, after string, limit int) (CollectionRunPage, error) {
	if after != "0" && !validRevision(after) || limit < 1 || limit > 100 {
		return CollectionRunPage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return CollectionRunPage{}, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM collection_runs WHERE id>$1 ORDER BY collection_runs.id LIMIT $2`, after, limit+1)
	if err != nil {
		return CollectionRunPage{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return CollectionRunPage{}, err
	}
	page := CollectionRunPage{Runs: []CollectionRun{}}
	if len(ids) > limit {
		ids = ids[:limit]
		page.NextAfter = &ids[limit-1]
	}
	for _, id := range ids {
		r, err := readRun(ctx, tx, id)
		if err != nil {
			return CollectionRunPage{}, err
		}
		page.Runs = append(page.Runs, r)
	}
	err = s.finish(ctx, tx, token, true)
	return page, err
}

type CollectionRunView struct {
	Run       CollectionRun   `json:"run"`
	Jobs      []CollectionJob `json:"jobs"`
	NextAfter *string         `json:"next_after"`
}

func (s *Service) GetCollectionRun(ctx context.Context, token, id, after string, limit int) (CollectionRunView, error) {
	if !validRevision(id) || after != "0" && !validRevision(after) || limit < 1 || limit > 100 {
		return CollectionRunView{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return CollectionRunView{}, err
	}
	defer tx.Rollback(ctx)
	r, err := readRun(ctx, tx, id)
	if err != nil {
		return CollectionRunView{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM collection_jobs WHERE run_id=$1 AND id>$2 ORDER BY collection_jobs.id LIMIT $3`, id, after, limit+1)
	if err != nil {
		return CollectionRunView{}, err
	}
	view := CollectionRunView{Run: r, Jobs: []CollectionJob{}}
	for rows.Next() {
		j, scanErr := scanJob(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		view.Jobs = append(view.Jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return CollectionRunView{}, err
	}
	if len(view.Jobs) > limit {
		view.Jobs = view.Jobs[:limit]
		view.NextAfter = &view.Jobs[limit-1].ID
	}
	err = s.finish(ctx, tx, token, true)
	return view, err
}

type collectionHistory struct {
	Job      *CollectionJob
	Snapshot *QuotaSnapshot
	Cooldown *time.Time
}

func readCollectionHistory(ctx context.Context, tx pgx.Tx, account string) (collectionHistory, error) {
	var h collectionHistory
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM collection_jobs WHERE account_id=$1 ORDER BY (state IN ('queued','running')) DESC,collection_jobs.id DESC LIMIT 1`, account))
	if err == nil {
		h.Job = &j
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	var body []byte
	err = tx.QueryRow(ctx, `SELECT snapshot FROM quota_observations WHERE account_id=$1 ORDER BY id DESC LIMIT 1`, account).Scan(&body)
	if err == nil {
		err = json.Unmarshal(body, &h.Snapshot)
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return h, err
	}
	err = tx.QueryRow(ctx, `SELECT max(retry_after) FROM collection_jobs WHERE account_id=$1`, account).Scan(&h.Cooldown)
	h.Cooldown = utc(h.Cooldown)
	return h, err
}

func (s *Service) nextCollection(now time.Time, policy MonitoringConfig, a storedAccount, c storedConfig, h collectionHistory) *time.Time {
	if !policy.Enabled || !a.Monitoring || !a.SourceCurrent || !a.ConnectionAvailable || a.SourceMode != "passive" || a.IdentityState != "verified" {
		return nil
	}
	if h.Job != nil && (h.Job.State == "queued" || h.Job.State == "running") {
		return nil
	}
	due := now
	if h.Job != nil {
		base := h.Job.QueuedAt
		if h.Job.StartedAt != nil {
			base = *h.Job.StartedAt
		}
		due = base.Add(time.Duration(policy.IntervalSeconds) * time.Second)
		changed := h.Job.AccountRevision != a.Revision || h.Job.ConnectionRevision != c.Revision || h.Job.PolicyRevision != policy.Revision
		if policy.RefreshOnChange && changed {
			due = now
		}
		if policy.NearResetEnabled && h.Snapshot != nil && h.Snapshot.SourceID == a.SourceID && h.Snapshot.ConnectionRevision == c.Revision {
			for _, w := range h.Snapshot.Windows {
				if w.ResetAt != nil && now.Sub(*w.ResetAt).Abs() <= time.Duration(policy.NearResetLeadSeconds)*time.Second {
					near := base.Add(time.Duration(policy.NearResetIntervalSeconds) * time.Second)
					if near.Before(due) {
						due = near
					}
				}
			}
		}
		minimum := base.Add(60 * time.Second)
		if minimum.After(due) {
			due = minimum
		}
	}
	if h.Cooldown != nil && h.Cooldown.After(due) {
		due = *h.Cooldown
	}
	return &due
}

type AccountObservation struct {
	AccountID       string         `json:"account_id"`
	RequestedMode   string         `json:"requested_mode"`
	Snapshot        *QuotaSnapshot `json:"snapshot"`
	LatestAttempt   *CollectionJob `json:"latest_attempt"`
	Freshness       string         `json:"freshness"`
	StaleWarning    bool           `json:"stale_warning"`
	NextScheduledAt *time.Time     `json:"next_scheduled_at"`
}

func (s *Service) GetAccountObservation(ctx context.Context, token, id string) (AccountObservation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AccountObservation{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "accounts")
	if err == nil {
		err = access.CheckAccount(id)
	}
	if err != nil {
		return AccountObservation{}, err
	}
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return AccountObservation{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return AccountObservation{}, err
	}
	a, err := readManaged(ctx, tx, id, c, false)
	if err != nil {
		return AccountObservation{}, err
	}
	h, err := readCollectionHistory(ctx, tx, id)
	if err != nil {
		return AccountObservation{}, err
	}
	now := time.Now().UTC()
	view := AccountObservation{AccountID: id, RequestedMode: a.SourceMode, Snapshot: h.Snapshot, LatestAttempt: h.Job, Freshness: "missing", NextScheduledAt: s.nextCollection(now, policy, a, c, h)}
	if h.Snapshot != nil {
		switch {
		case !a.SourceCurrent || h.Snapshot.SourceID != a.SourceID:
			view.Freshness = "source_changed"
		case !a.ConnectionAvailable:
			view.Freshness = "connection_unavailable"
		case h.Snapshot.ConnectionRevision != c.Revision:
			view.Freshness = "connection_changed"
		case a.IdentityState != "verified":
			view.Freshness = "identity_unverified"
		case h.Snapshot.SourceMode != a.SourceMode:
			view.Freshness = "source_mode_changed"
		case h.Snapshot.SourceAt == nil:
			view.Freshness = "unknown"
		case h.Snapshot.SourceAt.After(now):
			view.Freshness = "future"
		case now.Sub(*h.Snapshot.SourceAt) > time.Duration(policy.StaleAfterSeconds)*time.Second:
			view.Freshness = "stale"
		default:
			view.Freshness = "fresh"
		}
		for i := range h.Snapshot.Windows {
			w := &h.Snapshot.Windows[i]
			w.ResetExpired = w.ResetAt != nil && !now.Before(*w.ResetAt)
		}
	}
	view.StaleWarning = policy.StaleWarningsEnabled && (view.Freshness == "missing" || view.Freshness == "unknown" || view.Freshness == "future" || view.Freshness == "stale")
	err = s.finishAccountRead(ctx, tx, token)
	return view, err
}

type MonitoringStatus struct {
	Enabled         bool           `json:"enabled"`
	WorkerSeenAt    *time.Time     `json:"worker_seen_at"`
	WorkerRecent    bool           `json:"worker_recent"`
	NextScheduledAt *time.Time     `json:"next_scheduled_at"`
	LastSuccessAt   *time.Time     `json:"last_success_at"`
	LatestRun       *CollectionRun `json:"latest_run"`
	ActiveAvailable bool           `json:"active_available"`
	ActiveReason    string         `json:"active_reason"`
}

func (s *Service) GetMonitoringStatus(ctx context.Context, token string) (MonitoringStatus, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return MonitoringStatus{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return MonitoringStatus{}, err
	}
	result := MonitoringStatus{Enabled: policy.Enabled, ActiveReason: "ACTIVE_QUERY_SIDE_EFFECT"}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return MonitoringStatus{}, err
	}
	var latestID *string
	err = tx.QueryRow(ctx, `SELECT heartbeat_at,
		(SELECT id::text FROM collection_runs ORDER BY collection_runs.id DESC LIMIT 1),
		(SELECT max(finished_at) FROM collection_jobs WHERE state='succeeded')
		FROM monitoring_worker WHERE singleton`).Scan(&result.WorkerSeenAt, &latestID, &result.LastSuccessAt)
	if err != nil {
		return MonitoringStatus{}, err
	}
	result.WorkerSeenAt, result.LastSuccessAt = utc(result.WorkerSeenAt), utc(result.LastSuccessAt)
	if result.WorkerSeenAt != nil {
		age := time.Since(*result.WorkerSeenAt)
		result.WorkerRecent = age >= 0 && age < 15*time.Second
	}
	if latestID != nil {
		run, err := readRun(ctx, tx, *latestID)
		if err != nil {
			return MonitoringStatus{}, err
		}
		result.LatestRun = &run
	}
	if policy.Enabled {
		accounts, err := loadCollectionAccounts(ctx, tx, c, true, []string{})
		if err != nil {
			return MonitoringStatus{}, err
		}
		now := time.Now().UTC()
		for _, a := range accounts {
			h, err := readCollectionHistory(ctx, tx, a.ID)
			if err != nil {
				return MonitoringStatus{}, err
			}
			next := s.nextCollection(now, policy, a, c, h)
			if next != nil && (result.NextScheduledAt == nil || next.Before(*result.NextScheduledAt)) {
				result.NextScheduledAt = next
			}
		}
	}
	return result, s.finish(ctx, tx, token, true)
}
