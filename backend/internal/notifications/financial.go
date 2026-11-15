package notifications

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

// FinancialSource owns current source qualification; the mail worker never reconstructs money inputs.
type FinancialSource interface {
	FinancialNotificationsAvailable(context.Context, pgx.Tx) (bool, error)
	ValidateFinancialNotification(context.Context, pgx.Tx, FinancialEvidence) (bool, error)
}

type FinancialEvidence struct {
	Kind               string    `json:"kind"`
	SubjectID          string    `json:"subject_id"`
	SourceID           string    `json:"source_id"`
	ConnectionRevision string    `json:"connection_revision"`
	SubjectRevision    string    `json:"subject_revision"`
	RecordID           string    `json:"record_id"`
	PreviousID         string    `json:"previous_id"`
	AccountIDs         []string  `json:"account_ids"`
	AsOf               time.Time `json:"as_of"`
}

func financialKind(kind string) bool {
	return kind == "balance_low" || kind == "recommendation_changed" || kind == "estimate_changed"
}
func financialPreference(c storedConfig, kind string) bool {
	switch kind {
	case "balance_low":
		return c.Preferences.BalanceLow
	case "recommendation_changed":
		return c.Preferences.RecommendationChanged
	case "estimate_changed":
		return c.Preferences.EstimateChanged
	}
	return false
}
func (s *Service) financialAvailable(ctx context.Context, tx pgx.Tx) (bool, error) {
	if s.Financial == nil {
		return false, nil
	}
	return s.Financial.FinancialNotificationsAvailable(ctx, tx)
}
func (s *Service) projectFinancialCapabilities(c *storedConfig, available bool) {
	c.SecretStorageAvailable = s.aead != nil
	for i := range c.Capabilities {
		capability := &c.Capabilities[i]
		if financialKind(capability.Kind) {
			capability.Available = available
			if available {
				capability.Reason = "SIMULATED_COMPLETE_BASIS_ONLY"
			}
			capability.Effective = available && c.FinancialEnabled && c.Enabled && ready(*c) && financialPreference(*c, capability.Kind)
		}
		if c.SecretSet && !c.SecretStorageAvailable {
			capability.Effective = false
		}
	}
}

// RecordFinancial shares the immutable business-result transaction and cannot activate old saved preferences.
func RecordFinancial(ctx context.Context, tx pgx.Tx, evidence FinancialEvidence, body string) error {
	if !financialKind(evidence.Kind) || !validID(evidence.SubjectID) || !validID(evidence.SourceID) || !validRevision(evidence.ConnectionRevision) || !validRevision(evidence.SubjectRevision) || !validRevision(evidence.RecordID) || evidence.PreviousID != "0" && !validRevision(evidence.PreviousID) || len(evidence.AccountIDs) < 1 || len(evidence.AccountIDs) > 500 || evidence.AsOf.IsZero() || evidence.AsOf.After(time.Now()) || len(body) == 0 || len(body) > 2048 {
		return auth.ErrInput
	}
	for i, id := range evidence.AccountIDs {
		if !validID(id) || slices.Contains(evidence.AccountIDs[:i], id) {
			return auth.ErrInput
		}
	}
	if evidence.Kind == "estimate_changed" && (len(evidence.AccountIDs) != 1 || evidence.AccountIDs[0] != evidence.SubjectID) || evidence.Kind == "balance_low" && evidence.PreviousID != "0" || evidence.Kind != "balance_low" && evidence.PreviousID == "0" {
		return auth.ErrInput
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, enqueueLock); err != nil {
		return err
	}
	c, err := readConfig(ctx, tx, false)
	if err != nil || !c.FinancialEnabled {
		return err
	}
	key := "financial:" + evidence.Kind + ":" + evidence.RecordID + ":" + evidence.PreviousID
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE event_key=$1)`, key).Scan(&exists); err != nil || exists {
		return err
	}
	n := storedNotification{Notification: Notification{Kind: evidence.Kind, SubjectType: "participant", SubjectID: evidence.SubjectID, ParticipantID: &evidence.SubjectID, State: "queued", Reason: "QUEUED"}, sourceID: &evidence.SourceID, connectionRevision: &evidence.ConnectionRevision, subjectRevision: &evidence.SubjectRevision, financial: &evidence}
	if evidence.Kind == "estimate_changed" {
		n.SubjectType, n.ParticipantID = "account", nil
	}
	subjects := map[string]string{"balance_low": "Sub-Pool simulated balance below advice", "recommendation_changed": "Sub-Pool simulated wallet advice changed", "estimate_changed": "Sub-Pool simulated estimate capacity changed"}
	reasons := map[string]string{"balance_low": "SIMULATED_BALANCE_BELOW_TARGET", "recommendation_changed": "SIMULATED_RECOMMENDATION_CHANGED", "estimate_changed": "SIMULATED_ESTIMATE_CAPACITY_CHANGED"}
	n.details = Details{EventKey: key, Subject: subjects[evidence.Kind], Body: body, EventReason: reasons[evidence.Kind]}
	switch {
	case !c.Enabled:
		n.State, n.Reason = "skipped", "CHANNEL_DISABLED"
	case !financialPreference(c, evidence.Kind):
		n.State, n.Reason = "skipped", "EVENT_DISABLED"
	case !ready(c):
		n.State, n.Reason = "skipped", "CHANNEL_INCOMPLETE"
	default:
		var duplicate bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE kind=$1 AND subject_type=$2 AND subject_id=$3 AND config_revision=$4 AND state IN ('queued','dispatching','accepted','unknown') AND created_at>=clock_timestamp()-make_interval(secs=>$5))`, n.Kind, n.SubjectType, n.SubjectID, c.Revision, c.DedupeSeconds).Scan(&duplicate)
		if err != nil {
			return err
		}
		if duplicate {
			n.State, n.Reason = "skipped", "DUPLICATE_WINDOW"
		}
	}
	_, err = insert(ctx, tx, n, c)
	return err
}

func (s *Service) financialEligible(ctx context.Context, tx pgx.Tx, n storedNotification) (bool, error) {
	if s.Financial == nil || n.financial == nil {
		return false, nil
	}
	e := *n.financial
	if e.Kind != n.Kind || e.SubjectID != n.SubjectID || n.sourceID == nil || *n.sourceID != e.SourceID || n.connectionRevision == nil || *n.connectionRevision != e.ConnectionRevision || n.subjectRevision == nil || *n.subjectRevision != e.SubjectRevision {
		return false, nil
	}
	return s.Financial.ValidateFinancialNotification(ctx, tx, e)
}

func financialJSON(e *FinancialEvidence) ([]byte, error) {
	if e == nil {
		return nil, nil
	}
	return json.Marshal(e)
}
