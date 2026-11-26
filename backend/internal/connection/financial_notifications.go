package connection

import (
	"context"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/notifications"
)

func (s *Service) FinancialNotificationSupport(ctx context.Context, tx pgx.Tx) (notifications.FinancialSupport, error) {
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return notifications.FinancialSupport{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return notifications.FinancialSupport{}, err
	}
	support := notifications.FinancialSupport{}
	if policy.Enabled {
		support.NativeSourceID = nativeFinancialSource(c)
	}
	return support, nil
}

func nativeFinancialSource(c storedConfig) string {
	if c.SourceID == nil || !c.CredentialSet || !c.SecretStorageAvailable || c.reverificationRequired || c.Diagnostic == nil || c.Diagnostic.Version == nil {
		return ""
	}
	if !c.Diagnostic.Saved || c.Diagnostic.SourceID == nil || *c.Diagnostic.SourceID != *c.SourceID || c.Diagnostic.Revision != c.Revision {
		return ""
	}
	check := c.Diagnostic.capability("version.read")
	if check == nil || check.State != "available" || keyModeProbeForVersion(*c.Diagnostic.Version) == "" {
		return ""
	}
	return *c.SourceID
}

func financialApplicationsCurrent(ctx context.Context, tx pgx.Tx, read *WalletRead) (bool, error) {
	if read == nil || read.ReadAt == nil {
		return false, nil
	}
	var current bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=$1 AND r.binding_id=$2 AND (NOT (`+walletApplicationResolvedSQL+`) OR r.attempted_at>$3 OR EXISTS(SELECT 1 FROM wallet_application_results x WHERE x.application_id=r.id AND x.recorded_at>$3)))`, read.ParticipantID, read.BindingID, *read.ReadAt).Scan(&current)
	return current, err
}

// ValidateFinancialNotification uses current local heads and frozen independent proofs; it never reads or writes an upstream.
func (s *Service) ValidateFinancialNotification(ctx context.Context, tx pgx.Tx, e notifications.FinancialEvidence) (bool, error) {
	if e.Native == nil {
		return false, nil
	}
	if e.Kind == "estimate_changed" {
		return s.validateNativeEstimateNotification(ctx, tx, e)
	}
	return s.validateNativeWalletNotification(ctx, tx, e)
}
