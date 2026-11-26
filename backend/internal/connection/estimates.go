package connection

import (
	"context"
	"encoding/json"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"

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
	Kind                 string               `json:"kind"`
	Basis                string               `json:"basis"`
	Period               *ObservationPeriod   `json:"period"`
	ManualStartAt        *time.Time           `json:"manual_start_at"`
	ManualEndAt          *time.Time           `json:"manual_end_at"`
	ObservationIDs       []string             `json:"observation_ids"`
	CurrentObservationID *string              `json:"current_observation_id"`
	SourceAt             *time.Time           `json:"source_at"`
	OldestSampleAt       *time.Time           `json:"oldest_sample_at"`
	CurrentUsedPercent   *string              `json:"current_used_percent"`
	CostProof            *SimulationCostProof `json:"cost_proof,omitempty"`
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
	Configuration          EstimateConfiguration `json:"configuration"`
	Latest                 *EstimateRecord       `json:"latest"`
	CurrentState           string                `json:"current_state"`
	CurrentRemaining       *estimation.Amount    `json:"current_remaining"`
	NeedsRecalculation     bool                  `json:"needs_recalculation"`
	CheckedAt              time.Time             `json:"checked_at"`
	NativeWeekly           *NativeWeeklyCurrent  `json:"native_weekly,omitempty"`
	SimulationConfigurable bool                  `json:"simulation_configurable,omitempty"`
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
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT max(id) FROM quota_observations WHERE account_id=$1),0)::text,COALESCE((SELECT max(id) FROM usage_captures WHERE account_id=$1 AND collection_day IS NULL),0)::text,COALESCE((SELECT revision FROM observation_management WHERE account_id=$1),1)::text`, account).Scan(&observation, &capture, &management)
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
	result := EstimateCurrent{Configuration: EstimateConfiguration{AccountID: account, Revision: "1"}, CurrentState: "not_configured", CheckedAt: time.Now().UTC()}
	result.NativeWeekly, err = s.nativeWeeklyCurrent(ctx, tx, a, c, policy, result.CheckedAt)
	if err != nil {
		return EstimateCurrent{}, err
	}
	allocation, err := readWeeklyAllocation(ctx, tx, access, a, result.NativeWeekly)
	if err != nil {
		return EstimateCurrent{}, err
	}
	if result.NativeWeekly != nil {
		result.NativeWeekly.Allocation = allocation
	}
	if err = s.finishEstimateRead(ctx, tx, token); err != nil {
		return EstimateCurrent{}, err
	}
	result.CheckedAt = time.Now().UTC()
	if weekly := result.NativeWeekly; weekly != nil && weekly.stale(policy, result.CheckedAt) {
		weekly.State, weekly.Remaining, weekly.Progress = "source_stale", nil, nil
		if !weekly.activeCycle(result.CheckedAt) || weekly.Latest == nil || weekly.Latest.Cumulative == nil ||
			weekly.Latest.Cumulative.Confidence != "trusted" {
			weekly.Latest, weekly.Allocation = nil, nil
		} else if shares := weekly.Allocation; shares != nil {
			for i := range shares.Members {
				shares.Members[i].Remaining = nil
			}
			if shares.Reserve != nil {
				shares.Reserve.Remaining = nil
			}
		}
	}
	return result, nil
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
