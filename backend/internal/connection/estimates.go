package connection

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

type EstimateAssumptions struct {
	Plan               string   `json:"plan"`
	CostBasis          string   `json:"cost_basis"`
	KeepRatio          string   `json:"keep_ratio"`
	Premise            string   `json:"premise"`
	Windows            []string `json:"windows"`
	SourceID           string   `json:"source_id"`
	ConnectionRevision string   `json:"connection_revision"`
	AccountRevision    string   `json:"account_revision"`
	SourceMode         string   `json:"source_mode"`
}

type EstimateConfiguration struct {
	AccountID   string               `json:"account_id"`
	Revision    string               `json:"revision"`
	Assumptions *EstimateAssumptions `json:"assumptions"`
	SavedAt     *time.Time           `json:"saved_at"`
}

type EstimateWindow struct {
	Kind                 string             `json:"kind"`
	Basis                string             `json:"basis"`
	Period               *ObservationPeriod `json:"period"`
	ManualStartAt        *time.Time         `json:"manual_start_at"`
	ManualEndAt          *time.Time         `json:"manual_end_at"`
	ObservationIDs       []string           `json:"observation_ids"`
	CurrentObservationID *string            `json:"current_observation_id"`
	SourceAt             *time.Time         `json:"source_at"`
	OldestSampleAt       *time.Time         `json:"oldest_sample_at"`
	CurrentUsedPercent   *string            `json:"current_used_percent"`
	estimation.Result
}

type EstimateSnapshot struct {
	RulesVersion       string                `json:"rules_version"`
	Configuration      EstimateConfiguration `json:"configuration"`
	Trigger            string                `json:"trigger"`
	PolicyRevision     string                `json:"policy_revision"`
	ManagementRevision string                `json:"management_revision"`
	ObservationHead    string                `json:"observation_head"`
	CaptureHead        string                `json:"capture_head"`
	State              string                `json:"state"`
	Windows            []EstimateWindow      `json:"windows"`
	Remaining          *estimation.Amount    `json:"remaining"`
	Currency           string                `json:"currency"`
	Complete           bool                  `json:"complete"`
	SettlementEligible bool                  `json:"settlement_eligible"`
	ModelCalibrated    bool                  `json:"model_calibrated"`
	Limitations        []string              `json:"limitations"`
}

type EstimateRecord struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	GeneratedAt time.Time `json:"generated_at"`
	EstimateSnapshot
}

type EstimateCurrent struct {
	Configuration      EstimateConfiguration `json:"configuration"`
	Latest             *EstimateRecord       `json:"latest"`
	CurrentState       string                `json:"current_state"`
	CurrentRemaining   *estimation.Amount    `json:"current_remaining"`
	NeedsRecalculation bool                  `json:"needs_recalculation"`
	CheckedAt          time.Time             `json:"checked_at"`
}

func readEstimateConfiguration(ctx context.Context, tx pgx.Tx, account string, write bool) (EstimateConfiguration, error) {
	result := EstimateConfiguration{AccountID: account, Revision: "1"}
	var body []byte
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT revision::text,configuration,saved_at FROM estimate_config WHERE account_id=$1`+lock, account).Scan(&result.Revision, &body, &result.SavedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err == nil && body != nil {
		err = json.Unmarshal(body, &result.Assumptions)
	}
	result.SavedAt = utc(result.SavedAt)
	return result, err
}

func estimateHeads(ctx context.Context, tx pgx.Tx, account string) (observation, capture, management string, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT max(id) FROM quota_observations WHERE account_id=$1),0)::text,COALESCE((SELECT max(id) FROM usage_captures WHERE account_id=$1),0)::text,COALESCE((SELECT revision FROM observation_management WHERE account_id=$1),1)::text`, account).Scan(&observation, &capture, &management)
	return
}

func scanEstimate(row pgx.Row) (EstimateRecord, error) {
	var record EstimateRecord
	var body []byte
	err := row.Scan(&record.ID, &record.AccountID, &record.GeneratedAt, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, auth.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(body, &record.EstimateSnapshot)
	}
	record.GeneratedAt = record.GeneratedAt.UTC()
	return record, err
}

