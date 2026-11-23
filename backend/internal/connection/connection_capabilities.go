package connection

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (d *Diagnostic) capability(id string) *Check {
	for i := range d.Checks {
		if d.Checks[i].ID == id {
			return &d.Checks[i]
		}
	}
	return nil
}

func diagnosticQuotaStaleAfter(ctx context.Context, tx pgx.Tx) (time.Duration, error) {
	var seconds int
	err := tx.QueryRow(ctx, `SELECT stale_after_seconds FROM monitoring_config WHERE singleton`).Scan(&seconds)
	return time.Duration(seconds) * time.Second, err
}

func (s *Service) diagnoseQuota(ctx context.Context, tx pgx.Tx, c storedConfig, kind, secret string, staleAfter time.Duration) (Check, error) {
	const id = "quota.read"
	var accountID, revision string
	var fingerprint []byte
	err := tx.QueryRow(ctx, `SELECT upstream_id::text,revision::text,resource_fingerprint FROM managed_accounts
		WHERE source_id=$1 AND identity_state='verified' AND observed_connection_revision=$2
		ORDER BY observed_at DESC,id LIMIT 1 FOR SHARE`, c.SourceID, c.Revision).Scan(&accountID, &revision, &fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return unknown(id, "ACCOUNT_CHECK_REQUIRED"), nil
	}
	if err != nil {
		return Check{}, err
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+accountID, kind, secret, id, true)
	if check.State != "available" {
		return check, nil
	}
	account, valid := inspectAccount(body, accountID, secret)
	if !valid {
		return invalid(check), nil
	}
	if account.identity != "verified" || !bytes.Equal(account.fingerprint, fingerprint) {
		check.State, check.Reason = "unverified", "QUOTA_IDENTITY_UNVERIFIED"
		return check, nil
	}
	quota, valid := passiveQuota(body)
	if !valid {
		return invalid(check), nil
	}
	now := time.Now().UTC()
	for _, window := range quota.Windows {
		if window.UsedPercent != nil && quota.SourceAt != nil && !quota.SourceAt.After(now) {
			if now.Sub(*quota.SourceAt) > staleAfter {
				check.State, check.Reason = "unverified", "QUOTA_SNAPSHOT_STALE"
			} else {
				check.Reason = "VERIFIED_PASSIVE_QUOTA"
				check.Evidence = &CapabilityEvidence{UpstreamID: accountID, VerifiedAt: *quota.SourceAt, AccountRevision: revision}
			}
			return check, nil
		}
	}
	check.State, check.Reason = "unverified", "QUOTA_WINDOW_UNVERIFIED"
	return check, nil
}

func (s *Service) recheckDiagnosticQuota(ctx context.Context, tx pgx.Tx, c storedConfig, d *Diagnostic) error {
	quota := d.capability("quota.read")
	if quota == nil || quota.Evidence == nil {
		return nil
	}
	var current bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM managed_accounts WHERE source_id=$1 AND upstream_id=$2
		AND revision=$3 AND observed_connection_revision=$4 AND identity_state='verified')`,
		c.SourceID, quota.Evidence.UpstreamID, quota.Evidence.AccountRevision, c.Revision).Scan(&current)
	if err != nil {
		return err
	}
	staleAfter, err := diagnosticQuotaStaleAfter(ctx, tx)
	if err != nil {
		return err
	}
	switch {
	case !current || c.reverificationRequired:
		*quota = unknown("quota.read", "QUOTA_IDENTITY_UNVERIFIED")
	case time.Since(quota.Evidence.VerifiedAt) > staleAfter:
		*quota = unknown("quota.read", "QUOTA_SNAPSHOT_STALE")
	}
	return nil
}

const dispatchWriteReceipt = `SELECT a.upstream_id::text,d.updated_at
	FROM (SELECT * FROM dispatch_pauses WHERE source_id=$1 AND connection_revision=$2 ORDER BY id DESC LIMIT 1) d
	JOIN managed_accounts a ON a.id=d.account_id AND a.source_id=d.source_id AND a.revision=d.account_revision
	WHERE d.updated_at >= $3 AND a.identity_state='verified' AND a.observed_connection_revision=$2
	AND (d.state='paused' AND d.reason='VERIFIED_PAUSE' OR d.state='restored' AND d.reason='VERIFIED_RESTORE')`

const pricingWriteReceipt = `SELECT group_id::text,updated_at FROM
	(SELECT * FROM pricing_changes WHERE source_id=$1 AND connection_revision=$2 ORDER BY id DESC LIMIT 1) p
	WHERE updated_at >= $3 AND native_after IS NOT NULL
	AND (state='applied' AND reason='APPLIED' OR state='reverted' AND reason='REVERTED')`

const nativeWalletWriteReceipt = `SELECT b.upstream_user_id::text,x.recorded_at
	FROM wallet_application_results x JOIN wallet_application_requests r ON r.id=x.application_id
	JOIN participant_bindings b ON b.id=r.binding_id AND b.participant_id=r.participant_id
	WHERE b.source_id=$1 AND b.ended_at IS NULL AND x.recorded_at >= $3 AND x.resolved
	AND x.snapshot->>'unit'='native_wallet' AND x.snapshot->>'state'='applied'
	AND x.snapshot #>> '{details,input,connection_revision}'=$2
	AND r.snapshot #> '{details,input}'=x.snapshot #> '{details,input}'
	AND x.snapshot #>> '{details,upstream_user_id}'=b.upstream_user_id::text
	ORDER BY x.id DESC LIMIT 1`

func (s *Service) projectWriteCapabilities(ctx context.Context, tx pgx.Tx, c storedConfig, d *Diagnostic) error {
	management := d.capability("management_auth")
	if c.SourceID == nil || c.CredentialKind == nil || c.reverificationRequired || management == nil || management.State != "available" {
		return nil
	}
	mode := d.capability("run_mode")
	for _, v := range []struct {
		id, query string
	}{
		{"wallet.write", nativeWalletWriteReceipt},
		{"scheduling.write", dispatchWriteReceipt},
		{"pricing.write", pricingWriteReceipt},
	} {
		check := d.capability(v.id)
		if check == nil {
			continue
		}
		switch {
		case mode == nil || mode.State != "available" || d.RunMode == nil || *d.RunMode != "standard":
			*check = unknown(v.id, "STANDARD_MODE_REQUIRED")
		case !c.SecretStorageAvailable || d.VersionSince == nil:
			*check = unknown(v.id, "WRITE_EVIDENCE_REQUIRED")
		default:
			var receipt CapabilityEvidence
			err := tx.QueryRow(ctx, v.query, c.SourceID, c.Revision, *d.VersionSince).Scan(&receipt.UpstreamID, &receipt.VerifiedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				*check = unknown(v.id, "WRITE_EVIDENCE_REQUIRED")
			} else if err != nil {
				return err
			} else {
				receipt.VerifiedAt = receipt.VerifiedAt.UTC()
				*check = Check{ID: v.id, State: "available", Reason: "VERIFIED_WRITE", Evidence: &receipt}
			}
		}
	}
	return nil
}
