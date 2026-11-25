package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

// FinancialSource owns current source qualification; the mail worker never reconstructs money inputs.
type FinancialSource interface {
	FinancialNotificationSupport(context.Context, pgx.Tx) (FinancialSupport, error)
	ValidateFinancialNotification(context.Context, pgx.Tx, FinancialEvidence) (bool, error)
}

type FinancialSupport struct {
	Simulation     bool
	NativeSourceID string
}

type NativeFinancialEvidence struct {
	PolicyRevision string `json:"policy_revision"`
	BindingID      string `json:"binding_id,omitempty"`
	CycleKey       string `json:"cycle_key,omitempty"`
}

type FinancialEvidence struct {
	Kind               string                   `json:"kind"`
	SubjectID          string                   `json:"subject_id"`
	SourceID           string                   `json:"source_id"`
	ConnectionRevision string                   `json:"connection_revision"`
	SubjectRevision    string                   `json:"subject_revision"`
	RecordID           string                   `json:"record_id"`
	PreviousID         string                   `json:"previous_id"`
	AccountIDs         []string                 `json:"account_ids"`
	AsOf               time.Time                `json:"as_of"`
	Native             *NativeFinancialEvidence `json:"native,omitempty"`
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
func (s *Service) financialAvailable(ctx context.Context, tx pgx.Tx) (FinancialSupport, error) {
	if s.Financial == nil {
		return FinancialSupport{}, nil
	}
	return s.Financial.FinancialNotificationSupport(ctx, tx)
}

func nativeFinancialEnabled(c storedConfig, source string) bool {
	return c.FinancialEnabled && c.nativeSourceID != nil && *c.nativeSourceID == source
}

// ReadNativePolicy is a scheduling hint. RecordFinancial and dispatch recheck the saved revision.
func ReadNativePolicy(ctx context.Context, tx pgx.Tx, source string) (string, Preferences, error) {
	c, err := queryConfig(ctx, tx, "")
	if err != nil || !c.Enabled || !ready(c) || !nativeFinancialEnabled(c, source) {
		return "", Preferences{}, err
	}
	return c.Revision, c.Preferences, nil
}

func (s *Service) projectFinancialCapabilities(c *storedConfig, support FinancialSupport) {
	c.SecretStorageAvailable = s.aead != nil
	if support.NativeSourceID != "" {
		c.NativeSourceID = &support.NativeSourceID
		c.NativeFinancialEnabled = nativeFinancialEnabled(*c, support.NativeSourceID)
	}
	for i := range c.Capabilities {
		capability := &c.Capabilities[i]
		if financialKind(capability.Kind) {
			capability.Available = support.Simulation || c.NativeSourceID != nil
			if support.Simulation {
				capability.Reason = "SIMULATED_COMPLETE_BASIS_ONLY"
			} else if c.NativeSourceID != nil {
				capability.Reason = "NATIVE_CONFIRMED_BASIS_ONLY"
			}
			activated := support.Simulation && c.FinancialEnabled || c.NativeFinancialEnabled
			capability.Effective = activated && c.Enabled && ready(*c) && financialPreference(*c, capability.Kind)
		}
		if c.SecretSet && !c.SecretStorageAvailable {
			capability.Effective = false
		}
	}
}

func financialEventKey(e FinancialEvidence) (string, bool) {
	if e.Native == nil {
		return "financial:" + e.Kind + ":" + e.RecordID + ":" + e.PreviousID, (e.PreviousID == "0") == (e.Kind == "balance_low")
	}
	n := e.Native
	if !validRevision(n.PolicyRevision) || !validRevision(e.PreviousID) || e.PreviousID == e.RecordID {
		return "", false
	}
	if e.Kind == "estimate_changed" && n.BindingID != "" || e.Kind != "estimate_changed" && !validRevision(n.BindingID) {
		return "", false
	}
	identity := []string{e.Kind, e.SourceID, e.SubjectID, n.BindingID}
	if e.Kind == "balance_low" {
		digest, err := hex.DecodeString(n.CycleKey)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != n.CycleKey {
			return "", false
		}
		identity = append(identity, n.CycleKey)
	} else {
		if n.CycleKey != "" {
			return "", false
		}
		identity = append(identity, e.RecordID, e.PreviousID)
	}
	body, _ := json.Marshal(identity)
	return fmt.Sprintf("financial:native:%s:%x", e.Kind, sha256.Sum256(body)), true
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
	key, valid := financialEventKey(evidence)
	if !valid || evidence.Kind == "estimate_changed" && (len(evidence.AccountIDs) != 1 || evidence.AccountIDs[0] != evidence.SubjectID) {
		return auth.ErrInput
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, enqueueLock); err != nil {
		return err
	}
	c, err := readConfig(ctx, tx, false)
	if err != nil || !c.FinancialEnabled || evidence.Native != nil &&
		(!nativeFinancialEnabled(c, evidence.SourceID) || c.Revision != evidence.Native.PolicyRevision) {
		return err
	}
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
	if evidence.Native != nil {
		subjects = map[string]string{"balance_low": "Sub-Pool wallet balance below 20%", "recommendation_changed": "Sub-Pool weekly wallet advice changed", "estimate_changed": "Sub-Pool seven-day capacity changed"}
		reasons = map[string]string{"balance_low": "NATIVE_BALANCE_BELOW_ISSUANCE", "recommendation_changed": "NATIVE_RECOMMENDATION_CHANGED", "estimate_changed": "NATIVE_ESTIMATE_CAPACITY_CHANGED"}
	}
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
	key, valid := financialEventKey(e)
	if !valid || key != n.details.EventKey || e.Kind != n.Kind || e.SubjectID != n.SubjectID || n.sourceID == nil || *n.sourceID != e.SourceID || n.connectionRevision == nil || *n.connectionRevision != e.ConnectionRevision || n.subjectRevision == nil || *n.subjectRevision != e.SubjectRevision {
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
