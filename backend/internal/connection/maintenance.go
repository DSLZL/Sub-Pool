package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type MaintenanceBasis struct {
	Fingerprint           string  `json:"fingerprint"`
	SourceID              string  `json:"source_id"`
	ConnectionRevision    string  `json:"connection_revision"`
	AccountRevision       string  `json:"account_revision"`
	PolicyRevision        string  `json:"policy_revision"`
	ManagementRevision    string  `json:"management_revision"`
	EstimateRevision      string  `json:"estimate_revision"`
	ObservationHead       string  `json:"observation_head"`
	CaptureHead           string  `json:"capture_head"`
	EstimateHead          string  `json:"estimate_head"`
	PreviousAppliedPlanID *string `json:"previous_applied_plan_id"`
}

type maintenancePreview struct {
	Scope        MaintenanceScope      `json:"scope"`
	Kind         string                `json:"kind"`
	RulesVersion string                `json:"rules_version"`
	Basis        MaintenanceBasis      `json:"basis"`
	Preview      MaintenanceProjection `json:"preview"`
	Difference   MaintenanceDifference `json:"difference"`
	Issues       []MaintenanceIssue    `json:"issues"`
	Limitations  []string              `json:"limitations"`
}

type MaintenancePlan struct {
	ID string `json:"id"`
	maintenancePreview
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	Validity    string     `json:"validity"`
	RequestedBy *string    `json:"requested_by"`
	QueuedAt    *time.Time `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	ResultID    *string    `json:"result_id"`
}

const maintenanceColumns = `id::text,snapshot,actor_id,created_at,expires_at,state,reason,queued_actor_id,queued_at,started_at,finished_at`

func scanMaintenance(row pgx.Row) (MaintenancePlan, error) {
	var p MaintenancePlan
	var body []byte
	err := row.Scan(&p.ID, &body, &p.CreatedBy, &p.CreatedAt, &p.ExpiresAt, &p.State, &p.Reason, &p.RequestedBy, &p.QueuedAt, &p.StartedAt, &p.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, auth.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(body, &p.maintenancePreview); err != nil {
		return p, err
	}
	p.CreatedAt, p.ExpiresAt = p.CreatedAt.UTC(), p.ExpiresAt.UTC()
	p.QueuedAt, p.StartedAt, p.FinishedAt = utc(p.QueuedAt), utc(p.StartedAt), utc(p.FinishedAt)
	p.Validity = "terminal"
	switch p.State {
	case "ready":
		p.Validity = "valid"
	case "blocked":
		p.Validity = "blocked"
	case "queued", "running":
		p.Validity = "pending"
	case "applied":
		p.ResultID = &p.ID
	}
	return p, nil
}

var maintenanceRuleName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
var maintenanceFingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)

func maintenanceTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !observationTimestamp.MatchString(value) || t.Year() < 1970 || t.Year() >= 9999 {
		return time.Time{}, auth.ErrInput
	}
	return t, nil
}

func normalizeMaintenance(in MaintenanceInput) (MaintenanceInput, error) {
	from, err := maintenanceTime(in.From)
	until, endErr := maintenanceTime(in.Until)
	if err != nil || endErr != nil || !from.Before(until) || until.After(time.Now().UTC()) || !managedID(in.AccountID) || !maintenanceRuleName.MatchString(in.RulesVersion) || (in.Kind != "rebuild" && in.Kind != "cycles" && in.Kind != "upgrade") {
		return in, auth.ErrInput
	}
	in.From, in.Until = from.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano)
	return in, nil
}

func (s *Service) maintenanceEnvironment(ctx context.Context, tx pgx.Tx, account string, write bool) (MonitoringConfig, storedConfig, storedAccount, error) {
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return policy, storedConfig{}, storedAccount{}, err
	}
	// Native writers hold SHARE through dispatch; maintenance commits before a new write can start.
	c, err := s.config(ctx, tx, write)
	if err != nil {
		return policy, c, storedAccount{}, err
	}
	a, err := readManaged(ctx, tx, account, c, write)
	return policy, c, a, err
}

func buildMaintenance(ctx context.Context, tx pgx.Tx, in MaintenanceInput, policy MonitoringConfig, c storedConfig, a storedAccount) (maintenancePreview, error) {
	out := maintenancePreview{Scope: in.MaintenanceScope, Kind: in.Kind, RulesVersion: in.RulesVersion, Issues: []MaintenanceIssue{}, Limitations: []string{"RETAINED_RECEIPTS_ONLY", "PERIOD_COVERAGE_NOT_CERTIFIED", "NO_FINANCIAL_RECALCULATION", "ESTIMATE_HISTORY_UNCHANGED", "NO_UPSTREAM_WRITES_OR_NOTIFICATIONS", "RULE_UPGRADE_UNAVAILABLE"}}
	var remotePending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pricing_changes WHERE state IN ('applying','reverting','needs_review'))
	 OR EXISTS(SELECT 1 FROM dispatch_pauses WHERE state IN ('pausing','restoring','needs_review'))`).Scan(&remotePending); err != nil {
		return out, err
	}
	from, _ := maintenanceTime(in.From)
	until, _ := maintenanceTime(in.Until)
	rows, err := tx.Query(ctx, `SELECT q.id::text,q.job_id::text,q.received_at,q.snapshot,COALESCE(v.action='exclude',false) FROM quota_observations q
	 LEFT JOIN LATERAL (SELECT action FROM observation_changes WHERE observation_id=q.id ORDER BY observation_changes.id DESC LIMIT 1) v ON true
	 WHERE q.account_id=$1 AND q.received_at>=$2 AND q.received_at<$3 ORDER BY q.id LIMIT $4`, in.AccountID, ceilMicrosecond(from), ceilMicrosecond(until), maintenanceObservationLimit+1)
	if err != nil {
		return out, err
	}
	facts := []maintenanceFact{}
	for rows.Next() {
		var fact maintenanceFact
		if err = rows.Scan(&fact.ID, &fact.JobID, &fact.ReceivedAt, &fact.Raw, &fact.Excluded); err == nil {
			err = json.Unmarshal(fact.Raw, &fact.snapshot)
		}
		fact.ReceivedAt = fact.ReceivedAt.UTC()
		if err != nil {
			break
		}
		facts = append(facts, fact)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Preview, out.Issues = projectMaintenance(facts, a.SourceID)
	if remotePending {
		out.Issues = append(out.Issues, MaintenanceIssue{Code: "REMOTE_OPERATION_UNRESOLVED", Blocking: true})
	}
	if in.Kind == "upgrade" {
		out.Issues = append(out.Issues, MaintenanceIssue{Code: "RULE_UPGRADE_UNAVAILABLE", Blocking: true})
	}
	if in.RulesVersion != maintenanceRules {
		out.Preview.Periods = []MaintenancePeriod{}
		out.Issues = append(out.Issues, MaintenanceIssue{Code: "RULES_UNVERIFIED", Blocking: true})
	}
	cfg, err := readEstimateConfiguration(ctx, tx, a.ID, false)
	if err != nil {
		return out, err
	}
	observation, capture, management, err := estimateHeads(ctx, tx, a.ID)
	if err != nil {
		return out, err
	}
	var estimate string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(id),0)::text FROM estimate_results WHERE account_id=$1`, a.ID).Scan(&estimate); err != nil {
		return out, err
	}
	scope, err := json.Marshal(in.MaintenanceScope)
	if err != nil {
		return out, err
	}
	previous, err := scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE account_id=$1 AND state='applied' AND snapshot->'scope'=$2::jsonb AND snapshot->>'rules_version'=$3 ORDER BY maintenance_plans.id DESC LIMIT 1`, a.ID, scope, in.RulesVersion))
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return out, err
	}
	var previousPlan *MaintenancePlan
	if err == nil {
		previousPlan = &previous
	}
	out.Basis = MaintenanceBasis{SourceID: a.SourceID, ConnectionRevision: c.Revision, AccountRevision: a.Revision, PolicyRevision: policy.Revision, ManagementRevision: management, EstimateRevision: cfg.Revision, ObservationHead: observation, CaptureHead: capture, EstimateHead: estimate}
	if previousPlan != nil {
		out.Basis.PreviousAppliedPlanID = &previous.ID
	} else {
		out.Issues = append(out.Issues, MaintenanceIssue{Code: "NO_PRIOR_PROJECTION"})
	}
	out.Difference = maintenanceDifference(previous.Preview, out.Preview)
	if out.Difference.Added+out.Difference.Removed+out.Difference.Changed > 0 {
		out.Issues = append(out.Issues, MaintenanceIssue{Code: "PERIOD_INDEX_DIFFERENCE"})
	}
	body, err := json.Marshal(struct {
		Input    MaintenanceInput
		Account  ManagedAccount
		Config   Configuration
		Policy   MonitoringConfig
		Estimate EstimateConfiguration
		Basis    MaintenanceBasis
		Facts    []maintenanceFact
		Previous *MaintenancePlan
	}{in, a.ManagedAccount, c.Configuration, policy, cfg, out.Basis, facts, previousPlan})
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(body)
	out.Basis.Fingerprint = hex.EncodeToString(digest[:])
	return out, nil
}