const estimateColumns = "id::text,account_id,generated_at,snapshot"

func latestEstimate(ctx context.Context, tx pgx.Tx, account string) (*EstimateRecord, error) {
	result, err := scanEstimate(tx.QueryRow(ctx, `SELECT `+estimateColumns+` FROM estimate_results WHERE account_id=$1 ORDER BY estimate_results.id DESC LIMIT 1`, account))
	if errors.Is(err, auth.ErrNotFound) {
		return nil, nil
	}
	return &result, err
}

func (s *Service) estimateEnvironment(ctx context.Context, tx pgx.Tx, account string) (MonitoringConfig, storedConfig, storedAccount, error) {
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return policy, storedConfig{}, storedAccount{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return policy, c, storedAccount{}, err
	}
	a, err := readManaged(ctx, tx, account, c, false)
	return policy, c, a, err
}

func (s *Service) finishEstimateRead(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeRead(ctx, tx, token, "estimates"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) GetEstimateCurrent(ctx context.Context, token, account string) (EstimateCurrent, error) {
	if !managedID(account) {
		return EstimateCurrent{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EstimateCurrent{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "estimates")
	if err == nil {
		err = access.CheckAccount(account)
	}
	if err != nil {
		return EstimateCurrent{}, err
	}
	policy, c, a, err := s.estimateEnvironment(ctx, tx, account)
	if err != nil {
		return EstimateCurrent{}, err
	}
	cfg, err := readEstimateConfiguration(ctx, tx, account, false)
	if err != nil {
		return EstimateCurrent{}, err
	}
	latest, err := latestEstimate(ctx, tx, account)
	if err != nil {
		return EstimateCurrent{}, err
	}
	observation, capture, management, err := estimateHeads(ctx, tx, account)
	if err != nil {
		return EstimateCurrent{}, err
	}
	result := EstimateCurrent{Configuration: cfg, Latest: latest, CurrentState: "not_configured", CheckedAt: time.Now().UTC()}
	if cfg.Assumptions != nil {
		result.CurrentState = "recalculation_required"
		result.NeedsRecalculation = true
		if latest != nil {
			result.CurrentState = latest.State
			result.NeedsRecalculation = false
			if !a.SourceCurrent || !a.ConnectionAvailable || a.IdentityState != "verified" || cfg.Assumptions.SourceID != a.SourceID || cfg.Assumptions.ConnectionRevision != c.Revision || cfg.Assumptions.AccountRevision != a.Revision || cfg.Assumptions.SourceMode != a.SourceMode {
				result.CurrentState = "condition_changed"
			} else if latest.Configuration.Revision != cfg.Revision || latest.PolicyRevision != policy.Revision || latest.ObservationHead != observation || latest.CaptureHead != capture || latest.ManagementRevision != management {
				result.CurrentState = "recalculation_required"
				result.NeedsRecalculation = true
			} else {
				for _, w := range latest.Windows {
					if w.SourceAt == nil {
						continue
					}
					if w.SourceAt.After(result.CheckedAt) || result.CheckedAt.Sub(*w.SourceAt) > time.Duration(policy.StaleAfterSeconds)*time.Second || w.OldestSampleAt != nil && result.CheckedAt.Sub(*w.OldestSampleAt) > time.Duration(policy.StaleAfterSeconds)*time.Second || w.Period != nil && !result.CheckedAt.Before(w.Period.EndAt) {
						result.CurrentState = "source_stale"
						break
					}
				}
			}
		}
	}
	// shortcut: usage-v1 cannot prove interval coverage; qualify a new provider before exposing a current amount.
	if result.CurrentState == "scenario" {
		result.CurrentState = "evidence_incomplete"
	}
	if err = s.finishEstimateRead(ctx, tx, token); err != nil {
		return EstimateCurrent{}, err
	}
	return result, nil
}

type EstimateSaveInput struct {
	AccountID string `json:"account_id"`
	Revision  string `json:"revision"`
	EstimateAssumptions
}

func validEstimateAssumptions(in EstimateAssumptions) bool {
	if in.Plan != estimation.Plan || in.CostBasis != "standard_usd" || !estimation.ValidKeep(in.KeepRatio) || !observationReason(in.Premise) || len(in.Windows) < 1 || len(in.Windows) > 2 {
		return false
	}
	for i, w := range in.Windows {
		if w != "short" && w != "seven_day" || slices.Contains(in.Windows[:i], w) {
			return false
		}
	}
	return true
}

func (s *Service) SaveEstimateConfiguration(ctx context.Context, token string, input EstimateSaveInput) (EstimateRecord, error) {
	if !managedID(input.AccountID) || !validRevision(input.Revision) || !managedID(input.SourceID) || !validRevision(input.ConnectionRevision) || !validRevision(input.AccountRevision) || !validEstimateAssumptions(input.EstimateAssumptions) || input.SourceMode != "" {
		return EstimateRecord{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return EstimateRecord{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = readMonitoringConfig(ctx, tx, false); err != nil {
		return EstimateRecord{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return EstimateRecord{}, err
	}
	a, err := readManaged(ctx, tx, input.AccountID, c, true)
	if err != nil {
		return EstimateRecord{}, err
	}
	if !a.SourceCurrent || !a.ConnectionAvailable || a.IdentityState != "verified" || input.SourceID != a.SourceID || input.ConnectionRevision != c.Revision || input.AccountRevision != a.Revision {
		return EstimateRecord{}, auth.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO estimate_config(account_id) VALUES($1) ON CONFLICT DO NOTHING`, a.ID); err != nil {
		return EstimateRecord{}, err
	}
	cfg, err := readEstimateConfiguration(ctx, tx, a.ID, true)
	if err != nil {
		return EstimateRecord{}, err
	}
	if cfg.Revision != input.Revision {
		return EstimateRecord{}, auth.ErrConflict
	}
	input.SourceMode = a.SourceMode
	input.Windows = slices.Clone(input.Windows)
	slices.Sort(input.Windows)
	body, err := json.Marshal(input.EstimateAssumptions)
	if err != nil {
		return EstimateRecord{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return EstimateRecord{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE estimate_config SET revision=revision+1,configuration=$2,saved_at=$3 WHERE account_id=$1`, a.ID, body, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		return EstimateRecord{}, err
	}
	result, err := s.appendAccountEstimate(ctx, tx, a.ID, "configuration", &actor.ID, nil)
	if err != nil {
		return EstimateRecord{}, err
	}
	if result == nil {
		return EstimateRecord{}, auth.ErrConflict
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return EstimateRecord{}, err
	}
	return *result, nil
}

func (s *Service) RecomputeEstimate(ctx context.Context, token, account, revision, latestID string) (EstimateRecord, error) {
	if !managedID(account) || !validRevision(revision) || !validUsageAfter(latestID) {
		return EstimateRecord{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return EstimateRecord{}, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = s.estimateEnvironment(ctx, tx, account); err != nil {
		return EstimateRecord{}, err
	}
	cfg, err := readEstimateConfiguration(ctx, tx, account, true)
	if err != nil {
		return EstimateRecord{}, err
	}
	latest, err := latestEstimate(ctx, tx, account)
	if err != nil {
		return EstimateRecord{}, err
	}
	head := "0"
	if latest != nil {
		head = latest.ID
	}
	if cfg.Assumptions == nil || cfg.Revision != revision || latestID != head {
		return EstimateRecord{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return EstimateRecord{}, err
	}
	result, err := s.appendAccountEstimate(ctx, tx, account, "manual_recompute", &actor.ID, nil)
	if err != nil {
		return EstimateRecord{}, err
	}
	if result == nil {
		return EstimateRecord{}, auth.ErrConflict
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return EstimateRecord{}, err
	}
	return *result, nil
}

type EstimateHistoryInput struct {
	AccountID   string `json:"account_id"`
	After       string `json:"after"`
	Limit       int    `json:"limit"`
	WindowKind  string `json:"window_kind"`
	PeriodEndAt string `json:"period_end_at"`
}

type EstimateHistory struct {
	AccountID string           `json:"account_id"`
	Records   []EstimateRecord `json:"records"`
	NextAfter *string          `json:"next_after"`
}

func (s *Service) ListEstimateHistory(ctx context.Context, token string, in EstimateHistoryInput) (EstimateHistory, error) {
	if !managedID(in.AccountID) || !validUsageAfter(in.After) || in.Limit < 1 || in.Limit > 100 || in.WindowKind != "all" && in.WindowKind != "short" && in.WindowKind != "seven_day" {
		return EstimateHistory{}, auth.ErrInput
	}
	if in.PeriodEndAt != "" {
		at, err := time.Parse(time.RFC3339Nano, in.PeriodEndAt)
		if err != nil || !observationTimestamp.MatchString(in.PeriodEndAt) || in.WindowKind == "all" || at.Year() < 1970 || at.Year() >= 9999 {
			return EstimateHistory{}, auth.ErrInput
		}
		in.PeriodEndAt = at.UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EstimateHistory{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "estimates")
	if err == nil {
		err = access.CheckAccount(in.AccountID)
	}
	if err != nil {
		return EstimateHistory{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE id=$1)`, in.AccountID).Scan(&exists); err != nil {
		return EstimateHistory{}, err
	}
	if !exists {
		return EstimateHistory{}, auth.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+estimateColumns+` FROM estimate_results WHERE account_id=$1 AND id>$2::bigint AND ($3='all' OR EXISTS(SELECT 1 FROM jsonb_array_elements(snapshot->'windows') w WHERE w->>'kind'=$3 AND ($4='' OR w->'period'->>'end_at'=$4))) ORDER BY estimate_results.id LIMIT $5`, in.AccountID, in.After, in.WindowKind, in.PeriodEndAt, in.Limit+1)
	if err != nil {
		return EstimateHistory{}, err
	}
	result := EstimateHistory{AccountID: in.AccountID, Records: []EstimateRecord{}}
	for rows.Next() {
		r, e := scanEstimate(rows)
		if e != nil {
			err = e
			break
		}
		result.Records = append(result.Records, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return EstimateHistory{}, err
	}
	if len(result.Records) > in.Limit {
		result.Records = result.Records[:in.Limit]
		result.NextAfter = &result.Records[in.Limit-1].ID
	}
	if err = s.finishEstimateRead(ctx, tx, token); err != nil {
		return EstimateHistory{}, err
	}
	return result, nil
}

func (s *Service) GetEstimateRecord(ctx context.Context, token, id string) (EstimateRecord, error) {
	if !validRevision(id) {
		return EstimateRecord{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EstimateRecord{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "estimates")
	if err != nil {
		return EstimateRecord{}, err
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"account_id"}, nil, 2)
	if err != nil {
		return EstimateRecord{}, err
	}
	args = append([]any{id}, args...)
	result, err := scanEstimate(tx.QueryRow(ctx, `SELECT `+estimateColumns+` FROM estimate_results WHERE id=$1 AND `+scope, args...))
	if err != nil {
		return EstimateRecord{}, err
	}
	if err = s.finishEstimateRead(ctx, tx, token); err != nil {
		return EstimateRecord{}, err
	}
	return result, nil
}

func estimateAudit(ctx context.Context, tx pgx.Tx, r EstimateRecord, actor, task *string) error {
	_, err := audit.Record(ctx, tx, audit.Entry{OperationID: "estimate:" + r.ID, ActorID: actor, TaskID: task, Action: "estimate." + r.Trigger, Outcome: "succeeded", TargetType: "estimate", TargetID: r.ID, Revision: &r.Configuration.Revision, SourceID: &r.Configuration.Assumptions.SourceID})
	return err
}