func maintenanceBlocked(p maintenancePreview) bool {
	for _, issue := range p.Issues {
		if issue.Blocking {
			return true
		}
	}
	return false
}

func maintenanceAudit(ctx context.Context, tx pgx.Tx, p MaintenancePlan, actor *string, action, outcome string) error {
	entry := audit.Entry{OperationID: "maintenance:" + p.ID, ActorID: actor, Action: "maintenance." + action, Outcome: outcome, TargetType: "maintenance", TargetID: p.ID, SourceID: &p.Basis.SourceID}
	if actor == nil {
		task := "maintenance"
		entry.TaskID = &task
	}
	_, err := audit.Record(ctx, tx, entry)
	return err
}

func (s *Service) PreflightMaintenance(ctx context.Context, token string, in MaintenanceInput) (MaintenancePlan, error) {
	in, err := normalizeMaintenance(in)
	if err != nil {
		return MaintenancePlan{}, err
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return MaintenancePlan{}, err
	}
	defer tx.Rollback(ctx)
	policy, c, a, err := s.maintenanceEnvironment(ctx, tx, in.AccountID, false)
	if err != nil {
		return MaintenancePlan{}, err
	}
	preview, err := buildMaintenance(ctx, tx, in, policy, c, a)
	if err != nil {
		return MaintenancePlan{}, err
	}
	body, err := json.Marshal(preview)
	if err != nil {
		return MaintenancePlan{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	state, reason := "ready", "READY"
	if maintenanceBlocked(preview) {
		state, reason = "blocked", "PREFLIGHT_BLOCKED"
	}
	p, err := scanMaintenance(tx.QueryRow(ctx, `INSERT INTO maintenance_plans(account_id,actor_id,created_at,expires_at,snapshot,state,reason) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+maintenanceColumns, a.ID, actor.ID, now, now.Add(10*time.Minute), body, state, reason))
	if err == nil {
		err = maintenanceAudit(ctx, tx, p, &actor.ID, "preflight", "succeeded")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return p, err
}

func checkMaintenance(ctx context.Context, tx pgx.Tx, p *MaintenancePlan, policy MonitoringConfig, c storedConfig, a storedAccount) error {
	if p.State != "ready" {
		return nil
	}
	if !time.Now().UTC().Before(p.ExpiresAt) {
		p.Validity = "expired"
		return nil
	}
	in := MaintenanceInput{MaintenanceScope: p.Scope, Kind: p.Kind, RulesVersion: p.RulesVersion}
	current, err := buildMaintenance(ctx, tx, in, policy, c, a)
	if err == nil && (maintenanceBlocked(current) || current.Basis.Fingerprint != p.Basis.Fingerprint) {
		p.Validity = "basis_changed"
	}
	return err
}

func (s *Service) GetMaintenancePlan(ctx context.Context, token, id string) (MaintenancePlan, error) {
	if !validRevision(id) {
		return MaintenancePlan{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return MaintenancePlan{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1`, id))
	if err != nil {
		return p, err
	}
	policy, c, a, err := s.maintenanceEnvironment(ctx, tx, p.Scope.AccountID, false)
	if err == nil {
		p, err = scanMaintenance(tx.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE id=$1`, id))
	}
	if err == nil {
		err = checkMaintenance(ctx, tx, &p, policy, c, a)
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return p, err
}

type MaintenancePage struct {
	AccountID string            `json:"account_id"`
	Plans     []MaintenancePlan `json:"plans"`
	NextAfter *string           `json:"next_after"`
}

func (s *Service) ListMaintenancePlans(ctx context.Context, token, account, after string, limit int) (MaintenancePage, error) {
	out := MaintenancePage{AccountID: account, Plans: []MaintenancePlan{}}
	if !managedID(account) || after != "0" && !validRevision(after) || limit < 1 || limit > 20 {
		return out, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	policy, c, a, err := s.maintenanceEnvironment(ctx, tx, account, false)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_plans WHERE account_id=$1 AND id>$2 ORDER BY maintenance_plans.id LIMIT $3`, account, after, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		p, e := scanMaintenance(rows)
		if e != nil {
			err = e
			break
		}
		out.Plans = append(out.Plans, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Plans) > limit {
		out.Plans = out.Plans[:limit]
		out.NextAfter = &out.Plans[limit-1].ID
	}
	for i := range out.Plans {
		if err = checkMaintenance(ctx, tx, &out.Plans[i], policy, c, a); err != nil {
			return out, err
		}
	}
	err = s.finish(ctx, tx, token, true)
	return out, err
}
